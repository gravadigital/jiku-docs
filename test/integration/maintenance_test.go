package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gravadigital/jiku-docs/internal/blob"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/service"
	tu "github.com/gravadigital/jiku-docs/internal/testutil"
)

func TestPurges(t *testing.T) {
	e := tu.NewEnv(t, tu.Options{NoJanitor: true})
	ctx := context.Background()
	svc := e.Inst.Svc
	f := writable(e)
	doc := e.Doc(tu.User, f, "viejo.md", "contenido")
	var key string
	if err := e.DB.QueryRow(ctx, `SELECT storage_key FROM document_versions WHERE document_id = $1`, doc).Scan(&key); err != nil {
		t.Fatal(err)
	}
	e.OK(e.Cmd(tu.User, "documents."+doc+".delete", map[string]any{"baseVersion": 1}))
	// Within 30 days nothing happens; after, the document and its objects are gone for good.
	if n, err := svc.PurgeDeleted(ctx); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if _, err := e.DB.Exec(ctx, `UPDATE documents SET deleted_at = now() - interval '31 days' WHERE id = $1`, doc); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.PurgeDeleted(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, err := e.Blob.Get(ctx, key, 0); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("object must be gone from docs/: %v", err)
	}
	if _, err := e.Blob.Get(ctx, blob.TrashKey(key), 0); err != nil {
		t.Fatalf("the object of the purged document waits in trash/: %v", err)
	}
	e.Fail(e.Cmd(tu.User, "documents."+doc+".restore", map[string]any{"fromVersion": 1, "baseVersion": 1}), protocol.CodeDocumentNotFound)

	// Orphan objects (a failed transaction) older than the grace period.
	if err := e.Blob.Put(ctx, "docs/HUERFANO/1-abc.md", []byte("x"), "text/markdown"); err != nil {
		t.Fatal(err)
	}
	svc.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if n := sweepOrphans(t, svc); n != 1 {
		t.Fatal(n)
	}
	// The orphan went to trash/, not deleted; the sweep never touches the trash.
	if _, err := e.Blob.Get(ctx, "docs/HUERFANO/1-abc.md", 0); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("orphan still under docs/: %v", err)
	}
	if b, err := e.Blob.Get(ctx, blob.TrashKey("docs/HUERFANO/1-abc.md"), 0); err != nil || string(b) != "x" {
		t.Fatalf("orphan must be in trash/: %q %v", b, err)
	}
	if n := sweepOrphans(t, svc); n != 0 {
		t.Fatalf("trash/ is not swept (%d)", n)
	}
	live := e.Doc(tu.User, f, "vivo.md", "vivo")
	if n := sweepOrphans(t, svc); n != 0 {
		t.Fatalf("live versions are never orphans (%d)", n)
	}
	svc.Now = protocol.Now

	// Pending attachments after 24 h, uploads after their expiry, share data after their windows.
	e.OK(e.Cmd(tu.User, "attachments.request-upload", map[string]any{"id": tu.ID(), "documentId": live, "fileName": "a.png",
		"mimeType": "image/png", "fileSize": 1, "checksum": tu.Sum("a")}))
	if _, err := e.DB.Exec(ctx, `UPDATE attachments SET created_at = now() - interval '25 hours'`); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.PurgePendingAttachments(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	up := e.OK(e.Cmd(tu.User, "documents.request-content-upload", map[string]any{"checksum": tu.Sum("z"), "size": 1}))
	put(t, up["uploadUrl"].(string), []byte("z"))
	if _, err := e.DB.Exec(ctx, `UPDATE content_uploads SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.PurgeUploads(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, err := e.Blob.Get(ctx, "uploads/"+up["contentUploadId"].(string), 0); !errors.Is(err, blob.ErrNotFound) {
		t.Fatal("upload object must be deleted")
	}
	if err := svc.PurgeShareData(ctx); err != nil {
		t.Fatal(err)
	}
	// Changelog older than 30 days, once published, goes; the watermark makes old cursors invalid.
	e.Eventually("published", func() bool {
		var n int
		_ = e.DB.QueryRow(ctx, `SELECT count(*) FROM changelog WHERE published_at IS NULL`).Scan(&n)
		return n == 0
	})
	if _, err := e.DB.Exec(ctx, `UPDATE changelog SET occurred_at = now() - interval '31 days' WHERE seq <= 3`); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.PurgeChangelog(ctx); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	e.Fail(e.Q(tu.User, "documents.changes", map[string]any{"sinceSeq": 1}), protocol.CodeInvalidCursor)
	e.OK(e.Q(tu.User, "documents.changes", map[string]any{"sinceSeq": 3}))
}

func TestRelationReview(t *testing.T) {
	e := tu.NewEnv(t, tu.Options{NoJanitor: true})
	ctx := context.Background()
	f := writable(e)
	content := "---\ntitle: Con relaciones # se conserva\njiku:\n  projects: [12, 31]\n  tasks: [900]\n---\n# Cuerpo\n"
	doc := e.Doc(tu.User, f, "rel.md", content)
	// Project 31 and task 900 disappear from Jiku.
	delete(e.Core.Projects, 31)
	delete(e.Core.Tasks, 900)
	n, err := e.Inst.Svc.ReviewRelations(ctx)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	g := e.OK(e.Q(tu.User, "documents.get", map[string]any{"id": doc, "include": []string{"content", "relations"}}))
	rel := g["relations"].(map[string]any)
	if len(rel["projects"].([]any)) != 1 || len(rel["tasks"].([]any)) != 0 || g["version"].(float64) != 2 {
		t.Fatal(g)
	}
	if !strings.Contains(g["content"].(string), "# Cuerpo") || g["title"] != "Con relaciones" {
		t.Fatal(g["content"])
	}
	v := e.Q(tu.User, "documents.versions.list", map[string]any{"filter": map[string]any{"documentId": doc}}).Items()[0]
	if v["author"] != tu.DocsUser || v["via"] != "service" || !strings.Contains(v["message"].(string), "projects 31") {
		t.Fatal(v)
	}
	ev := e.WaitEvent("document.updated", nil)
	if ev.Body["changes"].(map[string]any)["relations"] == nil {
		t.Fatal(ev.Body)
	}
	if n, _ := e.Inst.Svc.ReviewRelations(ctx); n != 0 {
		t.Fatal("second run has nothing to do")
	}
}

// sweepOrphans runs a whole orphan sweep (what the janitor spreads over a week, a page per
// minute) and returns the objects it deleted.
func sweepOrphans(t *testing.T, svc *service.Service) int {
	t.Helper()
	ctx := context.Background()
	if err := svc.StartOrphanSweep(ctx); err != nil {
		t.Fatal(err)
	}
	total := 0
	for i := 0; i < 100; i++ {
		n, done, err := svc.PurgeOrphans(ctx)
		if err != nil {
			t.Fatal(err)
		}
		total += n
		if done {
			return total
		}
	}
	t.Fatal("the sweep never ends")
	return 0
}

// The orphan sweep is paginated: the cursor in service_state survives between pages, and an
// object recorded in pruned_objects is left to the compaction.
func TestOrphanSweepPaginated(t *testing.T) {
	e := tu.NewEnv(t, tu.Options{NoJanitor: true})
	ctx := context.Background()
	svc := e.Inst.Svc
	for i := 0; i < service.OrphanPage+5; i++ {
		if err := e.Blob.Put(ctx, fmt.Sprintf("docs/HUERFANO/%04d.md", i), []byte("x"), "text/markdown"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.DB.Exec(ctx, `INSERT INTO pruned_objects (storage_key, document_id, version) VALUES ('docs/HUERFANO/0000.md', 'X', 2)`); err != nil {
		t.Fatal(err)
	}
	svc.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	defer func() { svc.Now = protocol.Now }()
	if n, done, err := svc.PurgeOrphans(ctx); n != 0 || !done || err != nil {
		t.Fatalf("no sweep in progress: %d %v %v", n, done, err)
	}
	if err := svc.StartOrphanSweep(ctx); err != nil {
		t.Fatal(err)
	}
	n, done, err := svc.PurgeOrphans(ctx)
	if err != nil || done || n != service.OrphanPage-1 {
		t.Fatalf("first page: %d %v %v", n, done, err)
	}
	var cursor string
	if err := e.DB.QueryRow(ctx, `SELECT value->>'after' FROM service_state WHERE key = 'orphan_sweep'`).Scan(&cursor); err != nil ||
		cursor != fmt.Sprintf("docs/HUERFANO/%04d.md", service.OrphanPage-1) {
		t.Fatalf("cursor %q %v", cursor, err)
	}
	if rest := sweepOrphans(t, svc); rest != 5 {
		t.Fatal(rest)
	}
	if _, err := e.Blob.Get(ctx, "docs/HUERFANO/0000.md", 0); err != nil {
		t.Fatal("the object waiting in pruned_objects is not the sweep's")
	}
}
