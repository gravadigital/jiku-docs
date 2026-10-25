package hedgedoc

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/oklog/ulid/v2"
	"gopkg.in/yaml.v3"

	"github.com/gravadigital/jiku-docs/internal/markdown"
)

// deterministicULID derives a stable ULID: the time part from t, the entropy from the seed.
// Re-running the import produces the same ids, which makes it idempotent.
func deterministicULID(t time.Time, seed string) string {
	sum := sha256.Sum256([]byte(seed))
	if t.IsZero() || t.Before(time.Unix(0, 0)) {
		t = time.Unix(0, 0)
	}
	id, err := ulid.New(ulid.Timestamp(t), bytes.NewReader(sum[:]))
	if err != nil {
		id = ulid.MustNew(0, bytes.NewReader(sum[:]))
	}
	return id.String()
}

// DocumentID is the jiku-docs id of a note.
func DocumentID(n Note) string { return deterministicULID(n.CreatedAt, "hedgedoc-note:"+n.ID) }

// AttachmentID is the id of an asset of a note.
func AttachmentID(n Note, assetURL string) string {
	return deterministicULID(n.CreatedAt, "hedgedoc-asset:"+n.ID+":"+assetURL)
}

// FolderID is the id of an imported folder path.
func FolderID(p string) string {
	return deterministicULID(time.Unix(1700000000, 0), "hedgedoc-folder:"+p)
}

// EncodeNoteID is HedgeDoc's URL form of a note id: base64url of the UUID bytes.
func EncodeNoteID(id string) string {
	b, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Resolver maps the URL slugs of HedgeDoc (alias, shortid, encoded id, raw uuid) to notes.
type Resolver struct{ bySlug map[string]*Note }

// NewResolver indexes notes.
func NewResolver(notes []Note) *Resolver {
	r := &Resolver{bySlug: map[string]*Note{}}
	for i := range notes {
		n := &notes[i]
		for _, s := range []string{n.ID, EncodeNoteID(n.ID), n.ShortID, n.Alias} {
			if s != "" {
				if _, taken := r.bySlug[s]; !taken {
					r.bySlug[s] = n
				}
			}
		}
	}
	return r
}

// Find resolves a slug, as HedgeDoc's parseNoteId does.
func (r *Resolver) Find(slug string) *Note {
	slug, _ = url.PathUnescape(slug)
	return r.bySlug[slug]
}

// FileName builds a file name from a title: readable, without / or \, at most 120 characters.
func FileName(title, fallback string) string {
	t := strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\':
			return '-'
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, title)
	t = strings.Join(strings.Fields(t), " ")
	t = strings.Trim(t, ". ")
	if r := []rune(t); len(r) > 120 {
		t = strings.TrimSpace(string(r[:120]))
	}
	if t == "" {
		t = fallback
	}
	if strings.HasSuffix(strings.ToLower(t), ".md") {
		t = t[:len(t)-3]
	}
	return t + ".md"
}

// UniqueName returns name, or "name (2).md", "name (3).md"... avoiding taken (lowercase keys).
func UniqueName(name string, taken map[string]bool) string {
	if !taken[strings.ToLower(name)] {
		return name
	}
	base := strings.TrimSuffix(name, ".md")
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s (%d).md", base, i)
		if !taken[strings.ToLower(candidate)] {
			return candidate
		}
	}
}

var urlRe = regexp.MustCompile(`https?://[^\s<>"'()\[\]{}]+`)

// AssetURLs lists the distinct URLs of a content that start with one of the asset prefixes
// (scheme-less, e.g. "sfo2.digitaloceanspaces.com/grava-internal-01/docs-grava-io/").
func AssetURLs(content string, prefixes []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range urlRe.FindAllString(content, -1) {
		u = strings.TrimRight(u, ".,;:!?*_")
		bare := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		for _, p := range prefixes {
			if strings.HasPrefix(bare, p) && !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	}
	sort.Strings(out)
	return out
}

// RewriteNoteLinks replaces links to other HedgeDoc notes (`{base}/{slug}`, `/s/{shortid}`,
// `/p/{shortid}`) with the jiku-docs document link. Unresolved links stay as they are and are
// reported.
func RewriteNoteLinks(content, base string, r *Resolver, link func(*Note) string) (string, []string) {
	base = strings.TrimRight(base, "/")
	if base == "" {
		return content, nil
	}
	var unresolved []string
	re := linkRegexp(base)
	out := re.ReplaceAllStringFunc(content, func(m string) string {
		sub := re.FindStringSubmatch(m)
		slug, anchor := sub[1], sub[2]
		if strings.HasPrefix(slug, "uploads") || slug == "new" || strings.HasPrefix(slug, "auth") {
			return m
		}
		n := r.Find(slug)
		if n == nil {
			unresolved = append(unresolved, slug)
			return m
		}
		target := link(n)
		if target == "" {
			unresolved = append(unresolved, slug)
			return m
		}
		return target + anchor
	})
	return out, unresolved
}

var (
	linkReMu    sync.Mutex
	linkReCache = map[string]*regexp.Regexp{}
)

func linkRegexp(base string) *regexp.Regexp {
	linkReMu.Lock()
	defer linkReMu.Unlock()
	if re, ok := linkReCache[base]; ok {
		return re
	}
	re := regexp.MustCompile(regexp.QuoteMeta(base) + `/(?:(?:s|p)/)?([A-Za-z0-9_\-.%~]+)(#[^\s)"'<>\]]*)?`)
	linkReCache[base] = re
	return re
}

// HedgeDocMeta is what the import records about the original note in the frontmatter.
type HedgeDocMeta struct {
	ID         string         `yaml:"id"`
	ShortID    string         `yaml:"shortid,omitempty"`
	Alias      string         `yaml:"alias,omitempty"`
	CreatedAt  string         `yaml:"createdAt"`
	UpdatedAt  string         `yaml:"updatedAt"`
	Author     string         `yaml:"author,omitempty"`
	LastEditor string         `yaml:"lastEditor,omitempty"`
	Permission string         `yaml:"permission,omitempty"`
	Meta       map[string]any `yaml:"meta,omitempty"`
}

// keys of a HedgeDoc frontmatter that jiku-docs would interpret: they move under hedgedoc.meta.
var reservedKeys = map[string]bool{"visibility": true, "jiku": true, "topics": true, "hedgedoc": true}

// BuildContent turns a HedgeDoc note into a jiku-docs document: a frontmatter with the title,
// topics taken from HedgeDoc tags and the original metadata, followed by the body. A broken
// HedgeDoc frontmatter is kept verbatim as a code block so nothing is lost.
func BuildContent(n Note, body string) (string, []string) {
	var warnings []string
	meta := map[string]any{}
	var topics []string
	title := strings.TrimSpace(n.Title)
	yamlText, rest, present, ferr := markdown.Split(body)
	if present && ferr == nil {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(yamlText), &m); err != nil {
			warnings = append(warnings, "frontmatter de HedgeDoc inválido: se conserva como bloque de código")
			rest = "```yaml\n" + yamlText + "```\n\n" + rest
		} else {
			for k, v := range m {
				switch {
				case k == "title":
					if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
						title = strings.TrimSpace(s)
					}
				case k == "tags":
					topics = append(topics, tagsOf(v)...)
				case reservedKeys[k]:
					meta[k] = v
				default:
					meta[k] = v
				}
			}
		}
		body = rest
	} else if present && ferr != nil {
		warnings = append(warnings, "frontmatter de HedgeDoc sin cierre: se importa como texto")
	}
	var slugs []string
	seen := map[string]bool{}
	for _, t := range topics {
		if s := markdown.NormalizeTopic(t); s != "" && !seen[s] {
			seen[s] = true
			slugs = append(slugs, s)
		}
	}
	sort.Strings(slugs)
	fm := struct {
		Title    string       `yaml:"title,omitempty"`
		Topics   []string     `yaml:"topics,omitempty"`
		HedgeDoc HedgeDocMeta `yaml:"hedgedoc"`
	}{
		Title:  title,
		Topics: slugs,
		HedgeDoc: HedgeDocMeta{
			ID: n.ID, ShortID: n.ShortID, Alias: n.Alias,
			CreatedAt: n.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: n.UpdatedAt.UTC().Format(time.RFC3339),
			Author: n.Owner, LastEditor: n.LastEditor, Permission: n.Permission,
		},
	}
	if len(meta) > 0 {
		fm.HedgeDoc.Meta = meta
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	_ = enc.Encode(fm)
	_ = enc.Close()
	body = strings.ReplaceAll(body, "\r\n", "\n")
	return "---\n" + buf.String() + "---\n\n" + strings.TrimLeft(body, "\n"), warnings
}

func tagsOf(v any) []string {
	switch t := v.(type) {
	case string:
		var out []string
		for _, p := range strings.Split(t, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	case []any:
		var out []string
		for _, el := range t {
			out = append(out, fmt.Sprint(el))
		}
		return out
	}
	return nil
}

// Message is the message of the first version: who wrote the note originally and when.
func Message(n Note) string {
	author := n.Owner
	if author == "" {
		author = "anónimo"
	}
	msg := fmt.Sprintf("Importado de HedgeDoc (nota %s). Autor original: %s. Creada: %s.", n.ID, author,
		n.CreatedAt.UTC().Format("2006-01-02 15:04"))
	if n.LastEditor != "" {
		msg += fmt.Sprintf(" Última edición: %s por %s.", n.UpdatedAt.UTC().Format("2006-01-02 15:04"), n.LastEditor)
	}
	if r := []rune(msg); len(r) > 500 {
		msg = string(r[:500])
	}
	return msg
}

// AssetName is the file name of an asset URL.
func AssetName(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return "adjunto"
	}
	name := path.Base(p.Path)
	if name == "" || name == "/" || name == "." {
		return "adjunto"
	}
	if r := []rune(name); len(r) > 200 {
		name = string(r[len(r)-200:])
	}
	return name
}

// readAll reads up to max bytes; more is an error.
func readAll(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("asset larger than %d bytes", max)
	}
	return b, nil
}

// MergeFrontmatter adds relations and topics to the frontmatter of a document, keeping what it
// already has: the `jiku` lists and the topics are merged without duplicates, the topics
// normalised as the service does (kebab-case without accents), and every other key stays as it
// was. A document without a frontmatter gets one. With nothing to add the content is returned
// as is.
func MergeFrontmatter(content string, rel markdown.Relations, topics []string) (string, error) {
	extraTopics, err := NormalizeTopics(topics)
	if err != nil {
		return "", err
	}
	if rel.Empty() && len(extraTopics) == 0 {
		return content, nil
	}
	fm, _, ferr := markdown.Parse(content)
	if ferr != nil {
		return "", ferr
	}
	yamlText, body, present, _ := markdown.Split(content)
	root := &yaml.Node{Kind: yaml.MappingNode}
	if present && strings.TrimSpace(yamlText) != "" {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(yamlText), &doc); err != nil {
			return "", err
		}
		if len(doc.Content) > 0 {
			root = doc.Content[0]
		}
	}
	if !present {
		body = "\n" + content
	}
	merged := markdown.Relations{}
	for _, k := range markdown.RelationKinds {
		merged.Set(k.Key, append(append([]int64{}, fm.Relations.Get(k.Key)...), rel.Get(k.Key)...))
	}
	merged = merged.Normalize()
	allTopics, _ := NormalizeTopics(append(append([]string{}, fm.Topics...), extraTopics...))

	if len(allTopics) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, t := range allTopics {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: t})
		}
		setKey(root, "topics", seq, "title")
	}
	if !merged.Empty() {
		block := &yaml.Node{Kind: yaml.MappingNode}
		for _, k := range markdown.RelationKinds {
			ids := merged.Get(k.Key)
			if len(ids) == 0 {
				continue
			}
			seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
			for _, id := range ids {
				seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(id)})
			}
			block.Content = append(block.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k.Key}, seq)
		}
		setKey(root, "jiku", block, "topics")
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

// setKey replaces the value of key in a mapping node or, when absent, inserts it right after the
// key `after` (or first, when that is absent too).
func setKey(m *yaml.Node, key string, val *yaml.Node, after string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	pos := 0
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == after {
			pos = i + 2
		}
	}
	pair := []*yaml.Node{{Kind: yaml.ScalarNode, Value: key}, val}
	m.Content = append(m.Content[:pos], append(pair, m.Content[pos:]...)...)
}

// NormalizeTopics normalises topics as the service does, sorted and without duplicates. A topic
// that is empty once normalised is an error.
func NormalizeTopics(topics []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range topics {
		s := markdown.NormalizeTopic(t)
		if s == "" {
			return nil, fmt.Errorf("el tema %q queda vacío al normalizarlo", t)
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out, nil
}

// WithoutRelation returns rel without one id of an entity type (client, project, requirement, task).
func WithoutRelation(rel markdown.Relations, entityType string, id int64) (markdown.Relations, bool) {
	out := markdown.Relations{}
	removed := false
	for _, k := range markdown.RelationKinds {
		var kept []int64
		for _, v := range rel.Get(k.Key) {
			if k.EntityType == entityType && v == id {
				removed = true
				continue
			}
			kept = append(kept, v)
		}
		out.Set(k.Key, kept)
	}
	return out, removed
}

// ReimportMessage is the message of a version written by a later run of the import.
func ReimportMessage(n Note) string {
	return fmt.Sprintf("Reimportado de HedgeDoc (nota %s): contenido, relaciones y temas de la planilla actualizados.", n.ID)
}

// IsImportMessage reports whether a version message was written by the import.
func IsImportMessage(msg string) bool {
	return strings.HasPrefix(msg, "Importado de HedgeDoc") || strings.HasPrefix(msg, "Reimportado de HedgeDoc")
}
