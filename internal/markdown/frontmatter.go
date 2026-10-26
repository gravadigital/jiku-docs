// Package markdown parses and transforms the documents: frontmatter (the source of truth of
// title, visibility, topics and relations), titles, topic slugs, search chunks and the
// sanitised HTML served to shared links.
package markdown

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

// Relations are the links of a document with Jiku entities, by type.
type Relations struct {
	Clients      []int64 `json:"clients"`
	Projects     []int64 `json:"projects"`
	Requirements []int64 `json:"requirements"`
	Tasks        []int64 `json:"tasks"`
}

// RelationKinds in a stable order, with their frontmatter key and entity type.
var RelationKinds = []struct {
	Key        string // key in the `jiku` block and in Relations JSON
	EntityType string // value of document_relations.entity_type
}{
	{"clients", "client"},
	{"projects", "project"},
	{"requirements", "requirement"},
	{"tasks", "task"},
}

// Get returns the ids of a frontmatter key.
func (r *Relations) Get(key string) []int64 {
	switch key {
	case "clients":
		return r.Clients
	case "projects":
		return r.Projects
	case "requirements":
		return r.Requirements
	case "tasks":
		return r.Tasks
	}
	return nil
}

// Set replaces the ids of a frontmatter key.
func (r *Relations) Set(key string, ids []int64) {
	switch key {
	case "clients":
		r.Clients = ids
	case "projects":
		r.Projects = ids
	case "requirements":
		r.Requirements = ids
	case "tasks":
		r.Tasks = ids
	}
}

// Normalize sorts, dedupes and turns nil lists into empty ones (stable JSON).
func (r Relations) Normalize() Relations {
	out := Relations{}
	for _, k := range RelationKinds {
		out.Set(k.Key, uniqueSorted(r.Get(k.Key)))
	}
	return out
}

// Empty reports whether there is no relation at all.
func (r Relations) Empty() bool {
	return len(r.Clients)+len(r.Projects)+len(r.Requirements)+len(r.Tasks) == 0
}

func uniqueSorted(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Frontmatter is what the service reads from the YAML block.
type Frontmatter struct {
	Present    bool
	Title      string // "" when absent
	Visibility string // "internal" (default), "public" or "restricted"
	Topics     []string
	Relations  Relations
	// FirstProject and FirstClient are the first ids of `jiku.projects` and `jiku.clients` as
	// written (Relations is sorted): the generated location uses them. 0 when absent.
	FirstProject int64
	FirstClient  int64
}

// Visibilities of a document (P-1): internal (default), public (also externals of a related
// project) and restricted (only admins and services).
const (
	VisibilityInternal   = "internal"
	VisibilityPublic     = "public"
	VisibilityRestricted = "restricted"
)

// ValidVisibility reports a known visibility value.
func ValidVisibility(v string) bool {
	return v == VisibilityInternal || v == VisibilityPublic || v == VisibilityRestricted
}

// FrontmatterError is a `frontmatter_invalid` with its details.
type FrontmatterError struct {
	Line   int
	Field  string
	Reason string
}

func (e *FrontmatterError) Error() string {
	return fmt.Sprintf("frontmatter inválido (línea %d, %s): %s", e.Line, e.Field, e.Reason)
}

// Split separates the frontmatter block from the body. yamlText is the YAML without the
// delimiters; bodyStart is the 1-based line where the body begins. A document without a
// frontmatter returns present=false and the whole content as body.
func Split(content string) (yamlText, body string, present bool, err *FrontmatterError) {
	c := strings.TrimPrefix(content, "\uFEFF")
	if !strings.HasPrefix(c, "---") {
		return "", content, false, nil
	}
	firstEnd := strings.IndexByte(c, '\n')
	first := c
	if firstEnd >= 0 {
		first = c[:firstEnd]
	}
	if strings.TrimRight(first, " \t\r") != "---" {
		return "", content, false, nil
	}
	if firstEnd < 0 {
		return "", "", true, &FrontmatterError{Line: 1, Field: "frontmatter", Reason: "el bloque no tiene cierre (---)"}
	}
	rest := c[firstEnd+1:]
	offset := 0
	for {
		nl := strings.IndexByte(rest[offset:], '\n')
		var line string
		if nl < 0 {
			line = rest[offset:]
		} else {
			line = rest[offset : offset+nl]
		}
		trimmed := strings.TrimRight(line, " \t\r")
		if trimmed == "---" || trimmed == "..." {
			yamlText = rest[:offset]
			if nl < 0 {
				body = ""
			} else {
				body = rest[offset+nl+1:]
			}
			return yamlText, body, true, nil
		}
		if nl < 0 {
			return "", "", true, &FrontmatterError{Line: 1, Field: "frontmatter", Reason: "el bloque no tiene cierre (---)"}
		}
		offset += nl + 1
	}
}

var yamlLineRe = regexp.MustCompile(`line (\d+)`)

// Parse reads the frontmatter of a document. The body is returned without the block.
func Parse(content string) (Frontmatter, string, *FrontmatterError) {
	fm := Frontmatter{Visibility: "internal", Topics: []string{}}
	yamlText, body, present, ferr := Split(content)
	if ferr != nil {
		return fm, "", ferr
	}
	if !present {
		return fm, body, nil
	}
	fm.Present = true
	if strings.TrimSpace(yamlText) == "" {
		return fm, body, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(yamlText), &doc); err != nil {
		line := 1
		if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
			n, _ := strconv.Atoi(m[1])
			line = n + 1
		}
		reason := strings.TrimPrefix(err.Error(), "yaml: ")
		return fm, "", &FrontmatterError{Line: line, Field: "frontmatter", Reason: "YAML mal formado: " + reason}
	}
	if len(doc.Content) == 0 {
		return fm, body, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fm, "", &FrontmatterError{Line: root.Line + 1, Field: "frontmatter", Reason: "el frontmatter tiene que ser un mapa de claves"}
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i], root.Content[i+1]
		line := key.Line + 1
		switch key.Value {
		case "title":
			if val.Kind != yaml.ScalarNode {
				return fm, "", &FrontmatterError{Line: line, Field: "title", Reason: "tiene que ser un texto"}
			}
			fm.Title = strings.TrimSpace(val.Value)
		case "visibility":
			if val.Kind != yaml.ScalarNode || !ValidVisibility(val.Value) {
				return fm, "", &FrontmatterError{Line: line, Field: "visibility", Reason: "tiene que ser internal, public o restricted"}
			}
			fm.Visibility = val.Value
		case "topics":
			topics, err := parseTopics(val)
			if err != nil {
				err.Line = val.Line + 1
				return fm, "", err
			}
			fm.Topics = topics
		case "jiku":
			rel, err := parseJiku(val)
			if err != nil {
				return fm, "", err
			}
			fm.Relations = rel
			if len(rel.Projects) > 0 {
				fm.FirstProject = rel.Projects[0]
			}
			if len(rel.Clients) > 0 {
				fm.FirstClient = rel.Clients[0]
			}
		}
	}
	fm.Relations = fm.Relations.Normalize()
	return fm, body, nil
}

func parseTopics(val *yaml.Node) ([]string, *FrontmatterError) {
	if val.Kind == yaml.ScalarNode && val.Tag == "!!null" {
		return []string{}, nil
	}
	if val.Kind != yaml.SequenceNode {
		return nil, &FrontmatterError{Field: "topics", Reason: "tiene que ser una lista de textos"}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, item := range val.Content {
		if item.Kind != yaml.ScalarNode || item.Tag == "!!null" {
			return nil, &FrontmatterError{Line: item.Line + 1, Field: "topics", Reason: "cada tema tiene que ser un texto"}
		}
		slug := NormalizeTopic(item.Value)
		if slug == "" {
			return nil, &FrontmatterError{Line: item.Line + 1, Field: "topics", Reason: fmt.Sprintf("el tema %q queda vacío al normalizarlo", item.Value)}
		}
		if !seen[slug] {
			seen[slug] = true
			out = append(out, slug)
		}
	}
	sort.Strings(out)
	return out, nil
}

func parseJiku(val *yaml.Node) (Relations, *FrontmatterError) {
	var rel Relations
	if val.Kind == yaml.ScalarNode && val.Tag == "!!null" {
		return rel, nil
	}
	if val.Kind != yaml.MappingNode {
		return rel, &FrontmatterError{Line: val.Line + 1, Field: "jiku", Reason: "tiene que ser un mapa con clients, projects, requirements y tasks"}
	}
	for i := 0; i+1 < len(val.Content); i += 2 {
		key, list := val.Content[i], val.Content[i+1]
		field := "jiku." + key.Value
		known := false
		for _, k := range RelationKinds {
			if k.Key == key.Value {
				known = true
			}
		}
		if !known {
			return rel, &FrontmatterError{Line: key.Line + 1, Field: field, Reason: "clave desconocida: solo se aceptan clients, projects, requirements y tasks"}
		}
		if list.Kind == yaml.ScalarNode && list.Tag == "!!null" {
			continue
		}
		if list.Kind != yaml.SequenceNode {
			return rel, &FrontmatterError{Line: list.Line + 1, Field: field, Reason: "tiene que ser una lista de ids numéricos"}
		}
		ids := make([]int64, 0, len(list.Content))
		for _, item := range list.Content {
			n, err := strconv.ParseInt(item.Value, 10, 64)
			if item.Kind != yaml.ScalarNode || item.Tag != "!!int" || err != nil || n <= 0 {
				return rel, &FrontmatterError{Line: item.Line + 1, Field: field, Reason: fmt.Sprintf("%q no es un id numérico válido", item.Value)}
			}
			ids = append(ids, n)
		}
		rel.Set(key.Value, ids)
	}
	return rel, nil
}

// NormalizeTopic turns a free label into a kebab-case slug without accents: "Buenas Prácticas" -> "buenas-practicas".
func NormalizeTopic(s string) string { return slug(s, 64) }

func slug(s string, max int) string {
	t := norm.NFD.String(strings.ToLower(strings.TrimSpace(s)))
	var b strings.Builder
	dash := false
	for _, r := range t {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			dash = false
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len([]rune(out)) > max {
		out = strings.Trim(string([]rune(out)[:max]), "-")
	}
	return out
}

// Title resolves the title: frontmatter `title`, else the first level-1 heading of the body,
// else the file name without `.md`.
func Title(fm Frontmatter, body, fileName string) string {
	title := fm.Title
	if title == "" {
		title = FirstHeading(body)
	}
	if title == "" {
		title = strings.TrimSuffix(fileName, ".md")
		if strings.HasSuffix(strings.ToLower(title), ".md") {
			title = title[:len(title)-3]
		}
	}
	title = strings.Join(strings.Fields(title), " ")
	if r := []rune(title); len(r) > 300 {
		title = string(r[:300])
	}
	return title
}

var (
	atxH1Re     = regexp.MustCompile(`^ {0,3}#[ \t]+(.+?)[ \t]*#*[ \t]*$`)
	setextH1Re  = regexp.MustCompile(`^ {0,3}=+[ \t]*$`)
	fenceOpenRe = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
)

// FirstHeading returns the text of the first level-1 heading outside code fences.
func FirstHeading(body string) string {
	lines := strings.Split(body, "\n")
	fence := ""
	for i, line := range lines {
		line = strings.TrimRight(line, "\r")
		if fence != "" {
			if m := fenceOpenRe.FindStringSubmatch(line); m != nil && strings.HasPrefix(m[1], fence[:1]) && len(m[1]) >= len(fence) {
				fence = ""
			}
			continue
		}
		if m := fenceOpenRe.FindStringSubmatch(line); m != nil {
			fence = m[1]
			continue
		}
		if m := atxH1Re.FindStringSubmatch(line); m != nil {
			return strings.TrimSpace(m[1])
		}
		if i > 0 && setextH1Re.MatchString(line) && strings.TrimSpace(lines[i-1]) != "" {
			return strings.TrimSpace(lines[i-1])
		}
	}
	return ""
}

// RemoveRelations rewrites the `jiku` block of the frontmatter without the given ids, keeping
// every other key. Used by the daily relation review, which records the change as a new version.
func RemoveRelations(content string, remove map[string][]int64) (string, error) {
	yamlText, body, present, ferr := Split(content)
	if ferr != nil {
		return "", ferr
	}
	if !present {
		return content, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(yamlText), &doc); err != nil {
		return "", err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return content, nil
	}
	root := doc.Content[0]
	changed := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "jiku" || root.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		jiku := root.Content[i+1]
		for j := 0; j+1 < len(jiku.Content); j += 2 {
			ids := remove[jiku.Content[j].Value]
			list := jiku.Content[j+1]
			if len(ids) == 0 || list.Kind != yaml.SequenceNode {
				continue
			}
			kept := list.Content[:0]
			for _, item := range list.Content {
				n, err := strconv.ParseInt(item.Value, 10, 64)
				drop := false
				if err == nil {
					for _, id := range ids {
						if id == n {
							drop = true
						}
					}
				}
				if drop {
					changed = true
					continue
				}
				kept = append(kept, item)
			}
			list.Content = kept
			list.Style = yaml.FlowStyle
		}
	}
	if !changed {
		return content, nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	_ = enc.Close()
	return "---\n" + buf.String() + "---\n" + body, nil
}
