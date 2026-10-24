package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gravadigital/jiku-docs/internal/coreapi"
	"github.com/gravadigital/jiku-docs/internal/markdown"
	"github.com/gravadigital/jiku-docs/internal/store"
)

// Retention windows of the purges.
const (
	DeletedRetention     = 30 * 24 * time.Hour
	PendingAttachmentTTL = 24 * time.Hour
	ShareAccessRetention = 180 * 24 * time.Hour
	OrphanGrace          = time.Hour
	RenderRetention      = 24 * time.Hour
)

// ExpireLocks releases the locks past their expiry, emitting document.unlocked for each.
func (s *Service) ExpireLocks(ctx context.Context) (int, error) {
	now := s.now()
	n := 0
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT l.document_id, l.session_id, l.holder, l.via, l.expires_at FROM document_locks l
			WHERE l.expires_at <= $1 ORDER BY l.document_id FOR UPDATE SKIP LOCKED`, now)
		if err != nil {
			return err
		}
		type expired struct {
			doc  string
			lock lockRow
		}
		var list []expired
		for rows.Next() {
			var e expired
			if err := rows.Scan(&e.doc, &e.lock.SessionID, &e.lock.Holder, &e.lock.Via, &e.lock.ExpiresAt); err != nil {
				rows.Close()
				return err
			}
			list = append(list, e)
		}
		rows.Close()
		for _, e := range list {
			if _, err := tx.Exec(ctx, `DELETE FROM document_locks WHERE document_id = $1 AND session_id = $2`, e.doc, e.lock.SessionID); err != nil {
				return err
			}
			st, err := s.loadDocState(ctx, tx, e.doc)
			if err != nil {
				return err
			}
			if st.doc == nil {
				continue
			}
			em := s.emitter(tx, s.SelfCaller(), "")
			if err := em.docEvent(ctx, "document.unlocked", st, map[string]any{"reason": "expired",
				"lock": fromTo(e.lock.view(), nil)}, "service"); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// ActiveLocks counts live locks, for metrics.
func (s *Service) ActiveLocks(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM document_locks WHERE expires_at > now()`).Scan(&n)
	return n, err
}

// PurgeDeleted removes for good the documents deleted more than 30 days ago, with their
// versions and their objects.
func (s *Service) PurgeDeleted(ctx context.Context) (int, error) {
	cutoff := s.now().Add(-DeletedRetention)
	rows, err := s.DB.Query(ctx, `SELECT id FROM documents WHERE deleted_at IS NOT NULL AND deleted_at < $1 LIMIT 500`, cutoff)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	n := 0
	for _, id := range ids {
		var keys []string
		err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
			var still bool
			if err := tx.QueryRow(ctx, `SELECT deleted_at IS NOT NULL AND deleted_at < $2 FROM documents WHERE id = $1 FOR UPDATE`, id, cutoff).Scan(&still); err != nil {
				return err
			}
			if !still {
				return nil
			}
			krows, err := tx.Query(ctx, `SELECT storage_key FROM document_versions WHERE document_id = $1`, id)
			if err != nil {
				return err
			}
			for krows.Next() {
				var k string
				if err := krows.Scan(&k); err != nil {
					krows.Close()
					return err
				}
				keys = append(keys, k)
			}
			krows.Close()
			_, err = tx.Exec(ctx, `DELETE FROM documents WHERE id = $1`, id)
			return err
		})
		if err != nil {
			return n, err
		}
		for _, k := range keys {
			if err := s.Blob.Delete(ctx, k); err != nil {
				s.Log.Warn("purge: object not deleted (the orphan sweep retries)", "document", id, "err", err.Error())
			}
		}
		n++
	}
	return n, nil
}

// PurgeUploads removes expired or consumed content uploads and their objects.
func (s *Service) PurgeUploads(ctx context.Context) (int, error) {
	now := s.now()
	rows, err := s.DB.Query(ctx, `SELECT id, storage_key FROM content_uploads WHERE expires_at < $1 OR consumed_at < $2 LIMIT 1000`,
		now, now.Add(-time.Hour))
	if err != nil {
		return 0, err
	}
	type up struct{ id, key string }
	var list []up
	for rows.Next() {
		var u up
		if err := rows.Scan(&u.id, &u.key); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, u)
	}
	rows.Close()
	for _, u := range list {
		if err := s.Blob.Delete(ctx, u.key); err != nil {
			return 0, err
		}
		if _, err := s.DB.Exec(ctx, `DELETE FROM content_uploads WHERE id = $1`, u.id); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

// PurgePendingAttachments removes attachments never confirmed within 24 h.
func (s *Service) PurgePendingAttachments(ctx context.Context) (int64, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM attachments WHERE status = 'pending' AND created_at < $1`, s.now().Add(-PendingAttachmentTTL))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// PurgeShareData erases retry ciphers older than 5 minutes and accesses older than 180 days.
func (s *Service) PurgeShareData(ctx context.Context) error {
	now := s.now()
	if _, err := s.DB.Exec(ctx, `UPDATE shares SET token_retry_cipher = NULL WHERE token_retry_cipher IS NOT NULL AND created_at < $1`,
		now.Add(-ShareRetryTTL)); err != nil {
		return err
	}
	_, err := s.DB.Exec(ctx, `DELETE FROM share_accesses WHERE accessed_at < $1`, now.Add(-ShareAccessRetention))
	return err
}

// PurgeChangelog removes published events older than the feed retention and records the
// watermark that makes an older `sinceSeq` answer invalid_cursor.
func (s *Service) PurgeChangelog(ctx context.Context) (int64, error) {
	var n int64
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		var maxSeq *int64
		if err := tx.QueryRow(ctx, `SELECT max(seq) FROM changelog WHERE occurred_at < $1 AND published_at IS NOT NULL`,
			s.now().Add(-ChangelogRetention)).Scan(&maxSeq); err != nil {
			return err
		}
		if maxSeq == nil {
			return nil
		}
		tag, err := tx.Exec(ctx, `DELETE FROM changelog WHERE seq <= $1 AND published_at IS NOT NULL`, *maxSeq)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		val, _ := json.Marshal(map[string]any{"seq": *maxSeq})
		_, err = tx.Exec(ctx, `INSERT INTO service_state (key, value, updated_at) VALUES ('changelog_purged_through', $1, now())
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, val)
		return err
	})
	return n, err
}

// PurgeOrphans deletes objects no metadata points to: document contents of failed transactions,
// uploads without a row, and rendered share pages older than a day.
func (s *Service) PurgeOrphans(ctx context.Context) (int, error) {
	now := s.now()
	n := 0
	err := s.Blob.List(ctx, "docs/", func(key string, modified time.Time) error {
		if now.Sub(modified) < OrphanGrace {
			return nil
		}
		var used bool
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM document_versions WHERE storage_key = $1)`, key).Scan(&used); err != nil {
			return err
		}
		if !used {
			n++
			return s.Blob.Delete(ctx, key)
		}
		return nil
	})
	if err != nil {
		return n, err
	}
	err = s.Blob.List(ctx, "uploads/", func(key string, modified time.Time) error {
		if now.Sub(modified) < UploadTTL+OrphanGrace {
			return nil
		}
		var used bool
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM content_uploads WHERE storage_key = $1)`, key).Scan(&used); err != nil {
			return err
		}
		if !used {
			n++
			return s.Blob.Delete(ctx, key)
		}
		return nil
	})
	if err != nil {
		return n, err
	}
	err = s.Blob.List(ctx, "renders/", func(key string, modified time.Time) error {
		if now.Sub(modified) > RenderRetention {
			n++
			return s.Blob.Delete(ctx, key)
		}
		return nil
	})
	return n, err
}

// fresh is implemented by the cached core client: the review must see deletions immediately.
type fresh interface {
	EntitiesFresh(ctx context.Context, entityType string, ids []int64) (map[int64]coreapi.Entity, error)
}

// ReviewRelations checks every relation against core and removes the ones pointing to
// entities that no longer exist. Since the frontmatter is the source of truth of relations,
// the removal is a new version of the document, written by the service and recorded in its
// history. Locked documents are skipped and retried next time.
func (s *Service) ReviewRelations(ctx context.Context) (int, error) {
	lookup := s.Core.Entities
	if f, ok := s.Core.(fresh); ok {
		lookup = f.EntitiesFresh
	}
	missing := map[string]map[int64]bool{}
	for _, k := range markdown.RelationKinds {
		rows, err := s.DB.Query(ctx, `SELECT DISTINCT r.entity_id FROM document_relations r JOIN documents d ON d.id = r.document_id
			WHERE d.deleted_at IS NULL AND r.entity_type = $1`, k.EntityType)
		if err != nil {
			return 0, err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return 0, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if len(ids) == 0 {
			continue
		}
		found, err := lookup(ctx, k.EntityType, ids)
		if err != nil {
			return 0, err
		}
		for _, id := range ids {
			if _, ok := found[id]; !ok {
				if missing[k.Key] == nil {
					missing[k.Key] = map[int64]bool{}
				}
				missing[k.Key][id] = true
			}
		}
	}
	if len(missing) == 0 {
		return 0, nil
	}
	// Documents that reference any missing entity.
	byDoc := map[string]map[string][]int64{}
	for _, k := range markdown.RelationKinds {
		if len(missing[k.Key]) == 0 {
			continue
		}
		ids := make([]int64, 0, len(missing[k.Key]))
		for id := range missing[k.Key] {
			ids = append(ids, id)
		}
		rows, err := s.DB.Query(ctx, `SELECT r.document_id, r.entity_id FROM document_relations r JOIN documents d ON d.id = r.document_id
			WHERE d.deleted_at IS NULL AND r.entity_type = $1 AND r.entity_id = ANY($2)`, k.EntityType, ids)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var doc string
			var id int64
			if err := rows.Scan(&doc, &id); err != nil {
				rows.Close()
				return 0, err
			}
			if byDoc[doc] == nil {
				byDoc[doc] = map[string][]int64{}
			}
			byDoc[doc][k.Key] = append(byDoc[doc][k.Key], id)
		}
		rows.Close()
	}
	docIDs := make([]string, 0, len(byDoc))
	for id := range byDoc {
		docIDs = append(docIDs, id)
	}
	sort.Strings(docIDs)
	self := s.SelfCaller()
	n := 0
	for _, id := range docIDs {
		remove := byDoc[id]
		d, err := getDoc(ctx, s.DB, id, false)
		if err != nil || d == nil {
			continue
		}
		if l, _ := loadLock(ctx, s.DB, id, s.now()); l != nil {
			s.Log.Info("relation review: document locked, retrying next run", "document", id)
			continue
		}
		var key string
		if err := s.DB.QueryRow(ctx, `SELECT storage_key FROM document_versions WHERE document_id = $1 AND version = $2`, id, d.Version).Scan(&key); err != nil {
			return n, err
		}
		raw, err := s.Blob.Get(ctx, key, MaxDocumentBytes)
		if err != nil {
			return n, err
		}
		edited, err := markdown.RemoveRelations(string(raw), remove)
		if err != nil {
			s.Log.Warn("relation review: frontmatter not editable", "document", id, "err", err.Error())
			continue
		}
		var parts []string
		for _, k := range markdown.RelationKinds {
			for _, rid := range remove[k.Key] {
				parts = append(parts, fmt.Sprintf("%s %d", k.Key, rid))
			}
		}
		msg := "Revisión de relaciones: se quitaron entidades que ya no existen en Jiku (" + strings.Join(parts, ", ") + ")"
		if len(msg) > 500 {
			msg = msg[:497] + "..."
		}
		if edited == string(raw) {
			// The frontmatter no longer lists them (should not happen): drop the stale rows.
			for key, ids := range remove {
				et := ""
				for _, k := range markdown.RelationKinds {
					if k.Key == key {
						et = k.EntityType
					}
				}
				if _, err := s.DB.Exec(ctx, `DELETE FROM document_relations WHERE document_id = $1 AND entity_type = $2 AND entity_id = ANY($3)`,
					id, et, ids); err != nil {
					return n, err
				}
			}
			continue
		}
		call := &Call{Caller: self, CorrelationID: "relation-review"}
		if _, err := s.UpdateDocument(ctx, call, id, d.Version, &contentInput{raw: []byte(edited)}, "", "service", msg); err != nil {
			s.Log.Warn("relation review: update failed", "document", id, "err", err.Error())
			continue
		}
		n++
	}
	return n, nil
}

// JobDue reports whether a periodic job should run, and marks it as run when it is.
func (s *Service) JobDue(ctx context.Context, name string, every time.Duration) (bool, error) {
	now := s.now()
	due := false
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT value FROM service_state WHERE key = $1 FOR UPDATE`, "job:"+name).Scan(&raw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			var v struct {
				At time.Time `json:"at"`
			}
			_ = json.Unmarshal(raw, &v)
			if now.Sub(v.At) < every {
				return nil
			}
		}
		due = true
		val, _ := json.Marshal(map[string]any{"at": now})
		_, err = tx.Exec(ctx, `INSERT INTO service_state (key, value, updated_at) VALUES ($1, $2, now())
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, "job:"+name, val)
		return err
	})
	return due, err
}
