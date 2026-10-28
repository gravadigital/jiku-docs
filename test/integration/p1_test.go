package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gravadigital/jiku-docs/internal/coreapi"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	tu "github.com/gravadigital/jiku-docs/internal/testutil"
)

func newDoc(e *tu.Env, user string, p map[string]any) map[string]any {
	e.T.Helper()
	content := p["content"].(string)
	p["checksum"] = tu.Sum(content)
	if _, ok := p["id"]; !ok {
		p["id"] = tu.ID()
	}
	return e.OK(e.Cmd(user, "documents.new", p))
}

// P-1, section 2: documents.new without folderId or fileName.
func TestGeneratedLocation(t *testing.T) {
	e := tu.NewEnv(t)
	cases := []struct{ content, path string }{
		{"---\njiku:\n  projects: [12]\n---\n# Minuta de kickoff\n", "/clients/acme-s-a/exo-videoconferencia/minuta-de-kickoff.md"},
		{"---\njiku:\n  projects: [12]\n---\n# Minuta de kickoff\n\notra\n", "/clients/acme-s-a/exo-videoconferencia/minuta-de-kickoff-2.md"},
		{"---\njiku:\n  clients: [4]\n---\n# Acuerdo marco\n", "/clients/acme-s-a/general/acuerdo-marco.md"},
		{"---\njiku:\n  projects: [31, 12]\n  clients: [4]\n---\n# Varios proyectos\n", "/clients/acme-s-a/portal-zeta/varios-proyectos.md"},
		{"---\ntitle: Guía de estilo\n---\nsin encabezado\n", "/general/guia-de-estilo.md"},
		{"sin título\n", "/general/document.md"},
	}
	for _, c := range cases {
		r := newDoc(e, tu.User, map[string]any{"content": c.content})
		if r["path"] != c.path || r["folderId"] == nil || !strings.HasSuffix(c.path, r["fileName"].(string)) {
			t.Errorf("%q: got %v, want %s", c.content, r, c.path)
		}
	}
	// A project without a client goes under no-client.
	r := newDoc(e, tu.User, map[string]any{"content": "---\njiku:\n  projects: [31]\n---\n# Portal\n"})
	if r["path"] != "/clients/no-client/portal-zeta/portal.md" {
		t.Fatal(r)
	}
	// A given folderId with a generated name, and a given name with a generated folder.
	f := e.Folder("", "manual", nil)
	if r := newDoc(e, tu.User, map[string]any{"folderId": f, "content": "# Nombre generado\n"}); r["path"] != "/manual/nombre-generado.md" {
		t.Fatal(r)
	}
	if r := newDoc(e, tu.User, map[string]any{"fileName": "Propio.md", "content": "# X\n"}); r["path"] != "/general/Propio.md" {
		t.Fatal(r)
	}
	// Retrying the same creation without folder returns the same document.
	p := map[string]any{"id": tu.ID(), "content": "# Reintento\n"}
	first := newDoc(e, tu.User, p)
	if again := newDoc(e, tu.User, p); again["path"] != first["path"] {
		t.Fatalf("retry %v vs %v", again, first)
	}
	// The generated folders exist and were announced; a user still cannot create one by hand.
	e.WaitEvent("folder.created", func(ev tu.Event) bool {
		return ev.Body["entity"].(map[string]any)["path"] == "/clients/acme-s-a/exo-videoconferencia/"
	})
	// Changing relations later does not move the document.
	id := first["id"].(string)
	e.OK(e.Cmd(tu.User, "documents."+id+".relations.update", map[string]any{"baseVersion": 1, "add": map[string]any{"projects": []int{12}}}))
	if d := e.OK(e.Q(tu.User, "documents.get", map[string]any{"id": id})); d["path"] != first["path"] {
		t.Fatalf("moved: %v", d["path"])
	}
}

func TestRelationsUpdate(t *testing.T) {
	e := tu.NewEnv(t)
	f := writable(e)
	content := "---\n# mi comentario\ntitle: Acta\ntopics: [infra]\njiku:\n  tasks: [900]\n---\n# Acta\n\ncuerpo intacto\n"
	id := e.Doc(tu.User, f, "acta.md", content)
	r := e.OK(e.Cmd(tu.User, "documents."+id+".relations.update", map[string]any{"baseVersion": 1, "add": map[string]any{"projects": []int{12}}}))
	rel := r["relations"].(map[string]any)
	if r["version"].(float64) != 2 || len(rel["projects"].([]any)) != 1 || rel["clients"].([]any)[0].(float64) != 4 || rel["tasks"].([]any)[0].(float64) != 900 {
		t.Fatalf("assigning a project adds its client: %v", r)
	}
	g := e.OK(e.Q(tu.User, "documents.get", map[string]any{"id": id, "include": []string{"content"}}))
	want := "---\n# mi comentario\ntitle: Acta\ntopics: [infra]\njiku:\n  clients: [4]\n  projects: [12]\n  tasks: [900]\n---\n# Acta\n\ncuerpo intacto\n"
	if g["content"] != want {
		t.Fatalf("frontmatter:\n%s", g["content"])
	}
	v := e.Q(tu.User, "documents.versions.list", map[string]any{"filter": map[string]any{"documentId": id}}).Items()
	if v[0]["kind"] != "update" || v[0]["message"] != "Client assigned: Acmé S.A.; Project assigned: EXO - Videoconferencia" || v[0]["authorName"] != "Usuario" || v[0]["via"] != "human" {
		t.Fatalf("version %v", v[0])
	}
	// Idempotent: nothing to change is success with the current version, even with a stale base.
	if again := e.OK(e.Cmd(tu.User, "documents."+id+".relations.update", map[string]any{"baseVersion": 1, "add": map[string]any{"projects": []int{12}}})); again["version"].(float64) != 2 {
		t.Fatal(again)
	}
	e.Fail(e.Cmd(tu.User, "documents."+id+".relations.update", map[string]any{"baseVersion": 1, "remove": map[string]any{"tasks": []int{900}}}), protocol.CodeVersionConflict)
	e.Fail(e.Cmd(tu.User, "documents."+id+".relations.update", map[string]any{"baseVersion": 2, "add": map[string]any{"projects": []int{999}}}), protocol.CodeInvalidRelation)
	e.Fail(e.Cmd(tu.User, "documents."+id+".relations.update", map[string]any{"baseVersion": 2, "add": map[string]any{"tasks": []int{1}}, "remove": map[string]any{"tasks": []int{1}}}), protocol.CodeInvalidFields)
	e.Fail(e.Cmd(tu.User, "documents."+id+".relations.update", map[string]any{"baseVersion": 2}), protocol.CodeInvalidFields)
	e.Fail(e.CmdRaw(tu.User, "documents."+id+".relations.update", map[string]any{"baseVersion": 2, "via": "agent", "add": map[string]any{"tasks": []int{1}}}), protocol.CodeInvalidFields)
	e.Fail(e.CmdRaw(tu.User, "documents."+id+".relations.update", map[string]any{"baseVersion": 2, "add": map[string]any{"tasks": []int{-1}}}), protocol.CodeInvalidFields)
	r = e.OK(e.Cmd(tu.User2, "documents."+id+".relations.update", map[string]any{"baseVersion": 2, "remove": map[string]any{"projects": []int{12}, "clients": []int{4}, "tasks": []int{900}}}))
	if r["version"].(float64) != 3 {
		t.Fatal(r)
	}
	g = e.OK(e.Q(tu.User, "documents.get", map[string]any{"id": id, "include": []string{"content", "relations"}}))
	if g["content"] != "---\n# mi comentario\ntitle: Acta\ntopics: [infra]\n---\n# Acta\n\ncuerpo intacto\n" || g["updatedByName"] != "Usuaria" {
		t.Fatalf("removed:\n%v", g)
	}
	ev := e.WaitEvent("document.updated", func(ev tu.Event) bool { return ev.Body["snapshot"].(map[string]any)["version"].(float64) == 3 })
	if ev.Body["changes"].(map[string]any)["relations"] == nil {
		t.Fatal(ev.Body)
	}
	// A lock of another session blocks it; with the session it goes through.
	s := tu.ID()
	e.OK(e.Cmd(tu.User, "documents."+id+".lock", map[string]any{"sessionId": s, "via": "human"}))
	e.Fail(e.Cmd(tu.User2, "documents."+id+".relations.update", map[string]any{"baseVersion": 3, "add": map[string]any{"projects": []int{31}}}), protocol.CodeDocumentLocked)
	e.OK(e.Cmd(tu.User, "documents."+id+".relations.update", map[string]any{"baseVersion": 3, "sessionId": s, "add": map[string]any{"projects": []int{31}}}))
	// Externals never write.
	e.Fail(e.Cmd(tu.External, "documents."+id+".relations.update", map[string]any{"baseVersion": 4, "add": map[string]any{"projects": []int{31}}}), protocol.CodeCallerNotAuthorized)
}

func TestSuggestRelations(t *testing.T) {
	e := tu.NewEnv(t)
	f := writable(e)
	e.Core.SetProject(coreapi.Project{ID: 40, Name: "Tesisler - Mobile", Code: "TSM"})
	e.Core.SetClient(7, "Verifarma")
	id := e.Doc(tu.User, f, "a.md", "# Reunión de Videoconferencia\n\nVimos el portal zeta y el portal zeta otra vez. Verifarma pidió algo.\n")
	items := e.Q(tu.User, "documents.suggest-relations", map[string]any{"id": id}).Items()
	if len(items) != 3 {
		t.Fatalf("%v", items)
	}
	first, second, third := items[0], items[1], items[2]
	if first["projectId"].(float64) != 12 || first["clientId"].(float64) != 4 || first["reason"] != "The title mentions “Videoconferencia”" {
		t.Fatalf("title weighs more: %v", items)
	}
	if second["projectId"].(float64) != 31 || second["clientId"] != nil || !strings.Contains(second["reason"].(string), "(2 times)") || second["score"].(float64) >= first["score"].(float64) {
		t.Fatalf("content: %v", second)
	}
	if third["projectId"] != nil || third["clientId"].(float64) != 7 {
		t.Fatalf("client-only candidate: %v", third)
	}
	// Linked projects are excluded.
	e.OK(e.Cmd(tu.User, "documents."+id+".relations.update", map[string]any{"baseVersion": 1, "add": map[string]any{"projects": []int{12}}}))
	for _, it := range e.Q(tu.User, "documents.suggest-relations", map[string]any{"id": id}).Items() {
		if it["projectId"] == float64(12) {
			t.Fatal("linked project suggested")
		}
	}
	e.Fail(e.Q(tu.User, "documents.suggest-relations", map[string]any{"id": tu.ID()}), protocol.CodeDocumentNotFound)
	pub := e.Doc(tu.Admin, f, "p.md", "---\nvisibility: public\njiku:\n  projects: [12]\n---\n# Portal Zeta\n")
	e.Fail(e.Q(tu.External, "documents.suggest-relations", map[string]any{"id": pub}), protocol.CodePermissionDenied)
}

func TestFiltersMatchAndNames(t *testing.T) {
	e := tu.NewEnv(t)
	f := writable(e)
	direct := e.Doc(tu.User, f, "d.md", "---\ntopics: [infra]\njiku:\n  projects: [12]\n  clients: [4]\n---\n# Directo aduana\n")
	viaReq := e.Doc(tu.User2, f, "r.md", "---\njiku:\n  requirements: [812]\n---\n# Por requisito aduana\n")
	none := e.Doc(tu.Admin, f, "n.md", "# Suelto aduana\n")
	ids := func(r tu.Resp, key string) string {
		var out []string
		for _, it := range r.Items() {
			out = append(out, it[key].(string))
		}
		return strings.Join(out, ",")
	}
	list := func(filter map[string]any) string {
		return ids(e.Q(tu.User, "documents.list", map[string]any{"filter": filter, "sort": []string{"path"}}), "id")
	}
	if got := list(map[string]any{"hasProject": false}); got != none {
		t.Errorf("no project: %s", got)
	}
	if got := list(map[string]any{"hasProject": true}); got != direct+","+viaReq {
		t.Errorf("hasProject: %s", got)
	}
	if got := list(map[string]any{"hasClient": false}); got != none+","+viaReq {
		t.Errorf("no client: %s", got)
	}
	if got := list(map[string]any{"hasTopic": true}); got != direct {
		t.Errorf("hasTopic: %s", got)
	}
	if got := list(map[string]any{"hasTopic": false, "hasProject": true}); got != viaReq {
		t.Errorf("combined: %s", got)
	}
	// projectMatch, clients, projects and the names.
	r := e.Q(tu.User, "documents.list", map[string]any{"filter": map[string]any{"projectId": 12}, "sort": []string{"path"}})
	items := r.Items()
	if len(items) != 2 || items[0]["projectMatch"] != "direct" || items[1]["projectMatch"] != "indirect" {
		t.Fatalf("projectMatch %s", r.Raw)
	}
	if items[0]["clients"].([]any)[0].(float64) != 4 || items[0]["projects"].([]any)[0].(float64) != 12 || len(items[1]["projects"].([]any)) != 0 {
		t.Fatalf("clients/projects %v", items)
	}
	if items[0]["updatedByName"] != "Usuario" || items[1]["updatedByName"] != "Usuaria" {
		t.Fatalf("names %v", items)
	}
	if _, ok := e.Q(tu.User, "documents.list", nil).Items()[0]["projectMatch"]; ok {
		t.Error("projectMatch only with filter.projectId")
	}
	full := e.Q(tu.User, "documents.list", map[string]any{"filter": map[string]any{"id": direct}, "fields": []string{"createdByName", "updatedByName"}}).Items()
	if full[0]["createdByName"] != "Usuario" {
		t.Fatal(full)
	}
	e.Eventually("indexed", func() bool {
		return len(e.Q(tu.User, "documents.search", map[string]any{"query": "aduana"}).Items()) == 3
	})
	hits := e.Q(tu.User, "documents.search", map[string]any{"query": "aduana", "filter": map[string]any{"projectId": 12}}).Items()
	if len(hits) != 2 {
		t.Fatalf("search %v", hits)
	}
	for _, h := range hits {
		want := map[string]string{direct: "direct", viaReq: "indirect"}[h["documentId"].(string)]
		if h["projectMatch"] != want || h["updatedByName"] == nil || h["createdByName"] == nil {
			t.Fatalf("search hit %v", h)
		}
	}
	if got := ids(e.Q(tu.User, "documents.search", map[string]any{"query": "aduana", "filter": map[string]any{"hasProject": false}}), "documentId"); got != none {
		t.Errorf("search no project: %s", got)
	}
	// Names of the service and of the api.
	sid := tu.ID()
	e.OK(e.Cmd(tu.Connector, "documents.new", map[string]any{"id": sid, "folderId": f, "fileName": "bot.md", "content": "b", "checksum": tu.Sum("b")}))
	if d := e.OK(e.Q(tu.User, "documents.get", map[string]any{"id": sid})); d["createdByName"] != "Conector" {
		t.Fatal(d)
	}
	e.OK(e.Cmd(tu.APIUser, "documents."+sid+".update", map[string]any{"actor": map[string]any{"id": tu.User, "roles": []string{"user"}, "username": "usu"},
		"baseVersion": 1, "content": "c", "checksum": tu.Sum("c")}))
	g := e.OK(e.Q(tu.User, "documents.get", map[string]any{"id": sid, "version": 1}))
	g2 := e.OK(e.Q(tu.User, "documents.get", map[string]any{"id": sid}))
	if g["updatedByName"] != "Conector" || g2["updatedByName"] != "usu" {
		t.Fatalf("names by version %v / %v", g["updatedByName"], g2["updatedByName"])
	}
	// meta.describe publishes the new filters, fields and the restricted visibility.
	docs := e.OK(e.Q(tu.User, "meta.describe", map[string]any{"resources": []string{"documents"}}))["resources"].(map[string]any)["documents"].(map[string]any)
	fl := docs["filterable"].(map[string]any)
	if fl["hasProject"] == nil || fl["hasClient"] == nil || fl["hasTopic"] == nil || docs["base"].(map[string]any)["updatedByName"] == nil ||
		len(docs["enums"].(map[string]any)["visibility"].([]any)) != 3 {
		t.Fatalf("describe %v", docs)
	}
}

// The P-1 data migration over a world built with the folder ACL: restricted where a `user` could
// not read, internal for a public document under externalBlocked, links revoked, a version with a
// message, applied once.
func TestMigrationP1(t *testing.T) {
	e := tu.NewEnv(t)
	ctx := context.Background()
	open := e.Folder("", "abierta", nil)
	e.ACL(open, tu.Rule("role", "user", "write"))
	imported := e.Folder("", "imported", map[string]any{"inheritAcl": false})
	e.ACL(imported, tu.Rule("role", "admin", "manage"))
	sub := e.Folder(imported, "sub", nil) // inherits the closed one
	perUser := e.Folder("", "por-usuario", map[string]any{"inheritAcl": false})
	e.ACL(perUser, tu.Rule("user", tu.User, "write")) // a rule for one user id does not count
	infra := e.Folder("", "infra", map[string]any{"inheritAcl": false, "externalBlocked": true})
	e.ACL(infra, tu.Rule("role", "user", "read"))
	srv := e.Folder(infra, "servidores", nil)
	rootDoc := e.Doc(tu.Admin, protocol.RootFolderID, "raiz.md", "# Raíz\n") // the root is admin-only
	keep := e.Doc(tu.Admin, open, "abierto.md", "---\nvisibility: public\njiku:\n  projects: [12]\n---\n# Abierto\n")
	imp := e.Doc(tu.Connector, imported, "nota.md", "---\ntitle: Nota\nhedgedoc:\n  id: x\n---\ncuerpo\n")
	deep := e.Doc(tu.Connector, sub, "honda.md", "sin frontmatter\n")
	pu := e.Doc(tu.Admin, perUser, "pu.md", "# PU\n")
	blocked := e.Doc(tu.Admin, srv, "srv.md", "---\nvisibility: public\njiku:\n  projects: [12]\n---\n# Srv\n")
	gone := e.Doc(tu.Admin, imported, "borrado.md", "# Borrado\n")
	e.OK(e.Cmd(tu.Admin, "documents."+gone+".delete", map[string]any{"baseVersion": 1}))
	exp := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	sh := e.OK(e.Cmd(tu.Admin, "shares.new", map[string]any{"id": tu.ID(), "documentId": imp, "mode": "latest", "expiresAt": exp}))
	// The world above was written with the new model, so reset the migration marker and run it
	// as a service that starts on that data.
	if _, err := e.DB.Exec(ctx, `DELETE FROM service_state WHERE key LIKE 'migration:%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, `UPDATE documents SET created_by_name = NULL, updated_by_name = NULL; UPDATE document_versions SET author_name = NULL`); err != nil {
		t.Fatal(err)
	}
	rep, err := e.Inst.Svc.MigrateP1(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Restricted != 5 || rep.Internalized != 1 || rep.SharesRevoked != 1 || rep.Versions != 6 {
		t.Fatalf("report %+v", rep)
	}
	vis := func(id string) string {
		var v string
		if err := e.DB.QueryRow(ctx, `SELECT visibility FROM documents WHERE id = $1`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	for id, want := range map[string]string{rootDoc: "restricted", keep: "public", imp: "restricted", deep: "restricted", pu: "restricted",
		blocked: "internal", gone: "restricted"} {
		if got := vis(id); got != want {
			t.Errorf("%s: %s, want %s", id, got, want)
		}
	}
	g := e.OK(e.Q(tu.Admin, "documents.get", map[string]any{"id": imp, "include": []string{"content"}}))
	if g["content"] != "---\nvisibility: restricted\ntitle: Nota\nhedgedoc:\n  id: x\n---\ncuerpo\n" || g["version"].(float64) != 2 {
		t.Fatalf("rewritten %v", g)
	}
	if d := e.OK(e.Q(tu.Admin, "documents.get", map[string]any{"id": deep, "include": []string{"content"}})); d["content"] != "---\nvisibility: restricted\n---\nsin frontmatter\n" {
		t.Fatalf("no frontmatter: %q", d["content"])
	}
	v := e.Q(tu.Admin, "documents.versions.list", map[string]any{"filter": map[string]any{"documentId": blocked}}).Items()
	if v[0]["via"] != "service" || !strings.HasPrefix(v[0]["message"].(string), "P-1 migration") || v[0]["protected"] != true {
		t.Fatal(v[0])
	}
	e.Fail(e.Q(tu.User, "documents.get", map[string]any{"id": imp}), protocol.CodeDocumentNotFound)
	e.Fail(e.Q(tu.APIUser, "shares.resolve", map[string]any{"token": sh["token"]}), protocol.CodeShareNotFound)
	e.Fail(e.Q(tu.External, "documents.get", map[string]any{"id": blocked}), protocol.CodeDocumentNotFound)
	e.OK(e.Q(tu.External, "documents.get", map[string]any{"id": keep}))
	// Applied once: an admin who changes it later is not undone by a restart.
	back := "# Raíz pública para todos\n"
	e.OK(e.Cmd(tu.Admin, "documents."+rootDoc+".update", map[string]any{"baseVersion": 2, "content": back, "checksum": tu.Sum(back)}))
	if rep2, err := e.Inst.Svc.MigrateP1(ctx); err != nil || rep2.Versions != 6 || vis(rootDoc) != "internal" {
		t.Fatalf("second run %+v %v %s", rep2, err, vis(rootDoc))
	}
	// Names backfill from core.
	if done, err := e.Inst.Svc.BackfillNames(ctx); err != nil || !done {
		t.Fatal(done, err)
	}
	var n int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM documents WHERE created_by_name IS NULL`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d documents without name", n)
	}
	if d := e.OK(e.Q(tu.Admin, "documents.get", map[string]any{"id": keep})); d["createdByName"] != "Admin" {
		t.Fatal(d)
	}
}
