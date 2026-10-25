package contract_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/gravadigital/jiku-docs/internal/contract"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/query"
	"github.com/gravadigital/jiku-docs/internal/service"
)

func load(t *testing.T) *contract.Validator {
	t.Helper()
	root, err := contract.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	v, err := contract.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func patterns(eps []service.Endpoint) []string {
	out := make([]string, len(eps))
	for i, e := range eps {
		out[i] = e.Pattern
	}
	sort.Strings(out)
	return out
}

func channelPatterns(d *contract.Doc) []string {
	var out []string
	for _, c := range d.Channels() {
		out = append(out, c.Pattern)
	}
	sort.Strings(out)
	return out
}

// The service registers exactly the methods of the contracts: 18 commands, 13 queries.
func TestEndpointsMatchContracts(t *testing.T) {
	v := load(t)
	s := &service.Service{}
	cmds, qs := patterns(s.Commands()), patterns(s.Queries())
	if len(cmds) != 18 || len(qs) != 13 {
		t.Fatalf("expected 18 commands and 13 queries, got %d and %d", len(cmds), len(qs))
	}
	if got, want := strings.Join(cmds, " "), strings.Join(channelPatterns(v.Commands), " "); got != want {
		t.Errorf("commands differ from the contract:\nservice:  %s\ncontract: %s", got, want)
	}
	if got, want := strings.Join(qs, " "), strings.Join(channelPatterns(v.Queries), " "); got != want {
		t.Errorf("queries differ from the contract:\nservice:  %s\ncontract: %s", got, want)
	}
	// Endpoint names must be unique inside each micro service.
	for _, eps := range [][]service.Endpoint{s.Commands(), s.Queries()} {
		seen := map[string]bool{}
		for _, e := range eps {
			n := protocol.EndpointName(e.Pattern)
			if seen[n] {
				t.Errorf("duplicate endpoint name %s", n)
			}
			seen[n] = true
		}
	}
}

// Every request and reply schema of the contracts compiles.
func TestSchemasCompile(t *testing.T) {
	v := load(t)
	for _, d := range []*contract.Doc{v.Commands, v.Queries} {
		for _, c := range d.Channels() {
			if c.Request == "" || c.Reply == "" {
				t.Errorf("%s %s: missing request or reply schema", d.Name, c.Name)
				continue
			}
			for _, ref := range []string{c.Request, c.Reply} {
				if _, err := d.Schema(ref); err != nil {
					t.Errorf("%s %s: %v", d.Name, ref, err)
				}
			}
		}
	}
	for _, ref := range []string{"DocumentEventEnvelope", "FolderEventEnvelope", "AttachmentEventEnvelope", "ShareEventEnvelope",
		"DocumentNotificationEnvelope", "AccessChangedEnvelope"} {
		if _, err := v.Events.Schema("#/components/schemas/" + ref); err != nil {
			t.Errorf("events %s: %v", ref, err)
		}
	}
	if _, err := v.Modules.Schema("#/components/schemas/ModuleRecord"); err != nil {
		t.Error(err)
	}
}

func enumOf(t *testing.T, v any) []string {
	t.Helper()
	m, _ := v.(map[string]any)
	if m == nil {
		return nil
	}
	if items, ok := m["items"].(map[string]any); ok {
		m = items
	}
	raw, _ := m["enum"].([]any)
	out := make([]string, len(raw))
	for i, x := range raw {
		out[i] = x.(string)
	}
	return out
}

func sameSet(a, b []string) bool {
	x, y := append([]string{}, a...), append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	return strings.Join(x, ",") == strings.Join(y, ",")
}

func schemaProps(v *contract.Validator, name string) map[string]any {
	s, _ := v.Queries.Raw["components"].(map[string]any)["schemas"].(map[string]any)[name].(map[string]any)
	p, _ := s["properties"].(map[string]any)
	return p
}

func filterProps(v *contract.Validator, schema string) []string {
	props := schemaProps(v, schema)
	f, _ := props["filter"].(map[string]any)
	if ref, ok := f["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		f = v.Queries.Raw["components"].(map[string]any)["schemas"].(map[string]any)[name].(map[string]any)
	}
	fp, _ := f["properties"].(map[string]any)
	var out []string
	for k := range fp {
		out = append(out, k)
	}
	return out
}

// The resource sheets that validate (and feed meta.describe) agree with the YAML: the mechanical
// cross-check core runs by hand, automated.
func TestSpecsMatchQueriesContract(t *testing.T) {
	v := load(t)
	cases := []struct {
		spec   *query.Spec
		schema string
	}{
		{findSpec("folders"), "FoldersListQuery"},
		{findSpec("documents"), "DocumentsListQuery"},
		{findSpec("documents.versions"), "VersionsListQuery"},
		{findSpec("topics"), "TopicsListQuery"},
		{findSpec("attachments"), "AttachmentsListQuery"},
		{findSpec("shares"), "SharesListQuery"},
		{service.SearchSpec(), "SearchQuery"},
		{service.ChangesSpec(), "ChangesQuery"},
	}
	for _, tc := range cases {
		props := schemaProps(v, tc.schema)
		if props == nil {
			t.Fatalf("schema %s not found", tc.schema)
		}
		filters := tc.spec.Filterable
		if tc.spec.AllowOr {
			filters = append(append([]string{}, filters...), "or")
		}
		if got := filterProps(v, tc.schema); !sameSet(got, filters) {
			t.Errorf("%s: filters %v, contract %v", tc.spec.Name, filters, got)
		}
		if tc.spec.Has("sort") {
			var want []string
			for _, s := range tc.spec.Sortable {
				want = append(want, s, "-"+s)
			}
			if got := enumOf(t, props["sort"]); !sameSet(got, want) {
				t.Errorf("%s: sort %v, contract %v", tc.spec.Name, want, got)
			}
		}
		if tc.spec.Has("fields") {
			if got := enumOf(t, props["fields"]); !sameSet(got, tc.spec.Fields) {
				t.Errorf("%s: fields %v, contract %v", tc.spec.Name, tc.spec.Fields, got)
			}
		}
		if tc.spec.Has("include") {
			if got := enumOf(t, props["include"]); !sameSet(got, tc.spec.Includable) {
				t.Errorf("%s: include %v, contract %v", tc.spec.Name, tc.spec.Includable, got)
			}
		}
		// Levers: the top-level keys of the request, minus actor and the non-list ones.
		var levers []string
		for k := range props {
			switch k {
			case "actor", "query", "mode", "limit", "sinceSeq":
				continue
			}
			levers = append(levers, k)
		}
		if !sameSet(levers, tc.spec.Levers) {
			t.Errorf("%s: levers %v, contract %v", tc.spec.Name, tc.spec.Levers, levers)
		}
	}
	// meta.describe accepts exactly the described resources.
	var names []string
	for _, s := range service.Specs() {
		names = append(names, s.Name)
	}
	if got := enumOf(t, schemaProps(v, "DescribeQuery")["resources"]); !sameSet(got, names) {
		t.Errorf("meta.describe resources %v, contract %v", names, got)
	}
}

func findSpec(name string) *query.Spec {
	for _, s := range service.Specs() {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// The error codes the service can emit are in the open catalog of the contracts.
func TestErrorCatalog(t *testing.T) {
	v := load(t)
	f := v.Commands.Raw["components"].(map[string]any)["schemas"].(map[string]any)["Failure"].(map[string]any)
	codes := f["properties"].(map[string]any)["errorCode"].(map[string]any)["examples"].([]any)
	known := map[string]bool{}
	for _, c := range codes {
		known[c.(string)] = true
	}
	for _, c := range []string{protocol.CodeInvalidFields, protocol.CodeDocumentNotFound, protocol.CodeVersionConflict,
		protocol.CodeDocumentLocked, protocol.CodeShareNotFound, protocol.CodeUpstreamUnavailable} {
		if !known[c] {
			t.Errorf("%s missing from the catalog", c)
		}
	}
}

// D-11: the api sends the same actor it sends to core, and both planes agree on its shape.
func TestActorSchema(t *testing.T) {
	v := load(t)
	cases := []struct {
		actor string
		ok    bool
	}{
		{`{"id":"1","roles":["user"]}`, true},
		{`{"id":"1","roles":[]}`, true},
		{`{"id":"1","roles":["external-user"],"name":"Ana","username":"ana","email":"ana@example.com"}`, true},
		{`{"id":"1"}`, false},
		{`{"id":"1","roles":"user"}`, false},
		{`{"id":"1","roles":["user"],"via":"human"}`, false},
		{`{"roles":["user"]}`, false},
	}
	for _, c := range cases {
		for _, r := range []struct{ plane, method, payload string }{
			{protocol.PlaneQueries, "documents.list", `{"actor":` + c.actor + `}`},
			{protocol.PlaneCommands, "folders.new", `{"actor":` + c.actor + `,"id":"01J00000000000000000000000","parentId":"01J00000000000000000000001","name":"x"}`},
		} {
			err := v.ValidateRequest(r.plane, r.method, []byte(r.payload))
			if (err == nil) != c.ok {
				t.Errorf("%s %s: actor %s: got err %v, want ok=%v", r.plane, r.method, c.actor, err, c.ok)
			}
		}
	}
}
