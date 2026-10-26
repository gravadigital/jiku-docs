package integration

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gravadigital/jiku-docs/internal/protocol"
	tu "github.com/gravadigital/jiku-docs/internal/testutil"
)

// writable returns a folder (since P-1 any internal person writes in any folder).
func writable(e *tu.Env) string {
	return e.Folder("", "equipo", nil)
}

func TestDocumentLifecycle(t *testing.T) {
	e := tu.NewEnv(t)
	folder := writable(e)
	content := "---\ntitle: Acceso a staging\ntopics: [Infra, accesos]\njiku:\n  clients: [4]\n  projects: [12]\n  requirements: [812]\n  tasks: [900]\n---\n\n# Staging\nPuerto 22.\n"
	id := tu.ID()
	payload := map[string]any{"id": id, "folderId": folder, "fileName": "staging.md", "content": content, "checksum": tu.Sum(content), "via": "agent", "message": "alta"}
	r := e.OK(e.Cmd(tu.User, "documents.new", payload))
	if r["version"].(float64) != 1 || r["path"] != "/equipo/staging.md" {
		t.Fatalf("%v", r)
	}
	// Retrying the same creation is success; another content with the same id is already_exists.
	e.OK(e.Cmd(tu.User, "documents.new", payload))
	other := "otro"
	e.Fail(e.Cmd(tu.User, "documents.new", map[string]any{"id": id, "folderId": folder, "fileName": "staging.md", "content": other, "checksum": tu.Sum(other)}), protocol.CodeAlreadyExists)
	// Same name in the folder (case-insensitive).
	e.Fail(e.Cmd(tu.User, "documents.new", map[string]any{"id": tu.ID(), "folderId": folder, "fileName": "STAGING.md", "content": other, "checksum": tu.Sum(other)}), protocol.CodeAlreadyExists)

	got := e.OK(e.Q(tu.User, "documents.get", map[string]any{"id": id, "include": []string{"content", "relations", "topics", "lock", "attachments"}}))
	if got["title"] != "Acceso a staging" || got["content"] != content || got["checksum"] != tu.Sum(content) || got["lock"] != nil {
		t.Fatalf("%v", got)
	}
	rel := got["relations"].(map[string]any)
	if len(rel["projects"].([]any)) != 1 || len(rel["tasks"].([]any)) != 1 {
		t.Fatalf("relations %v", rel)
	}
	if strings.Join(toStrings(got["topics"]), ",") != "accesos,infra" {
		t.Fatalf("topics %v", got["topics"])
	}
	created := e.WaitEvent("document.created", nil)
	snap := created.Body["snapshot"].(map[string]any)
	if created.Body["actor"].(map[string]any)["via"] != "agent" || snap["version"].(float64) != 1 {
		t.Fatalf("%v", created.Body)
	}
	if created.Body["changes"].(map[string]any)["message"] != "alta" {
		t.Errorf("message missing from changes")
	}

	// Update with the right base version.
	v2 := strings.Replace(content, "Puerto 22", "Puerto 2222", 1)
	upd := e.OK(e.Cmd(tu.User, "documents."+id+".update", map[string]any{"baseVersion": 1, "content": v2, "checksum": tu.Sum(v2)}))
	if upd["version"].(float64) != 2 {
		t.Fatal(upd)
	}
	// Same content again with a stale base: success with the current version (idempotent retry).
	again := e.OK(e.Cmd(tu.User, "documents."+id+".update", map[string]any{"baseVersion": 1, "content": v2, "checksum": tu.Sum(v2)}))
	if again["version"].(float64) != 2 {
		t.Fatal(again)
	}
	// Another content with a stale base: version_conflict with the current version.
	v3 := v2 + "más\n"
	c := e.Fail(e.Cmd(tu.User2, "documents."+id+".update", map[string]any{"baseVersion": 1, "content": v3, "checksum": tu.Sum(v3)}), protocol.CodeVersionConflict)
	if c.ErrorDetails["currentVersion"].(float64) != 2 {
		t.Fatal(c.ErrorDetails)
	}
	// Bad checksum, bad frontmatter, unknown relation.
	e.Fail(e.Cmd(tu.User, "documents."+id+".update", map[string]any{"baseVersion": 2, "content": v3, "checksum": tu.Sum("x")}), protocol.CodeChecksumMismatch)
	bad := "---\nvisibility: secreta\n---\n"
	fe := e.Fail(e.Cmd(tu.User, "documents."+id+".update", map[string]any{"baseVersion": 2, "content": bad, "checksum": tu.Sum(bad)}), protocol.CodeFrontmatterInvalid)
	if fe.ErrorDetails["field"] != "visibility" || fe.ErrorDetails["line"].(float64) != 2 {
		t.Fatal(fe.ErrorDetails)
	}
	unk := "---\njiku:\n  projects: [999]\n---\n"
	ir := e.Fail(e.Cmd(tu.User, "documents."+id+".update", map[string]any{"baseVersion": 2, "content": unk, "checksum": tu.Sum(unk)}), protocol.CodeInvalidRelation)
	if ir.ErrorDetails["entityType"] != "project" || ir.ErrorDetails["entityId"].(float64) != 999 {
		t.Fatal(ir.ErrorDetails)
	}
	// P-1: any internal person sets public; only an admin sets restricted.
	res := "---\nvisibility: restricted\n---\n# x\n"
	e.Fail(e.Cmd(tu.User, "documents."+id+".update", map[string]any{"baseVersion": 2, "content": res, "checksum": tu.Sum(res)}), protocol.CodePermissionDenied)
	// A secret is a warning, not a failure.
	sec := v2 + "password: Sup3rSecretValue\n"
	w := e.OK(e.Cmd(tu.User, "documents."+id+".update", map[string]any{"baseVersion": 2, "content": sec, "checksum": tu.Sum(sec)}))
	if w["version"].(float64) != 3 || toStrings(w["warnings"])[0] != "possible_secret_detected" {
		t.Fatal(w)
	}
	upEv := e.WaitEvent("document.updated", func(ev tu.Event) bool {
		return ev.Body["snapshot"].(map[string]any)["version"].(float64) == 2
	})
	if upEv.Body["changes"].(map[string]any)["version"].(map[string]any)["from"].(float64) != 1 {
		t.Error(upEv.Body["changes"])
	}

	// Versions and get by version.
	vers := e.Q(tu.User, "documents.versions.list", map[string]any{"filter": map[string]any{"documentId": id}})
	if items := vers.Items(); len(items) != 3 || items[0]["version"].(float64) != 3 || items[2]["via"] != "agent" {
		t.Fatalf("%s", vers.Raw)
	}
	old := e.OK(e.Q(tu.User, "documents.get", map[string]any{"id": id, "version": 1, "include": []string{"content"}}))
	if old["content"] != content || old["version"].(float64) != 1 {
		t.Fatal(old)
	}
	e.Fail(e.Q(tu.User, "documents.get", map[string]any{"id": id, "version": 9}), protocol.CodeDocumentNotFound)

	// Move and rename.
	sub := e.Folder(folder, "infra", nil)
	mv := e.OK(e.Cmd(tu.User, "documents."+id+".move", map[string]any{"baseVersion": 3, "folderId": sub, "fileName": "staging-2.md"}))
	if mv["path"] != "/equipo/infra/staging-2.md" || mv["version"].(float64) != 3 {
		t.Fatal(mv)
	}
	e.Fail(e.CmdRaw(tu.User, "documents."+id+".move", map[string]any{"baseVersion": 3, "fileName": "x.txt"}), protocol.CodeFileNameInvalid)
	e.Fail(e.Cmd(tu.User, "documents."+id+".move", map[string]any{"baseVersion": 1, "fileName": "y.md"}), protocol.CodeVersionConflict)
	e.WaitEvent("document.moved", nil)

	// Delete, restore an old version.
	e.Fail(e.Cmd(tu.User, "documents."+id+".delete", map[string]any{"baseVersion": 2}), protocol.CodeVersionConflict)
	e.OK(e.Cmd(tu.User, "documents."+id+".delete", map[string]any{"baseVersion": 3}))
	e.OK(e.Cmd(tu.User, "documents."+id+".delete", map[string]any{"baseVersion": 3}))
	if items := e.Q(tu.User, "documents.list", nil).Items(); len(items) != 0 {
		t.Fatalf("deleted documents are not listed by default: %v", items)
	}
	if items := e.Q(tu.User, "documents.list", map[string]any{"filter": map[string]any{"deleted": true}, "fields": []string{"id", "deletedAt"}}).Items(); len(items) != 1 || items[0]["deletedAt"] == nil {
		t.Fatalf("deleted filter: %v", items)
	}
	e.Fail(e.Cmd(tu.User, "documents."+id+".update", map[string]any{"baseVersion": 3, "content": v3, "checksum": tu.Sum(v3)}), protocol.CodeDocumentNotFound)
	rs := e.OK(e.Cmd(tu.User, "documents."+id+".restore", map[string]any{"fromVersion": 1, "baseVersion": 3}))
	if rs["version"].(float64) != 4 {
		t.Fatal(rs)
	}
	now := e.OK(e.Q(tu.User, "documents.get", map[string]any{"id": id, "include": []string{"content"}}))
	if now["content"] != content || now["deletedAt"] != nil {
		t.Fatal(now)
	}
	ev := e.WaitEvent("document.restored", nil)
	if ev.Body["changes"].(map[string]any)["restoredFrom"].(float64) != 1 {
		t.Error(ev.Body)
	}
	e.Fail(e.Cmd(tu.User, "documents."+id+".restore", map[string]any{"fromVersion": 99, "baseVersion": 4}), protocol.CodeDocumentNotFound)

	// Validation of names and sizes.
	e.Fail(e.CmdRaw(tu.User, "documents.new", map[string]any{"id": tu.ID(), "folderId": folder, "fileName": "a/b.md", "content": "x", "checksum": tu.Sum("x")}), protocol.CodeFileNameInvalid)
	e.Fail(e.CmdRaw(tu.User, "documents.new", map[string]any{"id": tu.ID(), "folderId": folder, "fileName": "a.txt", "content": "x", "checksum": tu.Sum("x")}), protocol.CodeFileNameInvalid)
	big := strings.Repeat("a", 256*1024+1)
	e.Fail(e.CmdRaw(tu.User, "documents.new", map[string]any{"id": tu.ID(), "folderId": folder, "fileName": "big.md", "content": big, "checksum": tu.Sum(big)}), protocol.CodeContentTooLarge)
	e.Fail(e.CmdRaw(tu.User, "documents.new", map[string]any{"id": tu.ID(), "folderId": folder, "fileName": "x.md", "checksum": tu.Sum("x")}), protocol.CodeInvalidFields)
	e.Fail(e.CmdRaw(tu.User, "documents.new", map[string]any{"id": tu.ID(), "folderId": folder, "fileName": "x.md", "content": "x", "checksum": tu.Sum("x"), "author": "me"}), protocol.CodeInvalidFields)
	// A user does not create a restricted document; a missing folder is folder_not_found.
	e.Fail(e.Cmd(tu.User, "documents.new", map[string]any{"id": tu.ID(), "folderId": folder, "fileName": "x.md", "content": res, "checksum": tu.Sum(res)}), protocol.CodePermissionDenied)
	e.Fail(e.Cmd(tu.User, "documents.new", map[string]any{"id": tu.ID(), "folderId": tu.ID(), "fileName": "x.md", "content": "x", "checksum": tu.Sum("x")}), protocol.CodeFolderNotFound)
}

func toStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, len(list))
	for i, x := range list {
		out[i], _ = x.(string)
	}
	return out
}

func TestLocks(t *testing.T) {
	e := tu.NewEnv(t, tu.Options{JanitorTick: 200 * time.Millisecond})
	folder := writable(e)
	id := e.Doc(tu.User, folder, "lock.md", "uno")
	s1, s2 := tu.ID(), tu.ID()
	l := e.OK(e.Cmd(tu.User, "documents."+id+".lock", map[string]any{"sessionId": s1, "via": "agent", "ttlSeconds": 300}))
	if l["sessionId"] != s1 {
		t.Fatal(l)
	}
	// Renewing is idempotent and emits no second document.locked.
	e.OK(e.Cmd(tu.User, "documents."+id+".lock", map[string]any{"sessionId": s1, "via": "agent", "ttlSeconds": 300}))
	locked := e.Fail(e.Cmd(tu.User2, "documents."+id+".lock", map[string]any{"sessionId": s2, "via": "human"}), protocol.CodeDocumentLocked)
	if locked.ErrorDetails["holder"] != tu.User || locked.ErrorDetails["holderName"] != "Usuario" || locked.ErrorDetails["via"] != "agent" || locked.ErrorDetails["expiresAt"] == nil {
		t.Fatal(locked.ErrorDetails)
	}
	// Writes from another session fail; from the session succeed.
	e.Fail(e.Cmd(tu.User2, "documents."+id+".update", map[string]any{"baseVersion": 1, "content": "dos", "checksum": tu.Sum("dos")}), protocol.CodeDocumentLocked)
	e.Fail(e.Cmd(tu.User, "documents."+id+".update", map[string]any{"baseVersion": 1, "content": "dos", "checksum": tu.Sum("dos")}), protocol.CodeDocumentLocked)
	e.OK(e.Cmd(tu.User, "documents."+id+".update", map[string]any{"baseVersion": 1, "content": "dos", "checksum": tu.Sum("dos"), "sessionId": s1, "via": "agent"}))
	g := e.OK(e.Q(tu.User2, "documents.get", map[string]any{"id": id, "include": []string{"lock"}}))
	if g["lock"].(map[string]any)["sessionId"] != s1 || g["lock"].(map[string]any)["holderName"] != "Usuario" {
		t.Fatal(g)
	}
	// unlock: not held, then force is for admins only, then the holder releases.
	e.Fail(e.Cmd(tu.User2, "documents."+id+".unlock", map[string]any{"sessionId": s2}), protocol.CodeLockNotHeld)
	e.Fail(e.Cmd(tu.User2, "documents."+id+".unlock", map[string]any{"sessionId": s2, "force": true}), protocol.CodePermissionDenied)
	e.OK(e.Cmd(tu.User, "documents."+id+".unlock", map[string]any{"sessionId": s1}))
	ev := e.WaitEvent("document.unlocked", nil)
	if ev.Body["changes"].(map[string]any)["reason"] != "released" {
		t.Error(ev.Body)
	}
	locks := 0
	for _, ev := range e.Events() {
		if ev.Body["type"] == "document.locked" {
			locks++
		}
	}
	if locks != 1 {
		t.Errorf("document.locked emitted %d times", locks)
	}
	// Admin forces a lock away.
	e.OK(e.Cmd(tu.User, "documents."+id+".lock", map[string]any{"sessionId": s1, "via": "human"}))
	e.OK(e.Cmd(tu.Admin, "documents."+id+".unlock", map[string]any{"sessionId": s2, "force": true}))
	e.WaitEvent("document.unlocked", func(ev tu.Event) bool { return ev.Body["changes"].(map[string]any)["reason"] == "forced" })

	// Expiry: the janitor releases it and emits document.unlocked as the service.
	e.OK(e.Cmd(tu.User, "documents."+id+".lock", map[string]any{"sessionId": s2, "via": "human", "ttlSeconds": 30}))
	if _, err := e.DB.Exec(context.Background(), `UPDATE document_locks SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	ex := e.WaitEvent("document.unlocked", func(ev tu.Event) bool { return ev.Body["changes"].(map[string]any)["reason"] == "expired" })
	if ex.Body["actor"].(map[string]any)["id"] != tu.DocsUser {
		t.Errorf("expiry actor %v", ex.Body["actor"])
	}
	e.OK(e.Cmd(tu.User2, "documents."+id+".lock", map[string]any{"sessionId": tu.ID(), "via": "human"}))
}

func TestLargeContentUpload(t *testing.T) {
	e := tu.NewEnv(t)
	folder := writable(e)
	big := "# Grande\n\n" + strings.Repeat("línea de texto con aduana\n", 15000) // ~400 KiB
	up := e.OK(e.Cmd(tu.User, "documents.request-content-upload", map[string]any{"checksum": tu.Sum(big), "size": len(big)}))
	put(t, up["uploadUrl"].(string), []byte(big))
	id := tu.ID()
	e.OK(e.Cmd(tu.User, "documents.new", map[string]any{"id": id, "folderId": folder, "fileName": "grande.md", "contentUploadId": up["contentUploadId"], "checksum": tu.Sum(big)}))
	// An upload is used once.
	e.Fail(e.Cmd(tu.User, "documents.new", map[string]any{"id": tu.ID(), "folderId": folder, "fileName": "otra.md", "contentUploadId": up["contentUploadId"], "checksum": tu.Sum(big)}), protocol.CodeUploadNotFound)
	g := e.OK(e.Q(tu.User, "documents.get", map[string]any{"id": id, "include": []string{"content"}}))
	if _, inline := g["content"]; inline {
		t.Fatal("content above 256 KiB must come as contentUrl")
	}
	if got := get(t, g["contentUrl"].(string)); string(got) != big {
		t.Fatalf("contentUrl returned %d bytes", len(got))
	}
	// Someone else's upload id is upload_not_found; a wrong checksum is checksum_mismatch.
	up2 := e.OK(e.Cmd(tu.User, "documents.request-content-upload", map[string]any{"checksum": tu.Sum(big), "size": len(big)}))
	put(t, up2["uploadUrl"].(string), []byte(big))
	e.Fail(e.Cmd(tu.User2, "documents.new", map[string]any{"id": tu.ID(), "folderId": folder, "fileName": "c.md", "contentUploadId": up2["contentUploadId"], "checksum": tu.Sum(big)}), protocol.CodeUploadNotFound)
	e.Fail(e.Cmd(tu.User, "documents.new", map[string]any{"id": tu.ID(), "folderId": folder, "fileName": "c.md", "contentUploadId": up2["contentUploadId"], "checksum": tu.Sum("x")}), protocol.CodeChecksumMismatch)
	e.Fail(e.CmdRaw(tu.User, "documents.request-content-upload", map[string]any{"checksum": tu.Sum(big), "size": 6 << 20}), protocol.CodeContentTooLarge)
	e.Fail(e.Cmd(tu.External, "documents.request-content-upload", map[string]any{"checksum": tu.Sum(big), "size": 10}), protocol.CodeCallerNotAuthorized)
}

func put(t *testing.T, url string, body []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "text/markdown") // what jiku-go sends: not part of the signature
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT %d: %s", resp.StatusCode, b)
	}
}

func get(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %d: %s", resp.StatusCode, b)
	}
	return b
}
