package integration

import (
	"testing"

	"github.com/gravadigital/jiku-docs/internal/protocol"
	tu "github.com/gravadigital/jiku-docs/internal/testutil"
)

// P-1: folders only organise the files. Internal persons see them all and write documents in
// any of them; only admins and services manage them; the ACL is stored but has no effect.
func TestFolders(t *testing.T) {
	e := tu.NewEnv(t)
	clients := e.Folder("", "clients", nil)
	acme := e.Folder(clients, "acme", nil)
	infra := e.Folder("", "infra", map[string]any{"inheritAcl": false, "externalBlocked": true, "sharingBlocked": true})

	// A user sees every folder, with write, even without any rule and under inheritAcl=false.
	got := e.OK(e.Q(tu.User, "folders.get", map[string]any{"id": infra, "include": []string{"effectivePermission"}}))
	if got["path"] != "/infra/" || got["effectivePermission"] != "write" {
		t.Fatalf("%v", got)
	}
	list := e.Q(tu.User2, "folders.list", map[string]any{"include": []string{"effectivePermission"}, "count": true})
	if len(list.Items()) != 4 || list.M()["page"].(map[string]any)["total"].(float64) != 4 { // root + 3
		t.Fatalf("user2 must see every folder: %s", list.Raw)
	}
	if p := e.OK(e.Q(tu.Admin, "folders.get", map[string]any{"id": acme, "include": []string{"effectivePermission"}})); p["effectivePermission"] != "manage" {
		t.Fatal(p)
	}
	// The obsolete ACL is only for admins and services.
	e.Fail(e.Q(tu.User, "folders.get", map[string]any{"id": clients, "include": []string{"acl"}}), protocol.CodePermissionDenied)
	e.OK(e.Q(tu.Admin, "folders.get", map[string]any{"id": infra, "include": []string{"acl"}}))
	// Externals never see folders.
	e.Fail(e.Q(tu.External, "folders.get", map[string]any{"id": clients}), protocol.CodeFolderNotFound)
	if items := e.Q(tu.External, "folders.list", nil).Items(); len(items) != 0 {
		t.Fatalf("external sees folders: %v", items)
	}

	// folders.* commands: admins and services only.
	e.Fail(e.Cmd(tu.User, "folders.new", map[string]any{"id": tu.ID(), "parentId": clients, "name": "x"}), protocol.CodePermissionDenied)
	e.Fail(e.Cmd(tu.User, "folders."+acme+".edit", map[string]any{"name": "y"}), protocol.CodePermissionDenied)
	e.Fail(e.Cmd(tu.User, "folders."+acme+".delete", nil), protocol.CodePermissionDenied)
	e.Fail(e.Cmd(tu.User, "folders."+acme+".acl.replace", map[string]any{"rules": []any{}}), protocol.CodePermissionDenied)
	e.Fail(e.Cmd(tu.External, "folders.new", map[string]any{"id": tu.ID(), "parentId": clients, "name": "x"}), protocol.CodeCallerNotAuthorized)
	pzt := tu.ID()
	e.OK(e.Cmd(tu.Connector, "folders.new", map[string]any{"id": pzt, "parentId": acme, "name": "pzt"}))
	// Idempotent retry; another payload with the same id is already_exists; same name is already_exists.
	e.OK(e.Cmd(tu.Connector, "folders.new", map[string]any{"id": pzt, "parentId": acme, "name": "pzt"}))
	e.Fail(e.Cmd(tu.Admin, "folders.new", map[string]any{"id": pzt, "parentId": acme, "name": "otro"}), protocol.CodeAlreadyExists)
	e.Fail(e.Cmd(tu.Admin, "folders.new", map[string]any{"id": tu.ID(), "parentId": acme, "name": "PZT"}), protocol.CodeAlreadyExists)
	e.Fail(e.Cmd(tu.Admin, "folders.new", map[string]any{"id": tu.ID(), "parentId": tu.ID(), "name": "huérfana"}), protocol.CodeFolderNotFound)

	// Unknown keys and bad names.
	e.Fail(e.CmdRaw(tu.Admin, "folders.new", map[string]any{"id": tu.ID(), "parentId": acme, "name": "a", "extra": 1}), protocol.CodeInvalidFields)
	e.Fail(e.CmdRaw(tu.Admin, "folders.new", map[string]any{"id": tu.ID(), "parentId": acme, "name": "a/b"}), protocol.CodeInvalidFields)
	e.Fail(e.CmdRaw(tu.Admin, "folders.new", map[string]any{"id": "lowercase", "parentId": acme, "name": "a"}), protocol.CodeInvalidFields)

	// A user writes a document in any folder, the ACL does not matter: inside the closed infra
	// too. Renaming acme moves the paths.
	e.Doc(tu.User, infra, "en-infra.md", "# Infra\n")
	doc := e.Doc(tu.User, pzt, "notas.md", "# Notas\n")
	e.OK(e.Cmd(tu.Admin, "folders."+acme+".edit", map[string]any{"name": "acme-sa"}))
	d := e.OK(e.Q(tu.User, "documents.get", map[string]any{"id": doc}))
	if d["path"] != "/clients/acme-sa/pzt/notas.md" {
		t.Fatalf("path not updated: %v", d["path"])
	}
	e.Fail(e.Cmd(tu.Admin, "folders."+acme+".edit", map[string]any{"parentId": pzt}), protocol.CodeInvalidFields)
	e.Fail(e.Cmd(tu.Admin, "folders."+protocol.RootFolderID+".edit", map[string]any{"name": "x"}), protocol.CodeInvalidFields)
	e.Fail(e.Cmd(tu.Admin, "folders."+protocol.RootFolderID+".delete", nil), protocol.CodeInvalidFields)

	// Delete: not empty, then empty, then again (idempotent).
	e.Fail(e.Cmd(tu.Admin, "folders."+pzt+".delete", nil), protocol.CodeFolderNotEmpty)
	e.OK(e.Cmd(tu.User, "documents."+doc+".delete", map[string]any{"baseVersion": 1}))
	e.OK(e.Cmd(tu.Admin, "folders."+pzt+".delete", nil))
	e.OK(e.Cmd(tu.Admin, "folders."+pzt+".delete", nil))
	e.Fail(e.Q(tu.User, "folders.get", map[string]any{"id": pzt}), protocol.CodeFolderNotFound)

	// The (obsolete) ACL is still validated deny-by-default and stored.
	e.Fail(e.CmdRaw(tu.Admin, "folders."+acme+".acl.replace", map[string]any{"rules": []any{map[string]any{"principalType": "group", "principal": "x", "permission": "read"}}}), protocol.CodeInvalidFields)
	e.Fail(e.CmdRaw(tu.Admin, "folders."+acme+".acl.replace", map[string]any{"rules": []any{tu.Rule("role", "user", "read"), tu.Rule("role", "user", "write")}}), protocol.CodeInvalidFields)
	e.ACL(acme, tu.Rule("role", "admin", "manage"))
	// ...without effect: a user still writes in acme.
	e.Doc(tu.User, acme, "sigue.md", "# Sigue\n")

	ev := e.WaitEvent("folder.acl.changed", func(ev tu.Event) bool { return ev.Body["entity"].(map[string]any)["id"] == acme })
	if ev.MsgID != ev.Body["eventId"] {
		t.Errorf("Nats-Msg-Id must be the eventId: %s vs %v", ev.MsgID, ev.Body["eventId"])
	}
	e.WaitEvent("folder.updated", nil)
	e.WaitEvent("folder.deleted", nil)
	e.Eventually("access.changed for user2", func() bool {
		for _, n := range e.Notes() {
			if n.UserID == tu.User2 && n.Type == "access.changed" {
				return true
			}
		}
		return false
	})
	for _, n := range e.Notes() {
		if n.UserID == tu.External && n.Type == "access.changed" {
			t.Error("externals do not receive access.changed for folders")
		}
	}
}
