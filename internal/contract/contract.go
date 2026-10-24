// Package contract validates messages against the AsyncAPI contracts of docs/apis/, which are the
// source of truth of every shape. The integration tests validate every request, reply, event and
// notification they see; the contract tests compare the resource sheets with the YAML.
package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/gravadigital/jiku-docs/internal/protocol"
)

// Doc is one AsyncAPI document.
type Doc struct {
	Name     string
	Raw      map[string]any
	compiler *jsonschema.Compiler
	url      string
	mu       sync.Mutex
	cache    map[string]*jsonschema.Schema
}

// Channel is a request/reply channel of a plane.
type Channel struct {
	Name      string
	Pattern   string // method pattern, e.g. documents.{documentId}.update
	Request   string // schema ref
	Reply     string // schema ref
	Operation map[string]any
}

// Validator holds the three contracts of jiku-docs plus the module registry.
type Validator struct {
	Commands *Doc
	Queries  *Doc
	Events   *Doc
	Modules  *Doc
}

// RepoRoot walks up from the working directory to the directory holding go.mod.
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found")
		}
		dir = parent
	}
}

// Load reads docs/apis from the repository root.
func Load(root string) (*Validator, error) {
	v := &Validator{}
	for name, dst := range map[string]**Doc{
		"jiku-docs-commands.yaml": &v.Commands, "jiku-docs-queries.yaml": &v.Queries,
		"jiku-docs-events.yaml": &v.Events, "jiku-modules.yaml": &v.Modules,
	} {
		d, err := loadDoc(filepath.Join(root, "docs", "apis", name))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		*dst = d
	}
	return v, nil
}

func toJSONCompatible(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, el := range x {
			out[k] = toJSONCompatible(el)
		}
		return out
	case map[any]any:
		out := map[string]any{}
		for k, el := range x {
			out[fmt.Sprint(k)] = toJSONCompatible(el)
		}
		return out
	case []any:
		for i := range x {
			x[i] = toJSONCompatible(x[i])
		}
		return x
	case int:
		return json.Number(fmt.Sprint(x))
	case float64:
		return json.Number(fmt.Sprint(x))
	}
	return v
}

func loadDoc(path string) (*Doc, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	raw = toJSONCompatible(raw).(map[string]any)
	// Round-trip through JSON so the schema library sees plain JSON values.
	j, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(j))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	url := "file:///" + filepath.Base(path) + ".json"
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	return &Doc{Name: filepath.Base(path), Raw: raw, compiler: c, url: url, cache: map[string]*jsonschema.Schema{}}, nil
}

// Schema compiles a `#/components/schemas/X` ref.
func (d *Doc) Schema(ref string) (*jsonschema.Schema, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s, ok := d.cache[ref]; ok {
		return s, nil
	}
	s, err := d.compiler.Compile(d.url + ref)
	if err != nil {
		return nil, err
	}
	d.cache[ref] = s
	return s, nil
}

// Validate checks a JSON document against a schema ref.
func (d *Doc) Validate(ref string, data []byte) error {
	s, err := d.Schema(ref)
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	if err := s.Validate(inst); err != nil {
		return fmt.Errorf("%s does not match %s %s: %w", truncate(data), d.Name, ref, err)
	}
	return nil
}

func truncate(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "…"
	}
	return string(b)
}

func get(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

func refOf(v any) string {
	if m, ok := v.(map[string]any); ok {
		if r, ok := m["$ref"].(string); ok {
			return r
		}
	}
	return ""
}

// resolveMessage returns the payload schema ref of a channel message entry.
func (d *Doc) resolveMessage(entry any) string {
	if r := refOf(entry); strings.HasPrefix(r, "#/components/messages/") {
		name := strings.TrimPrefix(r, "#/components/messages/")
		return refOf(get(d.Raw, "components", "messages", name, "payload"))
	}
	if m, ok := entry.(map[string]any); ok {
		return refOf(m["payload"])
	}
	return ""
}

// Channels lists the request/reply channels of a plane document.
func (d *Doc) Channels() []Channel {
	chans, _ := d.Raw["channels"].(map[string]any)
	ops, _ := d.Raw["operations"].(map[string]any)
	var out []Channel
	for name, raw := range chans {
		ch, _ := raw.(map[string]any)
		addr, _ := ch["address"].(string)
		i := strings.Index(addr, ".v1.")
		if i < 0 {
			continue
		}
		c := Channel{Name: name, Pattern: addr[i+4:]}
		c.Request = d.resolveMessage(get(ch, "messages", "request"))
		c.Reply = d.resolveMessage(get(ch, "messages", "reply"))
		for _, op := range ops {
			om, _ := op.(map[string]any)
			if refOf(get(om, "channel")) == "#/channels/"+name {
				c.Operation = om
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pattern < out[j].Pattern })
	return out
}

// ChannelFor finds the channel of a concrete method.
func (d *Doc) ChannelFor(method string) (Channel, bool) {
	for _, c := range d.Channels() {
		if _, ok := protocol.MatchPattern(c.Pattern, method); ok {
			return c, true
		}
	}
	return Channel{}, false
}

func (v *Validator) plane(plane string) *Doc {
	if plane == protocol.PlaneCommands {
		return v.Commands
	}
	return v.Queries
}

// ValidateRequest checks a request payload of a method.
func (v *Validator) ValidateRequest(plane, method string, payload []byte) error {
	d := v.plane(plane)
	c, ok := d.ChannelFor(method)
	if !ok {
		return fmt.Errorf("method %s is not in %s", method, d.Name)
	}
	return d.Validate(c.Request, payload)
}

// ValidateReply checks a reply of a method.
func (v *Validator) ValidateReply(plane, method string, reply []byte) error {
	d := v.plane(plane)
	c, ok := d.ChannelFor(method)
	if !ok {
		return fmt.Errorf("method %s is not in %s", method, d.Name)
	}
	return d.Validate(c.Reply, reply)
}

// ValidateEvent checks an event of JIKU_DOCS_EVENTS by its type.
func (v *Validator) ValidateEvent(eventType string, data []byte) error {
	var ref string
	switch {
	case strings.HasPrefix(eventType, "document."):
		ref = "#/components/schemas/DocumentEventEnvelope"
	case strings.HasPrefix(eventType, "folder."):
		ref = "#/components/schemas/FolderEventEnvelope"
	case strings.HasPrefix(eventType, "attachment."):
		ref = "#/components/schemas/AttachmentEventEnvelope"
	case strings.HasPrefix(eventType, "share."):
		ref = "#/components/schemas/ShareEventEnvelope"
	default:
		return fmt.Errorf("unknown event type %s", eventType)
	}
	return v.Events.Validate(ref, data)
}

// ValidateNotification checks a docs-notify message by its type.
func (v *Validator) ValidateNotification(notifyType string, data []byte) error {
	var ref string
	switch {
	case notifyType == "access.changed":
		ref = "#/components/schemas/AccessChangedEnvelope"
	case strings.HasPrefix(notifyType, "document."):
		ref = "#/components/schemas/DocumentNotificationEnvelope"
	case strings.HasPrefix(notifyType, "attachment."):
		ref = "#/components/schemas/AttachmentEventEnvelope"
	default:
		return fmt.Errorf("type %s must not be notified to persons", notifyType)
	}
	return v.Events.Validate(ref, data)
}

// ValidateModuleRecord checks the value of the JIKU_MODULES key.
func (v *Validator) ValidateModuleRecord(data []byte) error {
	return v.Modules.Validate("#/components/schemas/ModuleRecord", data)
}
