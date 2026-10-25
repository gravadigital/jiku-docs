package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/gravadigital/jiku-docs/internal/protocol"
	tu "github.com/gravadigital/jiku-docs/internal/testutil"
)

func sumBytes(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// attach uploads an attachment through the D-3 flow: jiku-docs -> core files -> PUT -> confirm.
func attach(e *tu.Env, user, doc, name string, data []byte) string {
	e.T.Helper()
	id := tu.ID()
	up := e.OK(e.Cmd(user, "attachments.request-upload", map[string]any{"id": id, "documentId": doc, "fileName": name,
		"mimeType": "image/png", "fileSize": len(data), "checksum": sumBytes(data)}))
	if up["link"] != "http://web.test/docs/attachments/"+id {
		e.T.Fatalf("link %v", up["link"])
	}
	put(e.T, up["uploadUrl"].(string), data)
	e.OK(e.Cmd(user, "attachments."+id+".confirm", nil))
	return id
}

func TestAttachments(t *testing.T) {
	e := tu.NewEnv(t)
	f := writable(e)
	doc := e.Doc(tu.User, f, "con-adjunto.md", "# Diagrama\n")
	png := []byte("\x89PNG fake image bytes")
	id := attach(e, tu.User, doc, "red.png", png)
	// Confirming twice is idempotent; the event is emitted once.
	e.OK(e.Cmd(tu.User, "attachments."+id+".confirm", nil))
	e.WaitEvent("attachment.created", nil)

	dl := e.OK(e.Cmd(tu.User2, "attachments."+id+".request-download", nil))
	if string(get(t, dl["downloadUrl"].(string))) != string(png) {
		t.Fatal("downloaded bytes differ")
	}
	list := e.Q(tu.User2, "attachments.list", map[string]any{"filter": map[string]any{"documentId": doc}}).Items()
	if len(list) != 1 || list[0]["fileName"] != "red.png" || list[0]["sizeBytes"].(float64) != float64(len(png)) {
		t.Fatal(list)
	}
	// Only the requester confirms; a wrong byte is checksum_mismatch; nothing uploaded is upload_not_found.
	id2 := tu.ID()
	up := e.OK(e.Cmd(tu.User, "attachments.request-upload", map[string]any{"id": id2, "documentId": doc, "fileName": "b.png",
		"mimeType": "image/png", "fileSize": 4, "checksum": sumBytes([]byte("abcd"))}))
	e.Fail(e.Cmd(tu.User, "attachments."+id2+".confirm", nil), protocol.CodeUploadNotFound)
	put(t, up["uploadUrl"].(string), []byte("abce"))
	e.Fail(e.Cmd(tu.User2, "attachments."+id2+".confirm", nil), protocol.CodePermissionDenied)
	e.Fail(e.Cmd(tu.User, "attachments."+id2+".confirm", nil), protocol.CodeChecksumMismatch)
	e.Fail(e.Cmd(tu.User, "attachments."+id2+".request-download", nil), protocol.CodeAttachmentNotFound)
	// Core's policy, translated.
	e.Fail(e.Cmd(tu.User, "attachments.request-upload", map[string]any{"id": tu.ID(), "documentId": doc, "fileName": "x.exe",
		"mimeType": "application/x-msdownload", "fileSize": 4, "checksum": sumBytes([]byte("abcd"))}), protocol.CodeAttachmentTypeNotAllowed)
	e.Fail(e.Cmd(tu.User, "attachments.request-upload", map[string]any{"id": tu.ID(), "documentId": doc, "fileName": "big.png",
		"mimeType": "image/png", "fileSize": 100 << 20, "checksum": sumBytes([]byte("abcd"))}), protocol.CodeAttachmentTooLarge)
	// Not visible = attachment_not_found; read-only = permission_denied for writes.
	e.Fail(e.Cmd(tu.External, "attachments."+id+".request-download", nil), protocol.CodeAttachmentNotFound)
	ro := e.Folder("", "ro", nil)
	e.ACL(ro, tu.Rule("role", "user", "read"), tu.Rule("role", "admin", "manage"))
	roDoc := e.Doc(tu.Admin, ro, "ro.md", "x")
	e.Fail(e.Cmd(tu.User, "attachments.request-upload", map[string]any{"id": tu.ID(), "documentId": roDoc, "fileName": "a.png",
		"mimeType": "image/png", "fileSize": 4, "checksum": sumBytes([]byte("abcd"))}), protocol.CodePermissionDenied)
	// Delete is logical and idempotent.
	e.OK(e.Cmd(tu.User, "attachments."+id+".delete", nil))
	e.OK(e.Cmd(tu.User, "attachments."+id+".delete", nil))
	e.Fail(e.Cmd(tu.User, "attachments."+id+".request-download", nil), protocol.CodeAttachmentNotFound)
	e.WaitEvent("attachment.deleted", nil)
	// The service never sent actor or identity fields to core (the double rejects them).
	if e.Core.CallCount("files.request-upload") < 3 {
		t.Error("uploads must go through core's file service")
	}
}

func TestShares(t *testing.T) {
	e := tu.NewEnv(t)
	f := e.Folder("", "manuales", nil)
	e.ACL(f, tu.Rule("role", "user", "write"), tu.Rule("user", tu.Admin, "manage"))
	content := "---\njiku:\n  projects: [12]\n---\n# Manual\n\nver [otro](otro.md) y ![red](http://web.test/docs/attachments/ATT)\n<script>alert(1)</script>\n"
	doc := e.Doc(tu.User, f, "manual.md", content)
	att := attach(e, tu.User, doc, "red.png", []byte("png!"))
	content2 := strings.Replace(content, "ATT", att, 1)
	e.OK(e.Cmd(tu.User, "documents."+doc+".update", map[string]any{"baseVersion": 1, "content": content2, "checksum": tu.Sum(content2)}))
	exp := time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)

	// write is not enough: shares need manage.
	e.Fail(e.Cmd(tu.User, "shares.new", map[string]any{"id": tu.ID(), "documentId": doc, "mode": "latest", "expiresAt": exp}), protocol.CodePermissionDenied)
	e.Fail(e.Cmd(tu.Admin, "shares.new", map[string]any{"id": tu.ID(), "documentId": doc, "mode": "latest",
		"expiresAt": time.Now().Add(200 * 24 * time.Hour).UTC().Format(time.RFC3339)}), protocol.CodeShareExpiryInvalid)
	e.Fail(e.Cmd(tu.Admin, "shares.new", map[string]any{"id": tu.ID(), "documentId": doc, "mode": "latest",
		"expiresAt": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}), protocol.CodeShareExpiryInvalid)
	e.Fail(e.CmdRaw(tu.Admin, "shares.new", map[string]any{"id": tu.ID(), "documentId": doc, "mode": "pinned", "expiresAt": exp}), protocol.CodeInvalidFields)

	sid := tu.ID()
	req := map[string]any{"id": sid, "documentId": doc, "mode": "latest", "expiresAt": exp, "note": "proveedor"}
	s := e.OK(e.Cmd(tu.Admin, "shares.new", req))
	token := s["token"].(string)
	if !strings.HasPrefix(token, "jds_") || s["url"] != "http://public.test/p/docs/"+token {
		t.Fatal(s)
	}
	// Retry within 5 minutes returns the same link; a different payload is already_exists.
	if again := e.OK(e.Cmd(tu.Admin, "shares.new", req)); again["token"] != token {
		t.Fatal("retry must return the same token")
	}
	req2 := map[string]any{"id": sid, "documentId": doc, "mode": "latest", "expiresAt": exp, "note": "otro"}
	e.Fail(e.Cmd(tu.Admin, "shares.new", req2), protocol.CodeAlreadyExists)
	created := e.WaitEvent("share.created", nil)
	if strings.Contains(string(created.Raw), token) {
		t.Fatal("share events never carry the token")
	}

	// Only the api, without actor, resolves.
	e.Fail(e.Q(tu.Admin, "shares.resolve", map[string]any{"token": token}), protocol.CodeCallerNotAuthorized)
	e.Fail(e.QRaw(tu.APIUser, "shares.resolve", map[string]any{"token": token, "actor": actor(tu.Admin, "admin")}), protocol.CodeCallerNotAuthorized)
	res := e.OK(e.Q(tu.APIUser, "shares.resolve", map[string]any{"token": token, "client": map[string]any{"ip": "203.0.113.9", "userAgent": "Firefox"}}))
	html := res["html"].(string)
	if !strings.Contains(html, `src="/p/docs/`+token+`/attachments/`+att+`"`) || strings.Contains(html, "<script") ||
		strings.Contains(html, "projects") || strings.Contains(html, "otro.md") || res["version"].(float64) != 2 {
		t.Fatalf("html %s", html)
	}
	if len(res["attachments"].([]any)) != 1 {
		t.Fatal(res)
	}
	sa := e.OK(e.Q(tu.APIUser, "shares.attachment", map[string]any{"token": token, "attachmentId": att}))
	if string(get(t, sa["downloadUrl"].(string))) != "png!" {
		t.Fatal("attachment via share")
	}
	e.Fail(e.Q(tu.APIUser, "shares.resolve", map[string]any{"token": "jds_" + strings.Repeat("A", 43)}), protocol.CodeShareNotFound)
	e.Fail(e.Q(tu.APIUser, "shares.resolve", map[string]any{"token": "garbage"}), protocol.CodeShareNotFound)

	// shares.list (manage), with the access counted and the IP never stored in clear.
	list := e.Q(tu.Admin, "shares.list", map[string]any{"filter": map[string]any{"documentId": doc, "status": "active"}}).Items()
	if len(list) != 1 || list[0]["accessCount"].(float64) != 1 || list[0]["note"] != "proveedor" || list[0]["token"] != nil {
		t.Fatal(list)
	}
	var ipHash []byte
	if err := e.DB.QueryRow(context.Background(), `SELECT ip_hash FROM share_accesses LIMIT 1`).Scan(&ipHash); err != nil || strings.Contains(string(ipHash), "203.0.113.9") {
		t.Fatal("ip hash", err)
	}
	e.Fail(e.Q(tu.User, "shares.list", map[string]any{"filter": map[string]any{"documentId": doc}}), protocol.CodePermissionDenied)

	// Pinned link keeps showing version 1.
	pinned := e.OK(e.Cmd(tu.Admin, "shares.new", map[string]any{"id": tu.ID(), "documentId": doc, "mode": "pinned", "version": 1, "expiresAt": exp, "includeAttachments": false}))
	pr := e.OK(e.Q(tu.APIUser, "shares.resolve", map[string]any{"token": pinned["token"]}))
	if pr["version"].(float64) != 1 || len(pr["attachments"].([]any)) != 0 {
		t.Fatal(pr)
	}
	e.Fail(e.Q(tu.APIUser, "shares.attachment", map[string]any{"token": pinned["token"], "attachmentId": att}), protocol.CodeShareNotFound)

	// Revoke: the creator or manage; then the link is dead.
	e.Fail(e.Cmd(tu.User, "shares."+sid+".revoke", nil), protocol.CodePermissionDenied)
	rv := e.OK(e.Cmd(tu.Admin, "shares."+sid+".revoke", nil))
	if again := e.OK(e.Cmd(tu.Admin, "shares."+sid+".revoke", nil)); again["revokedAt"] != rv["revokedAt"] {
		t.Fatal("revoking twice keeps the first date")
	}
	e.Fail(e.Q(tu.APIUser, "shares.resolve", map[string]any{"token": token}), protocol.CodeShareNotFound)
	e.WaitEvent("share.revoked", func(ev tu.Event) bool { return ev.Body["changes"].(map[string]any)["reason"] == "manual" })

	// Blocking sharing on the folder cuts live links; deleting the document revokes them.
	e.OK(e.Cmd(tu.Admin, "folders."+f+".edit", map[string]any{"sharingBlocked": true}))
	e.Fail(e.Q(tu.APIUser, "shares.resolve", map[string]any{"token": pinned["token"]}), protocol.CodeShareNotFound)
	e.Fail(e.Cmd(tu.Admin, "shares.new", map[string]any{"id": tu.ID(), "documentId": doc, "mode": "latest", "expiresAt": exp}), protocol.CodeSharingBlocked)
	e.OK(e.Cmd(tu.Admin, "folders."+f+".edit", map[string]any{"sharingBlocked": false}))
	e.OK(e.Q(tu.APIUser, "shares.resolve", map[string]any{"token": pinned["token"]}))
	e.OK(e.Cmd(tu.Admin, "documents."+doc+".delete", map[string]any{"baseVersion": 2}))
	e.Fail(e.Q(tu.APIUser, "shares.resolve", map[string]any{"token": pinned["token"]}), protocol.CodeShareNotFound)
	e.WaitEvent("share.revoked", func(ev tu.Event) bool { return ev.Body["changes"].(map[string]any)["reason"] == "document_deleted" })

	// Secret warning must be acknowledged.
	sec := e.Doc(tu.User, f, "secreto.md", "# Accesos\n\n-----BEGIN RSA PRIVATE KEY-----\n")
	e.Fail(e.Cmd(tu.Admin, "shares.new", map[string]any{"id": tu.ID(), "documentId": sec, "mode": "latest", "expiresAt": exp}), protocol.CodeSecretWarningNotAcked)
	e.OK(e.Cmd(tu.Admin, "shares.new", map[string]any{"id": tu.ID(), "documentId": sec, "mode": "latest", "expiresAt": exp, "acknowledgeSecretWarning": true}))

	// share.* never reach persons.
	for _, n := range e.Notes() {
		if strings.HasPrefix(n.Type, "share.") {
			t.Fatalf("share notification %v", n)
		}
	}
}
