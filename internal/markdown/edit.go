package markdown

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Edits of the frontmatter that keep the rest of the document as it is: the service rewrites
// one top-level key (`visibility`, `jiku`) and leaves every other line untouched, so comments,
// order and formatting of the person's frontmatter survive. A frontmatter written as a single
// flow mapping (`{title: x}`) cannot be edited line by line: it is re-encoded.

// keyBlock is the extent of a top-level key: lines [start, end) of the YAML text (0-based).
type keyBlock struct {
	key        string
	start, end int
}

// frontmatterParts locates the YAML text inside the content: content[:yamlStart] is the opening
// delimiter (and a BOM), content[yamlStart:yamlEnd] the YAML, content[yamlEnd:] the closing
// delimiter and the body.
func frontmatterParts(content string) (yamlStart, yamlEnd int, present bool, err error) {
	yamlText, _, present, ferr := Split(content)
	if ferr != nil {
		return 0, 0, false, ferr
	}
	if !present {
		return 0, 0, false, nil
	}
	bom := 0
	if strings.HasPrefix(content, "\uFEFF") {
		bom = len("\uFEFF")
	}
	nl := strings.IndexByte(content[bom:], '\n')
	yamlStart = bom + nl + 1
	return yamlStart, yamlStart + len(yamlText), true, nil
}

// topBlocks returns the top-level keys of a frontmatter with the lines each one spans. Trailing
// blank and comment lines of a block are left out of it. flow is true for a root written as a
// flow mapping, which cannot be edited by lines.
func topBlocks(yamlText string) (blocks []keyBlock, root *yaml.Node, flow bool, err error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(yamlText), &doc); err != nil {
		return nil, nil, false, err
	}
	if len(doc.Content) == 0 {
		return nil, nil, false, nil
	}
	root = doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, false, errors.New("the frontmatter must be a mapping of keys")
	}
	if root.Style&yaml.FlowStyle != 0 {
		return nil, root, true, nil
	}
	lines := strings.SplitAfter(yamlText, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k := root.Content[i]
		blocks = append(blocks, keyBlock{key: k.Value, start: k.Line - 1})
	}
	for i := range blocks {
		end := len(lines)
		if i+1 < len(blocks) {
			end = blocks[i+1].start
		}
		for end > blocks[i].start+1 {
			t := strings.TrimSpace(lines[end-1])
			if t != "" && !strings.HasPrefix(t, "#") {
				break
			}
			end--
		}
		blocks[i].end = end
	}
	return blocks, root, false, nil
}

// replaceKey replaces (or adds, or with an empty replacement removes) a top-level key of the
// frontmatter. block is the new text of the key, ending in "\n". atStart adds a new key at the
// top instead of at the bottom.
func replaceKey(content, key, block string, atStart bool, node func(root *yaml.Node)) (string, error) {
	start, end, present, err := frontmatterParts(content)
	if err != nil {
		return "", err
	}
	if !present {
		if block == "" {
			return content, nil
		}
		return "---\n" + block + "---\n" + content, nil
	}
	yamlText := content[start:end]
	blocks, root, flow, err := topBlocks(yamlText)
	if err != nil {
		return "", err
	}
	if flow {
		node(root)
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		root.Style = 0
		if err := enc.Encode(root); err != nil {
			return "", err
		}
		_ = enc.Close()
		return content[:start] + buf.String() + content[end:], nil
	}
	lines := strings.SplitAfter(yamlText, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
		lines[n-1] += "\n"
	}
	var out []string
	found := false
	for _, b := range blocks {
		if b.key != key {
			continue
		}
		found = true
		out = append(append(append(out, lines[:b.start]...), block), lines[b.end:]...)
		break
	}
	if !found {
		if block == "" {
			return content, nil
		}
		if atStart {
			out = append([]string{block}, lines...)
		} else {
			out = append(lines, block)
		}
	}
	return content[:start] + strings.Join(out, "") + content[end:], nil
}

// SetVisibility writes `visibility: <v>` in the frontmatter, adding a frontmatter when the
// document has none. Every other line stays as it is.
func SetVisibility(content, visibility string) (string, error) {
	if !ValidVisibility(visibility) {
		return "", fmt.Errorf("unknown visibility %q", visibility)
	}
	return replaceKey(content, "visibility", "visibility: "+visibility+"\n", true, func(root *yaml.Node) {
		setMapKey(root, "visibility", &yaml.Node{Kind: yaml.ScalarNode, Value: visibility})
	})
}

// SetRelations rewrites the `jiku` block of the frontmatter with the given relations (one flow
// list per non-empty kind; no block at all when every kind is empty). The rest of the
// frontmatter and the body are not touched.
func SetRelations(content string, rel Relations) (string, error) {
	rel = rel.Normalize()
	var b strings.Builder
	jiku := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range RelationKinds {
		ids := rel.Get(k.Key)
		if len(ids) == 0 {
			continue
		}
		parts := make([]string, len(ids))
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for i, id := range ids {
			parts[i] = strconv.FormatInt(id, 10)
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: parts[i]})
		}
		fmt.Fprintf(&b, "  %s: [%s]\n", k.Key, strings.Join(parts, ", "))
		jiku.Content = append(jiku.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k.Key}, seq)
	}
	block := ""
	if b.Len() > 0 {
		block = "jiku:\n" + b.String()
	}
	return replaceKey(content, "jiku", block, false, func(root *yaml.Node) {
		if block == "" {
			deleteMapKey(root, "jiku")
			return
		}
		setMapKey(root, "jiku", jiku)
	})
}

func setMapKey(root *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			root.Content[i+1] = val
			return
		}
	}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, val)
}

func deleteMapKey(root *yaml.Node, key string) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			root.Content = append(root.Content[:i], root.Content[i+2:]...)
			return
		}
	}
}

// Slug turns a name into a kebab-case slug without accents, of at most max runes:
// "Videoconferencia Móvil" -> "videoconferencia-movil". It may be empty.
func Slug(s string, max int) string {
	return slug(s, max)
}
