// Package blob is the content bucket of jiku-docs: one S3-compatible bucket (DigitalOcean Spaces
// in production, MinIO locally) holding every version of every document and the large uploads
// in transit. Attachments are NOT here: they live in core's file service (D-3).
package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/lifecycle"
)

// ErrNotFound is returned when an object does not exist.
var ErrNotFound = errors.New("blob: object not found")

// ErrTooLarge is returned when an object exceeds the requested maximum.
var ErrTooLarge = errors.New("blob: object too large")

// Store is the content bucket.
type Store interface {
	Put(ctx context.Context, key string, data []byte, contentType string) error
	Get(ctx context.Context, key string, maxBytes int64) ([]byte, error)
	Copy(ctx context.Context, src, dst string) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string, fn func(key string, modified time.Time) error) error
	PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error)
	PresignGet(ctx context.Context, key string, expiry time.Duration, contentType string) (string, error)
	Ping(ctx context.Context) error
}

// Config of the bucket.
type Config struct {
	Endpoint       string // http(s)://host:port, used by the service
	PublicEndpoint string // optional: the host presigned URLs are signed for (reachable by clients)
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	PathStyle      bool
}

// S3 implements Store over minio-go.
type S3 struct {
	client *minio.Client
	signer *minio.Client
	bucket string
}

func newClient(endpoint string, cfg Config) (*minio.Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid S3 endpoint %q", endpoint)
	}
	lookup := minio.BucketLookupDNS
	if cfg.PathStyle {
		lookup = minio.BucketLookupPath
	}
	return minio.New(u.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       u.Scheme == "https",
		Region:       cfg.Region, // explicit region: presigning never calls GetBucketLocation
		BucketLookup: lookup,
	})
}

// New connects to the bucket. It does not create it: the deployment does (compose init,
// Spaces console).
func New(cfg Config) (*S3, error) {
	client, err := newClient(cfg.Endpoint, cfg)
	if err != nil {
		return nil, err
	}
	signer := client
	if cfg.PublicEndpoint != "" && cfg.PublicEndpoint != cfg.Endpoint {
		signer, err = newClient(cfg.PublicEndpoint, cfg)
		if err != nil {
			return nil, err
		}
	}
	return &S3{client: client, signer: signer, bucket: cfg.Bucket}, nil
}

// EnsureUploadsLifecycle expires the large uploads in transit (`uploads/`) after one day, as a
// safety net under the janitor's purge.
func (s *S3) EnsureUploadsLifecycle(ctx context.Context) error {
	cfg := lifecycle.NewConfiguration()
	cfg.Rules = []lifecycle.Rule{{
		ID:         "jiku-docs-uploads",
		Status:     "Enabled",
		RuleFilter: lifecycle.Filter{Prefix: "uploads/"},
		Expiration: lifecycle.Expiration{Days: 1},
	}}
	return s.client.SetBucketLifecycle(ctx, s.bucket, cfg)
}

// EnsureBucket creates the bucket (with versioning) if missing. Used by tests and by the
// optional `storage init`.
func (s *S3) EnsureBucket(ctx context.Context, region string, versioning bool) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return err
	}
	if !exists {
		if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: region}); err != nil {
			return err
		}
	}
	if versioning {
		return s.client.EnableVersioning(ctx, s.bucket)
	}
	return nil
}

func isNotFound(err error) bool {
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || resp.StatusCode == 404
}

func (s *S3) Put(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (s *S3) Get(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer obj.Close()
	r := io.Reader(obj)
	if maxBytes > 0 {
		r = io.LimitReader(obj, maxBytes+1)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if maxBytes > 0 && int64(len(b)) > maxBytes {
		return nil, ErrTooLarge
	}
	return b, nil
}

func (s *S3) Copy(ctx context.Context, src, dst string) error {
	_, err := s.client.CopyObject(ctx,
		minio.CopyDestOptions{Bucket: s.bucket, Object: dst},
		minio.CopySrcOptions{Bucket: s.bucket, Object: src})
	if err != nil && isNotFound(err) {
		return ErrNotFound
	}
	return err
}

func (s *S3) Delete(ctx context.Context, key string) error {
	err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}

func (s *S3) List(ctx context.Context, prefix string, fn func(key string, modified time.Time) error) error {
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if obj.Err != nil {
			return obj.Err
		}
		if strings.HasSuffix(obj.Key, "/") {
			continue
		}
		if err := fn(obj.Key, obj.LastModified); err != nil {
			return err
		}
	}
	return nil
}

func (s *S3) PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error) {
	u, err := s.signer.PresignedPutObject(ctx, s.bucket, key, expiry)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (s *S3) PresignGet(ctx context.Context, key string, expiry time.Duration, contentType string) (string, error) {
	params := url.Values{}
	if contentType != "" {
		params.Set("response-content-type", contentType)
	}
	u, err := s.signer.PresignedGetObject(ctx, s.bucket, key, expiry, params)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (s *S3) Ping(ctx context.Context) error {
	ok, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("bucket %q does not exist", s.bucket)
	}
	return nil
}
