package query

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/validate"
)

// Cond is one parsed filter condition.
type Cond struct {
	Name string
	// Strings / Ints hold scalar-or-list values (one element for a scalar).
	Strings []string
	Ints    []int64
	Bool    *bool
	Gte     *time.Time
	Lte     *time.Time
}

// Filter is the parsed `filter`: conditions ANDed, plus at most one level of `or`.
type Filter struct {
	Conds map[string]Cond
	Or    []map[string]Cond
	// Raw is the decoded filter, for the cursor fingerprint.
	Raw any
}

// Get returns a top-level condition.
func (f *Filter) Get(name string) (Cond, bool) {
	if f == nil {
		return Cond{}, false
	}
	c, ok := f.Conds[name]
	return c, ok
}

// SortKey is one sort criterion.
type SortKey struct {
	Name string
	Desc bool
}

// List is a parsed list query.
type List struct {
	Filter  *Filter
	Sort    []SortKey // always ends with id
	SortRaw []string
	Limit   int
	Cursor  []any // decoded keys, nil on the first page
	Fields  []string
	Include []string
	Count   string // "", "true", "only"
}

// ParseFilter parses a filter object against the spec.
func ParseFilter(spec *Spec, o *validate.Object, key string) (*Filter, error) {
	raw, ok := o.Raw(key)
	if !ok || o.IsNull(key) {
		f := &Filter{Conds: map[string]Cond{}, Raw: map[string]any{}}
		for _, name := range spec.Filterable {
			if spec.FilterableSpecs[name].Required {
				return nil, protocol.InvalidField(validate.Join(o.Path(), key)+"."+name, nil, nil,
					"The required filter %q is missing", key+"."+name)
			}
		}
		return f, nil
	}
	if !validate.IsObject(raw) {
		return nil, protocol.InvalidField(validate.Join(o.Path(), key), validate.Value(raw), nil, "The filter must be an object")
	}
	allowed := spec.Filterable
	if spec.AllowOr {
		allowed = append(append([]string{}, spec.Filterable...), "or")
	}
	path := validate.Join(o.Path(), key)
	conds, orRaw, err := parseConds(spec, raw, path, allowed)
	if err != nil {
		return nil, err
	}
	f := &Filter{Conds: conds}
	if orRaw != nil {
		var items []json.RawMessage
		if !validate.IsArray(orRaw) || json.Unmarshal(orRaw, &items) != nil {
			return nil, protocol.InvalidField(path+".or", validate.Value(orRaw), nil, "filter.or must be a list of filters")
		}
		if len(items) > 10 {
			return nil, protocol.InvalidField(path+".or", nil, nil, "filter.or allows at most 10 items")
		}
		for i, item := range items {
			if !validate.IsObject(item) {
				return nil, protocol.InvalidField(path+".or", validate.Value(item), nil, "Each item of filter.or must be an object")
			}
			c, nested, err := parseConds(spec, item, path+".or["+itoa(i)+"]", spec.Filterable)
			if err != nil {
				return nil, err
			}
			if nested != nil {
				return nil, protocol.InvalidField(path+".or", "or", spec.Filterable, "filter.or allows a single level")
			}
			f.Or = append(f.Or, c)
		}
	}
	for _, name := range spec.Filterable {
		if spec.FilterableSpecs[name].Required {
			if _, ok := conds[name]; !ok {
				return nil, protocol.InvalidField(path+"."+name, nil, nil, "The required filter %q is missing", key+"."+name)
			}
		}
	}
	var decoded any
	_ = json.Unmarshal(raw, &decoded)
	f.Raw = decoded
	return f, nil
}

func itoa(i int) string { return strconv.Itoa(i) }

func parseConds(spec *Spec, raw json.RawMessage, path string, allowed []string) (map[string]Cond, json.RawMessage, error) {
	obj := validate.ParseObject(raw, path, allowed...)
	if err := obj.Err(); err != nil {
		return nil, nil, err
	}
	conds := map[string]Cond{}
	var orRaw json.RawMessage
	for _, name := range obj.Keys() {
		v, _ := obj.Raw(name)
		if name == "or" {
			orRaw = v
			continue
		}
		fs := spec.FilterableSpecs[name]
		c, err := parseCond(spec, fs, name, path+"."+name, v)
		if err != nil {
			return nil, nil, err
		}
		conds[name] = c
	}
	return conds, orRaw, nil
}

func parseCond(spec *Spec, fs FilterSpec, name, path string, v json.RawMessage) (Cond, error) {
	c := Cond{Name: name}
	bad := func(msg string) error {
		var allowed []string
		if fs.Enum != "" {
			allowed = spec.Enums[fs.Enum]
		}
		return protocol.InvalidField(path, validate.Value(v), allowed, "The filter %q %s", path, msg)
	}
	if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		return c, bad("does not accept null")
	}
	if fs.Range {
		if !validate.IsObject(v) {
			return c, bad("must be a range { gte, lte }")
		}
		o := validate.ParseObject(v, path, "gte", "lte")
		if gte, ok := o.Time("gte", false); ok {
			c.Gte = &gte
		}
		if lte, ok := o.Time("lte", false); ok {
			c.Lte = &lte
		}
		if err := o.Err(); err != nil {
			return c, err
		}
		if c.Gte == nil && c.Lte == nil {
			return c, bad("must have gte or lte")
		}
		return c, nil
	}
	if fs.Kind == KindBoolean {
		var b bool
		if err := json.Unmarshal(v, &b); err != nil {
			return c, bad("must be a boolean")
		}
		c.Bool = &b
		return c, nil
	}
	var elems []json.RawMessage
	if validate.IsArray(v) {
		if !fs.List {
			return c, bad("does not accept a list")
		}
		if err := json.Unmarshal(v, &elems); err != nil {
			return c, bad("is a malformed list")
		}
		max := fs.MaxItems
		if max == 0 {
			max = 200
		}
		if len(elems) == 0 {
			return c, bad("does not accept an empty list")
		}
		if len(elems) > max {
			return c, bad("allows at most 200 values")
		}
	} else {
		elems = []json.RawMessage{v}
	}
	for _, el := range elems {
		switch fs.Kind {
		case KindInteger:
			n, ok := validate.ElemInt(el)
			if !ok {
				return c, bad("must be an integer or a list of integers")
			}
			c.Ints = append(c.Ints, n)
		default:
			s, ok := validate.ElemString(el)
			if !ok {
				return c, bad("must be a string or a list of strings")
			}
			if fs.ULID && !protocol.IsULID(s) {
				return c, bad("must be a ULID")
			}
			if fs.Enum != "" && !contains(spec.Enums[fs.Enum], s) {
				return c, bad("is not an allowed value")
			}
			c.Strings = append(c.Strings, s)
		}
	}
	return c, nil
}

// ParseSort parses `sort` against the sortable names; `id` is always appended.
func ParseSort(spec *Spec, o *validate.Object) ([]SortKey, []string, error) {
	path := validate.Join(o.Path(), "sort")
	raw, ok := o.Raw("sort")
	if !spec.Has("sort") {
		ok = false
	}
	var names []string
	if ok && !o.IsNull("sort") {
		var list []json.RawMessage
		if !validate.IsArray(raw) || json.Unmarshal(raw, &list) != nil {
			return nil, nil, protocol.InvalidField(path, validate.Value(raw), sortAllowed(spec), "sort must be a list")
		}
		for _, el := range list {
			s, ok := validate.ElemString(el)
			if !ok || !contains(sortAllowed(spec), s) {
				return nil, nil, protocol.InvalidField(path, validate.Value(el), sortAllowed(spec), "%v is not an allowed sort", validate.Value(el))
			}
			names = append(names, s)
		}
	}
	if len(names) == 0 {
		names = spec.DefaultSort
	}
	var keys []SortKey
	seen := map[string]bool{}
	for _, n := range names {
		k := SortKey{Name: strings.TrimPrefix(n, "-"), Desc: strings.HasPrefix(n, "-")}
		if seen[k.Name] {
			return nil, nil, protocol.InvalidField(path, n, sortAllowed(spec), "The sort %q is repeated", k.Name)
		}
		seen[k.Name] = true
		keys = append(keys, k)
	}
	if key := spec.KeyName(); !seen[key] {
		keys = append(keys, SortKey{Name: key})
	}
	return keys, names, nil
}

func sortAllowed(spec *Spec) []string {
	out := make([]string, 0, 2*len(spec.Sortable))
	for _, s := range spec.Sortable {
		out = append(out, s, "-"+s)
	}
	return out
}

// ParsePage parses `page`: the limit is clamped to MaxLimit silently, like core.
func ParsePage(o *validate.Object, def int) (int, string, error) {
	limit := def
	if !o.Has("page") || o.IsNull("page") {
		return limit, "", nil
	}
	p, ok := o.Object("page", false, "limit", "cursor")
	if !ok {
		return 0, "", o.Err()
	}
	if n, ok := p.Int("limit", false, 1, 1<<31); ok {
		limit = int(n)
	}
	cursor := ""
	if p.Has("cursor") && !p.IsNull("cursor") {
		cursor, _ = p.String("cursor", false)
	}
	if err := p.Err(); err != nil {
		return 0, "", err
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	return limit, cursor, nil
}

// ParseList parses the levers of a list query (filter, sort, page, fields, include, count) that
// the spec declares. extra lists other top-level keys the handler reads itself.
func ParseList(spec *Spec, raw json.RawMessage, extra ...string) (*List, *validate.Object, error) {
	keys := append(append([]string{}, spec.Levers...), extra...)
	o := validate.ParseObject(raw, "", keys...)
	if err := o.Err(); err != nil {
		return nil, nil, err
	}
	q := &List{Limit: DefaultLimit}
	var err error
	if q.Filter, err = ParseFilter(spec, o, "filter"); err != nil {
		return nil, nil, err
	}
	if q.Sort, q.SortRaw, err = ParseSort(spec, o); err != nil {
		return nil, nil, err
	}
	var fields, include []string
	if spec.Has("fields") {
		fields, _ = o.StringList("fields", spec.Fields, 0)
	}
	if spec.Has("include") {
		include, _ = o.StringList("include", spec.Includable, 0)
	}
	if err := o.Err(); err != nil {
		return nil, nil, err
	}
	if len(fields) == 0 {
		fields = spec.Base
	}
	if contains(spec.Fields, spec.KeyName()) {
		fields = withKey(fields, spec.KeyName())
	}
	q.Fields = fields
	q.Include = include
	if spec.Has("count") && o.Has("count") && !o.IsNull("count") {
		v, _ := o.Raw("count")
		var b bool
		var s string
		switch {
		case json.Unmarshal(v, &b) == nil:
			if b {
				q.Count = "true"
			}
		case json.Unmarshal(v, &s) == nil && s == "only":
			q.Count = "only"
		default:
			return nil, nil, protocol.InvalidField("count", validate.Value(v), []string{"true", "false", "only"}, "count accepts true, false or \"only\"")
		}
	}
	if spec.Has("page") {
		limit, cursor, err := ParsePage(o, DefaultLimit)
		if err != nil {
			return nil, nil, err
		}
		q.Limit = limit
		if cursor != "" {
			keys, err := DecodeCursor(cursor, q.Filter.Raw, q.SortRaw, len(q.Sort))
			if err != nil {
				return nil, nil, err
			}
			q.Cursor = keys
		}
	}
	return q, o, nil
}

func withKey(fields []string, key string) []string {
	if contains(fields, key) {
		return fields
	}
	return append([]string{key}, fields...)
}

// ---- cursor ---------------------------------------------------------------------------------

type cursorBody struct {
	V int    `json:"v"`
	K []any  `json:"k"`
	H string `json:"h"`
}

const cursorVersion = 1

func normalize(v any) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, el := range x {
			out[i] = normalize(el)
		}
		sort.Slice(out, func(i, j int) bool {
			a, _ := json.Marshal(out[i])
			b, _ := json.Marshal(out[j])
			return string(a) < string(b)
		})
		return out
	case map[string]any:
		out := map[string]any{}
		for k, el := range x {
			out[k] = normalize(el)
		}
		return out
	}
	return v
}

// Fingerprint ties a cursor to its filter and sort: reordering the keys of the same filter does
// not invalidate it; changing the filter or the sort does.
func Fingerprint(filter any, sortRaw []string) string {
	if filter == nil {
		filter = map[string]any{}
	}
	b, _ := json.Marshal(map[string]any{"filter": normalize(filter), "sort": sortRaw})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// EncodeCursor builds the opaque cursor of the last returned row.
func EncodeCursor(keys []any, filter any, sortRaw []string) string {
	out := make([]any, len(keys))
	for i, k := range keys {
		if t, ok := k.(time.Time); ok {
			out[i] = t.UTC().Format(time.RFC3339Nano)
		} else {
			out[i] = k
		}
	}
	b, _ := json.Marshal(cursorBody{V: cursorVersion, K: out, H: Fingerprint(filter, sortRaw)})
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor validates and decodes a cursor.
func DecodeCursor(cursor string, filter any, sortRaw []string, n int) ([]any, error) {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, protocol.E(protocol.CodeInvalidCursor)
	}
	var body cursorBody
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil || body.V != cursorVersion || len(body.K) != n {
		return nil, protocol.E(protocol.CodeInvalidCursor)
	}
	if body.H != Fingerprint(filter, sortRaw) {
		return nil, protocol.E(protocol.CodeInvalidCursor)
	}
	return body.K, nil
}
