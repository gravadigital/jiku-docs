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
	// CopyToTrash copies key to papelera/<key> (with meta as user metadata, if any); ErrNotFound
	// when key does not exist. Deleting the original is the caller's next step.
	CopyToTrash(ctx context.Context, key string, meta map[string]string) error
	// RestoreFromTrash copies papelera/<key> back to key (never over an existing object) and
	// returns the trash copy's user metadata.
	RestoreFromTrash(ctx context.Context, key string) (meta map[string]string, restored bool, err error)
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

// TrashPrefix holds every content object the service removes (history compaction, orphan sweep,
// purge of deleted documents): an object is copied to papelera/<original key> and only then
// deleted, and a lifecycle rule expires the trash after DOCS_TRASH_RETENTION_DAYS. It replaces
// the bucket versioning as the safety net: `jiku-docs storage restore-from-trash` brings an
// object back while it is there.
const TrashPrefix = "papelera/"

// TrashKey is where key lands in the trash.
func TrashKey(key string) string { return TrashPrefix + key }

// Lifecycle rule ids, also what `storage init` checks.
const (
	RuleUploads = "jiku-docs-uploads"
	RuleTrash   = "jiku-docs-trash"
	// DefaultTrashDays is the default of DOCS_TRASH_RETENTION_DAYS.
	DefaultTrashDays = 7
)

// EnsureLifecycle sets the lifecycle rules of the bucket (it replaces the whole configuration).
// Both are expirations by age under a prefix, which every S3-compatible provider supports
// (DigitalOcean Spaces included):
//   - the large uploads in transit (`uploads/`) expire after one day, as a safety net under the
//     janitor's purge;
//   - the trash (`papelera/`) expires after trashDays: an object removed by the service can be
//     restored during that time, and its space is really freed afterwards.
func (s *S3) EnsureLifecycle(ctx context.Context, trashDays int) error {
	if trashDays < 1 {
		return fmt.Errorf("blob: trash retention must be at least 1 day, got %d", trashDays)
	}
	cfg := lifecycle.NewConfiguration()
	cfg.Rules = []lifecycle.Rule{
		{
			ID:         RuleUploads,
			Status:     "Enabled",
			RuleFilter: lifecycle.Filter{Prefix: "uploads/"},
			Expiration: lifecycle.Expiration{Days: 1},
		},
		{
			ID:         RuleTrash,
			Status:     "Enabled",
			RuleFilter: lifecycle.Filter{Prefix: TrashPrefix},
			Expiration: lifecycle.Expiration{Days: lifecycle.ExpirationDays(trashDays)},
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
func (s *S3) CheckLifecycle(ctx context.Context, trashDays int) error {
	rules, err := s.Lifecycle(ctx)
	if err != nil {
		return err
	}
	var uploads, trash bool
	for _, r := range rules {
		switch {
		case r.ID == RuleUploads && r.Enabled && r.Prefix == "uploads/" && r.ExpirationDays == 1:
			uploads = true
		case r.ID == RuleTrash && r.Enabled && r.Prefix == TrashPrefix && r.ExpirationDays == trashDays:
			trash = true
		}
	}
	if !uploads || !trash {
		return fmt.Errorf("blob: lifecycle rules missing (uploads/ after 1 day: %v; %s after %d days: %v)",
			uploads, TrashPrefix, trashDays, trash)
	}
	return nil
}

// VersioningStatus reports the versioning state of the bucket: "Enabled", "Suspended" or ""
// (never enabled).
func (s *S3) VersioningStatus(ctx context.Context) (string, error) {
	v, err := s.client.GetBucketVersioning(ctx, s.bucket)
	if err != nil {
		return "", err
	}
	return v.Status, nil
}

// SuspendVersioning stops keeping new versions. The versions the bucket already holds stay
// until someone deletes them: suspending does not remove them.
func (s *S3) SuspendVersioning(ctx context.Context) error {
	return s.client.SuspendVersioning(ctx, s.bucket)
}

// EnableVersioning turns versioning on (only tests use it: the content bucket runs without).
func (s *S3) EnableVersioning(ctx context.Context) error {
	return s.client.EnableVersioning(ctx, s.bucket)
}

// EnsureBucket creates the bucket if missing. Used by tests and by `storage init`.
func (s *S3) EnsureBucket(ctx context.Context, region string) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return err
	}
	if !exists {
		return s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: region})
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

// missingKey is a source object that does not exist (not a missing bucket).
func missingKey(err error) bool {
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || (resp.StatusCode == 404 && resp.Code != "NoSuchBucket")
}

// CopyToTrash copies key to papelera/<key>; the caller deletes the original afterwards (in a
// DeleteObjects batch). With meta, the copy carries it as user metadata (the source's content
// type is kept); without, it keeps the source's metadata. ErrNotFound: the original is gone.
// Copying again over an existing trash copy is harmless: same content.
func (s *S3) CopyToTrash(ctx context.Context, key string, meta map[string]string) error {
	dst := minio.CopyDestOptions{Bucket: s.bucket, Object: TrashKey(key)}
	if meta != nil {
		info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
		if err != nil {
			if missingKey(err) {
				return ErrNotFound
			}
			return err
		}
		dst.ReplaceMetadata, dst.UserMetadata, dst.ContentType = true, meta, info.ContentType
	}
	_, err := s.client.CopyObject(ctx, dst, minio.CopySrcOptions{Bucket: s.bucket, Object: key})
	if err != nil && missingKey(err) {
		return ErrNotFound
	}
	return err
}

// RestoreFromTrash copies papelera/<key> back to key and returns the user metadata of the trash
// copy (keys in lower case, without the x-amz-meta- prefix). The trash copy stays until the
// lifecycle expires it. If key already exists it is not overwritten (restored = false).
// ErrNotFound: the object is not in the trash (never was, or expired).
func (s *S3) RestoreFromTrash(ctx context.Context, key string) (meta map[string]string, restored bool, err error) {
	info, err := s.client.StatObject(ctx, s.bucket, TrashKey(key), minio.StatObjectOptions{})
	if err != nil {
		if missingKey(err) {
			return nil, false, ErrNotFound
		}
		return nil, false, err
	}
	meta = map[string]string{}
	for k, v := range info.UserMetadata {
		meta[strings.ToLower(strings.TrimPrefix(strings.ToLower(k), "x-amz-meta-"))] = v
	}
	if _, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{}); err == nil {
		return meta, false, nil
	} else if !missingKey(err) {
		return nil, false, err
	}
	// The restored object gets only its content type back: the trash metadata stays in the trash.
	_, err = s.client.CopyObject(ctx,
		minio.CopyDestOptions{Bucket: s.bucket, Object: key, ReplaceMetadata: true, UserMetadata: map[string]string{},
			ContentType: info.ContentType},
		minio.CopySrcOptions{Bucket: s.bucket, Object: TrashKey(key)})
	if err != nil {
		return nil, false, err
	}
	return meta, true, nil
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
