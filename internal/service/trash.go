package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gravadigital/jiku-docs/internal/blob"
	"github.com/gravadigital/jiku-docs/internal/store"
)

// The trash (docs/history-compaction-v1.md, section 4): every content object the service
// removes — versions compacted by the history compaction, orphans of the weekly sweep, the
// versions of a document purged 30 days after its deletion — is first copied to
// papelera/<original key> and only then deleted. A lifecycle rule expires the trash after
// DOCS_TRASH_RETENTION_DAYS; until then `jiku-docs storage restore-from-trash` brings it back.

// Sources of the objects moved to the trash (label of the metric).
const (
	TrashCompaction = "compaction"
	TrashOrphans    = "orphans"
	TrashPurge      = "deleted-documents"
)

// trashCopyWorkers is how many CopyObject calls run at once (S3 has no batch copy; the
// originals are then deleted with one DeleteObjects per batch).
const trashCopyWorkers = 16

// versionRowMeta is the user-metadata key of a trash copy that carries its version row.
const versionRowMeta = "jiku-version-row"

// maxVersionRowMeta keeps the metadata well under the 2 KB of user metadata S3 accepts.
const maxVersionRowMeta = 1800

type trashObject struct {
	Key  string
	Meta map[string]string // nil: the copy keeps the source's metadata
}

// moveToTrash moves objects to papelera/: the copies run in parallel, then the originals that
// were copied are deleted with DeleteObjects (MaxDeleteBatch keys per call). It returns the
// keys that are done — in the trash with the original gone, or the original already missing —
// and how many objects it moved now. A key that is not done is left as it was (maybe with a
// trash copy already made): calling again for it is safe.
func (s *Service) moveToTrash(ctx context.Context, source string, objs []trashObject) (done []string, moved int, err error) {
	for start := 0; start < len(objs); start += blob.MaxDeleteBatch {
		end := min(start+blob.MaxDeleteBatch, len(objs))
		d, m, berr := s.moveBatch(ctx, objs[start:end])
		done, moved = append(done, d...), moved+m
		if berr != nil {
			err = berr
			break
		}
	}
	if moved > 0 && s.Hooks.Trashed != nil {
		s.Hooks.Trashed(source, moved)
	}
	return done, moved, err
}

func (s *Service) moveBatch(ctx context.Context, objs []trashObject) ([]string, int, error) {
	if len(objs) == 0 {
		return nil, 0, nil
	}
	status := make([]error, len(objs))
	sem := make(chan struct{}, trashCopyWorkers)
	var wg sync.WaitGroup
	for i, o := range objs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			status[i] = s.Blob.CopyToTrash(ctx, o.Key, o.Meta)
		}()
	}
	wg.Wait()
	var done, copied []string
	var first error
	failed := 0
	for i, o := range objs {
		switch {
		case status[i] == nil:
			copied = append(copied, o.Key)
		case errors.Is(status[i], blob.ErrNotFound):
			done = append(done, o.Key) // the original is gone: nothing left to move
		default:
			failed++
			if first == nil {
				first = status[i]
			}
		}
	}
	gone, derr := s.Blob.DeleteMany(ctx, copied)
	done = append(done, gone...)
	if derr != nil {
		return done, len(gone), derr
	}
	if first != nil {
		return done, len(gone), fmt.Errorf("%d of %d objects not copied to the trash: %w", failed, len(objs), first)
	}
	return done, len(gone), nil
}

// versionRow is the row of a compacted version, kept in pruned_objects and carried by the trash
// copy so the version can be put back in the history.
type versionRow struct {
	DocumentID string    `json:"documentId"`
	Version    int       `json:"version"`
	Checksum   string    `json:"checksum"`
	SizeBytes  int64     `json:"sizeBytes"`
	Title      string    `json:"title,omitempty"`
	Author     string    `json:"author"`
	Via        string    `json:"via"`
	SessionID  *string   `json:"sessionId,omitempty"`
	Message    *string   `json:"message,omitempty"`
	Kind       string    `json:"kind"`
	CreatedAt  time.Time `json:"createdAt"`
}

// versionRowSQL builds the versionRow of a document_versions row (RETURNING of the compaction).
const versionRowSQL = `jsonb_build_object('documentId', document_id, 'version', version, 'checksum', checksum,
	'sizeBytes', size_bytes, 'title', title, 'author', author, 'via', via, 'sessionId', session_id, 'message', message,
	'kind', kind, 'createdAt', created_at)`

// encodeVersionRow is the user metadata of a trash copy: the row as base64 JSON (titles are
// not ASCII). Too large for S3 metadata, it drops the title and the session; a restore then
// takes the document's current title.
func encodeVersionRow(raw []byte) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var r versionRow
	if err := json.Unmarshal(raw, &r); err != nil || r.DocumentID == "" {
		return nil
	}
	for range 2 {
		b, _ := json.Marshal(r)
		enc := base64.StdEncoding.EncodeToString(b)
		if len(enc) <= maxVersionRowMeta {
			return map[string]string{versionRowMeta: enc}
		}
		r.Title, r.SessionID, r.Message = "", nil, nil
	}
	return nil
}

func decodeVersionRow(meta map[string]string) (*versionRow, error) {
	enc := meta[versionRowMeta]
	if enc == "" {
		return nil, errors.New("the trash copy carries no version row: it was not removed by the history compaction (orphan sweep or purge of a deleted document), so there is no version to put back")
	}
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, fmt.Errorf("version row of the trash copy: %w", err)
	}
	var r versionRow
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("version row of the trash copy: %w", err)
	}
	if r.DocumentID == "" || r.Version < 1 || r.Author == "" || r.Via == "" {
		return nil, errors.New("version row of the trash copy is incomplete")
	}
	return &r, nil
}

// TrashRestore reports what RestoreFromTrash did.
type TrashRestore struct {
	Key string `json:"key"`
	// ObjectRestored: the object was copied back (false: it already existed, left untouched).
	ObjectRestored bool `json:"objectRestored"`
	// DocumentID and Version of the version row, when one was asked for.
	DocumentID string `json:"documentId,omitempty"`
	Version    int    `json:"version,omitempty"`
	// RowRestored: the version is back in the history (false: the row already existed).
	RowRestored bool `json:"rowRestored"`
}

// ErrNotInTrash: the object is not in papelera/ (never was, or the lifecycle expired it).
var ErrNotInTrash = errors.New("the object is not in the trash (never removed, or expired after DOCS_TRASH_RETENTION_DAYS)")

// RestoreFromTrash copies papelera/<key> back to key (never over an existing object; the trash
// copy stays until it expires). With versionRow it also puts the compacted version back in
// document_versions, from the row the trash copy carries, marked protected so the next
// compaction does not remove it again. The document must exist, not be deleted, and the version
// must be older than the current one. The order — object first, row after — never leaves a row
// without its object.
func (s *Service) RestoreFromTrash(ctx context.Context, key string, versionRow bool) (*TrashRestore, error) {
	key = strings.TrimPrefix(strings.TrimSpace(key), blob.TrashPrefix)
	if key == "" || strings.HasPrefix(key, "/") {
		return nil, errors.New("a key of the bucket is required, for example docs/<documentId>/<version>-<sha256>.md")
	}
	meta, restored, err := s.Blob.RestoreFromTrash(ctx, key)
	if errors.Is(err, blob.ErrNotFound) {
		return nil, ErrNotInTrash
	}
	if err != nil {
		return nil, err
	}
	out := &TrashRestore{Key: key, ObjectRestored: restored}
	if !versionRow {
		return out, nil
	}
	r, err := decodeVersionRow(meta)
	if err != nil {
		return out, err
	}
	out.DocumentID, out.Version = r.DocumentID, r.Version
	err = store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		var current int
		var deleted *time.Time
		var title string
		err := tx.QueryRow(ctx, `SELECT current_version, deleted_at, title FROM documents WHERE id = $1 FOR UPDATE`, r.DocumentID).
			Scan(&current, &deleted, &title)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("document %s no longer exists: only the object was restored", r.DocumentID)
		}
		if err != nil {
			return err
		}
		if deleted != nil {
			return fmt.Errorf("document %s is deleted: restore it first, then run this again", r.DocumentID)
		}
		if r.Version >= current {
			return fmt.Errorf("version %d is not older than the current one (%d)", r.Version, current)
		}
		var existing *string
		err = tx.QueryRow(ctx, `SELECT storage_key FROM document_versions WHERE document_id = $1 AND version = $2`,
			r.DocumentID, r.Version).Scan(&existing)
		if err == nil {
			if *existing != key {
				return fmt.Errorf("version %d of %s already exists with another object (%s)", r.Version, r.DocumentID, *existing)
			}
			return nil // already back: idempotent
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if r.Title == "" {
			r.Title = title
		}
		if r.Kind == "" {
			r.Kind = KindUpdate
		}
		if _, err := tx.Exec(ctx, `INSERT INTO document_versions (document_id, version, storage_key, checksum, size_bytes, title,
				author, via, session_id, message, created_at, kind, protected)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, true)`,
			r.DocumentID, r.Version, key, r.Checksum, r.SizeBytes, r.Title, r.Author, r.Via, r.SessionID, r.Message,
			r.CreatedAt, r.Kind); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE documents SET pruned_versions_count = GREATEST(pruned_versions_count - 1, 0) WHERE id = $1`,
			r.DocumentID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM pruned_objects WHERE storage_key = $1`, key); err != nil {
			return err
		}
		out.RowRestored = true
		return nil
	})
	return out, err
}
