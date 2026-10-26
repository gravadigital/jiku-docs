package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gravadigital/jiku-docs/internal/markdown"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/query"
	"github.com/gravadigital/jiku-docs/internal/validate"
)

var documentSortCols = map[string]sortCol{
	"updatedAt": {"d.updated_at", "time"},
	"createdAt": {"d.created_at", "time"},
	"title":     {"d.title", "string"},
	"path":      {"d.path", "string"},
	"id":        {"d.id", "string"},
}

func (d *docRow) view() map[string]any {
	var deleted any
	if d.DeletedAt != nil {
		deleted = protocol.FormatTime(*d.DeletedAt)
	}
	return map[string]any{
		"id": d.ID, "title": d.Title, "path": d.Path, "folderId": d.FolderID, "fileName": d.FileName,
		"version": d.Version, "checksum": d.Checksum, "sizeBytes": d.Size, "visibility": d.Visibility,
		"createdAt": protocol.FormatTime(d.CreatedAt), "createdBy": d.CreatedBy,
		"updatedAt": protocol.FormatTime(d.UpdatedAt), "updatedBy": d.UpdatedBy, "deletedAt": deleted,
		"prunedVersionsCount": d.PrunedVersions,
		"createdByName":       strOrNil(d.CreatedByName), "updatedByName": strOrNil(d.UpdatedByName),
	}
}

func (d *docRow) sortValue(name string) any {
	switch name {
	case "updatedAt":
		return d.UpdatedAt
	case "createdAt":
		return d.CreatedAt
	case "title":
		return d.Title
	case "path":
		return d.Path
	}
	return d.ID
}

// docConds translates parsed document filters to SQL (alias d).
func docConds(conds map[string]query.Cond, a *args) []string {
	var out []string
	for _, name := range documentFilterable {
		c, ok := conds[name]
		if !ok {
			continue
		}
		switch name {
		case "id":
			out = append(out, "d.id = ANY("+a.add(c.Strings)+")")
		case "folderId":
			out = append(out, "d.folder_id = "+a.add(c.Strings[0]))
		case "path":
			out = append(out, "lower(d.path) = lower("+a.add(c.Strings[0])+")")
		case "pathPrefix":
			out = append(out, "d.path LIKE "+a.add(likePrefix(c.Strings[0]))+` ESCAPE '\'`)
		case "clientId", "requirementId", "taskId":
			et := map[string]string{"clientId": "client", "requirementId": "requirement", "taskId": "task"}[name]
			out = append(out, "EXISTS (SELECT 1 FROM document_relations fr WHERE fr.document_id = d.id AND fr.entity_type = '"+
				et+"' AND fr.entity_id = ANY("+a.add(c.Ints)+"))")
		case "projectId":
			out = append(out, "EXISTS (SELECT 1 FROM document_relations fr WHERE fr.document_id = d.id AND fr.project_id = ANY("+a.add(c.Ints)+"))")
		case "topic":
			out = append(out, "EXISTS (SELECT 1 FROM document_topics ft WHERE ft.document_id = d.id AND ft.topic = ANY("+a.add(c.Strings)+"))")
		case "visibility":
			out = append(out, "d.visibility = "+a.add(c.Strings[0]))
		case "createdBy":
			out = append(out, "d.created_by = ANY("+a.add(c.Strings)+")")
		case "updatedBy":
			out = append(out, "d.updated_by = ANY("+a.add(c.Strings)+")")
		case "createdAt", "updatedAt":
			col := map[string]string{"createdAt": "d.created_at", "updatedAt": "d.updated_at"}[name]
			if c.Gte != nil {
				out = append(out, col+" >= "+a.add(*c.Gte))
			}
			if c.Lte != nil {
				out = append(out, col+" <= "+a.add(*c.Lte))
			}
		case "fileName":
			out = append(out, "lower(d.file_name) LIKE lower("+a.add(likePrefix(c.Strings[0]))+`) ESCAPE '\'`)
		case "deleted":
			if *c.Bool {
				out = append(out, "d.deleted_at IS NOT NULL")
			} else {
				out = append(out, "d.deleted_at IS NULL")
			}
		case "hasProject", "hasClient", "hasTopic":
			// "Sin proyecto" (hasProject false) means no project at all: neither direct nor
			// through a requirement or a task. hasClient looks at jiku.clients only.
			exists := map[string]string{
				"hasProject": "EXISTS (SELECT 1 FROM document_relations hr WHERE hr.document_id = d.id AND hr.entity_type IN ('project','requirement','task'))",
				"hasClient":  "EXISTS (SELECT 1 FROM document_relations hr WHERE hr.document_id = d.id AND hr.entity_type = 'client')",
				"hasTopic":   "EXISTS (SELECT 1 FROM document_topics ht WHERE ht.document_id = d.id)",
			}[name]
			if *c.Bool {
				out = append(out, exists)
			} else {
				out = append(out, "NOT "+exists)
			}
		}
	}
	return out
}

// docWhere is visibility + filters (+ the default deleted = false).
func docWhere(v *viewer, f *query.Filter, a *args) []string {
	where := []string{v.docPredicate("d", a)}
	where = append(where, docConds(f.Conds, a)...)
	if _, ok := f.Conds["deleted"]; !ok {
		where = append(where, "d.deleted_at IS NULL")
	}
	if v.c.IsExternal() {
		where = append(where, "d.deleted_at IS NULL")
	}
	if len(f.Or) > 0 {
		var ors []string
		for _, item := range f.Or {
			conds := docConds(item, a)
			if len(conds) == 0 {
				conds = []string{"TRUE"}
			}
			ors = append(ors, "("+strings.Join(conds, " AND ")+")")
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}
	return where
}

func (s *Service) documentsList(ctx context.Context, c *Call) (any, error) {
	q, _, err := query.ParseList(documentsSpec, c.Payload)
	if err != nil {
		return nil, err
	}
	v, err := s.viewer(ctx, c.Caller)
	if err != nil {
		return nil, err
	}
	var a args
	where := docWhere(v, q.Filter, &a)
	var total *int64
	if q.Count != "" {
		var n int64
		if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM documents d WHERE `+strings.Join(where, " AND "), a...).Scan(&n); err != nil {
			return nil, err
		}
		total = &n
		if q.Count == "only" {
			return map[string]any{"items": []any{}, "page": map[string]any{"limit": q.Limit, "returned": 0, "total": n}}, nil
		}
	}
	ks, err := keysetWhere(q.Sort, documentSortCols, q.Cursor, &a)
	if err != nil {
		return nil, err
	}
	if ks != "" {
		where = append(where, ks)
	}
	sql := `SELECT ` + docCols + ` FROM documents d WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY ` + orderBy(q.Sort, documentSortCols) + ` LIMIT ` + a.add(q.Limit+1)
	rows, err := s.DB.Query(ctx, sql, a...)
	if err != nil {
		return nil, err
	}
	var docs []*docRow
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		docs = append(docs, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hasMore := len(docs) > q.Limit
	if hasMore {
		docs = docs[:q.Limit]
	}
	extras, err := s.loadIncludes(ctx, v, docs, q.Include)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}
	direct, err := s.directRelations(ctx, v, ids)
	if err != nil {
		return nil, err
	}
	matchSet := filterProjects(q.Filter)
	fulls := make([]map[string]any, len(docs))
	for i, d := range docs {
		fulls[i] = d.view()
	}
	s.fillNames(ctx, fulls, docNamePairs...)
	items := make([]map[string]any, len(docs))
	keys := make([][]any, len(docs))
	for i, d := range docs {
		full := fulls[i]
		full["clients"], full["projects"] = direct[d.ID].clients(), direct[d.ID].projects()
		it := project(full, q.Fields)
		for k, val := range extras[d.ID] {
			it[k] = val
		}
		if matchSet != nil {
			it["projectMatch"] = direct[d.ID].match(matchSet)
		}
		items[i] = it
		k := make([]any, len(q.Sort))
		for j, sk := range q.Sort {
			k[j] = d.sortValue(sk.Name)
		}
		keys[i] = k
	}
	return s.pageOut(q, items, keys, hasMore, total), nil
}

// loadIncludes batch-loads relations, topics and locks of a page.
func (s *Service) loadIncludes(ctx context.Context, v *viewer, docs []*docRow, include []string) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	if len(include) == 0 || len(docs) == 0 {
		return out, nil
	}
	ids := make([]string, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
		out[d.ID] = map[string]any{}
	}
	for _, inc := range include {
		switch inc {
		case "relations":
			rows, err := s.DB.Query(ctx, `SELECT document_id, entity_type, entity_id, project_id FROM document_relations WHERE document_id = ANY($1)`, ids)
			if err != nil {
				return nil, err
			}
			byDoc := map[string][]relRow{}
			for rows.Next() {
				var id string
				var r relRow
				if err := rows.Scan(&id, &r.EntityType, &r.EntityID, &r.ProjectID); err != nil {
					rows.Close()
					return nil, err
				}
				byDoc[id] = append(byDoc[id], r)
			}
			rows.Close()
			for _, id := range ids {
				out[id]["relations"] = v.relations(relationsView(byDoc[id]))
			}
		case "topics":
			rows, err := s.DB.Query(ctx, `SELECT document_id, topic FROM document_topics WHERE document_id = ANY($1) ORDER BY topic`, ids)
			if err != nil {
				return nil, err
			}
			byDoc := map[string][]string{}
			for rows.Next() {
				var id, t string
				if err := rows.Scan(&id, &t); err != nil {
					rows.Close()
					return nil, err
				}
				byDoc[id] = append(byDoc[id], t)
			}
			rows.Close()
			for _, id := range ids {
				t := byDoc[id]
				if t == nil {
					t = []string{}
				}
				out[id]["topics"] = t
			}
		case "lock":
			rows, err := s.DB.Query(ctx, `SELECT document_id, session_id, holder, holder_name, via, expires_at FROM document_locks
				WHERE document_id = ANY($1) AND expires_at > $2`, ids, s.now())
			if err != nil {
				return nil, err
			}
			byDoc := map[string]*lockRow{}
			for rows.Next() {
				var id string
				l := &lockRow{}
				if err := rows.Scan(&id, &l.SessionID, &l.Holder, &l.HolderName, &l.Via, &l.ExpiresAt); err != nil {
					rows.Close()
					return nil, err
				}
				byDoc[id] = l
			}
			rows.Close()
			var views []map[string]any
			for _, id := range ids {
				if l := byDoc[id]; l != nil {
					view := l.view()
					views = append(views, view)
					out[id]["lock"] = view
				} else {
					out[id]["lock"] = nil
				}
			}
			s.fillNames(ctx, views, lockNamePairs...)
		}
	}
	return out, nil
}

func (s *Service) documentsGet(ctx context.Context, c *Call) (any, error) {
	o := validate.ParseObject(c.Payload, "", "id", "version", "fields", "include")
	id, _ := o.ULID("id", true)
	version, hasVersion := o.Int("version", false, 1, 1<<31-1)
	fields, _ := o.StringList("fields", documentGetFields, 0)
	include, _ := o.StringList("include", documentGetIncludes, 0)
	if err := o.Err(); err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		fields = documentGetFields
	}
	fields = append([]string{"id"}, fields...)
	v, err := s.viewer(ctx, c.Caller)
	if err != nil {
		return nil, err
	}
	d, _, err := s.visibleDoc(ctx, s.DB, v, id, true)
	if err != nil {
		return nil, err
	}
	key := ""
	full := d.view()
	direct, err := s.directRelations(ctx, v, []string{d.ID})
	if err != nil {
		return nil, err
	}
	full["clients"], full["projects"] = direct[d.ID].clients(), direct[d.ID].projects()
	if hasVersion && int(version) != d.Version && v.c.IsExternal() {
		// An external person sees the document because it is public NOW. Earlier versions may be
		// from before it was published, and hold what was removed to publish it. No details: not
		// even whether the version exists or was compacted (that would reveal the history).
		return nil, protocol.E(protocol.CodeDocumentNotFound)
	}
	if hasVersion && int(version) != d.Version {
		var ver struct {
			key, checksum, title, author string
			authorName                   *string
			size                         int
			at                           time.Time
		}
		err := s.DB.QueryRow(ctx, `SELECT storage_key, checksum, size_bytes, title, author, author_name, created_at FROM document_versions
			WHERE document_id = $1 AND version = $2`, id, version).Scan(&ver.key, &ver.checksum, &ver.size, &ver.title, &ver.author,
			&ver.authorName, &ver.at)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, missingVersion(ctx, s.DB, id, d.Version, int(version))
		}
		if err != nil {
			return nil, err
		}
		key = ver.key
		full["version"], full["checksum"], full["sizeBytes"], full["title"] = int(version), ver.checksum, ver.size, ver.title
		full["updatedAt"], full["updatedBy"], full["updatedByName"] = protocol.FormatTime(ver.at), ver.author, strOrNil(ver.authorName)
	}
	s.fillNames(ctx, []map[string]any{full}, docNamePairs...)
	if key == "" && (v.c.IsExternal() || slices.Contains(include, "content")) {
		if err := s.DB.QueryRow(ctx, `SELECT storage_key FROM document_versions WHERE document_id = $1 AND version = $2`,
			id, d.Version).Scan(&key); err != nil {
			return nil, err
		}
	}
	// D-11: an external person never sees the frontmatter (it holds internal ids of
	// requirements and tasks). `checksum` and `sizeBytes` describe what it receives: the body.
	var external []byte
	if v.c.IsExternal() && (slices.Contains(fields, "checksum") || slices.Contains(fields, "sizeBytes") || slices.Contains(include, "content")) {
		raw, err := s.Blob.Get(ctx, key, MaxDocumentBytes)
		if err != nil {
			return nil, err
		}
		external = []byte(markdown.StripFrontmatter(string(raw)))
		full["checksum"], full["sizeBytes"] = Checksum(external), len(external)
	}
	if v.c.IsExternal() {
		delete(full, "prunedVersionsCount") // externals see the current version, never the history
	}
	out := project(full, fields)
	for _, inc := range include {
		switch inc {
		case "content":
			size, _ := full["sizeBytes"].(int)
			switch {
			case external != nil && size > MaxInlineBytes:
				u, err := s.presignExternalBody(ctx, d.ID, d.Version, external)
				if err != nil {
					return nil, err
				}
				out["contentUrl"] = u
			case external != nil:
				out["content"] = string(external)
			case size > MaxInlineBytes:
				u, err := s.Blob.PresignGet(ctx, key, PresignTTL, "text/markdown; charset=utf-8")
				if err != nil {
					return nil, err
				}
				out["contentUrl"] = u
			default:
				b, err := s.Blob.Get(ctx, key, MaxDocumentBytes)
				if err != nil {
					return nil, err
				}
				out["content"] = string(b)
			}
		case "relations", "topics", "lock":
			extra, err := s.loadIncludes(ctx, v, []*docRow{d}, []string{inc})
			if err != nil {
				return nil, err
			}
			out[inc] = extra[d.ID][inc]
		case "attachments":
			list, err := s.readyAttachments(ctx, d.ID)
			if err != nil {
				return nil, err
			}
			out["attachments"] = list
		}
	}
	return out, nil
}

// presignExternalBody stores the body without frontmatter that an external person receives
// under `renders/` (the janitor removes it after RenderRetention) and presigns it.
func (s *Service) presignExternalBody(ctx context.Context, docID string, version int, body []byte) (string, error) {
	sum := sha256.Sum256(body)
	key := "renders/external/" + docID + "/" + itoa(version) + "-" + base64.RawURLEncoding.EncodeToString(sum[:8]) + ".md"
	if err := s.Blob.Put(ctx, key, body, "text/markdown; charset=utf-8"); err != nil {
		return "", err
	}
	return s.Blob.PresignGet(ctx, key, PresignTTL, "text/markdown; charset=utf-8")
}

func (s *Service) readyAttachments(ctx context.Context, docID string) ([]map[string]any, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, document_id, file_name, mime_type, checksum, status, uploaded_by, size_bytes, core_file_id, created_at
		FROM attachments WHERE document_id = $1 AND status = 'ready' ORDER BY created_at, id`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		a := &attRow{}
		if err := rows.Scan(&a.ID, &a.DocumentID, &a.FileName, &a.MimeType, &a.Checksum, &a.Status, &a.UploadedBy, &a.Size,
			&a.CoreFileID, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s.attachmentView(a))
	}
	return out, rows.Err()
}

var versionSortCols = map[string]sortCol{"version": {"v.version", "int"}}

func (s *Service) versionsList(ctx context.Context, c *Call) (any, error) {
	q, _, err := query.ParseList(versionsSpec, c.Payload)
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
	var a args
	where := []string{"v.document_id = " + a.add(docCond.Strings[0])}
	if v.c.IsExternal() {
		// Only the current version, the one that is public (see documentsGet).
		where = append(where, "v.version = (SELECT cur.current_version FROM documents cur WHERE cur.id = v.document_id)")
	}
	if cnd, ok := q.Filter.Get("author"); ok {
		where = append(where, "v.author = ANY("+a.add(cnd.Strings)+")")
	}
	if cnd, ok := q.Filter.Get("via"); ok {
		where = append(where, "v.via = "+a.add(cnd.Strings[0]))
	}
	if cnd, ok := q.Filter.Get("kind"); ok {
		where = append(where, "v.kind = "+a.add(cnd.Strings[0]))
	}
	if cnd, ok := q.Filter.Get("protected"); ok {
		where = append(where, "v.protected = "+a.add(*cnd.Bool))
	}
	if cnd, ok := q.Filter.Get("createdAt"); ok {
		if cnd.Gte != nil {
			where = append(where, "v.created_at >= "+a.add(*cnd.Gte))
		}
		if cnd.Lte != nil {
			where = append(where, "v.created_at <= "+a.add(*cnd.Lte))
		}
	}
	ks, err := keysetWhere(q.Sort, versionSortCols, q.Cursor, &a)
	if err != nil {
		return nil, err
	}
	if ks != "" {
		where = append(where, ks)
	}
	rows, err := s.DB.Query(ctx, `SELECT v.version, v.checksum, v.size_bytes, v.author, v.via, v.session_id, v.message, v.created_at,
		v.kind, v.protected, v.author_name FROM document_versions v WHERE `+strings.Join(where, " AND ")+` ORDER BY `+orderBy(q.Sort, versionSortCols)+` LIMIT `+a.add(q.Limit+1), a...)
	if err != nil {
		return nil, err
	}
	var items []map[string]any
	var keys [][]any
	for rows.Next() {
		var version, size int
		var checksum, author, via, kind string
		var session, message, authorName *string
		var at time.Time
		var protected bool
		if err := rows.Scan(&version, &checksum, &size, &author, &via, &session, &message, &at, &kind, &protected, &authorName); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, map[string]any{"version": version, "checksum": checksum, "sizeBytes": size, "author": author,
			"authorName": strOrNil(authorName), "via": via, "sessionId": strOrNil(session), "message": strOrNil(message),
			"createdAt": protocol.FormatTime(at), "kind": kind, "protected": protected})
		keys = append(keys, []any{int64(version)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hasMore := len(items) > q.Limit
	if hasMore {
		items, keys = items[:q.Limit], keys[:q.Limit]
	}
	s.fillNames(ctx, items, versionNamePairs...)
	if v.c.IsExternal() {
		// The only visible version is the current one; like documents.get, its checksum and size
		// describe the body without frontmatter, the content an external person receives (D-11).
		for _, it := range items {
			var key string
			if err := s.DB.QueryRow(ctx, `SELECT storage_key FROM document_versions WHERE document_id = $1 AND version = $2`,
				docCond.Strings[0], it["version"]).Scan(&key); err != nil {
				return nil, err
			}
			raw, err := s.Blob.Get(ctx, key, MaxDocumentBytes)
			if err != nil {
				return nil, err
			}
			body := []byte(markdown.StripFrontmatter(string(raw)))
			it["checksum"], it["sizeBytes"] = Checksum(body), len(body)
		}
	}
	return s.pageOut(q, items, keys, hasMore, nil), nil
}

func strOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func (s *Service) documentsSearch(ctx context.Context, c *Call) (any, error) {
	o := validate.ParseObject(c.Payload, "", "query", "mode", "filter", "limit")
	text, _ := o.String("query", true, validate.MinLen(2), validate.MaxLen(1000))
	mode := "text"
	if o.Has("mode") {
		mode, _ = o.Enum("mode", true, "text", "semantic", "hybrid")
	}
	limit := int64(20)
	if o.Has("limit") {
		limit, _ = o.Int("limit", true, 1, 50)
	}
	if err := o.Err(); err != nil {
		return nil, err
	}
	if mode != "text" {
		return nil, protocol.E(protocol.CodeSemanticSearchNotConfigured)
	}
	filter, err := query.ParseFilter(searchSpec, o, "filter")
	if err != nil {
		return nil, err
	}
	v, err := s.viewer(ctx, c.Caller)
	if err != nil {
		return nil, err
	}
	var a args
	tsq := a.add(text)
	where := docWhere(v, filter, &a)
	where = append(where, "d.deleted_at IS NULL", "st.doc_tsv @@ q.tsq")
	// The document matches as a whole (exclusions see every section); its best fragment gives
	// the snippet.
	sql := `WITH q AS (SELECT websearch_to_tsquery('es_unaccent', ` + tsq + `) AS tsq),
	hits AS (
		SELECT DISTINCT ON (c.document_id) c.document_id, c.version, c.heading_path, c.body,
			ts_rank_cd(st.doc_tsv, q.tsq) AS score
		FROM index_chunks c JOIN index_status st ON st.document_id = c.document_id
		JOIN documents d ON d.id = c.document_id CROSS JOIN q
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY c.document_id, ts_rank_cd(c.tsv, q.tsq) DESC, c.chunk_no
	)
	SELECT h.document_id, h.version, d.title, d.path, h.heading_path,
		CASE WHEN h.body = '' THEN array_to_string(h.heading_path, ' › ')
		ELSE ts_headline('es_unaccent', h.body, q.tsq, 'MaxWords=40, MinWords=15, StartSel=**, StopSel=**, MaxFragments=2, FragmentDelimiter=" … "') END,
		h.score, d.updated_at, d.updated_by, d.updated_by_name, d.created_by, d.created_by_name, d.visibility
	FROM hits h JOIN documents d ON d.id = h.document_id CROSS JOIN q
	ORDER BY h.score DESC, h.document_id LIMIT ` + a.add(limit)
	rows, err := s.DB.Query(ctx, sql, a...)
	if err != nil {
		return nil, err
	}
	items := []map[string]any{}
	var ids []string
	for rows.Next() {
		var docID, title, path, snippet, updatedBy, createdBy, visibility string
		var updatedByName, createdByName *string
		var version int
		var heading []string
		var score float32
		var updated time.Time
		if err := rows.Scan(&docID, &version, &title, &path, &heading, &snippet, &score, &updated, &updatedBy, &updatedByName,
			&createdBy, &createdByName, &visibility); err != nil {
			rows.Close()
			return nil, err
		}
		if r := []rune(snippet); len(r) > 500 {
			snippet = string(r[:499]) + "…"
		}
		if heading == nil {
			heading = []string{}
		}
		ids = append(ids, docID)
		items = append(items, map[string]any{"documentId": docID, "version": version, "title": title, "path": path,
			"headingPath": heading, "snippet": snippet, "score": score, "updatedAt": protocol.FormatTime(updated), "updatedBy": updatedBy,
			"updatedByName": strOrNil(updatedByName), "createdBy": createdBy, "createdByName": strOrNil(createdByName),
			"visibility": visibility})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.fillNames(ctx, items, docNamePairs...)
	direct, err := s.directRelations(ctx, v, ids)
	if err != nil {
		return nil, err
	}
	matchSet := filterProjects(filter)
	for _, it := range items {
		id := it["documentId"].(string)
		it["clients"], it["projects"] = direct[id].clients(), direct[id].projects()
		if matchSet != nil {
			it["projectMatch"] = direct[id].match(matchSet)
		}
	}
	return map[string]any{"items": items}, nil
}

// direct are the clients and projects a document names in its own frontmatter (jiku.clients,
// jiku.projects), for the "Cliente · Proyecto" of each item and projectMatch.
type direct struct {
	clientIDs, projectIDs []int64 // as shown to the caller
	allProjects           []int64 // every direct project, for projectMatch
}

func (d *direct) clients() []int64 {
	if d == nil || d.clientIDs == nil {
		return []int64{}
	}
	return d.clientIDs
}

func (d *direct) projects() []int64 {
	if d == nil || d.projectIDs == nil {
		return []int64{}
	}
	return d.projectIDs
}

// match is projectMatch: direct when one of the filtered projects is in jiku.projects,
// indirect when the document reaches the project only through a requirement or a task.
func (d *direct) match(set map[int64]bool) string {
	if d != nil {
		for _, p := range d.allProjects {
			if set[p] {
				return "direct"
			}
		}
	}
	return "indirect"
}

// directRelations batch-loads the direct clients and projects of documents. An external person
// sees only the projects it has permission on, and no client ids (the relations are internal
// data, D-11).
func (s *Service) directRelations(ctx context.Context, v *viewer, ids []string) (map[string]*direct, error) {
	out := map[string]*direct{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT document_id, entity_type, entity_id FROM document_relations
		WHERE document_id = ANY($1) AND entity_type IN ('client', 'project') ORDER BY document_id, entity_type, entity_id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, et string
		var eid int64
		if err := rows.Scan(&id, &et, &eid); err != nil {
			return nil, err
		}
		d := out[id]
		if d == nil {
			d = &direct{}
			out[id] = d
		}
		switch {
		case et == "project":
			d.allProjects = append(d.allProjects, eid)
			if !v.c.IsExternal() || slices.Contains(v.projects, eid) {
				d.projectIDs = append(d.projectIDs, eid)
			}
		case et == "client" && !v.c.IsExternal():
			d.clientIDs = append(d.clientIDs, eid)
		}
	}
	return out, rows.Err()
}

// relations is the relations include as the viewer may see it: an external person gets only
// its own projects (no clients, requirements or tasks: internal ids, D-11).
func (v *viewer) relations(r markdown.Relations) markdown.Relations {
	if !v.c.IsExternal() {
		return r
	}
	out := markdown.Relations{Clients: []int64{}, Requirements: []int64{}, Tasks: []int64{}, Projects: []int64{}}
	for _, p := range r.Projects {
		if slices.Contains(v.projects, p) {
			out.Projects = append(out.Projects, p)
		}
	}
	return out
}

// filterProjects are the project ids of filter.projectId (top level and inside or), or nil.
func filterProjects(f *query.Filter) map[int64]bool {
	if f == nil {
		return nil
	}
	var set map[int64]bool
	add := func(conds map[string]query.Cond) {
		if c, ok := conds["projectId"]; ok {
			if set == nil {
				set = map[int64]bool{}
			}
			for _, id := range c.Ints {
				set[id] = true
			}
		}
	}
	add(f.Conds)
	for _, o := range f.Or {
		add(o)
	}
	return set
}

// ChangelogRetention is how long the feed keeps events.
const ChangelogRetention = 30 * 24 * time.Hour

func (s *Service) documentsChanges(ctx context.Context, c *Call) (any, error) {
	o := validate.ParseObject(c.Payload, "", "sinceSeq", "limit", "filter")
	since, _ := o.Int("sinceSeq", true, 0, 1<<62)
	limit := int64(200)
	if o.Has("limit") {
		limit, _ = o.Int("limit", true, 1, 500)
	}
	if err := o.Err(); err != nil {
		return nil, err
	}
	filter, err := query.ParseFilter(changesSpec, o, "filter")
	if err != nil {
		return nil, err
	}
	if since > 0 {
		var purged int64
		err := s.DB.QueryRow(ctx, `SELECT COALESCE((SELECT (value->>'seq')::bigint FROM service_state WHERE key = 'changelog_purged_through'), 0)`).Scan(&purged)
		if err != nil {
			return nil, err
		}
		if since < purged {
			return nil, protocol.Ef(protocol.CodeInvalidCursor, "sinceSeq es más viejo que la retención del feed (30 días): hacé una reconciliación completa").
				With("oldestSeq", purged+1)
		}
	}
	v, err := s.viewer(ctx, c.Caller)
	if err != nil {
		return nil, err
	}
	var a args
	where := []string{"c.seq > " + a.add(since), "c.entity_type = 'document'"}
	if cnd, ok := filter.Get("pathPrefix"); ok {
		where = append(where, "(c.payload->'entity'->>'path') LIKE "+a.add(likePrefix(cnd.Strings[0]))+` ESCAPE '\'`)
	}
	pred := v.docPredicate("d", &a)
	rows, err := s.DB.Query(ctx, `SELECT c.seq, c.type, c.entity_id, (c.payload->'snapshot'->>'version')::int,
		c.payload->'entity'->>'path', c.payload->'snapshot'->>'checksum', c.occurred_at, (d.id IS NOT NULL AND `+pred+`)
		FROM changelog c LEFT JOIN documents d ON d.id = c.entity_id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY c.seq LIMIT `+a.add(limit+1), a...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	next := since
	n := int64(0)
	hasMore := false
	for rows.Next() {
		var seq int64
		var typ, docID, path string
		var version *int
		var checksum *string
		var at time.Time
		var visible bool
		if err := rows.Scan(&seq, &typ, &docID, &version, &path, &checksum, &at, &visible); err != nil {
			return nil, err
		}
		n++
		if n > limit {
			hasMore = true
			break
		}
		next = seq
		if !visible {
			continue
		}
		var ver any
		if version != nil {
			ver = *version
		}
		items = append(items, map[string]any{"seq": seq, "type": typ, "documentId": docID, "version": ver, "path": path,
			"checksum": strOrNil(checksum), "occurredAt": protocol.FormatTime(at)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "nextSeq": next, "hasMore": hasMore}, nil
}
