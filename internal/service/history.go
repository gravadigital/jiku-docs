package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gravadigital/jiku-docs/internal/blob"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/store"
)

// History compaction (docs/history-compaction-v1.md): every save is a version, and a daily
// process thins out the old ones. Less than KeepAll old, every version stays; up to KeepBlocks,
// the last version of each editing block; older, the last of each calendar day. Protected
// versions and the current one are never removed.

// HistoryPolicy are the thresholds of the retention policy (section 6).
type HistoryPolicy struct {
	KeepAll     time.Duration  // DOCS_HISTORY_KEEP_ALL: every version younger than this stays
	KeepBlocks  time.Duration  // DOCS_HISTORY_KEEP_BLOCKS: up to this age, the last of each block
	BlockGap    time.Duration  // DOCS_HISTORY_BLOCK_GAP: a pause of this or more closes a block
	Location    *time.Location // DOCS_HISTORY_TIMEZONE: calendar days
	BatchSize   int            // DOCS_HISTORY_BATCH_SIZE: documents per batch
	DeleteBatch int            // DOCS_HISTORY_S3_DELETE_BATCH: keys per DeleteObjects call
}

// DefaultHistoryPolicy are the values of the compaction document.
func DefaultHistoryPolicy() HistoryPolicy {
	loc, err := time.LoadLocation("America/Argentina/Buenos_Aires")
	if err != nil {
		loc = time.FixedZone("ART", -3*3600)
	}
	return HistoryPolicy{KeepAll: 24 * time.Hour, KeepBlocks: 30 * 24 * time.Hour, BlockGap: 30 * time.Minute,
		Location: loc, BatchSize: 100, DeleteBatch: blob.MaxDeleteBatch}
}

func (s *Service) historyPolicy() HistoryPolicy {
	p, d := s.History, DefaultHistoryPolicy()
	if p.KeepAll <= 0 {
		p.KeepAll = d.KeepAll
	}
	if p.KeepBlocks <= 0 {
		p.KeepBlocks = d.KeepBlocks
	}
	if p.BlockGap <= 0 {
		p.BlockGap = d.BlockGap
	}
	if p.Location == nil {
		p.Location = d.Location
	}
	if p.BatchSize <= 0 {
		p.BatchSize = d.BatchSize
	}
	if p.DeleteBatch <= 0 || p.DeleteBatch > blob.MaxDeleteBatch {
		p.DeleteBatch = blob.MaxDeleteBatch
	}
	return p
}

// Why a version stays (or goes), for the per-document log.
const (
	ReasonCurrent   = "current"
	ReasonCreate    = "create"
	ReasonRestore   = "restore"
	ReasonMessage   = "message"
	ReasonService   = "service"
	ReasonProtected = "protected"
	ReasonPinned    = "pinned"
	ReasonRecent    = "recent"        // younger than KeepAll
	ReasonBlock     = "last-of-block" // KeepAll..KeepBlocks
	ReasonDay       = "last-of-day"   // older than KeepBlocks
	ReasonInBlock   = "inside-block"  // pruned: not the last of its block
	ReasonInDay     = "inside-day"    // pruned: not the last of its day
)

// HistoryVersion is a version as the compaction sees it.
type HistoryVersion struct {
	Version    int
	Author     string
	Via        string
	Kind       string
	Protected  bool
	HasMessage bool
	CreatedAt  time.Time
	Key        string
	Size       int64
}

// VersionDecision is the verdict on one version.
type VersionDecision struct {
	Version int    `json:"version"`
	Keep    bool   `json:"keep"`
	Reason  string `json:"reason"`
	Key     string `json:"-"`
	Size    int64  `json:"-"`
}

// PlanHistory applies the retention policy to the versions of one document, ordered by
// version. It is pure: the same input always gives the same verdict.
//
//  1. Protected versions stay: the current one, the permanent protection (create, restore,
//     message, service) and the versions pinned by an active share.
//  2. Blocks are computed over every version of the document (a block that crosses the 24 h
//     limit is not split): same author, same via, and less than BlockGap between consecutive
//     versions. Between KeepAll and KeepBlocks old, the last version of each block stays.
//  3. Older than KeepBlocks, the rule works on what the block rule left: the last of each
//     calendar day (in Location) stays.
//  4. Every version younger than KeepAll stays.
func PlanHistory(vs []HistoryVersion, current int, pinned map[int]bool, now time.Time, p HistoryPolicy) []VersionDecision {
	n := len(vs)
	blockLast := make([]bool, n)
	for i := range vs {
		blockLast[i] = i == n-1 || vs[i+1].Author != vs[i].Author || vs[i+1].Via != vs[i].Via ||
			vs[i+1].CreatedAt.Sub(vs[i].CreatedAt) >= p.BlockGap
	}
	day := func(t time.Time) string { return t.In(p.Location).Format("2006-01-02") }
	lastOfDay := map[string]int{}
	for i, v := range vs {
		if now.Sub(v.CreatedAt) > p.KeepBlocks && blockLast[i] {
			lastOfDay[day(v.CreatedAt)] = i // ascending order: the last one wins
		}
	}
	out := make([]VersionDecision, n)
	for i, v := range vs {
		d := VersionDecision{Version: v.Version, Keep: true, Key: v.Key, Size: v.Size}
		age := now.Sub(v.CreatedAt)
		switch {
		case v.Version == current:
			d.Reason = ReasonCurrent
		case v.Protected:
			d.Reason = protectedReason(v)
		case pinned[v.Version]:
			d.Reason = ReasonPinned
		case age < p.KeepAll:
			d.Reason = ReasonRecent
		case age <= p.KeepBlocks:
			d.Reason = ReasonBlock
			if !blockLast[i] {
				d.Keep, d.Reason = false, ReasonInBlock
			}
		default:
			switch {
			case !blockLast[i]:
				d.Keep, d.Reason = false, ReasonInBlock
			case lastOfDay[day(v.CreatedAt)] != i:
				d.Keep, d.Reason = false, ReasonInDay
			default:
				d.Reason = ReasonDay
			}
		}
		out[i] = d
	}
	return out
}

func protectedReason(v HistoryVersion) string {
	switch {
	case v.Kind == KindCreate:
		return ReasonCreate
	case v.Kind == KindRestore:
		return ReasonRestore
	case v.HasMessage:
		return ReasonMessage
	case v.Via == "service":
		return ReasonService
	}
	return ReasonProtected
}

// HistoryOptions of one compaction run.
type HistoryOptions struct {
	// DocumentID compacts only this document, whether it has new activity or not.
	DocumentID string
	// DryRun reports what would be removed, without touching rows or objects.
	DryRun bool
}

// DocumentPlan is the verdict on one document (filled only in a dry run).
type DocumentPlan struct {
	DocumentID string            `json:"documentId"`
	Versions   []VersionDecision `json:"versions"`
}

// HistoryReport summarises a run.
type HistoryReport struct {
	// Skipped: another replica holds the compaction lock; nothing was done.
	Skipped        bool
	DryRun         bool
	Documents      int
	VersionsPruned int
	BytesFreed     int64
	// ObjectsTrashed: objects of compacted versions moved to trash/ in this run.
	ObjectsTrashed int
	ObjectsPending int
	Errors         int
	Duration       time.Duration
	Plans          []DocumentPlan
}

// ErrCompactionBusy is returned by CompactHistory when another process holds the lock.
var ErrCompactionBusy = errors.New("history compaction already running")

// CompactHistory runs the compaction: the pending documents in batches ordered by id (or one
// document), each in its own short transaction, and then the deletion of the pruned objects in
// S3 (moved to trash/, see trash.go). Only one process runs it at a time: if the advisory lock is taken, it returns a report
// with Skipped set.
func (s *Service) CompactHistory(ctx context.Context, opts HistoryOptions) (*HistoryReport, error) {
	start := time.Now()
	rep := &HistoryReport{DryRun: opts.DryRun}
	// store.LockHistoryCompaction: one replica (or one `jiku-docs history compact`) at a time.
	lock, err := store.TryAdvisoryLock(ctx, s.DB, store.LockHistoryCompaction)
	if err != nil {
		return nil, err
	}
	if lock == nil {
		rep.Skipped = true
		s.Log.Info("history compaction: another process holds the lock, skipping")
		return rep, nil
	}
	defer lock.Release()

	p := s.historyPolicy()
	s.Log.Info("history compaction started", "dryRun", opts.DryRun, "document", opts.DocumentID)
	var runErr error
	if opts.DocumentID != "" {
		runErr = s.compactOne(ctx, opts.DocumentID, opts.DryRun, p, rep)
	} else {
		cursor := ""
		for {
			ids, err := s.pendingDocuments(ctx, cursor, p)
			if err != nil {
				runErr = err
				break
			}
			for _, id := range ids {
				if err := s.compactOne(ctx, id, opts.DryRun, p, rep); err != nil {
					if ctx.Err() != nil {
						runErr = ctx.Err()
						break
					}
					rep.Errors++
					s.Log.Warn("history compaction: document failed", "document", id, "err", err.Error())
				}
			}
			if runErr != nil || len(ids) < p.BatchSize {
				break
			}
			cursor = ids[len(ids)-1]
		}
	}
	if !opts.DryRun && runErr == nil {
		n, err := s.DrainPrunedObjects(ctx)
		rep.ObjectsTrashed = n
		if err != nil {
			rep.Errors++
			s.Log.Warn("history compaction: objects not moved to the trash, the next run retries", "err", err.Error())
		}
	}
	if pending, _, err := s.PrunedObjectsPending(ctx); err == nil {
		rep.ObjectsPending = pending
	}
	rep.Duration = time.Since(start)
	attrs := []any{"dryRun", opts.DryRun, "documents", rep.Documents, "versionsPruned", rep.VersionsPruned,
		"bytesFreed", rep.BytesFreed, "objectsTrashed", rep.ObjectsTrashed, "objectsPending", rep.ObjectsPending,
		"errors", rep.Errors, "durationMs", rep.Duration.Milliseconds()}
	if runErr != nil {
		s.Log.Error("history compaction failed", append(attrs, "err", runErr.Error())...)
		return rep, runErr
	}
	s.Log.Info("history compaction finished", attrs...)
	return rep, nil
}

// pendingDocuments returns the next batch of documents (id > after) that need a compaction:
// never compacted, new activity since the last one, a pinned share that expired or was revoked
// since then, or a version that crossed a threshold of the policy since then (without this, a
// document edited the day before the run and never touched again would keep all its versions).
func (s *Service) pendingDocuments(ctx context.Context, after string, p HistoryPolicy) ([]string, error) {
	now := s.now()
	rows, err := s.DB.Query(ctx, `SELECT d.id FROM documents d
		WHERE d.deleted_at IS NULL AND d.id > $1 AND (
			d.last_compacted_at IS NULL OR d.updated_at > d.last_compacted_at
			OR EXISTS (SELECT 1 FROM shares sh WHERE sh.document_id = d.id AND sh.mode = 'pinned' AND (
				(sh.revoked_at IS NOT NULL AND sh.revoked_at > d.last_compacted_at)
				OR (sh.expires_at <= $2 AND sh.expires_at > d.last_compacted_at)))
			OR (d.updated_at > d.last_compacted_at - $4::interval AND EXISTS (
				SELECT 1 FROM document_versions v WHERE v.document_id = d.id AND NOT v.protected AND v.version <> d.current_version
				AND ((v.created_at > d.last_compacted_at - $3::interval AND v.created_at <= $2 - $3::interval)
				  OR (v.created_at > d.last_compacted_at - $4::interval AND v.created_at <= $2 - $4::interval)))))
		ORDER BY d.id LIMIT $5`, after, now, p.KeepAll, p.KeepBlocks, p.BatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// compactOne compacts one document in a short transaction: lock the row (serialises with
// update, restore and shares.new), read its versions and the active pinned shares, plan, delete
// the pruned rows recording their objects in pruned_objects, and update the counters. No call
// leaves the database inside the transaction.
func (s *Service) compactOne(ctx context.Context, id string, dryRun bool, p HistoryPolicy, rep *HistoryReport) error {
	var plan []VersionDecision
	var pruned int
	var bytes int64
	errSkip := errors.New("skip")
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		now := s.now()
		var current int
		var deleted *time.Time
		err := tx.QueryRow(ctx, `SELECT current_version, deleted_at FROM documents WHERE id = $1 FOR UPDATE`, id).Scan(&current, &deleted)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && deleted != nil) {
			return errSkip // gone or deleted: the purge after 30 days removes it with all its versions
		}
		if err != nil {
			return err
		}
		vs, err := loadHistory(ctx, tx, id)
		if err != nil {
			return err
		}
		pinned, err := pinnedVersions(ctx, tx, id, now)
		if err != nil {
			return err
		}
		plan = PlanHistory(vs, current, pinned, now, p)
		var prune []int
		bytes = 0
		for _, d := range plan {
			if !d.Keep {
				prune = append(prune, d.Version)
				bytes += d.Size
			}
		}
		pruned = len(prune)
		if dryRun {
			return errSkip // nothing is written
		}
		if len(prune) > 0 {
			if _, err := tx.Exec(ctx, `WITH gone AS (
					DELETE FROM document_versions WHERE document_id = $1 AND version = ANY($2)
					RETURNING storage_key, version, `+versionRowSQL+` AS row)
				INSERT INTO pruned_objects (storage_key, document_id, version, pruned_at, version_row)
				SELECT storage_key, $1, version, $3, row FROM gone ON CONFLICT (storage_key) DO NOTHING`, id, prune, now); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE documents SET last_compacted_at = $2, pruned_versions_count = pruned_versions_count + $3
			WHERE id = $1`, id, now, len(prune))
		return err
	})
	if err != nil && !errors.Is(err, errSkip) {
		return err
	}
	if plan == nil {
		return nil
	}
	rep.Documents++
	rep.VersionsPruned += pruned
	rep.BytesFreed += bytes
	if dryRun {
		rep.Plans = append(rep.Plans, DocumentPlan{DocumentID: id, Versions: plan})
	}
	level := slog.LevelDebug
	if dryRun {
		level = slog.LevelInfo
	}
	if s.Log.Enabled(ctx, level) {
		var kept, pruned []string
		for _, d := range plan {
			entry := fmt.Sprintf("%d:%s", d.Version, d.Reason)
			if d.Keep {
				kept = append(kept, entry)
			} else {
				pruned = append(pruned, entry)
			}
		}
		s.Log.Log(ctx, level, "history compaction: document", "document", id, "dryRun", dryRun, "kept", kept, "pruned", pruned)
	}
	return nil
}

func loadHistory(ctx context.Context, q store.Querier, docID string) ([]HistoryVersion, error) {
	rows, err := q.Query(ctx, `SELECT version, author, via, kind, protected, message IS NOT NULL, created_at, storage_key, size_bytes
		FROM document_versions WHERE document_id = $1 ORDER BY version`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryVersion
	for rows.Next() {
		var v HistoryVersion
		if err := rows.Scan(&v.Version, &v.Author, &v.Via, &v.Kind, &v.Protected, &v.HasMessage, &v.CreatedAt, &v.Key, &v.Size); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// pinnedVersions are the versions fixed by an active (not revoked, not expired) pinned share.
func pinnedVersions(ctx context.Context, q store.Querier, docID string, now time.Time) (map[int]bool, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT version FROM shares WHERE document_id = $1 AND mode = 'pinned'
		AND revoked_at IS NULL AND expires_at > $2`, docID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

// DrainPrunedObjects moves to trash/ the objects of compacted versions, in batches of
// DeleteBatch keys (parallel copies, then one DeleteObjects of the originals), each trash copy
// carrying its version row. A row of pruned_objects goes away only once its object is in the
// trash and the original is gone (or the original no longer existed). A row whose key is still
// used by a version (it should never happen) is dropped without touching the object. It
// returns how many objects it moved.
func (s *Service) DrainPrunedObjects(ctx context.Context) (int, error) {
	p := s.historyPolicy()
	total := 0
	for {
		rows, err := s.DB.Query(ctx, `SELECT p.storage_key, p.version_row,
				EXISTS (SELECT 1 FROM document_versions v WHERE v.storage_key = p.storage_key)
			FROM pruned_objects p ORDER BY p.pruned_at, p.storage_key LIMIT $1`, p.DeleteBatch)
		if err != nil {
			return total, err
		}
		var move []trashObject
		var inUse []string
		n := 0
		for rows.Next() {
			var key string
			var row []byte
			var used bool
			if err := rows.Scan(&key, &row, &used); err != nil {
				rows.Close()
				return total, err
			}
			n++
			if used {
				inUse = append(inUse, key)
			} else {
				move = append(move, trashObject{Key: key, Meta: encodeVersionRow(row)})
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return total, err
		}
		if n == 0 {
			return total, nil
		}
		done, moved, merr := s.moveToTrash(ctx, TrashCompaction, move)
		if done = append(done, inUse...); len(done) > 0 {
			if _, err := s.DB.Exec(ctx, `DELETE FROM pruned_objects WHERE storage_key = ANY($1)`, done); err != nil {
				return total, err
			}
		}
		total += moved
		if merr != nil {
			return total, merr
		}
		if n < p.DeleteBatch {
			return total, nil
		}
	}
}

// PrunedObjectsPending reports the rows waiting in pruned_objects and the age of the oldest
// (an alert above 24 h means the move to the trash does not progress).
func (s *Service) PrunedObjectsPending(ctx context.Context) (int, time.Duration, error) {
	var n int
	var oldest *time.Time
	if err := s.DB.QueryRow(ctx, `SELECT count(*), min(pruned_at) FROM pruned_objects`).Scan(&n, &oldest); err != nil {
		return 0, 0, err
	}
	if oldest == nil {
		return n, 0, nil
	}
	return n, s.now().Sub(*oldest), nil
}

// missingVersion explains a version without a row: newer than the current one, it never existed
// (document_not_found); otherwise the compaction removed it (version_pruned, with the nearest
// versions kept). Never use it for an external caller: for them every non-current version is
// document_not_found without details.
func missingVersion(ctx context.Context, q store.Querier, docID string, current, version int) error {
	if version > current {
		return protocol.Ef(protocol.CodeDocumentNotFound, "Version %d does not exist", version).With("version", version)
	}
	var prev, next *int
	if err := q.QueryRow(ctx, `SELECT max(version) FILTER (WHERE version < $2), min(version) FILTER (WHERE version > $2)
		FROM document_versions WHERE document_id = $1`, docID, version).Scan(&prev, &next); err != nil {
		return err
	}
	return protocol.Ef(protocol.CodeVersionPruned, "Version %d was pruned: choose one of the nearest kept versions", version).
		With("requestedVersion", version).With("previousAvailable", intOrNil(prev)).With("nextAvailable", intOrNil(next))
}

func intOrNil(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// JobDueDaily reports whether a daily job should run now: from `hour` (in loc) on, once per
// calendar day of loc. Same record as JobDue in service_state, so both can be inspected alike.
func (s *Service) JobDueDaily(ctx context.Context, name string, loc *time.Location, hour int) (bool, error) {
	now := s.now()
	local := now.In(loc)
	if local.Hour() < hour {
		return false, nil
	}
	due := false
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		var at *time.Time
		err := tx.QueryRow(ctx, `SELECT (value->>'at')::timestamptz FROM service_state WHERE key = $1 FOR UPDATE`, "job:"+name).Scan(&at)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if at != nil {
			last := at.In(loc)
			if last.Year() == local.Year() && last.YearDay() == local.YearDay() {
				return nil
			}
		}
		due = true
		_, err = tx.Exec(ctx, `INSERT INTO service_state (key, value, updated_at) VALUES ($1, jsonb_build_object('at', $2::timestamptz), now())
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, "job:"+name, now)
		return err
	})
	return due, err
}
