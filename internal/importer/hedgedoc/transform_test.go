package hedgedoc

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gravadigital/jiku-docs/internal/markdown"
	"github.com/gravadigital/jiku-docs/internal/protocol"
)

// Synthetic notes only: the real dump never enters the tests.
func notes() []Note {
	t0 := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	return []Note{
		{ID: "11111111-2222-3333-4444-555555555555", ShortID: "abcDEF123", Alias: "guia", Title: "Guía de despliegue",
			Content: "---\ntitle: Guía\ntags: infra, Despliegue\nvisibility: public\nrobots: noindex\n---\n# Guía\nver https://hd.example/s/xyz987 y https://hd.example/guia#paso-2\n" +
				"![img](https://cdn.example/assets/a.png) otra vez https://cdn.example/assets/a.png\n",
			CreatedAt: t0, UpdatedAt: t0.Add(time.Hour), Owner: "Ana", LastEditor: "Beto", Permission: "editable"},
		{ID: "99999999-8888-7777-6666-555555555555", ShortID: "xyz987", Title: "Notas / reunión", Content: "texto",
			CreatedAt: t0.Add(24 * time.Hour), UpdatedAt: t0.Add(25 * time.Hour)},
	}
}

func TestDeterministicIDs(t *testing.T) {
	n := notes()[0]
	if a, b := DocumentID(n), DocumentID(notes()[0]); a != b || !protocol.IsULID(a) {
		t.Error("document id must be a stable ULID")
	}
	if AttachmentID(n, "https://a") == AttachmentID(n, "https://b") || !protocol.IsULID(AttachmentID(n, "https://a")) {
		t.Error("attachment ids must differ per URL")
	}
	if !strings.HasPrefix(DocumentID(n), "01HQZ") { // ULID time part of 2024-03-01
		t.Logf("document id %s", DocumentID(n))
	}
}

func TestResolver(t *testing.T) {
	ns := notes()
	r := NewResolver(ns)
	for _, slug := range []string{"guia", "abcDEF123", ns[0].ID, EncodeNoteID(ns[0].ID)} {
		if n := r.Find(slug); n == nil || n.ID != ns[0].ID {
			t.Errorf("slug %s not resolved", slug)
		}
	}
	if r.Find("nope") != nil {
		t.Error("unknown slug")
	}
}

func TestBuildContentAndLinks(t *testing.T) {
	ns := notes()
	content, warnings := BuildContent(ns[0], ns[0].Content)
	if len(warnings) != 0 {
		t.Errorf("warnings %v", warnings)
	}
	fm, body, err := markdown.Parse(content)
	if err != nil {
		t.Fatalf("the generated frontmatter must be valid for jiku-docs: %v\n%s", err, content)
	}
	if fm.Title != "Guía" || fm.Visibility != "internal" || strings.Join(fm.Topics, ",") != "despliegue,infra" {
		t.Errorf("%+v", fm)
	}
	if !strings.Contains(content, "visibility: public") || !strings.Contains(content, "robots: noindex") {
		t.Errorf("original keys must be kept under hedgedoc.meta:\n%s", content)
	}
	if !strings.HasPrefix(strings.TrimLeft(body, "\n"), "# Guía") {
		t.Errorf("body %q", body)
	}
	r := NewResolver(ns)
	rewritten, unresolved := RewriteNoteLinks(content, "https://hd.example", r, func(n *Note) string { return "https://web/docs/" + n.ShortID })
	if !strings.Contains(rewritten, "https://web/docs/xyz987 ") || !strings.Contains(rewritten, "https://web/docs/abcDEF123#paso-2") {
		t.Errorf("links not rewritten:\n%s", rewritten)
	}
	if len(unresolved) != 0 {
		t.Errorf("unresolved %v", unresolved)
	}
	assets := AssetURLs(rewritten, []string{"cdn.example/assets/"})
	if len(assets) != 1 || assets[0] != "https://cdn.example/assets/a.png" {
		t.Errorf("assets %v", assets)
	}
	if AssetName(assets[0]) != "a.png" {
		t.Error("asset name")
	}
}

func TestBrokenHedgeDocFrontmatterIsKept(t *testing.T) {
	n := Note{ID: "x", Title: "T", Content: "---\ntitle: [roto\n---\ncuerpo", CreatedAt: time.Now()}
	content, warnings := BuildContent(n, n.Content)
	if len(warnings) != 1 || !strings.Contains(content, "```yaml\ntitle: [roto") {
		t.Errorf("%v\n%s", warnings, content)
	}
	if _, _, err := markdown.Parse(content); err != nil {
		t.Error(err)
	}
}

func TestFileNames(t *testing.T) {
	if got := FileName("Notas / reunión: 2026", "x"); got != "Notas - reunión: 2026.md" {
		t.Error(got)
	}
	if got := FileName("   ", "untitled-abc"); got != "untitled-abc.md" {
		t.Error(got)
	}
	taken := map[string]bool{"a.md": true, "a (2).md": true}
	if got := UniqueName("A.md", taken); got != "A (3).md" {
		t.Error(got)
	}
	if !strings.Contains(Message(notes()[0]), "Original author: Ana") {
		t.Error("message")
	}
}

func TestMergeFrontmatterOnGeneratedContent(t *testing.T) {
	n := notes()[0] // tags: infra, Despliegue
	content, _ := BuildContent(n, n.Content)
	rel := markdown.Relations{Projects: []int64{31, 12, 12}, Clients: []int64{4}}
	out, err := MergeFrontmatter(content, rel, []string{"Reuniones", "infra", "Técnico"})
	if err != nil {
		t.Fatal(err)
	}
	fm, body, ferr := markdown.Parse(out)
	if ferr != nil {
		t.Fatalf("%v\n%s", ferr, out)
	}
	if got := strings.Join(fm.Topics, ","); got != "despliegue,infra,reuniones,tecnico" {
		t.Errorf("topics = %s", got)
	}
	if fmt.Sprint(fm.Relations.Projects) != "[12 31]" || fmt.Sprint(fm.Relations.Clients) != "[4]" ||
		len(fm.Relations.Requirements)+len(fm.Relations.Tasks) != 0 {
		t.Errorf("relations = %+v", fm.Relations)
	}
	if fm.Title != "Guía" || !strings.Contains(out, "robots: noindex") || !strings.Contains(out, "id: "+n.ID) {
		t.Errorf("the rest of the frontmatter must stay:\n%s", out)
	}
	if !strings.Contains(out, "jiku:\n  clients: [4]\n  projects: [12, 31]\n") || strings.Contains(out, "requirements") {
		t.Errorf("jiku block:\n%s", out)
	}
	if strings.Index(out, "topics:") > strings.Index(out, "jiku:") || strings.Index(out, "jiku:") > strings.Index(out, "hedgedoc:") {
		t.Errorf("order title, topics, jiku, hedgedoc:\n%s", out)
	}
	if !strings.HasPrefix(strings.TrimLeft(body, "\n"), "# Guía") {
		t.Errorf("body %q", body)
	}
	// Idempotent: merging the same again changes nothing.
	again, err := MergeFrontmatter(out, rel, []string{"reuniones"})
	if err != nil || again != out {
		t.Errorf("second merge changed the content:\n%s", again)
	}
}

func TestMergeFrontmatterKeepsExistingRelations(t *testing.T) {
	in := "---\ntitle: X\ntopics: [Infra]\njiku:\n  projects: [31]\n  tasks: [900]\nextra: 1\n---\n\ncuerpo\n"
	out, err := MergeFrontmatter(in, markdown.Relations{Projects: []int64{12, 31}}, []string{"infra", "accesos"})
	if err != nil {
		t.Fatal(err)
	}
	fm, body, ferr := markdown.Parse(out)
	if ferr != nil {
		t.Fatal(ferr)
	}
	if fmt.Sprint(fm.Relations.Projects) != "[12 31]" || fmt.Sprint(fm.Relations.Tasks) != "[900]" {
		t.Errorf("%+v", fm.Relations)
	}
	if strings.Join(fm.Topics, ",") != "accesos,infra" || !strings.Contains(out, "extra: 1") || body != "\ncuerpo\n" {
		t.Errorf("%s", out)
	}
}

func TestMergeFrontmatterWithoutFrontmatterAndNoop(t *testing.T) {
	out, err := MergeFrontmatter("# Hola\n", markdown.Relations{Clients: []int64{4}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "---\njiku:\n  clients: [4]\n---\n\n# Hola\n" {
		t.Errorf("%q", out)
	}
	if out, _ := MergeFrontmatter("# Hola\n", markdown.Relations{}, nil); out != "# Hola\n" {
		t.Errorf("nothing to add must keep the content: %q", out)
	}
	if _, err := MergeFrontmatter("# Hola\n", markdown.Relations{}, []string{"--"}); err == nil {
		t.Error("a topic that normalises to nothing is an error")
	}
}

func TestWithoutRelation(t *testing.T) {
	rel, ok := WithoutRelation(markdown.Relations{Projects: []int64{12, 99}, Clients: []int64{99}}, "project", 99)
	if !ok || fmt.Sprint(rel.Projects) != "[12]" || fmt.Sprint(rel.Clients) != "[99]" {
		t.Errorf("%v %+v", ok, rel)
	}
	if _, ok := WithoutRelation(rel, "task", 1); ok {
		t.Error("absent relation")
	}
}

func TestIsImportMessage(t *testing.T) {
	n := notes()[0]
	for _, msg := range []string{
		Message(n), ReimportMessage(n),
		// Messages written by 1.0.0, in Spanish: still recognised as the import's own.
		"Importado de HedgeDoc (nota x). Autor original: Ana. Creada: 2024-01-01 10:00.",
		"Reimportado de HedgeDoc (nota x): contenido, relaciones y temas de la planilla actualizados.",
	} {
		if !IsImportMessage(msg) {
			t.Errorf("not recognised: %q", msg)
		}
	}
	for _, msg := range []string{"", "v2", "Project assigned: X", "Notes imported from HedgeDoc"} {
		if IsImportMessage(msg) {
			t.Errorf("recognised by mistake: %q", msg)
		}
	}
}
