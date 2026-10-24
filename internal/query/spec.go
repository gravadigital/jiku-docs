// Package query is the read grammar shared with core's `jiku-queries`: `filter` (scalar
// equality, list IN, `{gte, lte}` ranges, one level of `or`), `sort` (`-` for descending, `id`
// always appended), keyset `page`, `fields`, `include` and `count`. Each resource declares its
// whitelists once, in a Spec, and both the validator and `meta.describe` read that same Spec —
// so the description cannot go stale.
package query

// Filter kinds, as `meta.describe` names them (same vocabulary as core).
const (
	KindString  = "string"
	KindInteger = "integer"
	KindEnum    = "enum"
	KindDate    = "date"
	KindBoolean = "boolean"
)

// FilterSpec declares one filterable name.
type FilterSpec struct {
	Kind string
	// Enum names the enum of the spec whose values it accepts.
	Enum string
	// List accepts a list (IN) besides a scalar.
	List bool
	// Range is a `{gte, lte}` object (dates).
	Range bool
	// Prefix: the value is a prefix match (pathPrefix, fileName, prefix).
	Prefix bool
	// ULID: string values must be ULIDs.
	ULID bool
	// Required in the filter.
	Required bool
	// MaxItems of a list.
	MaxItems int
}

// FieldSpec declares a returned field (base or includable).
type FieldSpec struct {
	Kind        string   `json:"kind"` // field | computed | relation
	Cardinality string   `json:"cardinality,omitempty"`
	Fields      []string `json:"fields,omitempty"`
	Optional    bool     `json:"optional,omitempty"`
}

// Spec is the five whitelists of a resource.
type Spec struct {
	Name string
	// Base is the default returned set, in order.
	Base []string
	// Fields is every name `fields` may ask for (base plus the optional ones).
	Fields []string
	// Includable names, in order, with their description.
	Includable      []string
	IncludableSpecs map[string]FieldSpec
	// Filterable names, in order.
	Filterable      []string
	FilterableSpecs map[string]FilterSpec
	// AllowOr enables one level of `filter.or`.
	AllowOr bool
	// Sortable names, in order; DefaultSort when `sort` is absent.
	Sortable    []string
	DefaultSort []string
	Enums       map[string][]string
	// Levers are the top-level keys a list query accepts (filter, sort, page, fields, include, count).
	Levers []string
	// Key is the unique key appended as the last sort criterion ("id" by default).
	Key string
}

// Has reports whether the spec accepts a lever.
func (s *Spec) Has(lever string) bool { return contains(s.Levers, lever) }

// KeyName is the tie-breaker key of the keyset.
func (s *Spec) KeyName() string {
	if s.Key == "" {
		return "id"
	}
	return s.Key
}

// Paging limits, identical to core.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
