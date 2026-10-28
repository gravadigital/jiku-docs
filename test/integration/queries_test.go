package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gravadigital/jiku-docs/internal/protocol"
	tu "github.com/gravadigital/jiku-docs/internal/testutil"
)

func fm(projects string, visibility string, topics string) string {
	return "---\nvisibility: " + visibility + "\ntopics: [" + topics + "]\njiku:\n  projects: [" + projects + "]\n---\n"
}

// actor is the envelope the api sends for a person (D-11).
func actor(id string, roles ...string) map[string]any {
	if roles == nil {
		roles = []string{}
	}
	return map[string]any{"id": id, "roles": roles}
}

func TestListFiltersAndPagination(t *testing.T) {
	e := tu.NewEnv(t)
	clients := e.Folder("", "clients", nil)
	e.ACL(clients, tu.Rule("role", "user", "write"), tu.Rule("role", "admin", "manage"))
	acme := e.Folder(clients, "acme", nil)
	interno := e.Folder("", "interno", map[string]any{"inheritAcl": false})
	var ids []string
	for i := 0; i < 7; i++ {
		ids = append(ids, e.Doc(tu.User, acme, fmt.Sprintf("doc-%d.md", i), fm("12", "internal", "infra")+fmt.Sprintf("# Doc %d\n", i)))
	}
	reqDoc := e.Doc(tu.User, clients, "req.md", "---\njiku:\n  requirements: [812]\n---\n# Requisito\n")
	e.Doc(tu.Admin, interno, "secreto.md", fm("12", "restricted", "rrhh")+"# Interno\n")

	// projectId includes documents related to requirements of the project; the restricted one is
	// not readable by a user.
	byProject := e.Q(tu.User, "documents.list", map[string]any{"filter": map[string]any{"projectId": 12}, "count": true})
	if n := len(byProject.Items()); n != 8 || byProject.M()["page"].(map[string]any)["total"].(float64) != 8 {
		t.Fatalf("projectId: %d items %s", n, byProject.Raw)
	}
	// Keyset pagination: three pages of 3, no duplicates, the absence of cursor ends it.
	seen := map[string]bool{}
	var cursor any
	pages := 0
	for {
		p := map[string]any{"filter": map[string]any{"projectId": []int{12}}, "sort": []string{"path"}, "page": map[string]any{"limit": 3, "cursor": cursor}}
		r := e.Q(tu.User, "documents.list", p)
		pages++
		for _, it := range r.Items() {
			if seen[it["id"].(string)] {
				t.Fatalf("duplicate %v", it["id"])
			}
			seen[it["id"].(string)] = true
		}
		c, ok := r.M()["page"].(map[string]any)["cursor"]
		if !ok {
			break
		}
		cursor = c
		// Changing the filter between pages is invalid_cursor.
		e.Fail(e.Q(tu.User, "documents.list", map[string]any{"filter": map[string]any{"projectId": 31}, "sort": []string{"path"}, "page": map[string]any{"cursor": c}}), protocol.CodeInvalidCursor)
	}
	if len(seen) != 8 || pages != 3 {
		t.Fatalf("paginated %d documents in %d pages", len(seen), pages)
	}
	// Filters: path (exact, case-insensitive), pathPrefix, topic, or, fileName prefix, fields, include.
	one := e.Q(tu.User, "documents.list", map[string]any{"filter": map[string]any{"path": "/CLIENTS/acme/DOC-3.md"}, "fields": []string{"path", "fileName"}, "include": []string{"topics", "relations", "lock"}}).Items()
	if len(one) != 1 || one[0]["id"] != ids[3] || one[0]["fileName"] != "doc-3.md" || one[0]["title"] != nil || one[0]["lock"] != nil {
		t.Fatalf("path filter: %v", one)
	}
	if got := len(e.Q(tu.User, "documents.list", map[string]any{"filter": map[string]any{"pathPrefix": "/clients/acme/"}}).Items()); got != 7 {
		t.Errorf("pathPrefix %d", got)
	}
	if got := len(e.Q(tu.User, "documents.list", map[string]any{"filter": map[string]any{"or": []any{map[string]any{"requirementId": 812}, map[string]any{"fileName": "DOC-1"}}}}).Items()); got != 2 {
		t.Errorf("or %d", got)
	}
	if got := len(e.Q(tu.User, "documents.list", map[string]any{"filter": map[string]any{"topic": "rrhh"}}).Items()); got != 0 {
		t.Errorf("unreadable topic leaked: %d", got)
	}
	if got := len(e.Q(tu.Admin, "documents.list", map[string]any{"filter": map[string]any{"topic": "rrhh"}}).Items()); got != 1 {
		t.Errorf("admin topic %d", got)
	}
	only := e.Q(tu.User, "documents.list", map[string]any{"count": "only"})
	if len(only.Items()) != 0 || only.M()["page"].(map[string]any)["total"].(float64) != 8 {
		t.Errorf("count only %s", only.Raw)
	}
	e.Fail(e.QRaw(tu.User, "documents.list", map[string]any{"filter": map[string]any{"size": 1}}), protocol.CodeInvalidFields)
	e.Fail(e.QRaw(tu.User, "documents.list", map[string]any{"fields": []string{"content"}}), protocol.CodeInvalidFields)
	e.Fail(e.QRaw(tu.User, "documents.list", map[string]any{"userId": tu.User}), protocol.CodeInvalidFields)
	// Clamped limit, like core.
	if l := e.Q(tu.User, "documents.list", map[string]any{"page": map[string]any{"limit": 999}}).M()["page"].(map[string]any)["limit"]; l.(float64) != 200 {
		t.Errorf("limit %v", l)
	}

	// folders.list path filter: the root, with and without the trailing slash.
	root := e.Q(tu.Admin, "folders.list", map[string]any{"filter": map[string]any{"path": "/"}}).Items()
	if len(root) != 1 || root[0]["id"] != protocol.RootFolderID || root[0]["path"] != "/" {
		t.Fatalf("root: %v", root)
	}
	if got := e.Q(tu.User, "folders.list", map[string]any{"filter": map[string]any{"path": "/Clients/ACME"}}).Items(); len(got) != 1 || got[0]["id"] != acme {
		t.Fatalf("folder path: %v", got)
	}

	// topics.list counts only readable documents.
	topics := e.Q(tu.User, "topics.list", nil).Items()
	if len(topics) != 1 || topics[0]["topic"] != "infra" || topics[0]["documents"].(float64) != 7 {
		t.Fatalf("topics %v", topics)
	}
	_ = reqDoc
}

func TestMetaDescribe(t *testing.T) {
	e := tu.NewEnv(t)
	all := e.OK(e.Q(tu.User, "meta.describe", nil))["resources"].(map[string]any)
	if len(all) != 6 {
		t.Fatalf("%d resources", len(all))
	}
	docs := all["documents"].(map[string]any)
	if docs["defaults"].(map[string]any)["maxLimit"].(float64) != 200 || docs["filterable"].(map[string]any)["visibility"].(map[string]any)["kind"] != "enum" {
		t.Fatal(docs)
	}
	// Every sortable it declares works.
	for _, s := range docs["sortable"].([]any) {
		e.OK(e.Q(tu.User, "documents.list", map[string]any{"sort": []string{"-" + s.(string)}}))
	}
	one := e.OK(e.Q(tu.External, "meta.describe", map[string]any{"resources": []string{"shares"}}))["resources"].(map[string]any)
	if len(one) != 1 {
		t.Fatal(one)
	}
	e.Fail(e.QRaw(tu.User, "meta.describe", map[string]any{"resources": []string{}}), protocol.CodeInvalidFields)
	e.Fail(e.QRaw(tu.User, "meta.describe", map[string]any{"resources": []string{"tasks"}}), protocol.CodeInvalidFields)
}

func TestExternalVisibility(t *testing.T) {
	e := tu.NewEnv(t)
	clients := e.Folder("", "clients", nil)
	infra := e.Folder("", "infra", map[string]any{"externalBlocked": true})
	sub := e.Folder(infra, "servidores", nil)
	visible := e.Doc(tu.Admin, clients, "publico.md", fm("12", "public", "")+"# Público\naduana\n")
	viaReq := e.Doc(tu.Admin, clients, "req.md", "---\nvisibility: public\njiku:\n  requirements: [812]\n---\n# Req\n")
	e.Doc(tu.Admin, clients, "interno.md", fm("12", "internal", "")+"# Interno\n")
	e.Doc(tu.Admin, clients, "otro-proyecto.md", fm("31", "public", "")+"# Otro\n")
	// P-1: externalBlocked has no effect any more; the visibility decides.
	blocked := e.Doc(tu.Admin, sub, "srv.md", fm("12", "public", "")+"# Srv\n")
	hidden := e.Doc(tu.Admin, clients, "restringido.md", fm("12", "restricted", "")+"# Restringido\n")

	items := e.Q(tu.External, "documents.list", map[string]any{"sort": []string{"path"}}).Items()
	if len(items) != 3 || items[0]["id"] != visible || items[1]["id"] != viaReq || items[2]["id"] != blocked {
		t.Fatalf("external sees %v", items)
	}
	// An external sees only its own projects and no clients.
	if p := items[0]["projects"].([]any); len(p) != 1 || p[0].(float64) != 12 || len(items[0]["clients"].([]any)) != 0 {
		t.Fatalf("external relations %v", items[0])
	}
	e.Fail(e.Q(tu.External, "documents.get", map[string]any{"id": hidden}), protocol.CodeDocumentNotFound)
	e.OK(e.Q(tu.External, "documents.get", map[string]any{"id": visible, "include": []string{"content"}}))
	if items := e.Q(tu.External, "folders.list", nil).Items(); len(items) != 0 {
		t.Errorf("externals never see folders: %v", items)
	}
	// Externals never write.
	e.Fail(e.Cmd(tu.External, "documents."+visible+".delete", map[string]any{"baseVersion": 1}), protocol.CodeCallerNotAuthorized)
	// The api acting for the external person sees the same.
	viaAPI := e.Q(tu.APIUser, "documents.list", map[string]any{"actor": actor(tu.External, "external-user")}).Items()
	if len(viaAPI) != 3 {
		t.Fatalf("api+actor: %v", viaAPI)
	}
	// The api without actor is a service: everything.
	if n := len(e.Q(tu.APIUser, "documents.list", nil).Items()); n != 6 {
		t.Errorf("api as service sees %d", n)
	}
}

// An external person reads a public document as it is now, never the versions it had while
// it was internal: those can hold exactly what was removed before publishing it.
func TestExternalSeesOnlyTheCurrentVersion(t *testing.T) {
	e := tu.NewEnv(t)
	clients := e.Folder("", "clients", nil)
	id := e.Doc(tu.Admin, clients, "acta.md", fm("12", "internal", "")+"# Acta\nnota interna: margen del 40%\n")
	v2 := fm("12", "public", "") + "# Acta\nresumen para el cliente\n"
	e.OK(e.Cmd(tu.Admin, "documents."+id+".update", map[string]any{"baseVersion": 1, "content": v2, "checksum": tu.Sum(v2)}))

	cur := e.OK(e.Q(tu.External, "documents.get", map[string]any{"id": id, "include": []string{"content"}}))
	if cur["version"] != float64(2) || cur["content"] != "# Acta\nresumen para el cliente\n" {
		t.Fatalf("current version: %v", cur)
	}
	e.OK(e.Q(tu.External, "documents.get", map[string]any{"id": id, "version": 2}))
	e.Fail(e.Q(tu.External, "documents.get", map[string]any{"id": id, "version": 1, "include": []string{"content"}}),
		protocol.CodeDocumentNotFound)
	hist := e.Q(tu.External, "documents.versions.list", map[string]any{"filter": map[string]any{"documentId": id}}).Items()
	if len(hist) != 1 || hist[0]["version"] != float64(2) {
		t.Fatalf("external history: %v", hist)
	}
	// The api acting for the external person gets the same; internal people keep the whole history.
	e.Fail(e.Q(tu.APIUser, "documents.get", map[string]any{"id": id, "version": 1, "actor": actor(tu.External, "external-user")}),
		protocol.CodeDocumentNotFound)
	if n := len(e.Q(tu.Admin, "documents.versions.list", map[string]any{"filter": map[string]any{"documentId": id}}).Items()); n != 2 {
		t.Errorf("internal history has %d versions", n)
	}
	e.OK(e.Q(tu.Admin, "documents.get", map[string]any{"id": id, "version": 1}))
}

// D-11: an external person never receives the frontmatter (it holds internal ids of
// requirements and tasks), neither inline nor through contentUrl, and the checksum and size it
// gets describe the body it receives. Internal people keep the whole file.
func TestExternalNeverSeesFrontmatter(t *testing.T) {
	e := tu.NewEnv(t)
	clients := e.Folder("", "clients", nil)
	head := "---\nvisibility: public\njiku:\n  projects: [12]\n  requirements: [812]\n  tasks: [900]\n---\n"
	body := "# Minuta\nresumen para el cliente\n"
	id := e.Doc(tu.Admin, clients, "minuta.md", head+body)

	for name, r := range map[string]tu.Resp{
		"bus": e.Q(tu.External, "documents.get", map[string]any{"id": id, "include": []string{"content"}}),
		"api": e.Q(tu.APIUser, "documents.get", map[string]any{"id": id, "include": []string{"content"}, "actor": actor(tu.External, "external-user")}),
	} {
		g := e.OK(r)
		if g["content"] != body || g["checksum"] != tu.Sum(body) || g["sizeBytes"] != float64(len(body)) {
			t.Fatalf("%s: external get %v", name, g)
		}
	}
	meta := e.OK(e.Q(tu.External, "documents.get", map[string]any{"id": id, "fields": []string{"checksum", "sizeBytes"}}))
	if meta["checksum"] != tu.Sum(body) || meta["sizeBytes"] != float64(len(body)) {
		t.Fatalf("external metadata %v", meta)
	}
	hist := e.Q(tu.External, "documents.versions.list", map[string]any{"filter": map[string]any{"documentId": id}}).Items()
	if len(hist) != 1 || hist[0]["checksum"] != tu.Sum(body) {
		t.Fatalf("external history %v", hist)
	}
	// Internal people get the whole file, frontmatter included.
	in := e.OK(e.Q(tu.Admin, "documents.get", map[string]any{"id": id, "include": []string{"content"}}))
	if in["content"] != head+body || in["checksum"] != tu.Sum(head+body) {
		t.Fatalf("internal get %v", in)
	}
	// Search snippets never include the frontmatter.
	e.Eventually("indexed", func() bool {
		return len(e.Q(tu.External, "documents.search", map[string]any{"query": "resumen"}).Items()) == 1
	})
	for _, it := range e.Q(tu.External, "documents.search", map[string]any{"query": "resumen"}).Items() {
		if strings.Contains(it["snippet"].(string), "812") {
			t.Fatalf("snippet with frontmatter: %v", it)
		}
	}

	// Above 256 KiB the body comes as contentUrl, also without frontmatter.
	bigBody := "# Grande\n\n" + strings.Repeat("línea de texto para el cliente\n", 12000)
	big := head + bigBody
	up := e.OK(e.Cmd(tu.Admin, "documents.request-content-upload", map[string]any{"checksum": tu.Sum(big), "size": len(big)}))
	put(t, up["uploadUrl"].(string), []byte(big))
	bid := tu.ID()
	e.OK(e.Cmd(tu.Admin, "documents.new", map[string]any{"id": bid, "folderId": clients, "fileName": "grande.md", "contentUploadId": up["contentUploadId"], "checksum": tu.Sum(big)}))
	g := e.OK(e.Q(tu.External, "documents.get", map[string]any{"id": bid, "include": []string{"content"}}))
	if _, inline := g["content"]; inline {
		t.Fatal("a big body must come as contentUrl")
	}
	if got := get(t, g["contentUrl"].(string)); string(got) != bigBody || g["checksum"] != tu.Sum(bigBody) {
		t.Fatalf("external contentUrl returned %d bytes, checksum %v", len(got), g["checksum"])
	}
	ig := e.OK(e.Q(tu.Admin, "documents.get", map[string]any{"id": bid, "include": []string{"content"}}))
	if got := get(t, ig["contentUrl"].(string)); string(got) != big {
		t.Fatalf("internal contentUrl returned %d bytes", len(got))
	}
}

func TestSearch(t *testing.T) {
	e := tu.NewEnv(t)
	f := e.Folder("", "docs", nil)
	e.ACL(f, tu.Rule("role", "user", "read"), tu.Rule("role", "admin", "manage"))
	a := e.Doc(tu.Admin, f, "aduana.md", fm("12", "internal", "")+"# Integración con aduanas\n\nEl servicio de la Aduana responde lento.\n\n## Pruebas\nprueba de carga\n")
	e.Doc(tu.Admin, f, "otro.md", "# Otro tema\n\nnada que ver\n")
	priv := e.Folder("", "privado", map[string]any{"inheritAcl": false})
	e.Doc(tu.Admin, priv, "aduana-priv.md", "---\nvisibility: restricted\n---\n# Aduana privada\n")
	e.Eventually("indexed", func() bool {
		var n int
		_ = e.DB.QueryRow(context.Background(), `SELECT count(*) FROM index_status`).Scan(&n)
		return n == 3
	})
	hits := e.Q(tu.User, "documents.search", map[string]any{"query": "aduana integracion"}).Items()
	if len(hits) != 1 || hits[0]["documentId"] != a {
		t.Fatalf("search (stems + accents): %v", hits)
	}
	if hits[0]["snippet"] == "" || hits[0]["path"] != "/docs/aduana.md" {
		t.Error(hits[0])
	}
	if got := e.Q(tu.User, "documents.search", map[string]any{"query": "aduana -carga"}).Items(); len(got) != 0 {
		t.Errorf("exclusion: %v", got)
	}
	if got := e.Q(tu.User, "documents.search", map[string]any{"query": "aduana", "filter": map[string]any{"projectId": 31}}).Items(); len(got) != 0 {
		t.Errorf("filter: %v", got)
	}
	if got := e.Q(tu.Admin, "documents.search", map[string]any{"query": "aduana"}).Items(); len(got) != 2 {
		t.Errorf("admin sees the private one too: %v", got)
	}
	e.Fail(e.Q(tu.User, "documents.search", map[string]any{"query": "aduana", "mode": "semantic"}), protocol.CodeSemanticSearchNotConfigured)
	// Reindex from scratch gives the same result.
	if err := e.Inst.Indexer.Reindex(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := e.Q(tu.User, "documents.search", map[string]any{"query": "aduanas"}).Items(); len(got) != 1 {
		t.Errorf("after reindex: %v", got)
	}
}

func TestChangesFeed(t *testing.T) {
	e := tu.NewEnv(t)
	pub := e.Folder("", "general", nil)
	e.ACL(pub, tu.Rule("role", "user", "write"), tu.Rule("role", "admin", "manage"))
	priv := e.Folder("", "privado", map[string]any{"inheritAcl": false})
	empty := e.OK(e.Q(tu.User, "documents.changes", map[string]any{"sinceSeq": 0}))
	if len(empty["items"].([]any)) != 0 || empty["hasMore"] != false {
		t.Fatal(empty)
	}
	a := e.Doc(tu.User, pub, "a.md", "uno")
	e.Doc(tu.Admin, priv, "p.md", "---\nvisibility: restricted\n---\nprivado")
	e.OK(e.Cmd(tu.User, "documents."+a+".update", map[string]any{"baseVersion": 1, "content": "dos", "checksum": tu.Sum("dos")}))
	r := e.OK(e.Q(tu.User, "documents.changes", map[string]any{"sinceSeq": 0}))
	items := r["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("user must see only the readable document: %v", items)
	}
	last := items[1].(map[string]any)
	if last["type"] != "document.updated" || last["checksum"] != tu.Sum("dos") || last["version"].(float64) != 2 {
		t.Fatal(last)
	}
	next := r["nextSeq"].(float64)
	// Nothing new: nextSeq stays at sinceSeq.
	again := e.OK(e.Q(tu.User, "documents.changes", map[string]any{"sinceSeq": next}))
	if again["nextSeq"].(float64) != next || len(again["items"].([]any)) != 0 {
		t.Fatal(again)
	}
	// limit and hasMore.
	page := e.OK(e.Q(tu.Admin, "documents.changes", map[string]any{"sinceSeq": 0, "limit": 1}))
	if page["hasMore"] != true || len(page["items"].([]any)) != 1 {
		t.Fatal(page)
	}
	// pathPrefix.
	if got := e.OK(e.Q(tu.Admin, "documents.changes", map[string]any{"sinceSeq": 0, "filter": map[string]any{"pathPrefix": "/privado/"}}))["items"].([]any); len(got) != 1 {
		t.Fatal(got)
	}
	// A sinceSeq older than the retention watermark forces a full reconciliation.
	if _, err := e.DB.Exec(context.Background(), `INSERT INTO service_state (key, value) VALUES ('changelog_purged_through', '{"seq": 2}')`); err != nil {
		t.Fatal(err)
	}
	e.Fail(e.Q(tu.User, "documents.changes", map[string]any{"sinceSeq": 1}), protocol.CodeInvalidCursor)
	e.OK(e.Q(tu.User, "documents.changes", map[string]any{"sinceSeq": 0}))
}
