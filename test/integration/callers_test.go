package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/gravadigital/jiku-docs/internal/coreapi"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/registry"
	tu "github.com/gravadigital/jiku-docs/internal/testutil"
)

func TestCallerClasses(t *testing.T) {
	e := tu.NewEnv(t)
	f := writable(e)
	e.Fail(e.Q(tu.Ghost, "documents.list", nil), protocol.CodeUnknownCaller)
	e.Fail(e.Q(tu.Observer, "documents.list", nil), protocol.CodeCallerNotAuthorized)
	// actor only from the api.
	e.Fail(e.QRaw(tu.User, "documents.list", map[string]any{"actor": map[string]any{"id": tu.Admin}}), protocol.CodeInvalidFields)
	e.Fail(e.CmdRaw(tu.Connector, "folders.new", map[string]any{"actor": map[string]any{"id": tu.Admin}, "id": tu.ID(), "parentId": f, "name": "x"}), protocol.CodeInvalidFields)
	// The api with actor writes as the person: author and event actor are the person.
	id := tu.ID()
	e.OK(e.Cmd(tu.APIUser, "documents.new", map[string]any{"actor": map[string]any{"id": tu.User, "roles": []string{"user"}, "name": "Usuario",
		"username": "usuario", "email": "usuario@example.com"}, "id": id, "folderId": f,
		"fileName": "via-api.md", "content": "hola", "checksum": tu.Sum("hola")}))
	d := e.OK(e.Q(tu.User, "documents.get", map[string]any{"id": id}))
	if d["createdBy"] != tu.User {
		t.Fatalf("author %v", d["createdBy"])
	}
	ev := e.WaitEvent("document.created", func(ev tu.Event) bool { return ev.Body["entity"].(map[string]any)["id"] == id })
	if a := ev.Body["actor"].(map[string]any); a["id"] != tu.User || a["name"] != "Usuario" || a["via"] != "human" {
		t.Fatal(a)
	}
	// The api acting for a person without access is refused like the person.
	e.Fail(e.Cmd(tu.APIUser, "documents."+id+".delete", map[string]any{"actor": actor(tu.External, "external-user"), "baseVersion": 1}), protocol.CodeCallerNotAuthorized)
	// D-11: the class comes from actor.roles. Without a product role there is no access; without
	// roles the actor is malformed.
	e.Fail(e.Q(tu.APIUser, "documents.list", map[string]any{"actor": actor(tu.Ghost, "observer")}), protocol.CodeCallerNotAuthorized)
	e.Fail(e.Q(tu.APIUser, "documents.list", map[string]any{"actor": actor(tu.Ghost)}), protocol.CodeCallerNotAuthorized)
	e.Fail(e.Q(tu.APIUser, "documents.list", map[string]any{"actor": actor(tu.Admin, "internal-app")}), protocol.CodeCallerNotAuthorized)
	e.Fail(e.QRaw(tu.APIUser, "documents.list", map[string]any{"actor": map[string]any{"id": tu.Admin}}), protocol.CodeInvalidFields)
	e.Fail(e.QRaw(tu.APIUser, "documents.list", map[string]any{"actor": map[string]any{"id": tu.Admin, "roles": []string{"admin"}, "via": "human"}}), protocol.CodeInvalidFields)
	e.Fail(e.QRaw(tu.APIUser, "documents.list", map[string]any{"actor": map[string]any{"id": tu.Admin, "roles": "admin"}}), protocol.CodeInvalidFields)
	// A connector (internal-app) is a service: reads everything, writes as itself with via service.
	sid := tu.ID()
	e.OK(e.Cmd(tu.Connector, "documents.new", map[string]any{"id": sid, "folderId": f, "fileName": "bot.md", "content": "bot", "checksum": tu.Sum("bot"), "via": "agent"}))
	sv := e.Q(tu.Connector, "documents.versions.list", map[string]any{"filter": map[string]any{"documentId": sid}}).Items()
	if sv[0]["via"] != "service" || sv[0]["author"] != tu.Connector {
		t.Fatal(sv)
	}
	// Locks taken by a service keep the requested via; the event actor says service.
	e.OK(e.Cmd(tu.Connector, "documents."+sid+".lock", map[string]any{"sessionId": tu.ID(), "via": "agent"}))
	lk := e.WaitEvent("document.locked", func(ev tu.Event) bool { return ev.Body["entity"].(map[string]any)["id"] == sid })
	if lk.Body["actor"].(map[string]any)["via"] != "service" || lk.Body["snapshot"].(map[string]any)["lock"].(map[string]any)["via"] != "agent" {
		t.Fatal(lk.Body)
	}
	// A wrong id in the subject.
	e.Fail(e.CmdRaw(tu.User, "documents.not-an-id.delete", map[string]any{"baseVersion": 1}), protocol.CodeInvalidFields)
}

// D-11: core refreshes a person's roles in `users` only when that person runs a core command, so
// a web user who only reads may have a stale row. The api sends the roles of the verified token
// and jiku-docs uses them: no users.list for the api with actor.
func TestAPIActorRolesWinOverStaleUsersRow(t *testing.T) {
	e := tu.NewEnv(t)
	f := writable(e)
	fresh := "500000000000000088"
	e.Core.SetUser(coreapi.User{ID: fresh, Name: "Recién llegada", Username: "nueva", Roles: []string{}, IdentityType: "person"})
	// By the bus, the stale row decides: no product role.
	e.Fail(e.Q(fresh, "documents.list", nil), protocol.CodeCallerNotAuthorized)
	before := e.Core.CallCount("users.list")
	a := map[string]any{"id": fresh, "roles": []string{"user"}, "name": "Recién llegada"}
	e.OK(e.Q(tu.APIUser, "documents.list", map[string]any{"actor": a}))
	id := tu.ID()
	e.OK(e.Cmd(tu.APIUser, "documents.new", map[string]any{"actor": a, "id": id, "folderId": f, "fileName": "fresca.md", "content": "hola", "checksum": tu.Sum("hola")}))
	if d := e.OK(e.Q(tu.Admin, "documents.get", map[string]any{"id": id})); d["createdBy"] != fresh {
		t.Fatalf("author %v", d["createdBy"])
	}
	ev := e.WaitEvent("document.created", func(ev tu.Event) bool { return ev.Body["entity"].(map[string]any)["id"] == id })
	if ev.Body["actor"].(map[string]any)["name"] != "Recién llegada" {
		t.Fatal(ev.Body["actor"])
	}
	if n := e.Core.CallCount("users.list") - before; n != 0 {
		t.Fatalf("the api with actor consulted users.list %d times", n)
	}
	// Role rules of the ACL use actor.roles too: a folder readable only by `admin`.
	adm := e.Folder("", "solo-admin", map[string]any{"inheritAcl": false})
	e.ACL(adm, tu.Rule("role", "admin", "manage"))
	e.Doc(tu.Admin, adm, "a.md", "a")
	if n := len(e.Q(tu.APIUser, "documents.list", map[string]any{"filter": map[string]any{"folderId": adm}, "actor": a}).Items()); n != 0 {
		t.Fatalf("user sees %d docs of an admin-only folder", n)
	}
	admin := map[string]any{"id": fresh, "roles": []string{"admin"}}
	if n := len(e.Q(tu.APIUser, "documents.list", map[string]any{"filter": map[string]any{"folderId": adm}, "actor": admin}).Items()); n != 1 {
		t.Fatalf("admin by actor.roles sees %d docs", n)
	}
}

func TestCoreUnavailable(t *testing.T) {
	e := tu.NewEnv(t, tu.Options{CoreTimeout: 300 * time.Millisecond})
	f := writable(e)
	e.OK(e.Q(tu.User, "documents.list", nil)) // warms the cache of this user
	newcomer := "500000000000000077"
	u := e.Core.User(tu.User2)
	u.ID = newcomer
	e.Core.SetUser(u)
	e.Core.SetDown(true)
	// Cached identities keep working until they expire; uncached ones fail closed.
	e.OK(e.Q(tu.User, "documents.list", nil))
	e.Fail(e.Q(newcomer, "documents.list", nil), protocol.CodeUpstreamUnavailable)
	rel := "---\njiku:\n  projects: [31]\n---\n"
	e.Fail(e.Cmd(tu.User, "documents.new", map[string]any{"id": tu.ID(), "folderId": f, "fileName": "r.md", "content": rel, "checksum": tu.Sum(rel)}), protocol.CodeUpstreamUnavailable)
	e.Fail(e.Cmd(tu.User, "attachments.request-upload", map[string]any{"id": tu.ID(), "documentId": e.Doc(tu.User, f, "a.md", "a"),
		"fileName": "a.png", "mimeType": "image/png", "fileSize": 1, "checksum": tu.Sum("a")}), protocol.CodeUpstreamUnavailable)
	e.Core.SetDown(false)
	e.OK(e.Q(newcomer, "documents.list", nil))
}

func TestNotifications(t *testing.T) {
	e := tu.NewEnv(t)
	pub := e.Folder("", "general", nil)
	e.ACL(pub, tu.Rule("role", "user", "write"), tu.Rule("role", "admin", "manage"))
	priv := e.Folder("", "privado", map[string]any{"inheritAcl": false})
	open := e.Doc(tu.User, pub, "abierto.md", "---\nvisibility: internal\njiku:\n  projects: [12]\n---\nuno")
	closed := e.Doc(tu.Admin, priv, "cerrado.md", "dos")
	public := e.Doc(tu.Admin, pub, "publico.md", "---\nvisibility: public\njiku:\n  projects: [12]\n---\ntres")
	e.Eventually("fan-out", func() bool {
		n := 0
		for _, x := range e.Notes() {
			if x.Type == "document.created" {
				n++
			}
		}
		return n >= 6 // open: admin,user,user2; closed: admin; public: admin,user,user2,external = 8
	})
	time.Sleep(300 * time.Millisecond)
	got := map[string][]string{}
	for _, n := range e.Notes() {
		if n.Type != "document.created" {
			continue
		}
		id := n.Body["entity"].(map[string]any)["id"].(string)
		got[id] = append(got[id], n.UserID)
		if _, ok := n.Body["snapshot"].(map[string]any)["relations"]; ok {
			t.Fatal("notifications never carry relations")
		}
	}
	if len(got[open]) != 3 || len(got[closed]) != 1 || len(got[public]) != 4 {
		t.Fatalf("fan-out %v", got)
	}
	for _, u := range got[closed] {
		if u != tu.Admin {
			t.Fatal("only who can read gets notified")
		}
	}
	if !strings.Contains(strings.Join(got[public], ","), tu.External) || strings.Contains(strings.Join(got[open], ","), tu.External) {
		t.Fatalf("external fan-out %v", got)
	}
}

func TestModuleRegistry(t *testing.T) {
	e := tu.NewEnv(t)
	select {
	case <-e.Inst.Registered:
	case <-time.After(10 * time.Second):
		t.Fatal("module not registered")
	}
	ctx := context.Background()
	js, _ := jetstream.New(e.Client)
	kv, err := js.KeyValue(ctx, "JIKU_MODULES")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := kv.Get(ctx, "docs")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Contract.ValidateModuleRecord(entry.Value()); err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	_ = json.Unmarshal(entry.Value(), &rec)
	if rec["serviceName"] != "jiku-docs" || rec["protocol"] != "v1" {
		t.Fatal(rec)
	}
	// The micro services answer the standard ping the api uses for the module status.
	if _, err := e.Client.Request("$SRV.PING.jiku-docs-queries", nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	// Uninstall.
	if err := registry.Deregister(ctx, e.Inst.JS); err != nil {
		t.Fatal(err)
	}
	if _, err := kv.Get(ctx, "docs"); err == nil {
		t.Fatal("key still there")
	}
	// The stream exists with the contract's parameters.
	st, err := js.Stream(ctx, "JIKU_DOCS_EVENTS")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := st.Info(ctx)
	if info.Config.MaxAge != 30*24*time.Hour || info.Config.Duplicates != 2*time.Minute || info.Config.Subjects[0] != "dev.docs-events.v1.>" {
		t.Fatal(info.Config)
	}
}
