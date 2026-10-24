package query

import (
	"encoding/json"
	"testing"

	"github.com/gravadigital/jiku-docs/internal/protocol"
)

var spec = &Spec{
	Name: "documents", Base: []string{"id", "title"}, Fields: []string{"id", "title", "path"},
	Includable: []string{"lock"}, IncludableSpecs: map[string]FieldSpec{"lock": {Kind: "relation"}},
	Filterable: []string{"id", "projectId", "visibility", "updatedAt", "deleted"},
	FilterableSpecs: map[string]FilterSpec{
		"id": {Kind: KindString, ULID: true, List: true}, "projectId": {Kind: KindInteger, List: true},
		"visibility": {Kind: KindString, Enum: "visibility"}, "updatedAt": {Kind: KindDate, Range: true},
		"deleted": {Kind: KindBoolean},
	},
	AllowOr: true, Sortable: []string{"updatedAt", "title"}, DefaultSort: []string{"-updatedAt"},
	Enums:  map[string][]string{"visibility": {"internal", "public"}},
	Levers: []string{"filter", "sort", "page", "fields", "include", "count"},
}

func parse(t *testing.T, payload string) (*List, error) {
	t.Helper()
	q, _, err := ParseList(spec, json.RawMessage(payload))
	return q, err
}

func code(err error) string { return protocol.CodeOf(err) }

func TestParseListDefaults(t *testing.T) {
	q, err := parse(t, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if q.Limit != 50 || len(q.Sort) != 2 || q.Sort[0] != (SortKey{"updatedAt", true}) || q.Sort[1].Name != "id" {
		t.Errorf("defaults %+v", q)
	}
	if len(q.Fields) != 2 {
		t.Errorf("fields %v", q.Fields)
	}
}

func TestParseListRejects(t *testing.T) {
	for _, p := range []string{
		`{"unknown":1}`, `{"filter":{"nope":1}}`, `{"filter":{"visibility":"secret"}}`, `{"filter":{"projectId":"x"}}`,
		`{"filter":{"id":"not-a-ulid"}}`, `{"filter":{"updatedAt":"2026-01-01"}}`, `{"filter":{"or":[{"or":[]}]}}`,
		`{"sort":["size"]}`, `{"sort":["title","-title"]}`, `{"fields":["secret"]}`, `{"include":["content"]}`,
		`{"count":"all"}`, `{"page":{"limit":0}}`, `{"page":{"other":1}}`, `{"filter":{"visibility":null}}`, `[]`,
	} {
		if _, err := parse(t, p); code(err) != protocol.CodeInvalidFields {
			t.Errorf("%s: got %v", p, err)
		}
	}
}

func TestParseListAccepts(t *testing.T) {
	q, err := parse(t, `{"filter":{"projectId":[1,2],"updatedAt":{"gte":"2026-06-01T00:00:00Z"},"or":[{"visibility":"public"},{"deleted":true}]},
		"sort":["title"],"page":{"limit":999,"cursor":null},"fields":["path"],"include":["lock"],"count":"only"}`)
	if err != nil {
		t.Fatal(err)
	}
	if q.Limit != MaxLimit || q.Count != "only" || len(q.Filter.Or) != 2 || len(q.Filter.Conds["projectId"].Ints) != 2 {
		t.Errorf("%+v", q)
	}
	if q.Fields[0] != "id" {
		t.Errorf("id must always be returned: %v", q.Fields)
	}
}

func TestCursorTiedToFilterAndSort(t *testing.T) {
	q, err := parse(t, `{"filter":{"projectId":[2,1]},"sort":["title"]}`)
	if err != nil {
		t.Fatal(err)
	}
	cur := EncodeCursor([]any{"b", "01JBX7Q2M4N8P6R3T5V9W1Y0ZA"}, q.Filter.Raw, q.SortRaw)
	// Same filter with keys reordered: valid.
	if _, err := parse(t, `{"filter":{"projectId":[1,2]},"sort":["title"],"page":{"cursor":"`+cur+`"}}`); err != nil {
		t.Errorf("reordered filter must keep the cursor valid: %v", err)
	}
	for _, p := range []string{
		`{"filter":{"projectId":[1]},"sort":["title"],"page":{"cursor":"` + cur + `"}}`,
		`{"filter":{"projectId":[1,2]},"page":{"cursor":"` + cur + `"}}`,
		`{"page":{"cursor":"garbage"}}`,
	} {
		if _, err := parse(t, p); code(err) != protocol.CodeInvalidCursor {
			t.Errorf("%s: got %v", p, err)
		}
	}
}

func TestDescribe(t *testing.T) {
	d := Describe(spec)
	if d.Filterable["visibility"].Kind != KindEnum || d.Defaults.MaxLimit != 200 || len(d.Sortable) != 2 {
		t.Errorf("%+v", d)
	}
	if d.Enums["visibility"][1].Label != "Público" {
		t.Errorf("labels %+v", d.Enums)
	}
}
