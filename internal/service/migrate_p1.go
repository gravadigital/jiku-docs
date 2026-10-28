package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gravadigital/jiku-docs/internal/acl"
	"github.com/gravadigital/jiku-docs/internal/markdown"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/store"
)

// P-1 data migration (docs/p1/service.md, section 1): moving from the folder ACL to the
// visibility model never makes anything more visible than it was.
//
//  1. Every document a person with role `user` cannot read under the ACL in force becomes
//     `restricted` (`/imported/` and any folder without inheritance and without a rule for
//     `user`). Rules for a specific user id do not count: that person loses access, which is the
//     safe side.
//  2. Every `public` document in a folder with an effective externalBlocked becomes `internal`.
//  3. The live links of the documents that become restricted are revoked (reason restricted).
//  4. Each change is a new version written by the service, with a message: the visibility lives
//     in the frontmatter.
//
// It runs once, at startup and before the service answers anything: the `documents.visibility`
// column is set for every document first, in one transaction (from that moment the new model is
// safe), and then the versions are written one by one. A crash in between is resumed on the next
// start from the plan stored in service_state. When it finishes it is never applied again, so a
// later change by an admin is not undone.

const (
	p1StateKey     = "migration:p1-visibility"
	p1NamesKey     = "migration:p1-names"
	p1MsgRestrict  = "P-1 migration: visibility restricted (the folder was not readable by the role user)"
	p1MsgInternal  = "P-1 migration: visibility internal (the folder blocked external users)"
	p1MigrationVia = "service"
)

type p1State struct {
	Planned bool              `json:"planned"`
	Done    bool              `json:"done"`
	Plan    map[string]string `json:"plan,omitempty"` // document id -> target visibility
	Report  *P1Report         `json:"report,omitempty"`
}

// P1Report summarises what the migration changed.
type P1Report struct {
	Restricted    int `json:"restricted"`
	Internalized  int `json:"internalized"`
	SharesRevoked int `json:"sharesRevoked"`
	Versions      int `json:"versions"`
	Skipped       int `json:"skipped"`
}

// MigrateP1 applies the P-1 visibility migration if it has not finished yet. Safe to call from
// several replicas: they serialise on an advisory lock.
func (s *Service) MigrateP1(ctx context.Context) (*P1Report, error) {
	lock, err := store.AdvisoryLockWait(ctx, s.DB, store.LockMigrationP1)
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	st, err := s.p1Load(ctx)
	if err != nil {
		return nil, err
	}
	if st.Done {
		return st.Report, nil
	}
	if !st.Planned {
		if st, err = s.p1Plan(ctx); err != nil {
			return nil, fmt.Errorf("P-1 migration, plan: %w", err)
		}
	}
	rep := st.Report
	if rep == nil {
		rep = &P1Report{}
	}
	rep.Versions, rep.Skipped = 0, 0
	for id, target := range st.Plan {
		wrote, err := s.p1Rewrite(ctx, id, target)
		if err != nil {
			return nil, fmt.Errorf("P-1 migration, document %s: %w", id, err)
		}
		if wrote {
			rep.Versions++
		} else {
			rep.Skipped++
		}
	}
	done := p1State{Planned: true, Done: true, Report: rep}
	if err := s.p1Save(ctx, s.DB, done); err != nil {
		return nil, err
	}
	if s.Log != nil && len(st.Plan) > 0 {
		s.Log.Info("P-1 migration applied", "restricted", rep.Restricted, "internalized", rep.Internalized,
			"sharesRevoked", rep.SharesRevoked, "versions", rep.Versions, "unchangedFrontmatter", rep.Skipped)
	}
	return rep, nil
}

func (s *Service) p1Load(ctx context.Context) (p1State, error) {
	var st p1State
	var raw []byte
	err := s.DB.QueryRow(ctx, `SELECT value FROM service_state WHERE key = $1`, p1StateKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(raw, &st)
}

func (s *Service) p1Save(ctx context.Context, q store.Querier, st p1State) error {
	val, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO service_state (key, value, updated_at) VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, p1StateKey, val)
	return err
}

// p1Target is the visibility a document must have under the legacy ACL, or "" to keep it.
func p1Target(tree *acl.Tree, userEv *acl.Evaluator, folderID, visibility string) string {
	_, live := tree.Get(folderID)
	switch {
	case visibility == markdown.VisibilityRestricted:
		return ""
	case !live || userEv.Permission(folderID) < acl.Read:
		return markdown.VisibilityRestricted
	case visibility == markdown.VisibilityPublic && tree.ExternalBlocked(folderID):
		return markdown.VisibilityInternal
	}
	return ""
}

// LegacyUserCannotRead reports whether a person with role `user` could not read a folder under
// the folder ACL (obsolete since P-1). The importer uses it to keep imported notes restricted
// where the ACL would have hidden them.
func LegacyUserCannotRead(tree *acl.Tree, folderID string) bool {
	_, live := tree.Get(folderID)
	return !live || tree.For(acl.Principal{Roles: []string{"user"}}).Permission(folderID) < acl.Read
}

// p1Plan decides the targets, sets the column and revokes the links in one transaction.
func (s *Service) p1Plan(ctx context.Context) (p1State, error) {
	st := p1State{Planned: true, Plan: map[string]string{}, Report: &P1Report{}}
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		tree, err := treeTx(ctx, tx)
		if err != nil {
			return err
		}
		userEv := tree.For(acl.Principal{Roles: []string{"user"}})
		rows, err := tx.Query(ctx, `SELECT id, folder_id, visibility FROM documents ORDER BY id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, folder, vis string
			if err := rows.Scan(&id, &folder, &vis); err != nil {
				rows.Close()
				return err
			}
			if t := p1Target(tree, userEv, folder, vis); t != "" {
				st.Plan[id] = t
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		self := s.SelfCaller()
		em := s.emitter(tx, self, "p1-migration")
		now := s.now()
		for id, target := range st.Plan {
			d, err := getDoc(ctx, tx, id, true)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE documents SET visibility = $2 WHERE id = $1`, id, target); err != nil {
				return err
			}
			if target == markdown.VisibilityRestricted {
				st.Report.Restricted++
				var n int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM shares WHERE document_id = $1 AND revoked_at IS NULL`, id).Scan(&n); err != nil {
					return err
				}
				if err := s.revokeDocumentShares(ctx, tx, em, d, self.ID, now, RevokeRestricted); err != nil {
					return err
				}
				st.Report.SharesRevoked += n
			} else {
				st.Report.Internalized++
			}
		}
		return s.p1Save(ctx, tx, st)
	})
	return st, err
}

// p1Rewrite writes the version that puts the target visibility in the frontmatter. It is
// idempotent: a current version that already says so is left alone. Locks are not honoured
// (it is a service correction that must reach every document); the holder of a lock gets a
// version_conflict on its next save, as with any concurrent change.
func (s *Service) p1Rewrite(ctx context.Context, id, target string) (bool, error) {
	self := s.SelfCaller()
	for attempt := 0; attempt < 5; attempt++ {
		d, err := getDoc(ctx, s.DB, id, false)
		if err != nil || d == nil {
			return false, err
		}
		var key string
		if err := s.DB.QueryRow(ctx, `SELECT storage_key FROM document_versions WHERE document_id = $1 AND version = $2`,
			id, d.Version).Scan(&key); err != nil {
			return false, err
		}
		raw, err := s.Blob.Get(ctx, key, MaxDocumentBytes)
		if err != nil {
			return false, err
		}
		fm, _, ferr := markdown.Parse(string(raw))
		if ferr != nil {
			s.Log.Warn("P-1 migration: frontmatter not editable, only the column changed", "document", id, "err", ferr.Error())
			return false, nil
		}
		if fm.Visibility == target {
			return false, nil
		}
		edited, err := markdown.SetVisibility(string(raw), target)
		if err != nil {
			s.Log.Warn("P-1 migration: frontmatter not editable, only the column changed", "document", id, "err", err.Error())
			return false, nil
		}
		existing, err := loadRelations(ctx, s.DB, id)
		if err != nil {
			return false, err
		}
		p, err := s.prepare(ctx, []byte(edited), d.FileName, existing)
		if err != nil {
			return false, err
		}
		newVersion := d.Version + 1
		nkey := storageKey(id, newVersion, p.checksum)
		if err := s.Blob.Put(ctx, nkey, p.content, "text/markdown; charset=utf-8"); err != nil {
			return false, err
		}
		message := p1MsgRestrict
		if target == markdown.VisibilityInternal {
			message = p1MsgInternal
		}
		retry := false
		err = store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
			cur, err := getDoc(ctx, tx, id, true)
			if err != nil {
				return err
			}
			if cur == nil || cur.Version != d.Version {
				retry = true
				return nil
			}
			before, err := s.loadDocState(ctx, tx, id)
			if err != nil {
				return err
			}
			before.doc.Visibility = fm.Visibility // what the content said: the change the event reports
			now := s.now()
			name := nameOf(self)
			if err := insertVersion(ctx, tx, versionInput{docID: id, kind: KindUpdate, version: newVersion, key: nkey, p: p,
				author: self.ID, authorName: name, via: p1MigrationVia, message: message, at: now}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE documents SET title = $2, visibility = $3, current_version = $4, checksum = $5,
				size_bytes = $6, secret_warning = $7, updated_by = $8, updated_at = $9, updated_by_name = $10 WHERE id = $1`,
				id, p.title, target, newVersion, p.checksum, p.size, len(p.findings) > 0, self.ID, now, name); err != nil {
				return err
			}
			if err := writeMeta(ctx, tx, id, p); err != nil {
				return err
			}
			after, err := s.loadDocState(ctx, tx, id)
			if err != nil {
				return err
			}
			em := s.emitter(tx, self, "p1-migration")
			return em.docEvent(ctx, "document.updated", after, versionChanges(before, after, message), p1MigrationVia)
		})
		if err != nil {
			return false, err
		}
		if !retry {
			return true, nil
		}
	}
	return false, protocol.Ef(protocol.CodeInternalError, "the document kept changing during the P-1 migration")
}

// BackfillNames completes, once, the author names of rows written before P-1, from core's
// users. Ids core does not know keep a null name. It returns whether it finished.
func (s *Service) BackfillNames(ctx context.Context) (bool, error) {
	var done bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM service_state WHERE key = $1)`, p1NamesKey).Scan(&done)
	if err != nil || done {
		return done, err
	}
	users, err := s.Core.Users(ctx)
	if err != nil {
		return false, err
	}
	names := map[string]string{s.Set.SelfUserID: "jiku-docs"}
	if s.Set.APIUserID != "" {
		names[s.Set.APIUserID] = "api"
	}
	for _, u := range users {
		n := u.Name
		if n == "" {
			n = u.Username
		}
		if n != "" {
			names[u.ID] = n
		}
	}
	ids := make([]string, 0, len(names))
	vals := make([]string, 0, len(names))
	for id, n := range names {
		if id == "" {
			continue
		}
		ids = append(ids, id)
		vals = append(vals, n)
	}
	err = store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		for _, q := range []string{
			`UPDATE documents d SET created_by_name = n.name FROM unnest($1::text[], $2::text[]) AS n(id, name)
				WHERE d.created_by = n.id AND d.created_by_name IS NULL`,
			`UPDATE documents d SET updated_by_name = n.name FROM unnest($1::text[], $2::text[]) AS n(id, name)
				WHERE d.updated_by = n.id AND d.updated_by_name IS NULL`,
			`UPDATE document_versions v SET author_name = n.name FROM unnest($1::text[], $2::text[]) AS n(id, name)
				WHERE v.author = n.id AND v.author_name IS NULL`,
			`UPDATE document_locks l SET holder_name = n.name FROM unnest($1::text[], $2::text[]) AS n(id, name)
				WHERE l.holder = n.id AND l.holder_name IS NULL`,
		} {
			if _, err := tx.Exec(ctx, q, ids, vals); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO service_state (key, value, updated_at) VALUES ($1, '{"done": true}', now())
			ON CONFLICT (key) DO NOTHING`, p1NamesKey)
		return err
	})
	return err == nil, err
}
