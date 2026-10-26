package integration

import (
	"context"
	"testing"
	"time"

	"github.com/gravadigital/jiku-docs/internal/coreapi"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	tu "github.com/gravadigital/jiku-docs/internal/testutil"
)

// JIKU-WEB-5: the api sends `actor` without name nor username (the web token does not carry
// them). The name is resolved with core's users on write, and a null name is completed on read.
func TestActorNamesFromCore(t *testing.T) {
	e := tu.NewEnv(t)
	ctx := context.Background()
	f := writable(e)
	asUser := actor(tu.User, "user")

	// On write: documents.new, update and lock through the api store the name from core.
	id := tu.ID()
	e.OK(e.Cmd(tu.APIUser, "documents.new", map[string]any{"actor": asUser, "id": id, "folderId": f, "fileName": "web.md",
		"content": "# Web aduana\n", "checksum": tu.Sum("# Web aduana\n")}))
	e.OK(e.Cmd(tu.APIUser, "documents."+id+".update", map[string]any{"actor": asUser, "baseVersion": 1,
		"content": "# Web aduana\n\ndos\n", "checksum": tu.Sum("# Web aduana\n\ndos\n")}))
	session := tu.ID()
	e.OK(e.Cmd(tu.APIUser, "documents."+id+".lock", map[string]any{"actor": asUser, "sessionId": session, "via": "human"}))
	var created, updated, holder *string
	if err := e.DB.QueryRow(ctx, `SELECT d.created_by_name, d.updated_by_name, l.holder_name FROM documents d
		JOIN document_locks l ON l.document_id = d.id WHERE d.id = $1`, id).Scan(&created, &updated, &holder); err != nil {
		t.Fatal(err)
	}
	if created == nil || *created != "Usuario" || updated == nil || *updated != "Usuario" || holder == nil || *holder != "Usuario" {
		t.Fatalf("names stored on write: %v %v %v", created, updated, holder)
	}
	ev := e.WaitEvent("document.updated", func(ev tu.Event) bool { return ev.Body["entity"].(map[string]any)["id"] == id })
	if ev.Body["actor"].(map[string]any)["name"] != "Usuario" {
		t.Errorf("event actor %v", ev.Body["actor"])
	}
	// The actor's own name, when it comes, still wins.
	e.OK(e.Cmd(tu.APIUser, "documents."+id+".update", map[string]any{"actor": map[string]any{"id": tu.User, "roles": []string{"user"}, "name": "Usu Web"},
		"baseVersion": 2, "content": "# Web aduana\n\ntres\n", "checksum": tu.Sum("# Web aduana\n\ntres\n"), "sessionId": session, "via": "human"}))
	if g := e.OK(e.Q(tu.User, "documents.get", map[string]any{"id": id})); g["updatedByName"] != "Usu Web" {
		t.Fatalf("the actor's name must win: %v", g["updatedByName"])
	}

	// On read: rows without a name (written before the fix) show the name core knows.
	for _, q := range []string{
		`UPDATE documents SET created_by_name = NULL, updated_by_name = NULL WHERE id = $1`,
		`UPDATE document_versions SET author_name = NULL WHERE document_id = $1`,
		`UPDATE document_locks SET holder_name = NULL WHERE document_id = $1`,
	} {
		if _, err := e.DB.Exec(ctx, q, id); err != nil {
			t.Fatal(err)
		}
	}
	g := e.OK(e.Q(tu.User2, "documents.get", map[string]any{"id": id, "fields": []string{"createdByName", "updatedByName"}, "include": []string{"lock"}}))
	if g["createdByName"] != "Usuario" || g["updatedByName"] != "Usuario" || g["lock"].(map[string]any)["holderName"] != "Usuario" {
		t.Fatalf("get %v", g)
	}
	if g := e.OK(e.Q(tu.User2, "documents.get", map[string]any{"id": id, "version": 1})); g["updatedByName"] != "Usuario" {
		t.Fatalf("get version %v", g)
	}
	items := e.Q(tu.User2, "documents.list", map[string]any{"filter": map[string]any{"id": id},
		"fields": []string{"createdByName", "updatedByName"}, "include": []string{"lock"}}).Items()
	if len(items) != 1 || items[0]["createdByName"] != "Usuario" || items[0]["updatedByName"] != "Usuario" ||
		items[0]["lock"].(map[string]any)["holderName"] != "Usuario" {
		t.Fatalf("list %v", items)
	}
	for _, v := range e.Q(tu.User2, "documents.versions.list", map[string]any{"filter": map[string]any{"documentId": id}}).Items() {
		if v["authorName"] != "Usuario" {
			t.Fatalf("versions %v", v)
		}
	}
	locked := e.Fail(e.Cmd(tu.User2, "documents."+id+".lock", map[string]any{"sessionId": tu.ID(), "via": "human"}), protocol.CodeDocumentLocked)
	if locked.ErrorDetails["holderName"] != "Usuario" {
		t.Fatalf("document_locked %v", locked.ErrorDetails)
	}
	var hits []map[string]any
	e.Eventually("indexed", func() bool {
		hits = e.Q(tu.User2, "documents.search", map[string]any{"query": "aduana"}).Items()
		return len(hits) == 1
	})
	if hits[0]["createdByName"] != "Usuario" || hits[0]["updatedByName"] != "Usuario" {
		t.Fatalf("search %v", hits[0])
	}
	// Reads complete the reply only: the rows keep their null names.
	if err := e.DB.QueryRow(ctx, `SELECT updated_by_name FROM documents WHERE id = $1`, id).Scan(&updated); err != nil || updated != nil {
		t.Fatalf("a read wrote the name: %v %v", updated, err)
	}
	// An id core does not know keeps a null name.
	if _, err := e.DB.Exec(ctx, `UPDATE documents SET updated_by = 'desconocido' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if g := e.OK(e.Q(tu.User2, "documents.get", map[string]any{"id": id})); g["updatedByName"] != nil {
		t.Fatalf("unknown id %v", g["updatedByName"])
	}
}

// With core down the write goes on (the name is not an access decision) and the name stays
// null; once core answers again, reads show it.
func TestActorNamesCoreDown(t *testing.T) {
	e := tu.NewEnv(t, tu.Options{NoJanitor: true, NoIndexer: true, CoreTimeout: 500 * time.Millisecond})
	f := writable(e)
	const person = "399000000000000777"
	e.Core.SetUser(coreapi.User{ID: person, Name: "Persona Nueva", Roles: []string{"user"}, IdentityType: "person"})
	e.Core.SetDown(true)
	id := tu.ID()
	e.OK(e.Cmd(tu.APIUser, "documents.new", map[string]any{"actor": actor(person, "user"), "id": id, "folderId": f,
		"fileName": "caido.md", "content": "x", "checksum": tu.Sum("x")}))
	e.Core.SetDown(false)
	// Within the backoff core is not asked again: still null, and still no failure.
	if g := e.OK(e.Q(tu.Admin, "documents.get", map[string]any{"id": id})); g["updatedByName"] != nil {
		t.Fatalf("within backoff %v", g["updatedByName"])
	}
	e.Inst.Svc.Now = func() time.Time { return time.Now().Add(time.Minute) }
	defer func() { e.Inst.Svc.Now = protocol.Now }()
	if g := e.OK(e.Q(tu.Admin, "documents.get", map[string]any{"id": id})); g["updatedByName"] != "Persona Nueva" || g["createdByName"] != "Persona Nueva" {
		t.Fatalf("after core is back %v / %v", g["createdByName"], g["updatedByName"])
	}
}
