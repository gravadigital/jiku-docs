package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/gravadigital/jiku-docs/internal/blob"
	"github.com/gravadigital/jiku-docs/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Terminate()
	os.Exit(code)
}

func newBucket(t *testing.T) *blob.S3 {
	t.Helper()
	if testutil.SkipIntegration() {
		t.Skip("JIKU_DOCS_SKIP_INTEGRATION=1")
	}
	c, err := testutil.Shared()
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	bs, err := blob.New(blob.Config{Endpoint: c.MinIOEndpoint, Region: "us-east-1", Bucket: "init-" + hex.EncodeToString(b),
		AccessKey: c.MinIOUser, SecretKey: c.MinIOPassword, PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	return bs
}

// checkRules verifies the two lifecycle rules of `storage init`, and nothing else: no rule on
// non-current versions any more.
func checkRules(t *testing.T, bs *blob.S3, trashDays int) {
	t.Helper()
	ctx := context.Background()
	if err := bs.CheckLifecycle(ctx, trashDays); err != nil {
		t.Fatal(err)
	}
	rules, err := bs.Lifecycle(ctx)
	if err != nil || len(rules) != 2 {
		t.Fatalf("expected exactly two rules: %+v %v", rules, err)
	}
	for _, r := range rules {
		if r.NoncurrentDays != 0 {
			t.Fatalf("no rule may expire non-current versions: %+v", r)
		}
	}
}

// `jiku-docs storage init` on a new bucket: created without versioning, with the rules of
// uploads/ (1 day) and trash/ (DOCS_TRASH_RETENTION_DAYS). Running it twice changes nothing.
func TestStorageInitNewBucket(t *testing.T) {
	bs := newBucket(t)
	ctx := context.Background()
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, nil))
	for i := 0; i < 2; i++ {
		if err := initStorage(ctx, bs, "us-east-1", "nuevo", 7, log); err != nil {
			t.Fatal(err)
		}
	}
	if st, err := bs.VersioningStatus(ctx); err != nil || st == "Enabled" {
		t.Fatalf("versioning must stay off: %q %v", st, err)
	}
	checkRules(t, bs, 7)
	if strings.Contains(out.String(), "suspended") {
		t.Fatalf("nothing to suspend on a new bucket:\n%s", out.String())
	}
	// Another retention replaces the rule.
	if err := initStorage(ctx, bs, "us-east-1", "nuevo", 3, log); err != nil {
		t.Fatal(err)
	}
	checkRules(t, bs, 3)
}

// `storage init` on a bucket created with versioning (what it did before the trash): the
// versioning is suspended, the log says so and warns that the old versions stay, and the rules
// are replaced (the non-current versions rule goes).
func TestStorageInitSuspendsVersioning(t *testing.T) {
	bs := newBucket(t)
	ctx := context.Background()
	if err := bs.EnsureBucket(ctx, "us-east-1"); err != nil {
		t.Fatal(err)
	}
	if err := bs.EnableVersioning(ctx); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, nil))
	if err := initStorage(ctx, bs, "us-east-1", "versionado", 7, log); err != nil {
		t.Fatal(err)
	}
	if st, err := bs.VersioningStatus(ctx); err != nil || st != "Suspended" {
		t.Fatalf("versioning must be suspended: %q %v", st, err)
	}
	if !strings.Contains(out.String(), "bucket versioning suspended") || !strings.Contains(out.String(), "NOT removed by themselves") {
		t.Fatalf("the log must say it suspended the versioning and that old versions stay:\n%s", out.String())
	}
	checkRules(t, bs, 7)
	out.Reset()
	if err := initStorage(ctx, bs, "us-east-1", "versionado", 7, log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already suspended") {
		t.Fatalf("second run:\n%s", out.String())
	}
}
