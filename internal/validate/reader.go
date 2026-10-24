// Package validate decodes request payloads deny-by-default: an undeclared key, a wrong type or
// a value outside its rule is `invalid_fields` with `{ field, value, allowed }`, never ignored.
package validate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gravadigital/jiku-docs/internal/protocol"
)

// Object is a decoded JSON object whose keys are checked against a whitelist.
type Object struct {
	path    string
	fields  map[string]json.RawMessage
	allowed []string
	err     *protocol.Error
}

// ParseObject decodes raw as a JSON object and rejects any key outside allowed. An empty body is
// an empty object.
func ParseObject(raw json.RawMessage, path string, allowed ...string) *Object {
	o := &Object{path: path, allowed: allowed}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		o.fields = map[string]json.RawMessage{}
		return o
	}
	if trimmed[0] != '{' {
		o.err = protocol.InvalidField(orPayload(path), nil, nil, "Se esperaba un objeto JSON")
		return o
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &m); err != nil {
		o.err = protocol.InvalidField(orPayload(path), nil, nil, "JSON mal formado")
		return o
	}
	o.fields = m
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !contains(allowed, k) {
			o.err = protocol.InvalidField(orPayload(path), k, allowed,
				"El campo %q no existe en este pedido", join(path, k))
			return o
		}
	}
	return o
}

func orPayload(path string) string {
	if path == "" {
		return "payload"
	}
	return path
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Err returns the first error found.
func (o *Object) Err() error {
	if o.err == nil {
		return nil
	}
	return o.err
}

// Fail records an error if none was recorded yet.
func (o *Object) Fail(e *protocol.Error) {
	if o.err == nil {
		o.err = e
	}
}

// Has reports whether key is present (even with a null value).
func (o *Object) Has(key string) bool {
	_, ok := o.fields[key]
	return ok
}

// IsNull reports whether key is present with a JSON null.
func (o *Object) IsNull(key string) bool {
	v, ok := o.fields[key]
	return ok && bytes.Equal(bytes.TrimSpace(v), []byte("null"))
}

// Raw returns the raw value of key.
func (o *Object) Raw(key string) (json.RawMessage, bool) {
	v, ok := o.fields[key]
	return v, ok
}

// Keys returns the keys present.
func (o *Object) Keys() []string {
	keys := make([]string, 0, len(o.fields))
	for k := range o.fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Path is the dotted path of this object inside the payload.
func (o *Object) Path() string { return o.path }

func (o *Object) present(key string, required bool) (json.RawMessage, bool) {
	if o.err != nil {
		return nil, false
	}
	v, ok := o.fields[key]
	if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		if required {
			o.err = protocol.InvalidField(join(o.path, key), nil, nil, "Falta el campo obligatorio %q", join(o.path, key))
		}
		return nil, false
	}
	return v, true
}

func (o *Object) typeErr(key string, raw json.RawMessage, want string) {
	o.Fail(protocol.InvalidField(join(o.path, key), rawValue(raw), nil,
		"El campo %q tiene que ser %s", join(o.path, key), want))
}

func rawValue(raw json.RawMessage) any {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	if s, ok := v.(string); ok && len(s) > 200 {
		return s[:200] + "…"
	}
	return v
}

// StringRule checks a decoded string.
type StringRule func(s string) string

// MinLen requires at least n characters.
func MinLen(n int) StringRule {
	return func(s string) string {
		if utf8.RuneCountInString(s) < n {
			return fmt.Sprintf("tiene que tener al menos %d caracteres", n)
		}
		return ""
	}
}

// MaxLen allows at most n characters.
func MaxLen(n int) StringRule {
	return func(s string) string {
		if utf8.RuneCountInString(s) > n {
			return fmt.Sprintf("admite como máximo %d caracteres", n)
		}
		return ""
	}
}

// Pattern requires a regular expression match.
func Pattern(re *regexp.Regexp) StringRule {
	return func(s string) string {
		if !re.MatchString(s) {
			return "no tiene el formato esperado"
		}
		return ""
	}
}

// OneOf requires an enum value.
func OneOf(values ...string) StringRule {
	return func(s string) string {
		if !contains(values, s) {
			return "no es un valor permitido"
		}
		return ""
	}
}

// String reads a string field. Returns "" and ok=false when absent.
func (o *Object) String(key string, required bool, rules ...StringRule) (string, bool) {
	raw, ok := o.present(key, required)
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		o.typeErr(key, raw, "un texto")
		return "", false
	}
	for _, r := range rules {
		if msg := r(s); msg != "" {
			e := protocol.InvalidField(join(o.path, key), rawValue(raw), nil, "El campo %q %s", join(o.path, key), msg)
			o.Fail(e)
			return "", false
		}
	}
	return s, true
}

// Enum reads a string restricted to values, reporting `allowed` on failure.
func (o *Object) Enum(key string, required bool, values ...string) (string, bool) {
	raw, ok := o.present(key, required)
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || !contains(values, s) {
		o.Fail(protocol.InvalidField(join(o.path, key), rawValue(raw), values,
			"El campo %q no es un valor permitido", join(o.path, key)))
		return "", false
	}
	return s, true
}

var ulidRe = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// ULID reads an uppercase Crockford ULID.
func (o *Object) ULID(key string, required bool) (string, bool) {
	return o.String(key, required, Pattern(ulidRe))
}

// Bool reads a boolean, with a default when absent.
func (o *Object) Bool(key string, def bool) bool {
	raw, ok := o.present(key, false)
	if !ok {
		return def
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		o.typeErr(key, raw, "booleano")
		return def
	}
	return b
}

// OptBool reads an optional boolean, distinguishing absence.
func (o *Object) OptBool(key string) *bool {
	raw, ok := o.present(key, false)
	if !ok {
		return nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		o.typeErr(key, raw, "booleano")
		return nil
	}
	return &b
}

// Int reads an integer within [min, max].
func (o *Object) Int(key string, required bool, min, max int64) (int64, bool) {
	raw, ok := o.present(key, required)
	if !ok {
		return 0, false
	}
	n, ok := parseInt(raw)
	if !ok {
		o.typeErr(key, raw, "un entero")
		return 0, false
	}
	if n < min || n > max {
		o.Fail(protocol.InvalidField(join(o.path, key), n, nil,
			"El campo %q tiene que estar entre %d y %d", join(o.path, key), min, max))
		return 0, false
	}
	return n, true
}

func parseInt(raw json.RawMessage) (int64, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return 0, false
	}
	f, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	if n, err := f.Int64(); err == nil {
		return n, true
	}
	fl, err := f.Float64()
	if err != nil || fl != math.Trunc(fl) || math.Abs(fl) > 1<<53 {
		return 0, false
	}
	return int64(fl), true
}

// Time reads an RFC 3339 / ISO 8601 date-time.
func (o *Object) Time(key string, required bool) (time.Time, bool) {
	raw, ok := o.present(key, required)
	if !ok {
		return time.Time{}, false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		o.typeErr(key, raw, "una fecha ISO 8601")
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		o.typeErr(key, raw, "una fecha ISO 8601")
		return time.Time{}, false
	}
	return t.UTC(), true
}

// Object reads a nested object with its own whitelist.
func (o *Object) Object(key string, required bool, allowed ...string) (*Object, bool) {
	raw, ok := o.present(key, required)
	if !ok {
		return nil, false
	}
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '{' {
		o.typeErr(key, raw, "un objeto")
		return nil, false
	}
	child := ParseObject(raw, join(o.path, key), allowed...)
	if child.err != nil {
		o.Fail(child.err)
		return nil, false
	}
	return child, true
}

// Array reads a JSON array as raw elements, with a maximum length.
func (o *Object) Array(key string, required bool, maxItems int) ([]json.RawMessage, bool) {
	raw, ok := o.present(key, required)
	if !ok {
		return nil, false
	}
	var arr []json.RawMessage
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '[' {
		o.typeErr(key, raw, "una lista")
		return nil, false
	}
	if err := json.Unmarshal(raw, &arr); err != nil {
		o.typeErr(key, raw, "una lista")
		return nil, false
	}
	if maxItems > 0 && len(arr) > maxItems {
		o.Fail(protocol.InvalidField(join(o.path, key), nil, nil,
			"El campo %q admite como máximo %d elementos", join(o.path, key), maxItems))
		return nil, false
	}
	return arr, true
}

// StringList reads a list of strings, each checked against allowed when it is non-nil.
func (o *Object) StringList(key string, allowed []string, maxItems int) ([]string, bool) {
	arr, ok := o.Array(key, false, maxItems)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, el := range arr {
		var s string
		if err := json.Unmarshal(el, &s); err != nil {
			o.Fail(protocol.InvalidField(join(o.path, key), rawValue(el), allowed,
				"El campo %q tiene que ser una lista de textos", join(o.path, key)))
			return nil, false
		}
		if allowed != nil && !contains(allowed, s) {
			o.Fail(protocol.InvalidField(join(o.path, key), s, allowed,
				"%q no es un valor permitido en %q", s, join(o.path, key)))
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// Elem helpers decode a single raw element.

// ElemString decodes a raw string element.
func ElemString(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// ElemInt decodes a raw integer element.
func ElemInt(raw json.RawMessage) (int64, bool) { return parseInt(raw) }

// IsArray reports whether a raw value is a JSON array.
func IsArray(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '['
}

// IsObject reports whether a raw value is a JSON object.
func IsObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}

// Value returns the decoded value of raw, for error details.
func Value(raw json.RawMessage) any { return rawValue(raw) }

// Join builds a dotted path.
func Join(path, key string) string { return join(path, key) }

// Quote is a helper for messages listing names.
func Quote(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(q, ", ")
}
