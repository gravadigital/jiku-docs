package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/gravadigital/jiku-docs/internal/acl"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/query"
	"github.com/gravadigital/jiku-docs/internal/validate"
)

// ---- meta.describe -------------------------------------------------------------------------

func (s *Service) metaDescribe(ctx context.Context, c *Call) (any, error) {
	names := make([]string, len(describedSpecs))
	byName := map[string]*query.Spec{}
	for i, sp := range describedSpecs {
		names[i] = sp.Name
		byName[sp.Name] = sp
	}
	o := validate.ParseObject(c.Payload, "", "resources")
	if err := o.Err(); err != nil {
		return nil, err
	}
	selected := describedSpecs
	if o.Has("resources") && !o.IsNull("resources") {
		list, ok := o.StringList("resources", names, 0)
		if err := o.Err(); err != nil {
			return nil, err
		}
		if ok && len(list) == 0 {
			return nil, protocol.InvalidField("resources", []any{}, names, "resources does not accept an empty list: omit it to describe all")
		}
		selected = nil
		for _, n := range list {
			selected = append(selected, byName[n])
		}
	}
	resources := map[string]query.Description{}
	for _, sp := range selected {
		resources[sp.Name] = query.Describe(sp)
	}
	return map[string]any{"resources": resources}, nil
}

// ---- folders -------------------------------------------------------------------------------

var folderSortKinds = map[string]string{"path": "string", "name": "string", "createdAt": "time", "id": "string"}

func folderView(f *acl.Folder, tree *acl.Tree) map[string]any {
	var parent any
	if f.ParentID != "" {
		parent = f.ParentID
	}
	return map[string]any{
		"id": f.ID, "name": f.Name, "path": f.Path, "parentId": parent, "inheritAcl": f.InheritACL,
		"externalBlocked": tree.ExternalBlocked(f.ID), "sharingBlocked": tree.SharingBlocked(f.ID),
		"createdAt": protocol.FormatTime(f.CreatedAt), "createdBy": f.CreatedBy,
	}
}

func folderSortValue(f *acl.Folder, name string) any {
	switch name {
	case "path":
		return f.Path
	case "name":
		return f.Name
	case "createdAt":
		return f.CreatedAt
	}
	return f.ID
}

func compareValues(a, b any) int {
	switch x := a.(type) {
	case time.Time:
		y := b.(time.Time)
		switch {
		case x.Before(y):
			return -1
		case x.After(y):
			return 1
		}
		return 0
	case string:
		return strings.Compare(x, b.(string))
	case int64:
		y := b.(int64)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}

func compareKeys(keys []query.SortKey, a, b []any) int {
	for i, k := range keys {
		c := compareValues(a[i], b[i])
		if k.Desc {
			c = -c
		}
		if c != 0 {
			return c
		}
	}
	return 0
}

func (s *Service) foldersList(ctx context.Context, c *Call) (any, error) {
	q, _, err := query.ParseList(foldersSpec, c.Payload)
	if err != nil {
		return nil, err
	}
	tree, err := s.Tree(ctx)
	if err != nil {
		return nil, err
	}
	perm := folderPerm(c.Caller)
	var cursor []any
	if q.Cursor != nil {
		cursor = make([]any, len(q.Sort))
		for i, k := range q.Sort {
			v, err := cursorValue(folderSortKinds[k.Name], q.Cursor[i])
			if err != nil {
				return nil, err
			}
			cursor[i] = v
		}
	}
	type entry struct {
		f    *acl.Folder
		keys []any
	}
	var all []entry
	if perm >= acl.Read {
		for _, f := range tree.Folders {
			if !matchFolder(f, q.Filter.Conds) {
				continue
			}
			k := make([]any, len(q.Sort))
			for i, sk := range q.Sort {
				k[i] = folderSortValue(f, sk.Name)
			}
			all = append(all, entry{f, k})
		}
	}
	sort.Slice(all, func(i, j int) bool { return compareKeys(q.Sort, all[i].keys, all[j].keys) < 0 })
	var total *int64
	if q.Count != "" {
		n := int64(len(all))
		total = &n
		if q.Count == "only" {
			return map[string]any{"items": []any{}, "page": map[string]any{"limit": q.Limit, "returned": 0, "total": n}}, nil
		}
	}
	start := 0
	if cursor != nil {
		start = sort.Search(len(all), func(i int) bool { return compareKeys(q.Sort, all[i].keys, cursor) > 0 })
	}
	end := start + q.Limit
	hasMore := end < len(all)
	if end > len(all) {
		end = len(all)
	}
	items := make([]map[string]any, 0, end-start)
	keys := make([][]any, 0, end-start)
	for _, e := range all[start:end] {
		it := project(folderView(e.f, tree), q.Fields)
		for _, inc := range q.Include {
			if inc == "effectivePermission" {
				it["effectivePermission"] = permOrNil(perm)
			}
		}
		items = append(items, it)
		keys = append(keys, e.keys)
	}
	return s.pageOut(q, items, keys, hasMore, total), nil
}

func permOrNil(p acl.Perm) any {
	if p == acl.None {
		return nil
	}
	return p.String()
}

func matchFolder(f *acl.Folder, conds map[string]query.Cond) bool {
	if c, ok := conds["id"]; ok {
		found := false
		for _, id := range c.Strings {
			if id == f.ID {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if c, ok := conds["parentId"]; ok && f.ParentID != c.Strings[0] {
		return false
	}
	if c, ok := conds["path"]; ok && !strings.EqualFold(f.Path, folderPath(c.Strings[0])) {
		return false
	}
	if c, ok := conds["pathPrefix"]; ok && !strings.HasPrefix(f.Path, c.Strings[0]) {
		return false
	}
	return true
}

// folderPath normalises a folder path filter: folder paths always end with "/" ("/" is the
// root), and the filter accepts it with or without the trailing slash.
func folderPath(p string) string {
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

func (s *Service) foldersGet(ctx context.Context, c *Call) (any, error) {
	o := validate.ParseObject(c.Payload, "", "id", "fields", "include")
	id, _ := o.ULID("id", true)
	fields, _ := o.StringList("fields", foldersSpec.Fields, 0)
	include, _ := o.StringList("include", folderGetIncludes, 0)
	if err := o.Err(); err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		fields = foldersSpec.Base
	}
	fields = append([]string{"id"}, fields...)
	tree, err := s.Tree(ctx)
	if err != nil {
		return nil, err
	}
	perm := folderPerm(c.Caller)
	f, ok := tree.Get(id)
	if !ok || perm < acl.Read {
		return nil, protocol.E(protocol.CodeFolderNotFound)
	}
	out := project(folderView(f, tree), fields)
	for _, inc := range include {
		switch inc {
		case "effectivePermission":
			out["effectivePermission"] = permOrNil(perm)
		case "acl":
			if perm < acl.Manage {
				return nil, protocol.Ef(protocol.CodePermissionDenied, "Folder rules (obsolete) are visible only to administrators")
			}
			rules := f.Rules
			if rules == nil {
				rules = []acl.Rule{}
			}
			out["acl"] = rules
		}
	}
	return out, nil
}

// ---- topics --------------------------------------------------------------------------------

func (s *Service) topicsList(ctx context.Context, c *Call) (any, error) {
	q, _, err := query.ParseList(topicsSpec, c.Payload)
	if err != nil {
		return nil, err
	}
	v, err := s.viewer(ctx, c.Caller)
	if err != nil {
		return nil, err
	}
	var a args
	where := []string{"d.deleted_at IS NULL", v.docPredicate("d", &a)}
	if cnd, ok := q.Filter.Get("prefix"); ok {
		where = append(where, "t.topic LIKE "+a.add(likePrefix(cnd.Strings[0]))+` ESCAPE '\'`)
	}
	if q.Cursor != nil {
		cur, err := cursorValue("string", q.Cursor[0])
		if err != nil {
			return nil, err
		}
		where = append(where, "t.topic > "+a.add(cur))
	}
	rows, err := s.DB.Query(ctx, `SELECT t.topic, count(DISTINCT t.document_id) FROM document_topics t
		JOIN documents d ON d.id = t.document_id WHERE `+strings.Join(where, " AND ")+`
		GROUP BY t.topic ORDER BY t.topic LIMIT `+a.add(q.Limit+1), a...)
	if err != nil {
		return nil, err
	}
	var items []map[string]any
	var keys [][]any
	for rows.Next() {
		var topic string
		var n int64
		if err := rows.Scan(&topic, &n); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, map[string]any{"topic": topic, "documents": n})
		keys = append(keys, []any{topic})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hasMore := len(items) > q.Limit
	if hasMore {
		items, keys = items[:q.Limit], keys[:q.Limit]
	}
	return s.pageOut(q, items, keys, hasMore, nil), nil
}

// ---- attachments.list ----------------------------------------------------------------------

func (s *Service) attachmentsList(ctx context.Context, c *Call) (any, error) {
	q, _, err := query.ParseList(attachmentsSpec, c.Payload)
	if err != nil {
		return nil, err
	}
	docCond, _ := q.Filter.Get("documentId")
	v, err := s.viewer(ctx, c.Caller)
	if err != nil {
		return nil, err
	}
	if _, _, err := s.visibleDoc(ctx, s.DB, v, docCond.Strings[0], true); err != nil {
		return nil, err
	}
	list, err := s.readyAttachments(ctx, docCond.Strings[0])
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": list}, nil
}

// ---- shares.list ---------------------------------------------------------------------------

var shareSortCols = map[string]sortCol{
	"createdAt": {"s.created_at", "time"},
	"expiresAt": {"s.expires_at", "time"},
	"id":        {"s.id", "string"},
}

func (s *Service) sharesList(ctx context.Context, c *Call) (any, error) {
	q, _, err := query.ParseList(sharesSpec, c.Payload)
	if err != nil {
		return nil, err
	}
	docCond, _ := q.Filter.Get("documentId")
	v, err := s.viewer(ctx, c.Caller)
	if err != nil {
		return nil, err
	}
	d, _, err := s.visibleDoc(ctx, s.DB, v, docCond.Strings[0], true)
	if err != nil {
		return nil, err
	}
	// Any internal person who reads the document sees its links; externals never manage links.
	if v.c.IsExternal() {
		return nil, protocol.E(protocol.CodePermissionDenied)
	}
	now := s.now()
	var a args
	where := []string{"s.document_id = " + a.add(d.ID)}
	if cnd, ok := q.Filter.Get("status"); ok {
		switch cnd.Strings[0] {
		case "active":
			where = append(where, "s.revoked_at IS NULL AND s.expires_at > "+a.add(now))
		case "revoked":
			where = append(where, "s.revoked_at IS NOT NULL")
		case "expired":
			where = append(where, "s.revoked_at IS NULL AND s.expires_at <= "+a.add(now))
		}
	}
	if cnd, ok := q.Filter.Get("createdBy"); ok {
		where = append(where, "s.created_by = ANY("+a.add(cnd.Strings)+")")
	}
	ks, err := keysetWhere(q.Sort, shareSortCols, q.Cursor, &a)
	if err != nil {
		return nil, err
	}
	if ks != "" {
		where = append(where, ks)
	}
	rows, err := s.DB.Query(ctx, `SELECT `+prefixCols(shareCols, "s.")+` FROM shares s WHERE `+strings.Join(where, " AND ")+
		` ORDER BY `+orderBy(q.Sort, shareSortCols)+` LIMIT `+a.add(q.Limit+1), a...)
	if err != nil {
		return nil, err
	}
	var items []map[string]any
	var keys [][]any
	for rows.Next() {
		r, err := scanShare(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, r.view(now))
		k := make([]any, len(q.Sort))
		for i, sk := range q.Sort {
			switch sk.Name {
			case "createdAt":
				k[i] = r.CreatedAt
			case "expiresAt":
				k[i] = r.ExpiresAt
			default:
				k[i] = r.ID
			}
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hasMore := len(items) > q.Limit
	if hasMore {
		items, keys = items[:q.Limit], keys[:q.Limit]
	}
	return s.pageOut(q, items, keys, hasMore, nil), nil
}

func prefixCols(cols, prefix string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

var _ = json.Marshal
