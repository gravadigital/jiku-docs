package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/gravadigital/jiku-docs/internal/blob"
	"github.com/gravadigital/jiku-docs/internal/logging"
	"github.com/gravadigital/jiku-docs/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Terminate()
	os.Exit(code)
}

// `jiku-docs storage init` against a real MinIO: versioning on, and the lifecycle rule that
// expires non-current versions after 7 days (the safety net of the history compaction) together
// with the one of uploads/. Running it twice changes nothing.
func TestStorageInitLifecycle(t *testing.T) {
	if testutil.SkipIntegration() {
		t.Skip("JIKU_DOCS_SKIP_INTEGRATION=1")
	}
	c, err := testutil.Shared()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	bucket := "init-" + hex.EncodeToString(b)
	bs, err := blob.New(blob.Config{Endpoint: c.MinIOEndpoint, Region: "us-east-1", Bucket: bucket,
		AccessKey: c.MinIOUser, SecretKey: c.MinIOPassword, PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := initStorage(ctx, bs, "us-east-1", bucket, logging.Discard()); err != nil {
			t.Fatal(err)
		}
	}
	if on, err := bs.Versioning(ctx); err != nil || !on {
		t.Fatalf("versioning must be on: %v %v", on, err)
	}
	if err := bs.CheckLifecycle(ctx); err != nil {
		t.Fatal(err)
	}
	rules, err := bs.Lifecycle(ctx)
	if err != nil || len(rules) != 2 {
		t.Fatalf("expected exactly two rules: %+v %v", rules, err)
	}
	for _, r := range rules {
		if r.ID == blob.RuleNoncurrent && (r.NoncurrentDays != 7 || r.ExpirationDays != 0 || r.Prefix != "") {
			t.Fatalf("the non-current rule must only expire old versions, of every object: %+v", r)
		}
	}
	// A deleted object (what the compaction does) keeps its data as a non-current version, which
	// the rule expires a week later.
	key := "docs/01TEST/1-abc.md"
	if err := bs.Put(ctx, key, []byte("v1"), "text/markdown"); err != nil {
		t.Fatal(err)
	}
	if deleted, err := bs.DeleteMany(ctx, []string{key, "docs/01TEST/missing.md"}); err != nil || len(deleted) != 2 {
		t.Fatalf("both keys are gone (one never existed): %v %v", deleted, err)
	}
	versions, markers, err := bs.ObjectVersions(ctx, key)
	if err != nil || versions != 1 || markers != 1 {
		t.Fatalf("expected the old version and a delete marker, got %d versions, %d markers (%v)", versions, markers, err)
	}
}
