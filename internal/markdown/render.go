package markdown

import (
	"bytes"
	"net/url"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// attachmentPathRe matches the stable attachment link, absolute or relative:
// `{JIKU_WEB_URL}/docs/attachments/{id}` or `/docs/attachments/{id}`.
var attachmentPathRe = regexp.MustCompile(`(?:^|/)docs/attachments/([0-9A-HJKMNP-TV-Z]{26})/?$`)

// AttachmentID returns the attachment id a link points to, if it is a jiku-docs attachment link.
func AttachmentID(dest string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(dest))
	if err != nil {
		return "", false
	}
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	if u.Scheme == "" && !strings.HasPrefix(u.Path, "/") {
		return "", false
	}
	m := attachmentPathRe.FindStringSubmatch(u.Path)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// ShareRender configures how a document is rendered for a shared link.
type ShareRender struct {
	// AttachmentURL returns the public URL of an attachment of the shared document, or "" when
	// it must not be served (not of this document, not ready, or attachments excluded).
	AttachmentURL func(id string) string
}

var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

var policy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.RequireNoReferrerOnLinks(true)
	p.RequireNoFollowOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^language-[\w+-]+$`)).OnElements("code")
	p.AllowAttrs("checked", "disabled").OnElements("input")
	p.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	return p
}()

// isDocumentLink reports a link that points to another document of the tree: a relative `.md`
// path, or a jiku-docs document route. Those are shown as plain text in a shared page.
func isDocumentLink(dest string) bool {
	u, err := url.Parse(strings.TrimSpace(dest))
	if err != nil {
		return true
	}
	if u.Scheme == "" && u.Host == "" {
		if u.Path == "" {
			return false // in-page anchor (#section)
		}
		return true // any relative path is a reference into the tree
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return strings.Contains(u.Path, "/docs/documents/") || strings.HasSuffix(strings.ToLower(u.Path), ".md")
}

// RenderShare renders the body of a document (frontmatter removed) to sanitised HTML for a
// shared link: attachment links point to the public route when allowed, links to other
// documents become text.
func RenderShare(content string, opts ShareRender) (string, error) {
	_, body, present, ferr := Split(content)
	if ferr != nil || !present {
		body = content
		if ferr != nil {
			body = stripBrokenFrontmatter(content)
		}
	}
	src := []byte(body)
	doc := md.Parser().Parse(text.NewReader(src))

	type change struct {
		node ast.Node
		dest string // "" = unwrap the node into its children
	}
	var changes []change
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.Link:
			dest := string(v.Destination)
			if id, ok := AttachmentID(dest); ok {
				changes = append(changes, change{n, opts.attachment(id)})
			} else if isDocumentLink(dest) {
				changes = append(changes, change{n, ""})
			}
		case *ast.Image:
			dest := string(v.Destination)
			if id, ok := AttachmentID(dest); ok {
				changes = append(changes, change{n, opts.attachment(id)})
			} else if isDocumentLink(dest) {
				changes = append(changes, change{n, ""})
			}
		case *ast.AutoLink:
			if _, ok := AttachmentID(string(v.URL(src))); ok {
				changes = append(changes, change{n, ""})
			}
		}
		return ast.WalkContinue, nil
	})
	for _, c := range changes {
		if c.dest != "" {
			switch v := c.node.(type) {
			case *ast.Link:
				v.Destination = []byte(c.dest)
				continue
			case *ast.Image:
				v.Destination = []byte(c.dest)
				continue
			}
		}
		unwrap(c.node, src)
	}

	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		return "", err
	}
	return policy.Sanitize(buf.String()), nil
}

func (o ShareRender) attachment(id string) string {
	if o.AttachmentURL == nil {
		return ""
	}
	return o.AttachmentURL(id)
}

// unwrap replaces a node by its children (the text of a link, the alt text of an image).
func unwrap(n ast.Node, src []byte) {
	parent := n.Parent()
	if parent == nil {
		return
	}
	if al, ok := n.(*ast.AutoLink); ok {
		parent.ReplaceChild(parent, n, ast.NewString(al.Label(src)))
		return
	}
	for c := n.FirstChild(); c != nil; {
		next := c.NextSibling()
		parent.InsertBefore(parent, n, c)
		c = next
	}
	parent.RemoveChild(parent, n)
}

// stripBrokenFrontmatter drops a leading `---` block that does not parse, so a broken
// frontmatter never leaks into a public page.
func stripBrokenFrontmatter(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return content
	}
	for i := 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "---" || t == "..." {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return ""
}

// StripFrontmatter returns the body of a document.
func StripFrontmatter(content string) string {
	_, body, present, ferr := Split(content)
	if ferr != nil {
		return stripBrokenFrontmatter(content)
	}
	if !present {
		return content
	}
	return body
}
