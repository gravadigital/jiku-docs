package markdown

import (
	"strings"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	doc := "---\ntitle: Acceso al servidor de staging\nvisibility: public\ntopics: [Infra, Accesos, \"Buenas Prácticas\"]\n" +
		"jiku:\n  clients: [4]\n  projects: [31, 12, 12]\n  requirements: [812]\n  tasks: []\nother: kept\n---\n\n# Otro título\ntexto\n"
	fm, body, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	if fm.Title != "Acceso al servidor de staging" || fm.Visibility != "public" {
		t.Fatalf("unexpected %+v", fm)
	}
	if strings.Join(fm.Topics, ",") != "accesos,buenas-practicas,infra" {
		t.Errorf("topics %v", fm.Topics)
	}
	if len(fm.Relations.Projects) != 2 || fm.Relations.Projects[0] != 12 || fm.Relations.Clients[0] != 4 {
		t.Errorf("relations %+v", fm.Relations)
	}
	if !strings.HasPrefix(body, "\n# Otro título") {
		t.Errorf("body %q", body)
	}
}

func TestParseFrontmatterErrors(t *testing.T) {
	cases := map[string]struct {
		doc   string
		field string
		line  int
	}{
		"visibility":   {"---\nvisibility: secret\n---\n", "visibility", 2},
		"unknown jiku": {"---\njiku:\n  people: [1]\n---\n", "jiku.people", 3},
		"non numeric":  {"---\njiku:\n  projects: [abc]\n---\n", "jiku.projects", 3},
		"negative":     {"---\njiku:\n  projects: [-1]\n---\n", "jiku.projects", 3},
		"topics map":   {"---\ntopics: {a: 1}\n---\n", "topics", 2},
		"unterminated": {"---\ntitle: x\n", "frontmatter", 1},
		"bad yaml":     {"---\ntitle: [unclosed\n---\n", "frontmatter", 0},
		"not a map":    {"---\n- a\n- b\n---\n", "frontmatter", 2},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := Parse(tc.doc)
			if err == nil {
				t.Fatal("expected an error")
			}
			if err.Field != tc.field {
				t.Errorf("field %q, want %q (%s)", err.Field, tc.field, err.Reason)
			}
			if tc.line > 0 && err.Line != tc.line {
				t.Errorf("line %d, want %d", err.Line, tc.line)
			}
		})
	}
}

func TestNoFrontmatterAndHorizontalRule(t *testing.T) {
	fm, body, err := Parse("# Título\n\n---\n\ntexto")
	if err != nil || fm.Present || body != "# Título\n\n---\n\ntexto" {
		t.Fatalf("%+v %q %v", fm, body, err)
	}
}

func TestTitle(t *testing.T) {
	if got := Title(Frontmatter{}, "```\n# no\n```\n\n# Sí  título #\n", "x.md"); got != "Sí título" {
		t.Errorf("got %q", got)
	}
	if got := Title(Frontmatter{}, "Setext\n=====\n", "x.md"); got != "Setext" {
		t.Errorf("got %q", got)
	}
	if got := Title(Frontmatter{}, "sin encabezados", "notas de reunión.md"); got != "notas de reunión" {
		t.Errorf("got %q", got)
	}
	if got := Title(Frontmatter{Title: "FM"}, "# H1", "a.md"); got != "FM" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeTopic(t *testing.T) {
	for in, want := range map[string]string{
		"Buenas Prácticas": "buenas-practicas", "  Infra  ": "infra", "año 2026!!": "ano-2026", "---": "", "C++/Go": "c-go",
	} {
		if got := NormalizeTopic(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestChunks(t *testing.T) {
	body := "intro\n\n# A\ntexto a\n\n## A.1\ntexto a1\n```\n# no es encabezado\n```\n# B\n"
	chunks := Chunks(body)
	if len(chunks) != 4 {
		t.Fatalf("got %d chunks: %+v", len(chunks), chunks)
	}
	if strings.Join(chunks[2].HeadingPath, ">") != "A>A.1" || !strings.Contains(chunks[2].Body, "# no es encabezado") {
		t.Errorf("chunk 2 %+v", chunks[2])
	}
	if strings.Join(chunks[3].HeadingPath, ">") != "B" || chunks[3].Body != "" {
		t.Errorf("chunk 3 %+v", chunks[3])
	}
	long := strings.Repeat("palabra ", 900) + "\n\n" + strings.Repeat("otra ", 300)
	if n := len(Chunks(long)); n != 3 {
		t.Errorf("long section split in %d chunks, want 3", n)
	}
}

func TestRenderShare(t *testing.T) {
	att := "01JBX7Q2M4N8P6R3T5V9W1Y0ZA"
	other := "01JBX7Q2M4N8P6R3T5V9W1Y0ZB"
	doc := "---\njiku:\n  projects: [12]\n---\n# Título\n\n![diagrama](https://web.example/docs/attachments/" + att + ")\n" +
		"[otro adjunto](/docs/attachments/" + other + ")\n[doc](../infra/x.md) y [web](https://example.com/x)\n" +
		"[ref](https://web.example/docs/01JBX7Q2M4N8P6R3T5V9W1Y0ZC) fin\n" +
		"<script>alert(1)</script>\n[js](javascript:alert(1))\n"
	html, err := RenderShare(doc, ShareRender{AttachmentURL: func(id string) string {
		if id == att {
			return "/p/docs/TOKEN/attachments/" + id
		}
		return ""
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`src="/p/docs/TOKEN/attachments/` + att + `"`, "otro adjunto", "doc y", `href="https://example.com/x"`, "ref fin"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in %s", want, html)
		}
	}
	for _, bad := range []string{"projects", "<script", "javascript:", "/docs/attachments/" + other, "../infra", "01JBX7Q2M4N8P6R3T5V9W1Y0ZC"} {
		if strings.Contains(html, bad) {
			t.Errorf("unexpected %q in %s", bad, html)
		}
	}
}

func TestRemoveRelations(t *testing.T) {
	doc := "---\ntitle: X # comentario\njiku:\n  projects: [12, 31]\n  tasks: [5]\n---\ncuerpo\n"
	out, err := RemoveRelations(doc, map[string][]int64{"projects": {31}})
	if err != nil {
		t.Fatal(err)
	}
	fm, body, ferr := Parse(out)
	if ferr != nil {
		t.Fatal(ferr)
	}
	if len(fm.Relations.Projects) != 1 || fm.Relations.Projects[0] != 12 || len(fm.Relations.Tasks) != 1 || body != "cuerpo\n" {
		t.Errorf("got %+v body %q\n%s", fm.Relations, body, out)
	}
	if fm.Title != "X" {
		t.Errorf("title lost: %q", fm.Title)
	}
}

func TestAttachmentID(t *testing.T) {
	id := "01JBX7Q2M4N8P6R3T5V9W1Y0ZA"
	for dest, ok := range map[string]bool{
		"https://jiku.example/docs/attachments/" + id: true,
		"/docs/attachments/" + id:                     true,
		"docs/attachments/" + id:                      false,
		"https://x/docs/attachments/abc":              false,
		"mailto:a@b":                                  false,
	} {
		if _, got := AttachmentID(dest); got != ok {
			t.Errorf("%s: %v", dest, got)
		}
	}
}
