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

// nameOf is the name stored with what a caller writes: the actor's name (or username) when
// the api writes for a person, the name in core's users for a person on the bus, and the
// name of the service for a service. nil when there is none.
func nameOf(c *caller.Caller) *string {
	if c == nil || c.Name == "" {
		return nil
	}
	n := c.Name
	return &n
}

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
	// CreatedByName and UpdatedByName are the names stored on write (P-1); nil when unknown.
	CreatedByName *string
	UpdatedByName *string
	DeletedAt     *time.Time
	// PrunedVersions is how many versions the history compaction removed.
	PrunedVersions int
}

const docCols = `d.id, d.folder_id, d.file_name, d.path, d.title, d.visibility, d.current_version, d.checksum,
	d.size_bytes, d.secret_warning, d.created_by, d.created_at, d.updated_by, d.updated_at, d.deleted_at,
	d.pruned_versions_count, d.created_by_name, d.updated_by_name`

func scanDoc(row pgx.Row) (*docRow, error) {
	d := &docRow{}
	err := row.Scan(&d.ID, &d.FolderID, &d.FileName, &d.Path, &d.Title, &d.Visibility, &d.Version, &d.Checksum,
		&d.Size, &d.SecretWarning, &d.CreatedBy, &d.CreatedAt, &d.UpdatedBy, &d.UpdatedAt, &d.DeletedAt, &d.PrunedVersions,
		&d.CreatedByName, &d.UpdatedByName)
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
	SessionID  string
	Holder     string
	HolderName *string
	Via        string
	ExpiresAt  time.Time
}

func (l *lockRow) view() map[string]any {
	if l == nil {
		return nil
	}
	return map[string]any{"sessionId": l.SessionID, "holder": l.Holder, "holderName": strOrNil(l.HolderName), "via": l.Via,
		"expiresAt": protocol.FormatTime(l.ExpiresAt)}
}

// loadLock returns the live lock (expired locks are treated as absent).
func loadLock(ctx context.Context, q store.Querier, docID string, now time.Time) (*lockRow, error) {
	l := &lockRow{}
	err := q.QueryRow(ctx, `SELECT session_id, holder, holder_name, via, expires_at FROM document_locks WHERE document_id = $1`, docID).
		Scan(&l.SessionID, &l.Holder, &l.HolderName, &l.Via, &l.ExpiresAt)
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

// P-1 (docs/p1/service.md): the class of the caller and the visibility of the document decide
// access; folder ACL rules, externalBlocked and sharingBlocked have no effect.
//
//	visibility   admin          user           external-user                         service
//	internal     read + write   read + write   never                                 everything
//	public       read + write   read + write   read, if related to one of its        everything
//	                                           projects (project-permissions)
//	restricted   read + write   never          never                                 everything
//
// What a caller cannot read does not exist for it, by any path: *_not_found or omitted.

// personPerm is the permission of a non-external caller on a document of a visibility:
// services and admins manage, users write what is not restricted.
func personPerm(c *caller.Caller, visibility string) acl.Perm {
	switch {
	case c.IsService(), c.IsAdmin():
		return acl.Manage
	case c.IsExternal(), visibility == markdown.VisibilityRestricted:
		return acl.None
	}
	return acl.Write
}

// folderPerm is the permission of a caller on any live folder. Folders only organise the
// files (the sync daemon mirrors their paths): internal persons see them all and write
// documents in any of them; only admins and services manage them. Externals never see folders.
func folderPerm(c *caller.Caller) acl.Perm {
	switch {
	case c.IsService(), c.IsAdmin():
		return acl.Manage
	case c.IsExternal():
		return acl.None
	}
	return acl.Write
}

// checkVisibilityChange: any internal person sets internal or public; only admins (and
// services) set or remove restricted. Applied on every write that carries content (new,
// update, restore): the frontmatter is the content.
func checkVisibilityChange(c *caller.Caller, from, to string) error {
	if from == "" {
		from = markdown.VisibilityInternal
	}
	if from == to {
		return nil
	}
	if (from == markdown.VisibilityRestricted || to == markdown.VisibilityRestricted) && !c.SeesRestricted() {
		return protocol.Ef(protocol.CodePermissionDenied, "Only an administrator can set or remove the restricted visibility")
	}
	return nil
}

// viewer is a caller with the data needed to decide visibility, computed once per request.
type viewer struct {
	c        *caller.Caller
	projects []int64 // externals: projects from project-permissions
}

func (s *Service) viewer(ctx context.Context, c *caller.Caller) (*viewer, error) {
	v := &viewer{c: c}
	if c.IsExternal() {
		p, err := s.Core.ProjectPermissions(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		v.projects = p
	}
	return v, nil
}

// docPerm is the effective permission on a document. For externals it is read when the
// document is public and related to one of their projects, else none.
func (v *viewer) docPerm(ctx context.Context, q store.Querier, d *docRow) (acl.Perm, error) {
	if !v.c.IsExternal() {
		return personPerm(v.c, d.Visibility), nil
	}
	if d.Visibility != markdown.VisibilityPublic || len(v.projects) == 0 {
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
// It is the only gate of every list, count, search, feed and topic count.
func (v *viewer) docPredicate(alias string, a *args) string {
	switch {
	case v.c.IsService(), v.c.IsAdmin():
		return "TRUE"
	case v.c.IsExternal():
		if len(v.projects) == 0 {
			return "FALSE"
		}
		return "(" + alias + ".visibility = 'public' AND EXISTS (SELECT 1 FROM document_relations vr WHERE vr.document_id = " +
			alias + ".id AND vr.project_id = ANY(" + a.add(v.projects) + ")))"
	default:
		return alias + ".visibility <> 'restricted'"
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
