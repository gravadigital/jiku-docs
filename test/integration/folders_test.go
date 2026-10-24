package integration

import (
	"testing"

	"github.com/gravadigital/jiku-docs/internal/protocol"
	tu "github.com/gravadigital/jiku-docs/internal/testutil"
)

func TestFolderTreeAndACL(t *testing.T) {
	e := tu.NewEnv(t)
	clientes := e.Folder("", "clientes", nil)
	acme := e.Folder(clientes, "acme", nil)
	infra := e.Folder("", "infra", map[string]any{"inheritAcl": false, "externalBlocked": true, "sharingBlocked": true})

	// The user sees nothing until a rule says so.
	e.Fail(e.Q(tu.User, "folders.get", map[string]any{"id": acme}), protocol.CodeFolderNotFound)
	if items := e.Q(tu.User, "folders.list", nil).Items(); len(items) != 0 {
		t.Fatalf("user sees %d folders", len(items))
	}
	e.ACL(clientes, tu.Rule("role", "user", "read"))
	e.ACL(acme, tu.Rule("user", tu.User, "manage"))

	got := e.OK(e.Q(tu.User, "folders.get", map[string]any{"id": acme, "include": []string{"effectivePermission", "acl"}}))
	if got["path"] != "/clientes/acme/" || got["effectivePermission"] != "manage" || len(got["acl"].([]any)) != 1 {
		t.Fatalf("%v", got)
	}
	// read on /clientes/ is inherited by acme; acl needs manage.
	e.Fail(e.Q(tu.User2, "folders.get", map[string]any{"id": clientes, "include": []string{"acl"}}), protocol.CodePermissionDenied)
	list := e.Q(tu.User2, "folders.list", map[string]any{"include": []string{"effectivePermission"}, "count": true})
	if len(list.Items()) != 2 || list.M()["page"].(map[string]any)["total"].(float64) != 2 {
		t.Fatalf("user2 must see clientes and acme: %s", list.Raw)
	}
	// infra does not inherit: the admin who created it keeps an explicit manage rule (no
	// self-lockout), and nobody else sees it.
	in := e.OK(e.Q(tu.Admin, "folders.get", map[string]any{"id": infra, "include": []string{"acl"}}))
	if acl := in["acl"].([]any); len(acl) != 1 || acl[0].(map[string]any)["principal"] != tu.Admin {
		t.Fatalf("creator rule: %v", in["acl"])
	}
	e.Fail(e.Q(tu.User, "folders.get", map[string]any{"id": infra}), protocol.CodeFolderNotFound)
	e.Fail(e.Q(tu.External, "folders.get", map[string]any{"id": clientes}), protocol.CodeFolderNotFound)

	// Writes: readable without manage = permission_denied; invisible = folder_not_found.
	e.Fail(e.Cmd(tu.User2, "folders.new", map[string]any{"id": tu.ID(), "parentId": clientes, "name": "x"}), protocol.CodePermissionDenied)
	e.Fail(e.Cmd(tu.User2, "folders.new", map[string]any{"id": tu.ID(), "parentId": infra, "name": "x"}), protocol.CodeFolderNotFound)
	pzt := tu.ID()
	e.OK(e.Cmd(tu.User, "folders.new", map[string]any{"id": pzt, "parentId": acme, "name": "pzt"}))
	// Idempotent retry; another payload with the same id is already_exists; same name is already_exists.
	e.OK(e.Cmd(tu.User, "folders.new", map[string]any{"id": pzt, "parentId": acme, "name": "pzt"}))
	e.Fail(e.Cmd(tu.User, "folders.new", map[string]any{"id": pzt, "parentId": acme, "name": "otro"}), protocol.CodeAlreadyExists)
	e.Fail(e.Cmd(tu.User, "folders.new", map[string]any{"id": tu.ID(), "parentId": acme, "name": "PZT"}), protocol.CodeAlreadyExists)

	// Unknown keys and bad names.
	e.Fail(e.CmdRaw(tu.Admin, "folders.new", map[string]any{"id": tu.ID(), "parentId": acme, "name": "a", "extra": 1}), protocol.CodeInvalidFields)
	e.Fail(e.CmdRaw(tu.Admin, "folders.new", map[string]any{"id": tu.ID(), "parentId": acme, "name": "a/b"}), protocol.CodeInvalidFields)
	e.Fail(e.CmdRaw(tu.Admin, "folders.new", map[string]any{"id": "lowercase", "parentId": acme, "name": "a"}), protocol.CodeInvalidFields)

	// A document inside pzt, then rename acme: paths follow.
	doc := e.Doc(tu.User, pzt, "notas.md", "# Notas\n")
	e.OK(e.Cmd(tu.Admin, "folders."+acme+".edit", map[string]any{"name": "acme-sa"}))
	d := e.OK(e.Q(tu.User, "documents.get", map[string]any{"id": doc}))
	if d["path"] != "/clientes/acme-sa/pzt/notas.md" {
		t.Fatalf("path not updated: %v", d["path"])
	}
	// Moving into itself is invalid; moving needs manage on the destination.
	e.Fail(e.Cmd(tu.Admin, "folders."+acme+".edit", map[string]any{"parentId": pzt}), protocol.CodeInvalidFields)
	e.Fail(e.Cmd(tu.User, "folders."+pzt+".edit", map[string]any{"parentId": clientes}), protocol.CodePermissionDenied)
	// Root is immutable.
	e.Fail(e.Cmd(tu.Admin, "folders."+protocol.RootFolderID+".edit", map[string]any{"name": "x"}), protocol.CodeInvalidFields)
	e.Fail(e.Cmd(tu.Admin, "folders."+protocol.RootFolderID+".delete", nil), protocol.CodeInvalidFields)

	// Delete: not empty, then empty, then again (idempotent).
	e.Fail(e.Cmd(tu.User, "folders."+pzt+".delete", nil), protocol.CodeFolderNotEmpty)
	e.OK(e.Cmd(tu.User, "documents."+doc+".delete", map[string]any{"baseVersion": 1}))
	e.OK(e.Cmd(tu.User, "folders."+pzt+".delete", nil))
	e.OK(e.Cmd(tu.User, "folders."+pzt+".delete", nil))
	e.Fail(e.Q(tu.User, "folders.get", map[string]any{"id": pzt}), protocol.CodeFolderNotFound)

	// ACL rules are validated deny-by-default.
	e.Fail(e.CmdRaw(tu.Admin, "folders."+acme+".acl.replace", map[string]any{"rules": []any{map[string]any{"principalType": "group", "principal": "x", "permission": "read"}}}), protocol.CodeInvalidFields)
	e.Fail(e.CmdRaw(tu.Admin, "folders."+acme+".acl.replace", map[string]any{"rules": []any{tu.Rule("role", "user", "read"), tu.Rule("role", "user", "write")}}), protocol.CodeInvalidFields)

	ev := e.WaitEvent("folder.acl.changed", func(ev tu.Event) bool { return ev.Body["entity"].(map[string]any)["id"] == acme })
	if ev.MsgID != ev.Body["eventId"] {
		t.Errorf("Nats-Msg-Id must be the eventId: %s vs %v", ev.MsgID, ev.Body["eventId"])
	}
	e.WaitEvent("folder.updated", nil)
	e.WaitEvent("folder.deleted", nil)
	// acl changes reach every internal person as access.changed, without detail.
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
			t.Error("externals do not receive access.changed")
		}
	}
}
