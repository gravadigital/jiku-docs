package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gravadigital/jiku-docs/internal/blob"
	"github.com/gravadigital/jiku-docs/internal/logging"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/service"
	"github.com/gravadigital/jiku-docs/internal/store"
	tu "github.com/gravadigital/jiku-docs/internal/testutil"
)

// The cases of section 7 of docs/compactacion-historial-v1.md. Every test runs the service
// with a fixed clock: versions are written through the bus at chosen moments, and the
// compaction runs "at" histNow (03:00 in Buenos Aires).

var histNow = time.Date(2026, 10, 25, 6, 0, 0, 0, time.UTC)

// clock is the injectable `now` of the service, safe to move between requests.
type clock struct{ ns atomic.Int64 }

func (c *clock) Set(t time.Time)     { c.ns.Store(t.UnixNano()) }
func (c *clock) Now() time.Time      { return time.Unix(0, c.ns.Load()).UTC() }
func (c *clock) Add(d time.Duration) { c.Set(c.Now().Add(d)) }

type hist struct {
	t      *testing.T
	e      *tu.Env
	svc    *service.Service
	clk    *clock
	folder string
	rev    int
	cur    map[string]int
}

func newHist(t *testing.T) *hist {
	e := tu.NewEnv(t, tu.Options{NoJanitor: true})
	h := &hist{t: t, e: e, svc: e.Inst.Svc, clk: &clock{}, cur: map[string]int{}}
	h.clk.Set(histNow)
	h.svc.Now = h.clk.Now
	h.folder = writable(e)
	return h
}

func (h *hist) content() string {
	h.rev++
	return fmt.Sprintf("# Documento\n\nrevisión %d\n", h.rev)
}

// doc creates a document as user at `at`.
func (h *hist) doc(user string, at time.Time) string {
	h.t.Helper()
	h.clk.Set(at)
	id := tu.ID()
	c := h.content()
	h.e.OK(h.e.Cmd(user, "documents.new", map[string]any{"id": id, "folderId": h.folder, "fileName": id + ".md",
		"content": c, "checksum": tu.Sum(c)}))
	h.cur[id] = 1
	return id
}

// save writes one version as user at `at`; message "" means none. It returns the version.
func (h *hist) save(id, user, via, message string, at time.Time) int {
	h.t.Helper()
	h.clk.Set(at)
	c := h.content()
	p := map[string]any{"baseVersion": h.cur[id], "content": c, "checksum": tu.Sum(c)}
	if via != "" {
		p["via"] = via
	}
	if message != "" {
		p["message"] = message
	}
	r := h.e.OK(h.e.Cmd(user, "documents."+id+".update", p))
	h.cur[id] = int(r["version"].(float64))
	return h.cur[id]
}

// burst writes n versions every step from `from`; it returns the first and the last.
func (h *hist) burst(id, user, via string, from time.Time, n int, step time.Duration) (int, int) {
	h.t.Helper()
	first := 0
	for i := 0; i < n; i++ {
		v := h.save(id, user, via, "", from.Add(time.Duration(i)*step))
		if first == 0 {
			first = v
		}
	}
	return first, h.cur[id]
}

func (h *hist) versions(id string) []int {
	h.t.Helper()
	rows, err := h.e.DB.Query(context.Background(), `SELECT version FROM document_versions WHERE document_id = $1 ORDER BY version`, id)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func (h *hist) keys(id string) map[int]string {
	h.t.Helper()
	rows, err := h.e.DB.Query(context.Background(), `SELECT version, storage_key FROM document_versions WHERE document_id = $1`, id)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var v int
		var k string
		if err := rows.Scan(&v, &k); err != nil {
			h.t.Fatal(err)
		}
		out[v] = k
	}
	return out
}

// compact runs the compaction at `at`.
func (h *hist) compact(at time.Time, opts service.HistoryOptions) *service.HistoryReport {
	h.t.Helper()
	h.clk.Set(at)
	rep, err := h.svc.CompactHistory(context.Background(), opts)
	if err != nil {
		h.t.Fatal(err)
	}
	if rep.Skipped {
		h.t.Fatal("compaction skipped: lock held")
	}
	return rep
}

func (h *hist) expect(id string, want ...int) {
	h.t.Helper()
	got := h.versions(id)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		h.t.Fatalf("versions of %s: got %v, want %v", id, got, want)
	}
}

func span(from, to int) []int {
	var out []int
	for v := from; v <= to; v++ {
		out = append(out, v)
	}
	return out
}

func (h *hist) list(prefix string) []string {
	h.t.Helper()
	var out []string
	if err := h.e.Blob.List(context.Background(), prefix, func(key string, _ time.Time) error {
		out = append(out, key)
		return nil
	}); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *hist) objectExists(key string) bool {
	h.t.Helper()
	_, err := h.e.Blob.Get(context.Background(), key, 0)
	if err != nil && !errors.Is(err, blob.ErrNotFound) {
		h.t.Fatal(err)
	}
	return err == nil
}

// Case 1: 300 versions of one author in 2 hours, all younger than 24 h: nothing goes.
func TestHistory01RecentVersionsStay(t *testing.T) {
	h := newHist(t)
	id := h.doc(tu.User, histNow.Add(-3*time.Hour))
	h.burst(id, tu.User, "", histNow.Add(-2*time.Hour), 300, 24*time.Second)
	rep := h.compact(histNow, service.HistoryOptions{})
	if rep.VersionsPruned != 0 || rep.Documents != 1 {
		t.Fatalf("%+v", rep)
	}
	h.expect(id, span(1, 301)...)
}

// Case 2: the same, 3 days ago: only the last of the block and the current one stay (and the
// creation, always protected). The objects go to papelera/ (nothing of them stays under docs/),
// and the count is reported.
func TestHistory02OldBlockKeepsItsLast(t *testing.T) {
	h := newHist(t)
	start := histNow.Add(-72 * time.Hour)
	id := h.doc(tu.User, start.Add(-time.Hour))
	_, last := h.burst(id, tu.User, "", start, 300, 24*time.Second)
	cur := h.save(id, tu.User, "", "", histNow.Add(-time.Hour))
	keys := h.keys(id)
	rep := h.compact(histNow, service.HistoryOptions{})
	h.expect(id, 1, last, cur)
	if rep.VersionsPruned != 299 || rep.ObjectsTrashed != 299 || rep.ObjectsPending != 0 || rep.BytesFreed <= 0 {
		t.Fatalf("%+v", rep)
	}
	if h.objectExists(keys[2]) || h.objectExists(keys[last-1]) || !h.objectExists(keys[last]) || !h.objectExists(keys[1]) {
		t.Fatal("the objects of the pruned versions go, the others stay")
	}
	if left := h.list("docs/" + id + "/"); len(left) != 3 {
		t.Fatalf("only the kept versions stay under docs/: %d objects", len(left))
	}
	trash := h.list(blob.TrashPrefix + "docs/" + id + "/")
	if len(trash) != 299 || !h.objectExists(blob.TrashKey(keys[2])) || h.objectExists(blob.TrashKey(keys[last])) {
		t.Fatalf("the pruned objects are in papelera/: %d", len(trash))
	}
	g := h.e.OK(h.e.Q(tu.User, "documents.get", map[string]any{"id": id}))
	if g["prunedVersionsCount"] != float64(299) {
		t.Fatalf("prunedVersionsCount: %v", g)
	}
	// Version list: holes in the numbering, kind and protected.
	items := h.e.Q(tu.User, "documents.versions.list", map[string]any{"filter": map[string]any{"documentId": id}, "sort": []string{"version"}}).Items()
	if len(items) != 3 || items[0]["kind"] != "create" || items[0]["protected"] != true || items[1]["kind"] != "update" ||
		items[1]["protected"] != false || items[1]["version"] != float64(last) {
		t.Fatalf("%v", items)
	}
	// meta.describe publishes the new fields and filters.
	d := h.e.OK(h.e.Q(tu.User, "meta.describe", map[string]any{"resources": []string{"documents.versions"}}))
	res := d["resources"].(map[string]any)["documents.versions"].(map[string]any)
	base, filt, enums := res["base"].(map[string]any), res["filterable"].(map[string]any), res["enums"].(map[string]any)
	if base["kind"] == nil || base["protected"] == nil || filt["protected"].(map[string]any)["kind"] != "boolean" ||
		filt["kind"].(map[string]any)["enum"] != "kind" || len(enums["kind"].([]any)) != 3 {
		t.Fatalf("meta.describe: %v", res)
	}
}

// Case 3: Ana, Bruno and Ana again the same day, 3 days ago: the last of each block.
func TestHistory03AuthorsMakeBlocks(t *testing.T) {
	h := newHist(t)
	day := histNow.Add(-72 * time.Hour)
	id := h.doc(tu.User, day.Add(-2*time.Hour))
	_, a1 := h.burst(id, tu.User, "", day, 10, time.Minute)
	_, b := h.burst(id, tu.User2, "", day.Add(15*time.Minute), 10, time.Minute)
	_, a2 := h.burst(id, tu.User, "", day.Add(30*time.Minute), 10, time.Minute)
	h.compact(histNow, service.HistoryOptions{})
	h.expect(id, 1, a1, b, a2)
}

// Case 4: one author with a 31-minute pause: two blocks.
func TestHistory04PauseClosesBlock(t *testing.T) {
	h := newHist(t)
	day := histNow.Add(-72 * time.Hour)
	id := h.doc(tu.User, day.Add(-2*time.Hour))
	_, x := h.burst(id, tu.User, "", day, 5, time.Minute)                        // ends at +4 min
	_, y := h.burst(id, tu.User, "", day.Add(35*time.Minute), 5, time.Minute)    // 31 min later
	_, z := h.burst(id, tu.User, "", day.Add(80*time.Minute), 5, 29*time.Minute) // gaps of 29 min: one block
	h.compact(histNow, service.HistoryOptions{})
	h.expect(id, 1, x, y, z)
}

// Case 5: the same author alternating human and agent without pauses: every change of via
// closes a block, and agent versions are compacted like human ones (no protection by origin).
func TestHistory05AgentFollowsBlocks(t *testing.T) {
	h := newHist(t)
	day := histNow.Add(-72 * time.Hour)
	id := h.doc(tu.User, day.Add(-2*time.Hour))
	var lasts []int
	for i, via := range []string{"human", "agent", "human", "agent"} {
		_, last := h.burst(id, tu.User, via, day.Add(time.Duration(i*3)*time.Minute), 3, time.Minute)
		lasts = append(lasts, last)
	}
	h.compact(histNow, service.HistoryOptions{})
	h.expect(id, append([]int{1}, lasts...)...)
	if v := h.versions(id); len(v) != 5 {
		t.Fatal(v)
	}
}

// Case 6: several blocks on one day, 40 days ago: the last of the day.
func TestHistory06OldDayKeepsItsLast(t *testing.T) {
	h := newHist(t)
	day := histNow.AddDate(0, 0, -40)
	id := h.doc(tu.User, day.Add(-48*time.Hour))
	h.burst(id, tu.User, "", day, 3, time.Minute)
	h.burst(id, tu.User2, "", day.Add(10*time.Minute), 3, time.Minute)
	_, last := h.burst(id, tu.User, "", day.Add(20*time.Minute), 3, time.Minute)
	cur := h.save(id, tu.User, "", "", histNow.Add(-time.Hour))
	h.compact(histNow, service.HistoryOptions{})
	h.expect(id, 1, last, cur)
}

// Case 7: a block from 25 h to 23 h ago is evaluated whole: none of its versions younger than
// 24 h goes (the older ones are not the last of their block).
func TestHistory07BlockAcrossTheDayLimit(t *testing.T) {
	h := newHist(t)
	id := h.doc(tu.User, histNow.Add(-30*time.Hour))
	first, last := h.burst(id, tu.User, "", histNow.Add(-25*time.Hour), 13, 10*time.Minute) // until -23 h
	h.compact(histNow, service.HistoryOptions{})
	var young []int
	for v := first; v <= last; v++ {
		if at := histNow.Add(-25 * time.Hour).Add(time.Duration(v-first) * 10 * time.Minute); histNow.Sub(at) < 24*time.Hour {
			young = append(young, v)
		}
	}
	if len(young) != 6 { // -23h50 .. -23h; the one at exactly -24h is not younger than 24 h
		t.Fatalf("setup: %v", young)
	}
	h.expect(id, append([]int{1}, young...)...)
}

// Cases 8 and 9: an intermediate version with a message and a restoration, 60 days ago, stay.
func TestHistory08And09MessageAndRestoreStay(t *testing.T) {
	h := newHist(t)
	day := histNow.AddDate(0, 0, -60)
	id := h.doc(tu.User, day.Add(-48*time.Hour))
	a := h.save(id, tu.User, "", "", day)
	msg := h.save(id, tu.User, "", "Versión enviada al cliente", day.Add(time.Minute))
	h.save(id, tu.User, "", "", day.Add(2*time.Minute))
	h.clk.Set(day.Add(3 * time.Minute))
	r := h.e.OK(h.e.Cmd(tu.User, "documents."+id+".restore", map[string]any{"fromVersion": a, "baseVersion": h.cur[id]}))
	restored := int(r["version"].(float64))
	h.cur[id] = restored
	last := h.save(id, tu.User, "", "", day.Add(4*time.Minute))
	cur := h.save(id, tu.User, "", "", histNow.Add(-time.Hour))
	h.compact(histNow, service.HistoryOptions{})
	h.expect(id, 1, msg, restored, last, cur)
	items := h.e.Q(tu.User, "documents.versions.list", map[string]any{"filter": map[string]any{"documentId": id, "kind": "restore"}}).Items()
	if len(items) != 1 || items[0]["version"] != float64(restored) || items[0]["protected"] != true {
		t.Fatalf("restore filter: %v", items)
	}
	prot := h.e.Q(tu.User, "documents.versions.list", map[string]any{"filter": map[string]any{"documentId": id, "protected": true},
		"sort": []string{"version"}}).Items()
	if len(prot) != 3 || prot[0]["version"] != float64(1) || prot[1]["version"] != float64(msg) {
		t.Fatalf("protected filter: %v", prot)
	}
}

// Case 10: a version pinned by an active share stays; once the share is revoked, the next
// compaction (which selects the document for that reason) removes it.
func TestHistory10PinnedShare(t *testing.T) {
	h := newHist(t)
	day := histNow.AddDate(0, 0, -60)
	id := h.doc(tu.Admin, day.Add(-48*time.Hour))
	pinned := h.save(id, tu.Admin, "", "", day)
	last := h.save(id, tu.Admin, "", "", day.Add(time.Minute))
	cur := h.save(id, tu.Admin, "", "", histNow.Add(-time.Hour))
	h.clk.Set(histNow.Add(-30 * time.Minute))
	share := tu.ID()
	h.e.OK(h.e.Cmd(tu.Admin, "shares.new", map[string]any{"id": share, "documentId": id, "mode": "pinned", "version": pinned,
		"expiresAt": protocol.FormatTime(histNow.AddDate(0, 0, 30)), "includeAttachments": false}))
	// An expired pinned share protects nothing.
	h.e.OK(h.e.Cmd(tu.Admin, "shares.new", map[string]any{"id": tu.ID(), "documentId": id, "mode": "pinned", "version": last,
		"expiresAt": protocol.FormatTime(histNow.Add(-29 * time.Minute)), "includeAttachments": false}))
	h.compact(histNow, service.HistoryOptions{})
	h.expect(id, 1, pinned, last, cur)

	h.clk.Set(histNow.Add(time.Hour))
	h.e.OK(h.e.Cmd(tu.Admin, "shares."+share+".revoke", nil))
	rep := h.compact(histNow.Add(2*time.Hour), service.HistoryOptions{})
	if rep.Documents != 1 {
		t.Fatalf("the revoked share makes the document pending: %+v", rep)
	}
	h.expect(id, 1, last, cur)
}

// Case 11: a document without changes since its last compaction is not processed.
func TestHistory11UnchangedDocumentSkipped(t *testing.T) {
	h := newHist(t)
	id := h.doc(tu.User, histNow.Add(-72*time.Hour))
	h.burst(id, tu.User, "", histNow.Add(-71*time.Hour), 5, time.Minute)
	if rep := h.compact(histNow, service.HistoryOptions{}); rep.Documents != 1 {
		t.Fatalf("%+v", rep)
	}
	if rep := h.compact(histNow.Add(time.Hour), service.HistoryOptions{}); rep.Documents != 0 {
		t.Fatalf("an unchanged document must not be processed: %+v", rep)
	}
	// New activity makes it pending again.
	h.save(id, tu.User, "", "", histNow.Add(90*time.Minute))
	if rep := h.compact(histNow.Add(2*time.Hour), service.HistoryOptions{}); rep.Documents != 1 {
		t.Fatalf("%+v", rep)
	}
}

// Case 24: the session of the day before, without later activity, is compacted by the run that
// follows its crossing of 24 h. A document whose versions crossed a threshold of the policy
// (24 h or 30 days) since its last compaction is processed again without new activity:
// otherwise the session of the day before a run would stay whole forever.
func TestHistory24SessionOfTheDayBefore(t *testing.T) {
	h := newHist(t)
	// Yesterday afternoon (15:00 to 15:09 in Buenos Aires), 12 h before the 03:00 run.
	afternoon := histNow.Add(-12 * time.Hour)
	id := h.doc(tu.User, afternoon.Add(-time.Hour))
	_, last := h.burst(id, tu.User, "", afternoon, 10, time.Minute)
	h.compact(histNow, service.HistoryOptions{})
	h.expect(id, span(1, last)...)
	if rep := h.compact(histNow.Add(2*time.Hour), service.HistoryOptions{}); rep.Documents != 0 {
		t.Fatalf("nothing crossed a threshold yet: %+v", rep)
	}
	// The next daily run: the session crossed 24 h and nobody edited the document since.
	rep := h.compact(histNow.Add(24*time.Hour), service.HistoryOptions{})
	if rep.Documents != 1 || rep.VersionsPruned != last-2 {
		t.Fatalf("the versions crossed 24 h: %+v", rep)
	}
	h.expect(id, 1, last)
	// Nothing else crosses a threshold until 30 days: the following runs skip it.
	if rep := h.compact(histNow.Add(48*time.Hour), service.HistoryOptions{}); rep.Documents != 0 {
		t.Fatalf("%+v", rep)
	}
}

// Case 12: two runs in a row: the second removes nothing.
func TestHistory12Idempotent(t *testing.T) {
	h := newHist(t)
	day := histNow.AddDate(0, 0, -45)
	id := h.doc(tu.User, day.Add(-48*time.Hour))
	for d := 0; d < 20; d++ {
		at := day.AddDate(0, 0, d)
		h.burst(id, tu.User, "", at, 3, 10*time.Minute)
		h.burst(id, tu.User2, "agent", at.Add(40*time.Minute), 3, 20*time.Minute)
	}
	first := h.compact(histNow, service.HistoryOptions{DocumentID: id})
	before := h.versions(id)
	second := h.compact(histNow, service.HistoryOptions{DocumentID: id})
	if first.VersionsPruned == 0 || second.VersionsPruned != 0 || fmt.Sprint(h.versions(id)) != fmt.Sprint(before) {
		t.Fatalf("first %+v, second %+v", first, second)
	}
}

// failingBlob cuts the run between the copy to papelera/ and the deletion of the originals.
type failingBlob struct{ blob.Store }

func (failingBlob) DeleteMany(context.Context, []string) ([]string, error) {
	return nil, errors.New("simulated S3 outage")
}

// Case 13: the process stops after the commit, with the objects already copied to papelera/
// but not deleted: the rows stay in pruned_objects (the originals too, without rows of
// versions) and the next run finishes the move. An original that disappeared meanwhile counts
// as done.
func TestHistory13ResumesObjectDeletion(t *testing.T) {
	h := newHist(t)
	ctx := context.Background()
	start := histNow.Add(-72 * time.Hour)
	id := h.doc(tu.User, start.Add(-time.Hour))
	_, last := h.burst(id, tu.User, "", start, 5, time.Minute)
	keys := h.keys(id)
	h.clk.Set(histNow)
	broken := service.New(h.e.DB, failingBlob{h.e.Blob}, h.e.Cached, logging.Discard(), h.e.Settings)
	broken.Now = h.clk.Now
	rep, err := broken.CompactHistory(ctx, service.HistoryOptions{})
	if err != nil || rep.VersionsPruned != 4 || rep.ObjectsTrashed != 0 || rep.ObjectsPending != 4 || rep.Errors != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	h.expect(id, 1, last)
	if !h.objectExists(keys[2]) || !h.objectExists(blob.TrashKey(keys[2])) {
		t.Fatal("cut between the copy and the delete: the copy is in the trash and the original waits in pruned_objects")
	}
	// The original of version 3 disappears meanwhile: its row is done anyway.
	if err := h.e.Blob.Delete(ctx, keys[3]); err != nil {
		t.Fatal(err)
	}
	rep = h.compact(histNow.Add(time.Hour), service.HistoryOptions{})
	if rep.Documents != 0 || rep.ObjectsTrashed != 3 || rep.ObjectsPending != 0 {
		t.Fatalf("%+v", rep)
	}
	for v := 2; v < last; v++ {
		if h.objectExists(keys[v]) {
			t.Fatalf("object of version %d still there", v)
		}
		if !h.objectExists(blob.TrashKey(keys[v])) {
			t.Fatalf("object of version %d is not in the trash", v)
		}
	}
}

// Case 14: two replicas at once: only the one holding the advisory lock runs.
func TestHistory14OneReplicaRuns(t *testing.T) {
	h := newHist(t)
	ctx := context.Background()
	id := h.doc(tu.User, histNow.Add(-72*time.Hour))
	h.burst(id, tu.User, "", histNow.Add(-71*time.Hour), 5, time.Minute)
	h.clk.Set(histNow)
	other := service.New(h.e.DB, h.e.Blob, h.e.Cached, logging.Discard(), h.e.Settings) // the other replica
	other.Now = h.clk.Now
	conn, err := h.e.DB.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(store.LockHistoryCompaction)); err != nil {
		t.Fatal(err)
	}
	if rep, err := other.CompactHistory(ctx, service.HistoryOptions{}); err != nil || !rep.Skipped {
		t.Fatalf("must skip while another replica runs: %+v %v", rep, err)
	}
	h.expect(id, span(1, 6)...)
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, int64(store.LockHistoryCompaction)); err != nil {
		t.Fatal(err)
	}
	// Both at the same time: exactly one does the work.
	var wg sync.WaitGroup
	reps := make([]*service.HistoryReport, 2)
	for i, s := range []*service.Service{h.svc, other} {
		wg.Add(1)
		go func(i int, s *service.Service) {
			defer wg.Done()
			reps[i], _ = s.CompactHistory(ctx, service.HistoryOptions{})
		}(i, s)
	}
	wg.Wait()
	if reps[0].VersionsPruned+reps[1].VersionsPruned != 4 {
		t.Fatalf("%+v %+v", reps[0], reps[1])
	}
	h.expect(id, 1, 6)
}

// pruned builds a document with versions 2..4 compacted: 1 (create), 5 (last of the block), 6.
func (h *hist) pruned(user string) string {
	start := histNow.Add(-72 * time.Hour)
	id := h.doc(user, start.Add(-time.Hour))
	h.burst(id, user, "", start, 4, time.Minute)
	h.save(id, user, "", "", histNow.Add(-time.Hour))
	h.compact(histNow, service.HistoryOptions{})
	h.expect(id, 1, 5, 6)
	return id
}

// Case 15: documents.get of a compacted version: version_pruned with the nearest kept ones.
func TestHistory15GetPrunedVersion(t *testing.T) {
	h := newHist(t)
	id := h.pruned(tu.User)
	r := h.e.Fail(h.e.Q(tu.User, "documents.get", map[string]any{"id": id, "version": 3}), protocol.CodeVersionPruned)
	d := r.ErrorDetails
	if d["requestedVersion"] != float64(3) || d["previousAvailable"] != float64(1) || d["nextAvailable"] != float64(5) {
		t.Fatalf("%v", d)
	}
	// A version newer than the current one never existed.
	r = h.e.Fail(h.e.Q(tu.User, "documents.get", map[string]any{"id": id, "version": 7}), protocol.CodeDocumentNotFound)
	if r.ErrorDetails["version"] != float64(7) {
		t.Fatal(r.ErrorDetails)
	}
	h.e.OK(h.e.Q(tu.User, "documents.get", map[string]any{"id": id, "version": 5, "include": []string{"content"}}))
}

// Case 16: restore from a compacted version: version_pruned and no new version.
func TestHistory16RestorePrunedVersion(t *testing.T) {
	h := newHist(t)
	id := h.pruned(tu.User)
	r := h.e.Fail(h.e.Cmd(tu.User, "documents."+id+".restore", map[string]any{"fromVersion": 2, "baseVersion": 6}), protocol.CodeVersionPruned)
	if r.ErrorDetails["previousAvailable"] != float64(1) || r.ErrorDetails["nextAvailable"] != float64(5) {
		t.Fatal(r.ErrorDetails)
	}
	h.expect(id, 1, 5, 6)
	h.e.OK(h.e.Cmd(tu.User, "documents."+id+".restore", map[string]any{"fromVersion": 5, "baseVersion": 6}))
	h.expect(id, 1, 5, 6, 7)
}

// Case 17: shares.new pinned on a compacted version: version_pruned.
func TestHistory17PinnedSharePrunedVersion(t *testing.T) {
	h := newHist(t)
	id := h.pruned(tu.Admin)
	exp := protocol.FormatTime(histNow.AddDate(0, 0, 10))
	r := h.e.Fail(h.e.Cmd(tu.Admin, "shares.new", map[string]any{"id": tu.ID(), "documentId": id, "mode": "pinned", "version": 4,
		"expiresAt": exp, "includeAttachments": true}), protocol.CodeVersionPruned)
	if r.ErrorDetails["requestedVersion"] != float64(4) || r.ErrorDetails["nextAvailable"] != float64(5) {
		t.Fatal(r.ErrorDetails)
	}
	h.e.OK(h.e.Cmd(tu.Admin, "shares.new", map[string]any{"id": tu.ID(), "documentId": id, "mode": "pinned", "version": 5,
		"expiresAt": exp, "includeAttachments": true}))
}

// Case 18: an update that arrives while the same document is being compacted waits for the
// commit and applies normally; the current version is never removed.
func TestHistory18UpdateDuringCompaction(t *testing.T) {
	h := newHist(t)
	ctx := context.Background()
	start := histNow.Add(-72 * time.Hour)
	id := h.doc(tu.User, start.Add(-time.Hour))
	_, last := h.burst(id, tu.User, "", start, 20, time.Minute)
	h.clk.Set(histNow)
	// The compaction's transaction holds the row: the update waits for it.
	tx, err := h.e.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM documents WHERE id = $1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	done := make(chan tu.Resp, 1)
	c := h.content()
	go func() {
		done <- h.e.Cmd(tu.User, "documents."+id+".update", map[string]any{"baseVersion": last, "content": c, "checksum": tu.Sum(c)})
	}()
	select {
	case r := <-done:
		t.Fatalf("the update must wait for the compaction's commit: %s", r.Raw)
	case <-time.After(300 * time.Millisecond):
	}
	_ = tx.Rollback(ctx)
	if r := h.e.OK(<-done); r["version"] != float64(last+1) {
		t.Fatal(r)
	}
	// And for real: compaction and updates at the same time.
	h.cur[id] = last + 1
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := h.svc.CompactHistory(ctx, service.HistoryOptions{}); err != nil {
			t.Error(err)
		}
	}()
	for i := 0; i < 5; i++ {
		c := h.content()
		r := h.e.OK(h.e.Cmd(tu.User, "documents."+id+".update", map[string]any{"baseVersion": h.cur[id], "content": c, "checksum": tu.Sum(c)}))
		h.cur[id] = int(r["version"].(float64))
	}
	wg.Wait()
	g := h.e.OK(h.e.Q(tu.User, "documents.get", map[string]any{"id": id, "include": []string{"content"}}))
	if g["version"] != float64(h.cur[id]) || g["content"] == "" {
		t.Fatal(g)
	}
	h.expect(id, append([]int{1, last}, span(last+1, h.cur[id])...)...)
}

// Case 19: --dry-run reports what it would remove and changes neither rows nor objects.
func TestHistory19DryRun(t *testing.T) {
	h := newHist(t)
	ctx := context.Background()
	start := histNow.Add(-72 * time.Hour)
	id := h.doc(tu.User, start.Add(-time.Hour))
	_, last := h.burst(id, tu.User, "", start, 5, time.Minute)
	keys := h.keys(id)
	rep := h.compact(histNow, service.HistoryOptions{DryRun: true})
	if !rep.DryRun || rep.Documents != 1 || rep.VersionsPruned != 4 || len(rep.Plans) != 1 {
		t.Fatalf("%+v", rep)
	}
	var pruned []int
	for _, d := range rep.Plans[0].Versions {
		if !d.Keep {
			pruned = append(pruned, d.Version)
			if d.Reason != service.ReasonInBlock {
				t.Errorf("reason %s", d.Reason)
			}
		}
	}
	if fmt.Sprint(pruned) != "[2 3 4 5]" {
		t.Fatal(pruned)
	}
	h.expect(id, span(1, last)...)
	for _, k := range keys {
		if !h.objectExists(k) {
			t.Fatal("dry run deleted an object")
		}
	}
	var n int
	var compacted *time.Time
	if err := h.e.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM pruned_objects), last_compacted_at FROM documents WHERE id = $1`, id).
		Scan(&n, &compacted); err != nil || n != 0 || compacted != nil {
		t.Fatalf("dry run wrote: %d %v %v", n, compacted, err)
	}
}

// Case 20: a deleted document is not processed.
func TestHistory20DeletedDocumentSkipped(t *testing.T) {
	h := newHist(t)
	start := histNow.Add(-72 * time.Hour)
	id := h.doc(tu.User, start.Add(-time.Hour))
	_, last := h.burst(id, tu.User, "", start, 5, time.Minute)
	h.clk.Set(histNow.Add(-time.Minute))
	h.e.OK(h.e.Cmd(tu.User, "documents."+id+".delete", map[string]any{"baseVersion": last}))
	if rep := h.compact(histNow, service.HistoryOptions{}); rep.Documents != 0 {
		t.Fatalf("%+v", rep)
	}
	if rep := h.compact(histNow, service.HistoryOptions{DocumentID: id}); rep.Documents != 0 {
		t.Fatalf("not even on demand: %+v", rep)
	}
	h.expect(id, span(1, last)...)
}

// Case 21: an external person asking for a compacted version of a public document gets
// document_not_found without errorDetails: the history is not revealed (nor the count).
func TestHistory21ExternalNeverSeesPruned(t *testing.T) {
	h := newHist(t)
	start := histNow.Add(-72 * time.Hour)
	h.clk.Set(start.Add(-time.Hour))
	id := tu.ID()
	c := fm("12", "public", "") + "# Público\n"
	h.e.OK(h.e.Cmd(tu.Admin, "documents.new", map[string]any{"id": id, "folderId": h.folder, "fileName": "publico.md",
		"content": c, "checksum": tu.Sum(c)}))
	h.cur[id] = 1
	for i := 0; i < 4; i++ {
		h.clk.Set(start.Add(time.Duration(i) * time.Minute))
		c := fm("12", "public", "") + fmt.Sprintf("# Público\nrevisión %d\n", i)
		r := h.e.OK(h.e.Cmd(tu.Admin, "documents."+id+".update", map[string]any{"baseVersion": h.cur[id], "content": c, "checksum": tu.Sum(c)}))
		h.cur[id] = int(r["version"].(float64))
	}
	h.compact(histNow, service.HistoryOptions{})
	h.expect(id, 1, 5)
	h.e.Fail(h.e.Q(tu.Admin, "documents.get", map[string]any{"id": id, "version": 3}), protocol.CodeVersionPruned)
	for _, r := range []tu.Resp{
		h.e.Q(tu.External, "documents.get", map[string]any{"id": id, "version": 3}),
		h.e.Q(tu.External, "documents.get", map[string]any{"id": id, "version": 1}),
		h.e.Q(tu.External, "documents.get", map[string]any{"id": id, "version": 9}),
		h.e.Q(tu.APIUser, "documents.get", map[string]any{"id": id, "version": 3, "actor": actor(tu.External, "external-user")}),
	} {
		h.e.Fail(r, protocol.CodeDocumentNotFound)
		if r.ErrorDetails != nil {
			t.Fatalf("an external person gets no details: %v", r.ErrorDetails)
		}
	}
	g := h.e.OK(h.e.Q(tu.External, "documents.get", map[string]any{"id": id}))
	if _, ok := g["prunedVersionsCount"]; ok || g["version"] != float64(5) {
		t.Fatalf("external: %v", g)
	}
}

// Case 22: an agent version with a message, 60 days ago, stays.
func TestHistory22AgentWithMessageStays(t *testing.T) {
	h := newHist(t)
	day := histNow.AddDate(0, 0, -60)
	id := h.doc(tu.User, day.Add(-48*time.Hour))
	h.save(id, tu.User, "agent", "", day)
	marked := h.save(id, tu.User, "agent", "Trabajo terminado", day.Add(time.Minute))
	h.save(id, tu.User, "agent", "", day.Add(2*time.Minute))
	last := h.save(id, tu.User, "agent", "", day.Add(3*time.Minute))
	cur := h.save(id, tu.User, "", "", histNow.Add(-time.Hour))
	h.compact(histNow, service.HistoryOptions{})
	h.expect(id, 1, marked, last, cur)
}

// Case 23: a service version without a message, 60 days ago, stays (written in process, like the
// importer and the relation review do).
func TestHistory23ServiceVersionStays(t *testing.T) {
	h := newHist(t)
	ctx := context.Background()
	day := histNow.AddDate(0, 0, -60)
	id := h.doc(tu.User, day.Add(-48*time.Hour))
	h.save(id, tu.User, "", "", day)
	h.clk.Set(day.Add(time.Minute))
	c := h.content()
	out, err := h.svc.UpdateDocumentContent(ctx, &service.Call{Caller: h.svc.SelfCaller()}, id, h.cur[id], []byte(c), "service", "")
	if err != nil {
		t.Fatal(err)
	}
	svcVersion := out["version"].(int)
	h.cur[id] = svcVersion
	h.save(id, tu.User, "", "", day.Add(2*time.Minute))
	last := h.save(id, tu.User, "", "", day.Add(3*time.Minute))
	cur := h.save(id, tu.User, "", "", histNow.Add(-time.Hour))
	h.compact(histNow, service.HistoryOptions{})
	h.expect(id, 1, svcVersion, last, cur)
}

// The migration marks the existing versions: v1 create, the service's restorations restore, and
// protected creations, restorations, messages and service versions.
func TestHistoryMigrationBackfill(t *testing.T) {
	if tu.SkipIntegration() {
		t.Skip()
	}
	c, err := tu.Shared()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dsn, err := c.NewDatabase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := store.MigrateTo(ctx, db, nil, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO documents (id, folder_id, file_name, path, title, current_version, checksum, size_bytes,
		created_by, created_at, updated_by, updated_at) VALUES ('D1', '00000000000000000000000000', 'a.md', '/a.md', 'a', 6, 'x', 1,
		'u', now(), 'u', now())`); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		v      int
		via    string
		msg    any
		kind   string
		protec bool
	}{
		{1, "human", nil, "create", true},
		{2, "human", nil, "update", false},
		{3, "agent", nil, "update", false},
		{4, "human", "Restaurado desde la versión 2", "restore", true},
		{5, "agent", "hito", "update", true},
		{6, "service", nil, "update", true},
	}
	for _, r := range rows {
		if _, err := db.Exec(ctx, `INSERT INTO document_versions (document_id, version, storage_key, checksum, size_bytes, title, author,
			via, message, created_at) VALUES ('D1', $1, $2, 'x', 1, 'a', 'u', $3, $4, now())`, r.v, fmt.Sprint("k", r.v), r.via, r.msg); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := store.Migrate(ctx, db, nil); err != nil || n == 0 {
		t.Fatal(n, err)
	}
	for _, r := range rows {
		var kind string
		var protected bool
		if err := db.QueryRow(ctx, `SELECT kind, protected FROM document_versions WHERE document_id = 'D1' AND version = $1`, r.v).
			Scan(&kind, &protected); err != nil {
			t.Fatal(err)
		}
		if kind != r.kind || protected != r.protec {
			t.Errorf("version %d: %s/%v, want %s/%v", r.v, kind, protected, r.kind, r.protec)
		}
	}
	var compacted *time.Time
	var count int
	if err := db.QueryRow(ctx, `SELECT last_compacted_at, pruned_versions_count FROM documents WHERE id = 'D1'`).Scan(&compacted, &count); err != nil ||
		compacted != nil || count != 0 {
		t.Fatal(compacted, count, err)
	}
}

// The daily job runs once per calendar day of the configured zone, from the configured hour.
func TestHistoryDailyJob(t *testing.T) {
	h := newHist(t)
	ctx := context.Background()
	loc, _ := time.LoadLocation("America/Argentina/Buenos_Aires")
	due := func(at time.Time) bool {
		h.clk.Set(at)
		ok, err := h.svc.JobDueDaily(ctx, "history-compaction", loc, 3)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, loc)
	if due(day.Add(2 * time.Hour)) {
		t.Fatal("before the hour")
	}
	if !due(day.Add(3*time.Hour + time.Minute)) {
		t.Fatal("from the hour on")
	}
	if due(day.Add(23 * time.Hour)) {
		t.Fatal("once a day")
	}
	if due(day.Add(26 * time.Hour)) {
		t.Fatal("the next day, not before the hour")
	}
	if !due(day.Add(27 * time.Hour)) {
		t.Fatal("the next day")
	}
}
