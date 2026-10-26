package markdown

import "testing"

func TestSetVisibility(t *testing.T) {
	cases := []struct{ in, want string }{
		{"# Hola\n", "---\nvisibility: restricted\n---\n# Hola\n"},
		{"---\ntitle: A\n# comentario\nvisibility: public\ntopics: [x]\n---\ncuerpo\n",
			"---\ntitle: A\n# comentario\nvisibility: restricted\ntopics: [x]\n---\ncuerpo\n"},
		{"---\ntitle: A # c\n---\ncuerpo", "---\nvisibility: restricted\ntitle: A # c\n---\ncuerpo"},
		{"---\n---\nx\n", "---\nvisibility: restricted\n---\nx\n"},
		{"\uFEFF---\nvisibility: \"internal\"\n---\nx", "\uFEFF---\nvisibility: restricted\n---\nx"},
	}
	for _, c := range cases {
		got, err := SetVisibility(c.in, VisibilityRestricted)
		if err != nil || got != c.want {
			t.Errorf("SetVisibility(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
		fm, _, ferr := Parse(got)
		if ferr != nil || fm.Visibility != VisibilityRestricted {
			t.Errorf("reparse %q: %v %v", got, fm.Visibility, ferr)
		}
	}
	// A flow-mapping frontmatter is re-encoded.
	got, err := SetVisibility("---\n{title: A, visibility: public}\n---\nx", VisibilityInternal)
	if fm, _, ferr := Parse(got); err != nil || ferr != nil || fm.Visibility != VisibilityInternal || fm.Title != "A" {
		t.Errorf("flow: %q %v %v", got, err, ferr)
	}
	if _, err := SetVisibility("x", "secreta"); err == nil {
		t.Error("unknown visibility must fail")
	}
}

func TestSetRelations(t *testing.T) {
	in := "---\ntitle: A\njiku:\n  projects: [12]\n  # nota\n  tasks:\n    - 900\n\n# tema\ntopics: [infra]\n---\n# Cuerpo\njiku: no se toca\n"
	got, err := SetRelations(in, Relations{Projects: []int64{31, 12}, Clients: []int64{4}})
	want := "---\ntitle: A\njiku:\n  clients: [4]\n  projects: [12, 31]\n\n# tema\ntopics: [infra]\n---\n# Cuerpo\njiku: no se toca\n"
	if err != nil || got != want {
		t.Fatalf("got %q, %v\nwant %q", got, err, want)
	}
	fm, _, _ := Parse(got)
	if len(fm.Relations.Projects) != 2 || fm.Relations.Clients[0] != 4 || len(fm.Topics) != 1 {
		t.Fatalf("%+v", fm)
	}
	// Empty relations remove the block; a document without frontmatter gets one.
	if got, _ := SetRelations(want, Relations{}); got != "---\ntitle: A\n\n# tema\ntopics: [infra]\n---\n# Cuerpo\njiku: no se toca\n" {
		t.Errorf("remove: %q", got)
	}
	if got, _ := SetRelations("hola", Relations{Projects: []int64{1}}); got != "---\njiku:\n  projects: [1]\n---\nhola" {
		t.Errorf("new: %q", got)
	}
	if got, _ := SetRelations("---\ntitle: B\n---\nx", Relations{Tasks: []int64{2}}); got != "---\ntitle: B\njiku:\n  tasks: [2]\n---\nx" {
		t.Errorf("append: %q", got)
	}
}

func TestFirstRelationsAndRestricted(t *testing.T) {
	fm, _, err := Parse("---\nvisibility: restricted\njiku:\n  projects: [31, 12]\n  clients: [9, 4]\n---\n")
	if err != nil || fm.Visibility != VisibilityRestricted || fm.FirstProject != 31 || fm.FirstClient != 9 || fm.Relations.Projects[0] != 12 {
		t.Fatalf("%+v %v", fm, err)
	}
	if Slug("EXO - Videoconferencia Móvil", 100) != "exo-videoconferencia-movil" {
		t.Error(Slug("EXO - Videoconferencia Móvil", 100))
	}
}
