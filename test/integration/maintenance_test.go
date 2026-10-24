package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gravadigital/jiku-docs/internal/blob"
	"github.com/gravadigital/jiku-docs/internal/protocol"
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
		t.Fatalf("object must be deleted: %v", err)
	}
	e.Fail(e.Cmd(tu.User, "documents."+doc+".restore", map[string]any{"fromVersion": 1, "baseVersion": 1}), protocol.CodeDocumentNotFound)

	// Orphan objects (a failed transaction) older than the grace period.
	if err := e.Blob.Put(ctx, "docs/HUERFANO/1-abc.md", []byte("x"), "text/markdown"); err != nil {
		t.Fatal(err)
	}
	svc.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if n, err := svc.PurgeOrphans(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	live := e.Doc(tu.User, f, "vivo.md", "vivo")
	if n, _ := svc.PurgeOrphans(ctx); n != 0 {
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
