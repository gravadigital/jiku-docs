package query

// FilterDescription is one filterable name in `meta.describe`.
type FilterDescription struct {
	Kind     string `json:"kind"`
	Enum     string `json:"enum,omitempty"`
	List     bool   `json:"list,omitempty"`
	Range    bool   `json:"range,omitempty"`
	Prefix   bool   `json:"prefix,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// EnumValue is one value of an enum; the label falls back to the value, like core.
type EnumValue struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Description is the shape core's `meta.describe` returns per resource.
type Description struct {
	Base       map[string]FieldSpec         `json:"base"`
	Includable map[string]FieldSpec         `json:"includable"`
	Filterable map[string]FilterDescription `json:"filterable"`
	Sortable   []string                     `json:"sortable"`
	Defaults   Defaults                     `json:"defaults"`
	Enums      map[string][]EnumValue       `json:"enums"`
}

// Defaults of a resource.
type Defaults struct {
	Sort     []string `json:"sort"`
	Limit    int      `json:"limit"`
	MaxLimit int      `json:"maxLimit"`
}

// Labels of enum values shown by the interfaces.
var enumLabels = map[string]string{
	"internal": "Interno", "public": "Visible para el cliente", "restricted": "Restringido",
	"human": "Persona", "agent": "Agente", "service": "Servicio",
	"read": "Lectura", "write": "Escritura", "manage": "Administración",
	"latest": "Última versión", "pinned": "Versión fija",
	"active": "Activo", "revoked": "Revocado", "expired": "Vencido",
	"role": "Rol", "user": "Usuario",
	"create": "Creación", "update": "Edición", "restore": "Restauración",
}

// Describe projects a spec. It reads the very structures the validator reads.
func Describe(s *Spec) Description {
	d := Description{
		Base: map[string]FieldSpec{}, Includable: map[string]FieldSpec{},
		Filterable: map[string]FilterDescription{}, Sortable: append([]string{}, s.Sortable...),
		Enums: map[string][]EnumValue{},
	}
	for _, f := range s.Fields {
		d.Base[f] = FieldSpec{Kind: "field"}
	}
	for _, name := range s.Includable {
		d.Includable[name] = s.IncludableSpecs[name]
	}
	for _, name := range s.Filterable {
		fs := s.FilterableSpecs[name]
		kind := fs.Kind
		if fs.Enum != "" {
			kind = KindEnum
		}
		d.Filterable[name] = FilterDescription{Kind: kind, Enum: fs.Enum, List: fs.List, Range: fs.Range, Prefix: fs.Prefix, Required: fs.Required}
	}
	limit, max := DefaultLimit, MaxLimit
	if !s.Has("page") {
		limit, max = 0, 0
	}
	d.Defaults = Defaults{Sort: append([]string{}, s.DefaultSort...), Limit: limit, MaxLimit: max}
	if d.Defaults.Sort == nil {
		d.Defaults.Sort = []string{}
	}
	if d.Sortable == nil {
		d.Sortable = []string{}
	}
	for name, values := range s.Enums {
		ev := make([]EnumValue, len(values))
		for i, v := range values {
			label := enumLabels[v]
			if label == "" {
				label = v
			}
			ev[i] = EnumValue{Value: v, Label: label}
		}
		d.Enums[name] = ev
	}
	return d
}
