package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gravadigital/jiku-docs/internal/acl"
	"github.com/gravadigital/jiku-docs/internal/caller"
	"github.com/gravadigital/jiku-docs/internal/markdown"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/store"
)

func itoa(n int) string { return strconv.Itoa(n) }

// docRow is a row of `documents`.
type docRow struct {
	ID            string
	FolderID      string
	FileName      string
	Path          string
	Title         string
	Visibility    string
	Version       int
	Checksum      string
	Size          int
	SecretWarning bool
	CreatedBy     string
	CreatedAt     time.Time
	UpdatedBy     string
	UpdatedAt     time.Time
	DeletedAt     *time.Time
}

const docCols = `d.id, d.folder_id, d.file_name, d.path, d.title, d.visibility, d.current_version, d.checksum,
	d.size_bytes, d.secret_warning, d.created_by, d.created_at, d.updated_by, d.updated_at, d.deleted_at`

func scanDoc(row pgx.Row) (*docRow, error) {
	d := &docRow{}
	err := row.Scan(&d.ID, &d.FolderID, &d.FileName, &d.Path, &d.Title, &d.Visibility, &d.Version, &d.Checksum,
		&d.Size, &d.SecretWarning, &d.CreatedBy, &d.CreatedAt, &d.UpdatedBy, &d.UpdatedAt, &d.DeletedAt)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// getDoc loads a document (deleted ones too). forUpdate locks the row: every write on a
// document serialises on it.
func getDoc(ctx context.Context, q store.Querier, id string, forUpdate bool) (*docRow, error) {
	sql := `SELECT ` + docCols + ` FROM documents d WHERE d.id = $1`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	d, err := scanDoc(q.QueryRow(ctx, sql, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

// relRow is a row of document_relations.
type relRow struct {
	EntityType string
	EntityID   int64
	ProjectID  *int64
}

func loadRelations(ctx context.Context, q store.Querier, docID string) ([]relRow, error) {
	rows, err := q.Query(ctx, `SELECT entity_type, entity_id, project_id FROM document_relations
		WHERE document_id = $1 ORDER BY entity_type, entity_id`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []relRow
	for rows.Next() {
		var r relRow
		if err := rows.Scan(&r.EntityType, &r.EntityID, &r.ProjectID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func relationsView(rows []relRow) markdown.Relations {
	var r markdown.Relations
	for _, k := range markdown.RelationKinds {
		var ids []int64
		for _, row := range rows {
			if row.EntityType == k.EntityType {
				ids = append(ids, row.EntityID)
			}
		}
		r.Set(k.Key, ids)
	}
	return r.Normalize()
}

func loadTopics(ctx context.Context, q store.Querier, docID string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT topic FROM document_topics WHERE document_id = $1 ORDER BY topic`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// lockRow is the lock of a document.
type lockRow struct {
	SessionID string
	Holder    string
	Via       string
	ExpiresAt time.Time
}

func (l *lockRow) view() map[string]any {
	if l == nil {
		return nil
	}
	return map[string]any{"sessionId": l.SessionID, "holder": l.Holder, "via": l.Via, "expiresAt": protocol.FormatTime(l.ExpiresAt)}
}

// loadLock returns the live lock (expired locks are treated as absent).
func loadLock(ctx context.Context, q store.Querier, docID string, now time.Time) (*lockRow, error) {
	l := &lockRow{}
	err := q.QueryRow(ctx, `SELECT session_id, holder, via, expires_at FROM document_locks WHERE document_id = $1`, docID).
		Scan(&l.SessionID, &l.Holder, &l.Via, &l.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !l.ExpiresAt.After(now) {
		return nil, nil
	}
	return l, nil
}

// ---- who sees what -------------------------------------------------------------------------

// viewer is a caller with the data needed to decide visibility, computed once per request.
type viewer struct {
	c        *caller.Caller
	tree     *acl.Tree
	ev       *acl.Evaluator
	projects []int64 // externals: projects from project-permissions
}

func (s *Service) viewer(ctx context.Context, c *caller.Caller, tree *acl.Tree) (*viewer, error) {
	if tree == nil {
		var err error
		if tree, err = s.Tree(ctx); err != nil {
			return nil, err
		}
	}
	v := &viewer{c: c, tree: tree, ev: tree.For(c.Principal())}
	if c.IsExternal() {
		p, err := s.Core.ProjectPermissions(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		v.projects = p
	}
	return v, nil
}

// folderPerm is the effective permission on a folder. Externals have none: they never see
// folders, only documents.
func (v *viewer) folderPerm(folderID string) acl.Perm {
	if v.c.IsExternal() {
		return acl.None
	}
	return v.ev.Permission(folderID)
}

// docPerm is the effective permission on a document. For externals it is read when the three
// conditions of section 4 hold, else none.
func (v *viewer) docPerm(ctx context.Context, q store.Querier, d *docRow) (acl.Perm, error) {
	if _, ok := v.tree.Get(d.FolderID); !ok {
		return acl.None, nil
	}
	if !v.c.IsExternal() {
		return v.ev.Permission(d.FolderID), nil
	}
	if d.Visibility != "public" || v.tree.ExternalBlocked(d.FolderID) || len(v.projects) == 0 {
		return acl.None, nil
	}
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM document_relations WHERE document_id = $1 AND project_id = ANY($2))`,
		d.ID, v.projects).Scan(&ok)
	if err != nil || !ok {
		return acl.None, err
	}
	return acl.Read, nil
}

// docPredicate is the SQL condition "the caller can read document alias", appended to args.
func (v *viewer) docPredicate(alias string, a *args) string {
	switch {
	case v.c.IsService():
		return "TRUE"
	case v.c.IsExternal():
		if len(v.projects) == 0 {
			return "FALSE"
		}
		return "(" + alias + ".visibility = 'public' AND " + alias + ".folder_id = ANY(" + a.add(v.tree.NotExternalBlocked()) +
			") AND EXISTS (SELECT 1 FROM document_relations vr WHERE vr.document_id = " + alias + ".id AND vr.project_id = ANY(" +
			a.add(v.projects) + ")))"
	default:
		folders := v.ev.Folders(acl.Read)
		if len(folders) == 0 {
			return "FALSE"
		}
		return alias + ".folder_id = ANY(" + a.add(folders) + ")"
	}
}

// visibleDoc loads a document the caller can read; anything else is document_not_found.
// Deleted documents are visible (they can be restored) unless includeDeleted is false.
func (s *Service) visibleDoc(ctx context.Context, q store.Querier, v *viewer, id string, includeDeleted bool) (*docRow, acl.Perm, error) {
	d, err := getDoc(ctx, q, id, false)
	if err != nil {
		return nil, acl.None, err
	}
	if d == nil || (d.DeletedAt != nil && (!includeDeleted || v.c.IsExternal())) {
		return nil, acl.None, protocol.E(protocol.CodeDocumentNotFound)
	}
	perm, err := v.docPerm(ctx, q, d)
	if err != nil {
		return nil, acl.None, err
	}
	if perm < acl.Read {
		return nil, acl.None, protocol.E(protocol.CodeDocumentNotFound)
	}
	return d, perm, nil
}
