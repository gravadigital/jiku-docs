package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gravadigital/jiku-docs/internal/importer/hedgedoc"
	"github.com/gravadigital/jiku-docs/internal/logging"
	tu "github.com/gravadigital/jiku-docs/internal/testutil"
)

type memSource []hedgedoc.Note

func (m memSource) Notes(context.Context) ([]hedgedoc.Note, error) { return m, nil }

// Synthetic notes only: the real dump never enters the tests.
func TestHedgeDocImport(t *testing.T) {
	e := tu.NewEnv(t)
	assets := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/space/diagrama.png" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png-bytes"))
	}))
	defer assets.Close()
	host := strings.TrimPrefix(assets.URL, "http://")
	t0 := time.Date(2023, 5, 2, 10, 0, 0, 0, time.UTC)
	notes := memSource{
		{ID: "aaaaaaaa-0000-0000-0000-000000000001", ShortID: "infra01", Title: "Servidores", Owner: "Ana", Permission: "editable",
			Content:   "---\ntags: infra\n---\n# Servidores\n![red](" + assets.URL + "/space/diagrama.png)\nver https://hd.test/notas02\npassword: N0tAR3alPass\n",
			CreatedAt: t0, UpdatedAt: t0.Add(48 * time.Hour)},
		{ID: "aaaaaaaa-0000-0000-0000-000000000002", ShortID: "notas02", Title: "Notas sueltas", Owner: "Beto",
			Content: "# Notas\nsin mapeo\n", CreatedAt: t0.Add(time.Hour), UpdatedAt: t0.Add(time.Hour)},
		{ID: "aaaaaaaa-0000-0000-0000-000000000003", ShortID: "anon03", Title: "spam", Content: "casino", CreatedAt: t0},
		{ID: "aaaaaaaa-0000-0000-0000-000000000004", ShortID: "vacia04", Title: "vacía", Owner: "Ana", Content: "  \n", CreatedAt: t0},
	}
	dir := t.TempDir()
	mapping := filepath.Join(dir, "mapping.csv")
	if err := os.WriteFile(mapping, []byte("note,folder\ninfra01,/infra/servidores/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(dir, "report.csv")
	opts := hedgedoc.Options{Source: notes, Mapping: mapping, Report: report, HedgeDocURL: "https://hd.test",
		AssetPrefixes: []string{host + "/space/"}, MaxAssetBytes: 1 << 20}
	svc := e.Inst.Svc
	ctx := context.Background()

	// Dry run writes nothing.
	dry := opts
	dry.DryRun = true
	if err := hedgedoc.Run(ctx, dry, svc, logging.Discard()); err != nil {
		t.Fatal(err)
	}
	if n := len(e.Q(tu.Admin, "documents.list", nil).Items()); n != 0 {
		t.Fatalf("dry run created %d documents", n)
	}

	if err := hedgedoc.Run(ctx, opts, svc, logging.Discard()); err != nil {
		t.Fatal(err)
	}
	docs := e.Q(tu.DocsUser, "documents.list", map[string]any{"sort": []string{"path"}, "fields": []string{"path", "createdAt", "createdBy"}}).Items()
	if len(docs) != 2 || docs[0]["path"] != "/importado/Notas sueltas.md" || docs[1]["path"] != "/infra/servidores/Servidores.md" {
		t.Fatalf("%v", docs)
	}
	if docs[1]["createdAt"] != "2023-05-02T10:00:00.000Z" || docs[1]["createdBy"] != tu.DocsUser {
		t.Fatalf("original dates and service author: %v", docs[1])
	}
	srv := docs[1]["id"].(string)
	g := e.OK(e.Q(tu.Admin, "documents.get", map[string]any{"id": srv, "include": []string{"content", "attachments", "topics"}}))
	content := g["content"].(string)
	atts := g["attachments"].([]any)
	if len(atts) != 1 || !strings.Contains(content, "http://web.test/docs/attachments/"+atts[0].(map[string]any)["id"].(string)) {
		t.Fatalf("asset not migrated: %v\n%s", atts, content)
	}
	if !strings.Contains(content, "http://web.test/docs/"+docs[0]["id"].(string)) || strings.Contains(content, "hd.test") {
		t.Fatalf("note link not rewritten:\n%s", content)
	}
	if !strings.Contains(content, "hedgedoc:") || toStrings(g["topics"])[0] != "infra" {
		t.Fatalf("frontmatter:\n%s", content)
	}
	v := e.Q(tu.Admin, "documents.versions.list", map[string]any{"filter": map[string]any{"documentId": srv}}).Items()
	if len(v) != 1 || !strings.Contains(v[0]["message"].(string), "Autor original: Ana") {
		t.Fatal(v)
	}
	// /importado/ is for administrators only.
	if n := len(e.Q(tu.User, "documents.list", nil).Items()); n != 0 {
		t.Fatalf("user sees %d imported documents", n)
	}
	if n := len(e.Q(tu.Admin, "documents.list", map[string]any{"filter": map[string]any{"pathPrefix": "/importado/"}}).Items()); n != 1 {
		t.Fatalf("admin sees %d in /importado/", n)
	}
	// The report flags the possible secret, without the secret.
	rep, _ := os.ReadFile(report)
	if !strings.Contains(string(rep), "password_assignment") || strings.Contains(string(rep), "N0tAR3alPass") {
		t.Fatalf("report:\n%s", rep)
	}
	// Re-running is idempotent: nothing is written and no asset is downloaded again.
	downloads := 0
	countingHTTP := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			downloads++
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	opts.HTTP = countingHTTP
	if err := hedgedoc.Run(ctx, opts, svc, logging.Discard()); err != nil {
		t.Fatal(err)
	}
	if n := len(e.Q(tu.DocsUser, "documents.list", nil).Items()); n != 2 {
		t.Fatalf("rerun created duplicates: %d", n)
	}
	rep, _ = os.ReadFile(report)
	if strings.Contains(string(rep), ",failed,") || strings.Contains(string(rep), ",imported,") || !strings.Contains(string(rep), ",unchanged,") {
		t.Fatalf("rerun report:\n%s", rep)
	}
	if downloads != 0 {
		t.Fatalf("rerun downloaded %d assets again", downloads)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Relations and topics from the sheet: written in version 1, an invalid relation is discarded
// and reported without failing the note, and a rerun with new relations updates what only the
// import wrote.
func TestHedgeDocImportRelations(t *testing.T) {
	e := tu.NewEnv(t)
	t0 := time.Date(2024, 1, 10, 9, 0, 0, 0, time.UTC)
	notes := memSource{
		{ID: "bbbbbbbb-0000-0000-0000-000000000001", ShortID: "rel01", Title: "Reunión kickoff", Owner: "Ana",
			Content: "---\ntags: Reuniones\n---\n# Kickoff\n", CreatedAt: t0, UpdatedAt: t0},
		{ID: "bbbbbbbb-0000-0000-0000-000000000002", ShortID: "rel02", Title: "Arquitectura", Owner: "Ana",
			Content: "# Arquitectura\n", CreatedAt: t0.Add(time.Hour), UpdatedAt: t0.Add(time.Hour)},
		{ID: "bbbbbbbb-0000-0000-0000-000000000003", ShortID: "rel03", Title: "Editada", Owner: "Ana",
			Content: "# Editada\n", CreatedAt: t0.Add(2 * time.Hour), UpdatedAt: t0.Add(2 * time.Hour)},
	}
	dir := t.TempDir()
	mapping := filepath.Join(dir, "mapping.csv")
	report := filepath.Join(dir, "report.csv")
	write := func(sheet string) {
		if err := os.WriteFile(mapping, []byte(sheet), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("note,folder,projects,clients,requirements,topics\n" +
		"rel01,/clientes/acme/web/reuniones/,12;99,4,812,reuniones;Técnico\n" +
		"rel02,/clientes/acme/web/tecnico/,,,,\n" +
		"rel03,/clientes/acme/web/tecnico/,,,,\n")
	opts := hedgedoc.Options{Source: notes, Mapping: mapping, Report: report, HedgeDocURL: "https://hd.test", MaxAssetBytes: 1 << 20}
	ctx := context.Background()
	svc := e.Inst.Svc

	// Dry run with --check-relations (core of the service): reports the discard, writes nothing.
	dry := opts
	dry.DryRun, dry.CheckRelations = true, true
	if err := hedgedoc.Run(ctx, dry, svc, logging.Discard()); err != nil {
		t.Fatal(err)
	}
	if rep, _ := os.ReadFile(report); !strings.Contains(string(rep), "project 99: invalid_relation") {
		t.Fatalf("dry run report:\n%s", rep)
	}
	if n := len(e.Q(tu.Admin, "documents.list", nil).Items()); n != 0 {
		t.Fatalf("dry run created %d documents", n)
	}

	if err := hedgedoc.Run(ctx, opts, svc, logging.Discard()); err != nil {
		t.Fatal(err)
	}
	byProject := e.Q(tu.Admin, "documents.list", map[string]any{"filter": map[string]any{"projectId": 12}, "fields": []string{"path"}}).Items()
	if len(byProject) != 1 || byProject[0]["path"] != "/clientes/acme/web/reuniones/Reunión kickoff.md" {
		t.Fatalf("documents of project 12: %v", byProject)
	}
	id := byProject[0]["id"].(string)
	g := e.OK(e.Q(tu.Admin, "documents.get", map[string]any{"id": id, "include": []string{"content", "relations", "topics"}}))
	content := g["content"].(string)
	if !strings.Contains(content, "jiku:\n  clients: [4]\n  projects: [12]\n  requirements: [812]\n") || strings.Contains(content, "99") {
		t.Fatalf("version 1 without project 99:\n%s", content)
	}
	if strings.Join(toStrings(g["topics"]), ",") != "reuniones,tecnico" || g["version"].(float64) != 1 {
		t.Fatalf("topics %v version %v", g["topics"], g["version"])
	}
	rep, _ := os.ReadFile(report)
	if !strings.Contains(string(rep), "project 99: invalid_relation") || strings.Contains(string(rep), ",failed,") {
		t.Fatalf("report:\n%s", rep)
	}

	// A person edits rel03 in jiku-docs.
	edited := e.Q(tu.Admin, "documents.list", map[string]any{"filter": map[string]any{"path": "/clientes/acme/web/tecnico/Editada.md"}}).Items()[0]["id"].(string)
	body := "# Editada a mano\n"
	e.OK(e.Cmd(tu.Admin, "documents."+edited+".update", map[string]any{"baseVersion": 1, "content": body, "checksum": tu.Sum(body)}))

	// Rerun with new relations: rel02 (only the import wrote it) gets version 2; rel03 is not
	// overwritten; rel01 stays unchanged.
	write("note,folder,projects,clients,requirements,topics\n" +
		"rel01,/clientes/acme/web/reuniones/,12;99,4,812,reuniones;Técnico\n" +
		"rel02,/clientes/acme/web/tecnico/,31,,,tecnico\n" +
		"rel03,/clientes/acme/web/tecnico/,31,,,\n")
	if err := hedgedoc.Run(ctx, opts, svc, logging.Discard()); err != nil {
		t.Fatal(err)
	}
	byProject = e.Q(tu.Admin, "documents.list", map[string]any{"filter": map[string]any{"projectId": 31}, "fields": []string{"path", "version"}}).Items()
	if len(byProject) != 1 || byProject[0]["path"] != "/clientes/acme/web/tecnico/Arquitectura.md" || byProject[0]["version"].(float64) != 2 {
		t.Fatalf("documents of project 31: %v", byProject)
	}
	v := e.Q(tu.Admin, "documents.versions.list", map[string]any{"filter": map[string]any{"documentId": byProject[0]["id"]}}).Items()
	if len(v) != 2 || !strings.Contains(v[0]["message"].(string)+v[1]["message"].(string), "Reimportado de HedgeDoc") {
		t.Fatalf("versions %v", v)
	}
	rep, _ = os.ReadFile(report)
	lines := map[string]string{}
	for _, l := range strings.Split(string(rep), "\n") {
		if len(l) > 40 {
			lines[l[:36]] = l
		}
	}
	if !strings.Contains(lines[notes[0].ID], ",unchanged,") || !strings.Contains(lines[notes[1].ID], ",updated,") ||
		!strings.Contains(lines[notes[2].ID], ",exists,editada en jiku-docs") {
		t.Fatalf("rerun report:\n%s", rep)
	}
	if g := e.OK(e.Q(tu.Admin, "documents.get", map[string]any{"id": edited, "include": []string{"content"}})); g["content"] != body {
		t.Fatalf("a document edited in jiku-docs must not be overwritten: %v", g["content"])
	}
}
