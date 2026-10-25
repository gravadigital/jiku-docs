package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gravadigital/jiku-docs/internal/blob"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/service"
	tu "github.com/gravadigital/jiku-docs/internal/testutil"
)

// `jiku-docs storage restore-from-trash`: a version compacted by mistake comes back, object and
// row, from papelera/. The row it puts back is protected, so the next compaction leaves it.
func TestRestoreFromTrash(t *testing.T) {
	h := newHist(t)
	ctx := context.Background()
	start := histNow.Add(-72 * time.Hour)
	id := h.doc(tu.User, start.Add(-time.Hour))
	h.burst(id, tu.User, "agent", start, 4, time.Minute)
	h.save(id, tu.User, "", "", histNow.Add(-time.Hour))
	var before struct {
		key, checksum, title, author, via string
		size                              int
		created                           time.Time
	}
	if err := h.e.DB.QueryRow(ctx, `SELECT storage_key, checksum, title, author, via, size_bytes, created_at FROM document_versions
		WHERE document_id = $1 AND version = 3`, id).Scan(&before.key, &before.checksum, &before.title, &before.author, &before.via,
		&before.size, &before.created); err != nil {
		t.Fatal(err)
	}
	h.compact(histNow, service.HistoryOptions{})
	h.expect(id, 1, 5, 6)
	if h.objectExists(before.key) || !h.objectExists(blob.TrashKey(before.key)) {
		t.Fatal("the object of version 3 is in the trash")
	}

	// The object alone (also accepted with the papelera/ prefix).
	res, err := h.svc.RestoreFromTrash(ctx, blob.TrashKey(before.key), false)
	if err != nil || !res.ObjectRestored || res.RowRestored || res.Key != before.key {
		t.Fatalf("%+v %v", res, err)
	}
	if !h.objectExists(before.key) || !h.objectExists(blob.TrashKey(before.key)) {
		t.Fatal("the object is back, and the trash copy stays until it expires")
	}
	h.expect(id, 1, 5, 6)

	// Now the row: the version is back in the history, with its metadata, protected.
	res, err = h.svc.RestoreFromTrash(ctx, before.key, true)
	if err != nil || res.ObjectRestored || !res.RowRestored || res.DocumentID != id || res.Version != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	h.expect(id, 1, 3, 5, 6)
	var got struct {
		checksum, title, author, via, kind string
		size                               int
		created                            time.Time
		protected                          bool
	}
	if err := h.e.DB.QueryRow(ctx, `SELECT checksum, title, author, via, kind, size_bytes, created_at, protected FROM document_versions
		WHERE document_id = $1 AND version = 3`, id).Scan(&got.checksum, &got.title, &got.author, &got.via, &got.kind, &got.size,
		&got.created, &got.protected); err != nil {
		t.Fatal(err)
	}
	if got.checksum != before.checksum || got.title != before.title || got.author != before.author || got.via != "agent" ||
		got.kind != "update" || got.size != before.size || !got.created.Equal(before.created) || !got.protected {
		t.Fatalf("restored row %+v, original %+v", got, before)
	}
	g := h.e.OK(h.e.Q(tu.User, "documents.get", map[string]any{"id": id, "version": 3, "include": []string{"content"}}))
	if g["prunedVersionsCount"] != float64(2) || g["content"] == nil {
		t.Fatalf("%v", g)
	}
	// Idempotent.
	if res, err := h.svc.RestoreFromTrash(ctx, before.key, true); err != nil || res.ObjectRestored || res.RowRestored {
		t.Fatalf("%+v %v", res, err)
	}
	// The next compaction (new activity) leaves it: it is protected now.
	h.save(id, tu.User, "", "", histNow.Add(2*time.Hour))
	h.compact(histNow.Add(3*time.Hour), service.HistoryOptions{})
	h.expect(id, 1, 3, 5, 6, 7)

	// Not in the trash: a clear error.
	if _, err := h.svc.RestoreFromTrash(ctx, "docs/"+id+"/99-nada.md", false); !errors.Is(err, service.ErrNotInTrash) {
		t.Fatal(err)
	}
	// An orphan moved by the sweep carries no version row: the object comes back, the row cannot.
	orphan := "docs/HUERFANO/1-abc.md"
	if err := h.e.Blob.Put(ctx, orphan, []byte("x"), "text/markdown"); err != nil {
		t.Fatal(err)
	}
	h.svc.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if n := sweepOrphans(t, h.svc); n != 1 {
		t.Fatal(n)
	}
	h.svc.Now = protocol.Now
	if res, err := h.svc.RestoreFromTrash(ctx, orphan, true); err == nil || res == nil || !res.ObjectRestored || res.RowRestored {
		t.Fatalf("%+v %v", res, err)
	}
	if !h.objectExists(orphan) {
		t.Fatal("the orphan object is back")
	}
}
