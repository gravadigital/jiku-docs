package markdown

import (
	"regexp"
	"strings"
)

// Chunk is one indexable fragment of a document.
type Chunk struct {
	HeadingPath []string
	Body        string
}

// MaxChunkWords is where a section is cut by paragraphs.
const MaxChunkWords = 800

var atxRe = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.*?)[ \t]*#*[ \t]*$`)

type section struct {
	path  []string
	lines []string
}

// Chunks splits a body (without frontmatter) by headings; a section longer than ~800 words is
// cut by paragraphs. Each chunk carries the headings that contain it.
func Chunks(body string) []Chunk {
	var sections []section
	cur := section{}
	var stack []struct {
		level int
		text  string
	}
	fence := ""
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, "\r")
		if fence != "" {
			if m := fenceOpenRe.FindStringSubmatch(line); m != nil && strings.HasPrefix(m[1], fence[:1]) && len(m[1]) >= len(fence) {
				fence = ""
			}
			cur.lines = append(cur.lines, line)
			continue
		}
		if m := fenceOpenRe.FindStringSubmatch(line); m != nil {
			fence = m[1]
			cur.lines = append(cur.lines, line)
			continue
		}
		if m := atxRe.FindStringSubmatch(line); m != nil {
			sections = append(sections, cur)
			level := len(m[1])
			for len(stack) > 0 && stack[len(stack)-1].level >= level {
				stack = stack[:len(stack)-1]
			}
			stack = append(stack, struct {
				level int
				text  string
			}{level, strings.TrimSpace(m[2])})
			path := make([]string, len(stack))
			for i, s := range stack {
				path[i] = s.text
			}
			cur = section{path: path}
			continue
		}
		cur.lines = append(cur.lines, line)
	}
	sections = append(sections, cur)

	var out []Chunk
	for _, s := range sections {
		text := strings.TrimSpace(strings.Join(s.lines, "\n"))
		path := s.path
		if path == nil {
			path = []string{}
		}
		if text == "" {
			if len(path) > 0 {
				// A heading with no text still makes the heading searchable.
				out = append(out, Chunk{HeadingPath: path, Body: ""})
			}
			continue
		}
		for _, part := range splitByWords(text, MaxChunkWords) {
			out = append(out, Chunk{HeadingPath: path, Body: part})
		}
	}
	return out
}

func splitByWords(text string, max int) []string {
	if len(strings.Fields(text)) <= max {
		return []string{text}
	}
	paragraphs := regexp.MustCompile(`\n[ \t]*\n`).Split(text, -1)
	var out []string
	var cur []string
	words := 0
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.TrimSpace(strings.Join(cur, "\n\n")))
			cur = nil
			words = 0
		}
	}
	for _, p := range paragraphs {
		n := len(strings.Fields(p))
		if n > max {
			flush()
			fields := strings.Fields(p)
			for i := 0; i < len(fields); i += max {
				end := i + max
				if end > len(fields) {
					end = len(fields)
				}
				out = append(out, strings.Join(fields[i:end], " "))
			}
			continue
		}
		if words+n > max {
			flush()
		}
		cur = append(cur, p)
		words += n
	}
	flush()
	return out
}
