package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/gravadigital/jiku-docs/internal/protocol"
	tu "github.com/gravadigital/jiku-docs/internal/testutil"
)

func fm(projects string, visibility string, topics string) string {
	return "---\nvisibility: " + visibility + "\ntopics: [" + topics + "]\njiku:\n  projects: [" + projects + "]\n---\n"
}

func TestListFiltersAndPagination(t *testing.T) {
	e := tu.NewEnv(t)
	clientes := e.Folder("", "clientes", nil)
	e.ACL(clientes, tu.Rule("role", "user", "write"), tu.Rule("role", "admin", "manage"))
	acme := e.Folder(clientes, "acme", nil)
	interno := e.Folder("", "interno", map[string]any{"inheritAcl": false})
	var ids []string
	for i := 0; i < 7; i++ {
		ids = append(ids, e.Doc(tu.User, acme, fmt.Sprintf("doc-%d.md", i), fm("12", "internal", "infra")+fmt.Sprintf("# Doc %d\n", i)))
	}
	reqDoc := e.Doc(tu.User, clientes, "req.md", "---\njiku:\n  requirements: [812]\n---\n# Requisito\n")
	e.Doc(tu.Admin, interno, "secreto.md", fm("12", "internal", "rrhh")+"# Interno\n")

	// projectId includes documents related to requirements of the project; interno is not readable.
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
	one := e.Q(tu.User, "documents.list", map[string]any{"filter": map[string]any{"path": "/CLIENTES/acme/DOC-3.md"}, "fields": []string{"path", "fileName"}, "include": []string{"topics", "relations", "lock"}}).Items()
	if len(one) != 1 || one[0]["id"] != ids[3] || one[0]["fileName"] != "doc-3.md" || one[0]["title"] != nil || one[0]["lock"] != nil {
		t.Fatalf("path filter: %v", one)
	}
	if got := len(e.Q(tu.User, "documents.list", map[string]any{"filter": map[string]any{"pathPrefix": "/clientes/acme/"}}).Items()); got != 7 {
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
	if got := e.Q(tu.User, "folders.list", map[string]any{"filter": map[string]any{"path": "/Clientes/ACME"}}).Items(); len(got) != 1 || got[0]["id"] != acme {
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
	clientes := e.Folder("", "clientes", nil)
	infra := e.Folder("", "infra", map[string]any{"externalBlocked": true})
	sub := e.Folder(infra, "servidores", nil)
	visible := e.Doc(tu.Admin, clientes, "publico.md", fm("12", "public", "")+"# Público\naduana\n")
	viaReq := e.Doc(tu.Admin, clientes, "req.md", "---\nvisibility: public\njiku:\n  requirements: [812]\n---\n# Req\n")
	e.Doc(tu.Admin, clientes, "interno.md", fm("12", "internal", "")+"# Interno\n")
	e.Doc(tu.Admin, clientes, "otro-proyecto.md", fm("31", "public", "")+"# Otro\n")
	blocked := e.Doc(tu.Admin, sub, "srv.md", fm("12", "public", "")+"# Srv\n")

	items := e.Q(tu.External, "documents.list", map[string]any{"sort": []string{"path"}}).Items()
	if len(items) != 2 || items[0]["id"] != visible || items[1]["id"] != viaReq {
		t.Fatalf("external sees %v", items)
	}
	e.Fail(e.Q(tu.External, "documents.get", map[string]any{"id": blocked}), protocol.CodeDocumentNotFound)
	e.OK(e.Q(tu.External, "documents.get", map[string]any{"id": visible, "include": []string{"content"}}))
	if items := e.Q(tu.External, "folders.list", nil).Items(); len(items) != 0 {
		t.Errorf("externals never see folders: %v", items)
	}
	// Externals never write.
	e.Fail(e.Cmd(tu.External, "documents."+visible+".delete", map[string]any{"baseVersion": 1}), protocol.CodeCallerNotAuthorized)
	// The api acting for the external person sees the same.
	viaAPI := e.Q(tu.APIUser, "documents.list", map[string]any{"actor": map[string]any{"id": tu.External}}).Items()
	if len(viaAPI) != 2 {
		t.Fatalf("api+actor: %v", viaAPI)
	}
	// The api without actor is a service: everything.
	if n := len(e.Q(tu.APIUser, "documents.list", nil).Items()); n != 5 {
		t.Errorf("api as service sees %d", n)
	}
}

func TestSearch(t *testing.T) {
	e := tu.NewEnv(t)
	f := e.Folder("", "docs", nil)
	e.ACL(f, tu.Rule("role", "user", "read"), tu.Rule("role", "admin", "manage"))
	a := e.Doc(tu.Admin, f, "aduana.md", fm("12", "internal", "")+"# Integración con aduanas\n\nEl servicio de la Aduana responde lento.\n\n## Pruebas\nprueba de carga\n")
	e.Doc(tu.Admin, f, "otro.md", "# Otro tema\n\nnada que ver\n")
	priv := e.Folder("", "privado", map[string]any{"inheritAcl": false})
	e.Doc(tu.Admin, priv, "aduana-priv.md", "# Aduana privada\n")
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
	e.Doc(tu.Admin, priv, "p.md", "privado")
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
