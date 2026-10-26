package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gravadigital/jiku-docs/internal/acl"
	"github.com/gravadigital/jiku-docs/internal/blob"
	"github.com/gravadigital/jiku-docs/internal/caller"
	"github.com/gravadigital/jiku-docs/internal/markdown"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/store"
	"github.com/gravadigital/jiku-docs/internal/validate"
)

// docWritePerm checks a write on a document: invisible = not found, readable without the
// permission = permission_denied. It returns the permission for further checks.
func docWritePerm(c *caller.Caller, d *docRow, min acl.Perm) (acl.Perm, error) {
	p := personPerm(c, d.Visibility)
	if p < acl.Read {
		return p, protocol.E(protocol.CodeDocumentNotFound)
	}
	if p < min {
		return p, protocol.E(protocol.CodePermissionDenied)
	}
	return p, nil
}

// checkLock: a live lock of another session blocks the write. sessionID == "" means "no
// session"; holderOK allows the lock holder itself to bypass it (delete/restore carry no session).
func checkLock(l *lockRow, sessionID, callerID string, holderOK bool) error {
	if l == nil {
		return nil
	}
	if sessionID != "" && l.SessionID == sessionID && l.Holder == callerID {
		return nil
	}
	if holderOK && l.Holder == callerID {
		return nil
	}
	return protocol.E(protocol.CodeDocumentLocked).
		With("holder", l.Holder).With("holderName", strOrNil(l.HolderName)).With("via", l.Via).
		With("expiresAt", protocol.FormatTime(l.ExpiresAt))
}

// NewDocument is the input of documents.new (also used by the importer).
type NewDocument struct {
	ID       string
	FolderID string
	FileName string
	Content  []byte
	Via      string
	Message  string
	// CreatedAt overrides the timestamps (importer: original dates). Zero = now.
	CreatedAt time.Time
	UpdatedAt time.Time
	uploadID  string
}

func (s *Service) documentsNew(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	o := validate.ParseObject(c.Payload, "", "id", "folderId", "fileName", "content", "contentUploadId", "checksum", "via", "message")
	id, _ := o.ULID("id", true)
	folderID, _ := o.ULID("folderId", false)
	via, _ := o.Enum("via", false, "human", "agent", "service")
	message, _ := o.String("message", false, validate.MaxLen(500))
	if err := o.Err(); err != nil {
		return nil, err
	}
	fileName, err := readFileName(o, "fileName", false)
	if err != nil {
		return nil, err
	}
	in, err := s.readContent(ctx, c.Caller, o)
	if err != nil {
		return nil, err
	}
	return s.CreateDocument(ctx, c, NewDocument{ID: id, FolderID: folderID, FileName: fileName, Content: in.raw,
		Via: via, Message: message, uploadID: in.uploadID})
}

// CreateDocument creates a document with version 1. Retrying with the same id and content
// returns the existing document; with another content, already_exists. Without FolderID the
// folder is generated from the relations; without FileName the name comes from the title.
func (s *Service) CreateDocument(ctx context.Context, c *Call, in NewDocument) (map[string]any, error) {
	p, err := s.prepare(ctx, in.Content, in.FileName, nil)
	if err != nil {
		return nil, err
	}
	if err := checkVisibilityChange(c.Caller, "", p.fm.Visibility); err != nil {
		return nil, err
	}
	via := c.Caller.DefaultVia(in.Via)
	idempotent := func(ctx context.Context, q store.Querier) (map[string]any, error) {
		d, err := getDoc(ctx, q, in.ID, false)
		if err != nil || d == nil {
			return nil, err
		}
		var v1 string
		if err := q.QueryRow(ctx, `SELECT checksum FROM document_versions WHERE document_id = $1 AND version = 1`, in.ID).Scan(&v1); err != nil &&
			!errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		// Only a retry of the same caller-visible creation gets the document back: an id of a
		// document the caller cannot read reveals nothing but that the id is taken.
		if personPerm(c.Caller, d.Visibility) >= acl.Read && v1 == p.checksum &&
			(in.FolderID == "" || d.FolderID == in.FolderID) && (in.FileName == "" || strings.EqualFold(d.FileName, in.FileName)) {
			return map[string]any{"id": d.ID, "version": 1, "path": d.Path, "folderId": d.FolderID, "fileName": d.FileName}, nil
		}
		return nil, protocol.Ef(protocol.CodeAlreadyExists, "Ya existe un documento con ese id y otro contenido")
	}
	tree, err := s.Tree(ctx)
	if err != nil {
		return nil, err
	}
	if in.FolderID != "" {
		if err := requireFolder(c.Caller, tree, in.FolderID, acl.Write); err != nil {
			return nil, err
		}
	}
	if out, err := idempotent(ctx, s.DB); out != nil || err != nil {
		return out, err
	}
	folderID := in.FolderID
	if folderID == "" {
		if folderID, err = s.generatedFolder(ctx, c, p.fm); err != nil {
			return nil, err
		}
	}
	title := p.title
	if in.FileName == "" && title == "" {
		title = "documento"
		p.title = title
	}
	key := storageKey(in.ID, 1, p.checksum)
	if err := s.Blob.Put(ctx, key, p.content, "text/markdown; charset=utf-8"); err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		fileName := in.FileName
		if fileName == "" {
			if fileName, err = s.freeFileName(ctx, folderID, title); err != nil {
				return nil, err
			}
		}
		out, err := s.insertDocument(ctx, c, in, p, folderID, fileName, key, via)
		if errors.Is(err, errRetryIdempotent) {
			return idempotent(ctx, s.DB)
		}
		if errors.Is(err, errNameTaken) {
			if in.FileName == "" && attempt < 10 {
				continue // a concurrent creation took the generated name: pick the next one
			}
			return nil, protocol.Ef(protocol.CodeAlreadyExists, "Ya existe un documento con ese nombre en la carpeta").With("fileName", fileName)
		}
		return out, err
	}
}

var errNameTaken = errors.New("file name taken in the folder")

func (s *Service) insertDocument(ctx context.Context, c *Call, in NewDocument, p *prepared, folderID, fileName, key, via string) (map[string]any, error) {
	var out map[string]any
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		ok, err := lockFolderShared(ctx, tx, folderID)
		if err != nil {
			return err
		}
		if !ok {
			return protocol.E(protocol.CodeFolderNotFound)
		}
		tree, err := treeTx(ctx, tx)
		if err != nil {
			return err
		}
		if err := requireFolder(c.Caller, tree, folderID, acl.Write); err != nil {
			return err
		}
		folder, _ := tree.Get(folderID)
		created, updated := s.now(), s.now()
		if !in.CreatedAt.IsZero() {
			created = in.CreatedAt.UTC().Truncate(time.Millisecond)
			updated = created
		}
		if !in.UpdatedAt.IsZero() {
			updated = in.UpdatedAt.UTC().Truncate(time.Millisecond)
		}
		path := folder.Path + fileName
		name := nameOf(c.Caller)
		_, err = tx.Exec(ctx, `INSERT INTO documents (id, folder_id, file_name, path, title, visibility, current_version, checksum,
			size_bytes, secret_warning, created_by, created_at, updated_by, updated_at, created_by_name, updated_by_name)
			VALUES ($1,$2,$3,$4,$5,$6,1,$7,$8,$9,$10,$11,$10,$12,$13,$13)`,
			in.ID, folderID, fileName, path, p.title, p.fm.Visibility, p.checksum, p.size, len(p.findings) > 0,
			c.Caller.ID, created, updated, name)
		if store.IsUniqueViolation(err, "documents_pkey") {
			return errRetryIdempotent
		}
		if store.IsUniqueViolation(err, "") {
			return errNameTaken
		}
		if err != nil {
			return err
		}
		if err := insertVersion(ctx, tx, versionInput{docID: in.ID, kind: KindCreate, version: 1, key: key, p: p, author: c.Caller.ID,
			authorName: name, via: via, message: in.Message, at: created}); err != nil {
			return err
		}
		if err := writeMeta(ctx, tx, in.ID, p); err != nil {
			return err
		}
		if err := s.consumeUpload(ctx, tx, &contentInput{uploadID: in.uploadID}); err != nil {
			return err
		}
		st, err := s.loadDocState(ctx, tx, in.ID)
		if err != nil {
			return err
		}
		em := s.emitter(tx, c.Caller, c.CorrelationID)
		changes := map[string]any{"version": fromTo(0, 1)}
		if in.Message != "" {
			changes["message"] = in.Message
		}
		if err := em.docEvent(ctx, "document.created", st, changes, via); err != nil {
			return err
		}
		out = map[string]any{"id": in.ID, "version": 1, "path": path, "folderId": folderID, "fileName": fileName}
		if w := p.warnings(); w != nil {
			out["warnings"] = w
		}
		return nil
	})
	return out, err
}

var errRetryIdempotent = errors.New("document id taken concurrently")

func (s *Service) documentsUpdate(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	id := c.Params["documentId"]
	o := validate.ParseObject(c.Payload, "", "baseVersion", "content", "contentUploadId", "checksum", "sessionId", "via", "message")
	base, _ := o.Int("baseVersion", true, 1, 1<<31-1)
	sessionID, _ := o.ULID("sessionId", false)
	via, _ := o.Enum("via", false, "human", "agent", "service")
	message, _ := o.String("message", false, validate.MaxLen(500))
	if err := o.Err(); err != nil {
		return nil, err
	}
	in, err := s.readContent(ctx, c.Caller, o)
	if err != nil {
		return nil, err
	}
	return s.UpdateDocument(ctx, c, id, int(base), in, sessionID, via, message)
}

// UpdateDocument writes a new version.
func (s *Service) UpdateDocument(ctx context.Context, c *Call, id string, base int, in *contentInput, sessionID, reqVia, message string) (map[string]any, error) {
	cur, err := getDoc(ctx, s.DB, id, false)
	if err != nil {
		return nil, err
	}
	if cur == nil || cur.DeletedAt != nil {
		return nil, protocol.E(protocol.CodeDocumentNotFound)
	}
	existing, err := loadRelations(ctx, s.DB, id)
	if err != nil {
		return nil, err
	}
	p, err := s.prepare(ctx, in.raw, cur.FileName, existing)
	if err != nil {
		return nil, err
	}
	via := c.Caller.DefaultVia(reqVia)
	newVersion := base + 1
	key := storageKey(id, newVersion, p.checksum)
	// The content goes to S3 before the transaction, under a key that includes the checksum: a
	// failed transaction leaves an orphan the janitor removes, never metadata without content.
	if p.checksum != cur.Checksum {
		if err := s.Blob.Put(ctx, key, p.content, "text/markdown; charset=utf-8"); err != nil {
			return nil, err
		}
	}
	var out map[string]any
	err = store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		d, err := getDoc(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if d == nil || d.DeletedAt != nil {
			return protocol.E(protocol.CodeDocumentNotFound)
		}
		if _, err := docWritePerm(c.Caller, d, acl.Write); err != nil {
			return err
		}
		if p.checksum == d.Checksum {
			out = map[string]any{"id": id, "version": d.Version, "path": d.Path}
			return nil
		}
		lock, err := loadLock(ctx, tx, id, s.now())
		if err != nil {
			return err
		}
		if err := checkLock(lock, sessionID, c.Caller.ID, false); err != nil {
			return err
		}
		if base != d.Version {
			return protocol.E(protocol.CodeVersionConflict).With("currentVersion", d.Version)
		}
		if err := checkVisibilityChange(c.Caller, d.Visibility, p.fm.Visibility); err != nil {
			return err
		}
		before, err := s.loadDocState(ctx, tx, id)
		if err != nil {
			return err
		}
		now := s.now()
		name := nameOf(c.Caller)
		if err := insertVersion(ctx, tx, versionInput{docID: id, kind: KindUpdate, version: newVersion, key: key, p: p, author: c.Caller.ID,
			authorName: name, via: via, sessionID: sessionID, message: message, at: now}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE documents SET title = $2, visibility = $3, current_version = $4, checksum = $5,
			size_bytes = $6, secret_warning = $7, updated_by = $8, updated_at = $9, updated_by_name = $10 WHERE id = $1`,
			id, p.title, p.fm.Visibility, newVersion, p.checksum, p.size, len(p.findings) > 0, c.Caller.ID, now, name); err != nil {
			return err
		}
		em := s.emitter(tx, c.Caller, c.CorrelationID)
		if p.fm.Visibility == markdown.VisibilityRestricted && d.Visibility != markdown.VisibilityRestricted {
			if err := s.revokeDocumentShares(ctx, tx, em, d, c.Caller.ID, now, RevokeRestricted); err != nil {
				return err
			}
		}
		if err := writeMeta(ctx, tx, id, p); err != nil {
			return err
		}
		if err := s.consumeUpload(ctx, tx, in); err != nil {
			return err
		}
		after, err := s.loadDocState(ctx, tx, id)
		if err != nil {
			return err
		}
		changes := versionChanges(before, after, message)
		if err := em.docEvent(ctx, "document.updated", after, changes, via); err != nil {
			return err
		}
		out = map[string]any{"id": id, "version": newVersion, "path": d.Path}
		if w := p.warnings(); w != nil {
			out["warnings"] = w
		}
		return nil
	})
	return out, err
}

// UpdateDocumentContent is documents.{id}.update with inline content and no edit session, for
// in-process callers (the importer). base is the version the content was built on.
func (s *Service) UpdateDocumentContent(ctx context.Context, c *Call, id string, base int, content []byte, via, message string) (map[string]any, error) {
	return s.UpdateDocument(ctx, c, id, base, &contentInput{raw: content}, "", via, message)
}

func versionChanges(before, after *docState, message string) map[string]any {
	changes := map[string]any{"version": fromTo(before.doc.Version, after.doc.Version)}
	if before.doc.Title != after.doc.Title {
		changes["title"] = fromTo(before.doc.Title, after.doc.Title)
	}
	if before.doc.Visibility != after.doc.Visibility {
		changes["visibility"] = fromTo(before.doc.Visibility, after.doc.Visibility)
	}
	if r := diffRelations(before.relations, after.relations); r != nil {
		changes["relations"] = r
	}
	if t := diffStrings(before.topics, after.topics); t != nil {
		changes["topics"] = t
	}
	if message != "" {
		changes["message"] = message
	}
	return changes
}

func (s *Service) documentsMove(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	id := c.Params["documentId"]
	o := validate.ParseObject(c.Payload, "", "baseVersion", "folderId", "fileName", "sessionId")
	base, _ := o.Int("baseVersion", true, 1, 1<<31-1)
	folderID, hasFolder := o.ULID("folderId", false)
	sessionID, _ := o.ULID("sessionId", false)
	if err := o.Err(); err != nil {
		return nil, err
	}
	fileName, err := readFileName(o, "fileName", false)
	if err != nil {
		return nil, err
	}
	if !hasFolder && fileName == "" {
		return nil, protocol.InvalidField("payload", nil, []string{"folderId", "fileName"}, "Indicá folderId, fileName o los dos")
	}
	var out map[string]any
	err = store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		d, err := getDoc(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if d == nil || d.DeletedAt != nil {
			return protocol.E(protocol.CodeDocumentNotFound)
		}
		tree, err := treeTx(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := docWritePerm(c.Caller, d, acl.Write); err != nil {
			return err
		}
		target := d.FolderID
		if hasFolder {
			target = folderID
			if err := requireFolder(c.Caller, tree, target, acl.Write); err != nil {
				return err
			}
			if ok, err := lockFolderShared(ctx, tx, target); err != nil || !ok {
				if err == nil {
					err = protocol.E(protocol.CodeFolderNotFound)
				}
				return err
			}
		}
		name := d.FileName
		if fileName != "" {
			name = fileName
		}
		lock, err := loadLock(ctx, tx, id, s.now())
		if err != nil {
			return err
		}
		if err := checkLock(lock, sessionID, c.Caller.ID, false); err != nil {
			return err
		}
		if int(base) != d.Version {
			return protocol.E(protocol.CodeVersionConflict).With("currentVersion", d.Version)
		}
		folder, _ := tree.Get(target)
		path := folder.Path + name
		out = map[string]any{"id": id, "version": d.Version, "path": path}
		if target == d.FolderID && name == d.FileName {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE documents SET folder_id = $2, file_name = $3, path = $4 WHERE id = $1`, id, target, name, path)
		if store.IsUniqueViolation(err, "") {
			return protocol.Ef(protocol.CodeAlreadyExists, "Ya existe un documento con ese nombre en la carpeta destino")
		}
		if err != nil {
			return err
		}
		st, err := s.loadDocState(ctx, tx, id)
		if err != nil {
			return err
		}
		changes := map[string]any{"path": fromTo(d.Path, path)}
		if target != d.FolderID {
			changes["folderId"] = fromTo(d.FolderID, target)
		}
		em := s.emitter(tx, c.Caller, c.CorrelationID)
		return em.docEvent(ctx, "document.moved", st, changes, "")
	})
	return out, err
}

func (s *Service) documentsDelete(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	id := c.Params["documentId"]
	o := validate.ParseObject(c.Payload, "", "baseVersion")
	base, _ := o.Int("baseVersion", true, 1, 1<<31-1)
	if err := o.Err(); err != nil {
		return nil, err
	}
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		d, err := getDoc(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if d == nil {
			return protocol.E(protocol.CodeDocumentNotFound)
		}
		if _, err := docWritePerm(c.Caller, d, acl.Write); err != nil {
			return err
		}
		if d.DeletedAt != nil {
			return nil // already deleted: success
		}
		lock, err := loadLock(ctx, tx, id, s.now())
		if err != nil {
			return err
		}
		if err := checkLock(lock, "", c.Caller.ID, true); err != nil {
			return err
		}
		if int(base) != d.Version {
			return protocol.E(protocol.CodeVersionConflict).With("currentVersion", d.Version)
		}
		now := s.now()
		if _, err := tx.Exec(ctx, `UPDATE documents SET deleted_at = $2, deleted_by = $3 WHERE id = $1`, id, now, c.Caller.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM document_locks WHERE document_id = $1`, id); err != nil {
			return err
		}
		em := s.emitter(tx, c.Caller, c.CorrelationID)
		if err := s.revokeDocumentShares(ctx, tx, em, d, c.Caller.ID, now, RevokeDocumentDeleted); err != nil {
			return err
		}
		st, err := s.loadDocState(ctx, tx, id)
		if err != nil {
			return err
		}
		return em.docEvent(ctx, "document.deleted", st, nil, "")
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id}, nil
}

func (s *Service) documentsRestore(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	id := c.Params["documentId"]
	o := validate.ParseObject(c.Payload, "", "fromVersion", "baseVersion")
	from, _ := o.Int("fromVersion", true, 1, 1<<31-1)
	base, _ := o.Int("baseVersion", true, 1, 1<<31-1)
	if err := o.Err(); err != nil {
		return nil, err
	}
	cur, err := getDoc(ctx, s.DB, id, false)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, protocol.E(protocol.CodeDocumentNotFound)
	}
	if _, err := docWritePerm(c.Caller, cur, acl.Write); err != nil {
		return nil, err
	}
	var srcKey string
	err = s.DB.QueryRow(ctx, `SELECT storage_key FROM document_versions WHERE document_id = $1 AND version = $2`, id, from).Scan(&srcKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, missingVersion(ctx, s.DB, id, cur.Version, int(from))
	}
	if err != nil {
		return nil, err
	}
	raw, err := s.Blob.Get(ctx, srcKey, MaxDocumentBytes)
	if errors.Is(err, blob.ErrNotFound) {
		// The compaction removed it between the read of the row and the read of the object.
		return nil, missingVersion(ctx, s.DB, id, cur.Version, int(from))
	}
	if err != nil {
		return nil, err
	}
	existing, err := loadRelations(ctx, s.DB, id)
	if err != nil {
		return nil, err
	}
	p, err := s.prepare(ctx, raw, cur.FileName, existing)
	if err != nil {
		return nil, err
	}
	newVersion := int(base) + 1
	key := storageKey(id, newVersion, p.checksum)
	message := "Restaurado desde la versión " + itoa(int(from))
	if err := s.Blob.Put(ctx, key, p.content, "text/markdown; charset=utf-8"); err != nil {
		return nil, err
	}
	var out map[string]any
	err = store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		d, err := getDoc(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if _, err := docWritePerm(c.Caller, d, acl.Write); err != nil {
			return err
		}
		lock, err := loadLock(ctx, tx, id, s.now())
		if err != nil {
			return err
		}
		if err := checkLock(lock, "", c.Caller.ID, true); err != nil {
			return err
		}
		if int(base) != d.Version {
			return protocol.E(protocol.CodeVersionConflict).With("currentVersion", d.Version)
		}
		// With the row locked the compaction cannot run: if the version is still there now, the
		// content read above is that version's.
		var still bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM document_versions WHERE document_id = $1 AND version = $2)`,
			id, from).Scan(&still); err != nil {
			return err
		}
		if !still {
			return missingVersion(ctx, tx, id, d.Version, int(from))
		}
		if err := checkVisibilityChange(c.Caller, d.Visibility, p.fm.Visibility); err != nil {
			return err
		}
		wasDeleted := d.DeletedAt != nil
		if wasDeleted {
			if ok, err := lockFolderShared(ctx, tx, d.FolderID); err != nil || !ok {
				if err == nil {
					err = protocol.Ef(protocol.CodeFolderNotFound, "La carpeta del documento ya no existe")
				}
				return err
			}
		}
		before, err := s.loadDocState(ctx, tx, id)
		if err != nil {
			return err
		}
		now := s.now()
		name := nameOf(c.Caller)
		if err := insertVersion(ctx, tx, versionInput{docID: id, kind: KindRestore, version: newVersion, key: key, p: p, author: c.Caller.ID,
			authorName: name, via: c.Caller.DefaultVia(""), message: message, at: now}); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE documents SET title = $2, visibility = $3, current_version = $4, checksum = $5,
			size_bytes = $6, secret_warning = $7, updated_by = $8, updated_at = $9, deleted_at = NULL, deleted_by = NULL,
			updated_by_name = $10 WHERE id = $1`,
			id, p.title, p.fm.Visibility, newVersion, p.checksum, p.size, len(p.findings) > 0, c.Caller.ID, now, name)
		if store.IsUniqueViolation(err, "") {
			return protocol.Ef(protocol.CodeAlreadyExists, "Ya existe otro documento con ese nombre en la carpeta")
		}
		if err != nil {
			return err
		}
		if err := writeMeta(ctx, tx, id, p); err != nil {
			return err
		}
		after, err := s.loadDocState(ctx, tx, id)
		if err != nil {
			return err
		}
		changes := versionChanges(before, after, message)
		changes["restoredFrom"] = from
		em := s.emitter(tx, c.Caller, c.CorrelationID)
		if p.fm.Visibility == markdown.VisibilityRestricted && d.Visibility != markdown.VisibilityRestricted {
			if err := s.revokeDocumentShares(ctx, tx, em, d, c.Caller.ID, now, RevokeRestricted); err != nil {
				return err
			}
		}
		if err := em.docEvent(ctx, "document.restored", after, changes, ""); err != nil {
			return err
		}
		out = map[string]any{"id": id, "version": newVersion, "path": d.Path}
		if w := p.warnings(); w != nil {
			out["warnings"] = w
		}
		return nil
	})
	return out, err
}

func (s *Service) documentsLock(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	id := c.Params["documentId"]
	o := validate.ParseObject(c.Payload, "", "sessionId", "via", "ttlSeconds")
	sessionID, _ := o.ULID("sessionId", true)
	via, _ := o.Enum("via", true, "human", "agent")
	ttl := int64(120)
	if o.Has("ttlSeconds") {
		ttl, _ = o.Int("ttlSeconds", true, 30, 300)
	}
	if err := o.Err(); err != nil {
		return nil, err
	}
	var out map[string]any
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		d, err := getDoc(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if d == nil || d.DeletedAt != nil {
			return protocol.E(protocol.CodeDocumentNotFound)
		}
		if _, err := docWritePerm(c.Caller, d, acl.Write); err != nil {
			return err
		}
		now := s.now()
		expires := now.Add(time.Duration(ttl) * time.Second)
		var existing lockRow
		err = tx.QueryRow(ctx, `SELECT session_id, holder, holder_name, via, expires_at FROM document_locks WHERE document_id = $1`, id).
			Scan(&existing.SessionID, &existing.Holder, &existing.HolderName, &existing.Via, &existing.ExpiresAt)
		hasRow := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		em := s.emitter(tx, c.Caller, c.CorrelationID)
		out = map[string]any{"id": id, "sessionId": sessionID, "expiresAt": protocol.FormatTime(expires)}
		if hasRow && existing.ExpiresAt.After(now) {
			if existing.SessionID == sessionID && existing.Holder == c.Caller.ID {
				_, err := tx.Exec(ctx, `UPDATE document_locks SET expires_at = $2, via = $3, holder_name = $4 WHERE document_id = $1`,
					id, expires, via, nameOf(c.Caller))
				return err // renewal: no event
			}
			return checkLock(&existing, "", "", false)
		}
		if hasRow {
			// An expired lock nobody swept yet: record its end before taking over.
			if _, err := tx.Exec(ctx, `DELETE FROM document_locks WHERE document_id = $1`, id); err != nil {
				return err
			}
			st, err := s.loadDocState(ctx, tx, id)
			if err != nil {
				return err
			}
			if err := em.docEvent(ctx, "document.unlocked", st, map[string]any{"reason": "expired",
				"lock": fromTo(existing.view(), nil)}, existing.Via); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO document_locks (document_id, session_id, holder, holder_name, via, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6)`, id, sessionID, c.Caller.ID, nameOf(c.Caller), via, expires); err != nil {
			return err
		}
		st, err := s.loadDocState(ctx, tx, id)
		if err != nil {
			return err
		}
		return em.docEvent(ctx, "document.locked", st, nil, via)
	})
	return out, err
}

func (s *Service) documentsUnlock(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	id := c.Params["documentId"]
	o := validate.ParseObject(c.Payload, "", "sessionId", "force")
	sessionID, _ := o.ULID("sessionId", true)
	force := o.Bool("force", false)
	if err := o.Err(); err != nil {
		return nil, err
	}
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		d, err := getDoc(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if d == nil || d.DeletedAt != nil {
			return protocol.E(protocol.CodeDocumentNotFound)
		}
		if _, err := docWritePerm(c.Caller, d, acl.Write); err != nil {
			return err
		}
		if force && !c.Caller.SeesRestricted() {
			return protocol.Ef(protocol.CodePermissionDenied, "Forzar el desbloqueo es solo para administradores")
		}
		lock, err := loadLock(ctx, tx, id, s.now())
		if err != nil {
			return err
		}
		own := lock != nil && lock.SessionID == sessionID && lock.Holder == c.Caller.ID
		if !own && !force {
			return protocol.E(protocol.CodeLockNotHeld)
		}
		if lock == nil {
			return nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM document_locks WHERE document_id = $1`, id); err != nil {
			return err
		}
		reason := "released"
		if !own {
			reason = "forced"
		}
		st, err := s.loadDocState(ctx, tx, id)
		if err != nil {
			return err
		}
		em := s.emitter(tx, c.Caller, c.CorrelationID)
		return em.docEvent(ctx, "document.unlocked", st, map[string]any{"reason": reason, "lock": fromTo(lock.view(), nil)}, lock.Via)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id}, nil
}

func (s *Service) documentsRequestContentUpload(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	o := validate.ParseObject(c.Payload, "", "checksum", "size")
	checksum, _ := o.String("checksum", true, validate.Pattern(checksumRe))
	size, _ := o.Int("size", true, 1, 1<<40)
	if err := o.Err(); err != nil {
		return nil, err
	}
	if size > MaxDocumentBytes {
		return nil, protocol.E(protocol.CodeContentTooLarge).With("maxBytes", MaxDocumentBytes)
	}
	id := protocol.NewULID()
	key := "uploads/" + id
	now := s.now()
	if _, err := s.DB.Exec(ctx, `INSERT INTO content_uploads (id, checksum, size_bytes, storage_key, requested_by, created_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, checksum, size, key, c.Caller.ID, now, now.Add(UploadTTL)); err != nil {
		return nil, err
	}
	u, err := s.Blob.PresignPut(ctx, key, PresignTTL)
	if err != nil {
		return nil, err
	}
	return map[string]any{"contentUploadId": id, "uploadUrl": u, "expiresIn": int(PresignTTL.Seconds())}, nil
}
