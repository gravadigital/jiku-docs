package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/query"
)

// sortCol maps a sort name to its SQL expression and value type.
type sortCol struct {
	expr string
	kind string // time | string | int
}

func orderBy(keys []query.SortKey, cols map[string]sortCol) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		dir := "ASC"
		if k.Desc {
			dir = "DESC"
		}
		parts[i] = cols[k.Name].expr + " " + dir
	}
	return strings.Join(parts, ", ")
}

func cursorValue(kind string, v any) (any, error) {
	switch kind {
	case "time":
		s, ok := v.(string)
		if !ok {
			return nil, protocol.E(protocol.CodeInvalidCursor)
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return nil, protocol.E(protocol.CodeInvalidCursor)
		}
		return t, nil
	case "int":
		switch n := v.(type) {
		case json.Number:
			i, err := n.Int64()
			if err != nil {
				return nil, protocol.E(protocol.CodeInvalidCursor)
			}
			return i, nil
		case float64:
			return int64(n), nil
		}
		return nil, protocol.E(protocol.CodeInvalidCursor)
	default:
		s, ok := v.(string)
		if !ok {
			return nil, protocol.E(protocol.CodeInvalidCursor)
		}
		return s, nil
	}
}

// keysetWhere builds "(k1 > v1) OR (k1 = v1 AND k2 > v2) ..." honouring each direction.
func keysetWhere(keys []query.SortKey, cols map[string]sortCol, cursor []any, a *args) (string, error) {
	if cursor == nil {
		return "", nil
	}
	if len(cursor) != len(keys) {
		return "", protocol.E(protocol.CodeInvalidCursor)
	}
	vals := make([]string, len(keys))
	for i, k := range keys {
		v, err := cursorValue(cols[k.Name].kind, cursor[i])
		if err != nil {
			return "", err
		}
		vals[i] = a.add(v)
	}
	var ors []string
	for i, k := range keys {
		var ands []string
		for j := 0; j < i; j++ {
			ands = append(ands, fmt.Sprintf("%s = %s", cols[keys[j].Name].expr, vals[j]))
		}
		op := ">"
		if k.Desc {
			op = "<"
		}
		ands = append(ands, fmt.Sprintf("%s %s %s", cols[k.Name].expr, op, vals[i]))
		ors = append(ors, "("+strings.Join(ands, " AND ")+")")
	}
	return "(" + strings.Join(ors, " OR ") + ")", nil
}

// pageOut assembles `{ items, page }`: cuts the page at the byte budget (like core: half the NATS
// max_payload) and emits the cursor of the last returned item. The absence of `cursor` is the
// only end signal.
func (s *Service) pageOut(q *query.List, items []map[string]any, keys [][]any, hasMore bool, total *int64) map[string]any {
	budget := s.PageBudget
	if budget <= 0 {
		budget = 512 * 1024
	}
	size := 64
	cut := len(items)
	for i, it := range items {
		b, _ := json.Marshal(it)
		size += len(b) + 1
		if size > budget && i > 0 {
			cut = i
			hasMore = true
			break
		}
	}
	items = items[:cut]
	page := map[string]any{"limit": q.Limit, "returned": len(items)}
	if hasMore && len(items) > 0 {
		page["cursor"] = query.EncodeCursor(keys[len(items)-1], q.Filter.Raw, q.SortRaw)
	}
	if total != nil {
		page["total"] = *total
	}
	if items == nil {
		items = []map[string]any{}
	}
	return map[string]any{"items": items, "page": page}
}

// likePrefix escapes a value for `LIKE prefix%`.
func likePrefix(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s) + "%"
}

func project(full map[string]any, fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		if v, ok := full[f]; ok {
			out[f] = v
		}
	}
	return out
}
