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
	// DeleteMany deletes up to MaxDeleteBatch keys with one DeleteObjects call and returns the
	// keys that are gone (deleted now or already missing). A key that failed is not returned.
	DeleteMany(ctx context.Context, keys []string) ([]string, error)
	List(ctx context.Context, prefix string, fn func(key string, modified time.Time) error) error
	// ListFrom lists the keys of prefix that sort after startAfter ("" = from the start), in
	// lexical order. fn returning ErrStopList ends the listing without error.
	ListFrom(ctx context.Context, prefix, startAfter string, fn func(key string, modified time.Time) error) error
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

// MaxDeleteBatch is the most keys one DeleteObjects call accepts (S3 limit).
const MaxDeleteBatch = 1000

// ErrStopList ends a ListFrom early, without error.
var ErrStopList = errors.New("blob: stop listing")

// Lifecycle rule ids, also what `storage init` checks.
const (
	RuleUploads          = "jiku-docs-uploads"
	RuleNoncurrent       = "jiku-docs-noncurrent-versions"
	NoncurrentVersionTTL = 7 // days
)

// EnsureLifecycle sets the lifecycle rules of the bucket (it replaces the whole configuration):
//   - the large uploads in transit (`uploads/`) expire after one day, as a safety net under the
//     janitor's purge;
//   - the non-current versions of every object expire after 7 days: with versioning on, an object
//     deleted by the history compaction (or by a purge) can be recovered for a week, and its
//     space is really freed afterwards (docs/compactacion-historial-v1.md, section 4).
func (s *S3) EnsureLifecycle(ctx context.Context) error {
	cfg := lifecycle.NewConfiguration()
	cfg.Rules = []lifecycle.Rule{
		{
			ID:         RuleUploads,
			Status:     "Enabled",
			RuleFilter: lifecycle.Filter{Prefix: "uploads/"},
			Expiration: lifecycle.Expiration{Days: 1},
		},
		{
			ID:                          RuleNoncurrent,
			Status:                      "Enabled",
			RuleFilter:                  lifecycle.Filter{Prefix: ""},
			NoncurrentVersionExpiration: lifecycle.NoncurrentVersionExpiration{NoncurrentDays: NoncurrentVersionTTL},
		},
	}
	return s.client.SetBucketLifecycle(ctx, s.bucket, cfg)
}

// LifecycleRule is a rule as the bucket reports it.
type LifecycleRule struct {
	ID             string
	Prefix         string
	ExpirationDays int
	NoncurrentDays int
	Enabled        bool
}

// Lifecycle reads the lifecycle rules of the bucket.
func (s *S3) Lifecycle(ctx context.Context) ([]LifecycleRule, error) {
	cfg, err := s.client.GetBucketLifecycle(ctx, s.bucket)
	if err != nil {
		return nil, err
	}
	out := make([]LifecycleRule, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		prefix := r.RuleFilter.Prefix
		if prefix == "" {
			prefix = r.Prefix
		}
		out = append(out, LifecycleRule{
			ID: r.ID, Prefix: prefix, Enabled: r.Status == "Enabled",
			ExpirationDays: int(r.Expiration.Days),
			NoncurrentDays: int(r.NoncurrentVersionExpiration.NoncurrentDays),
		})
	}
	return out, nil
}

// CheckLifecycle verifies that the bucket holds the two rules EnsureLifecycle sets.
func (s *S3) CheckLifecycle(ctx context.Context) error {
	rules, err := s.Lifecycle(ctx)
	if err != nil {
		return err
	}
	var uploads, noncurrent bool
	for _, r := range rules {
		switch {
		case r.ID == RuleUploads && r.Enabled && r.Prefix == "uploads/" && r.ExpirationDays == 1:
			uploads = true
		case r.ID == RuleNoncurrent && r.Enabled && r.Prefix == "" && r.NoncurrentDays == NoncurrentVersionTTL:
			noncurrent = true
		}
	}
	if !uploads || !noncurrent {
		return fmt.Errorf("blob: lifecycle rules missing (uploads/ after 1 day: %v; non-current versions after %d days: %v)",
			uploads, NoncurrentVersionTTL, noncurrent)
	}
	return nil
}

// Versioning reports whether versioning is enabled on the bucket.
func (s *S3) Versioning(ctx context.Context) (bool, error) {
	v, err := s.client.GetBucketVersioning(ctx, s.bucket)
	if err != nil {
		return false, err
	}
	return v.Enabled(), nil
}

// ObjectVersions counts the versions (including delete markers) kept for a key, for tests and
// operation checks.
func (s *S3) ObjectVersions(ctx context.Context, key string) (versions, deleteMarkers int, err error) {
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: key, WithVersions: true}) {
		if obj.Err != nil {
			return 0, 0, obj.Err
		}
		if obj.Key != key {
			continue
		}
		if obj.IsDeleteMarker {
			deleteMarkers++
		} else {
			versions++
		}
	}
	return versions, deleteMarkers, nil
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

func (s *S3) DeleteMany(ctx context.Context, keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	if len(keys) > MaxDeleteBatch {
		return nil, fmt.Errorf("blob: %d keys exceed the DeleteObjects limit of %d", len(keys), MaxDeleteBatch)
	}
	ch := make(chan minio.ObjectInfo, len(keys))
	for _, k := range keys {
		ch <- minio.ObjectInfo{Key: k}
	}
	close(ch)
	failed := map[string]error{}
	var first error
	for res := range s.client.RemoveObjectsWithResult(ctx, s.bucket, ch, minio.RemoveObjectsOptions{}) {
		if res.Err != nil && !isNotFound(res.Err) {
			failed[res.ObjectName] = res.Err
			if first == nil {
				first = res.Err
			}
		}
	}
	deleted := make([]string, 0, len(keys))
	for _, k := range keys {
		if _, bad := failed[k]; !bad {
			deleted = append(deleted, k)
		}
	}
	if first != nil {
		return deleted, fmt.Errorf("blob: %d of %d objects not deleted: %w", len(failed), len(keys), first)
	}
	return deleted, nil
}

func (s *S3) List(ctx context.Context, prefix string, fn func(key string, modified time.Time) error) error {
	return s.ListFrom(ctx, prefix, "", fn)
}

func (s *S3) ListFrom(ctx context.Context, prefix, startAfter string, fn func(key string, modified time.Time) error) error {
	lctx, cancel := context.WithCancel(ctx)
	defer cancel() // stops the listing goroutine of minio-go when fn ends early
	for obj := range s.client.ListObjects(lctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true, StartAfter: startAfter}) {
		if obj.Err != nil {
			return obj.Err
		}
		if strings.HasSuffix(obj.Key, "/") {
			continue
		}
		if err := fn(obj.Key, obj.LastModified); err != nil {
			if errors.Is(err, ErrStopList) {
				return nil
			}
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
