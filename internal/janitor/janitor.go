// Package janitor runs the periodic work: lock expiry (with document.unlocked), purges (deleted
// documents after 30 days, uploads, pending attachments after 24 h, share accesses after 180
// days, share retry ciphers after 5 min, changelog after 30 days), the weekly orphan sweep (one
// page per minute), the daily review of relations against core and the daily history
// compaction. One replica runs it at a time (advisory lock); the compaction takes its own lock
// and runs beside the frequent jobs, so lock expiry never waits for it.
package janitor

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gravadigital/jiku-docs/internal/metrics"
	"github.com/gravadigital/jiku-docs/internal/service"
)

const leaderLock = 7_301_000_004

// Janitor runs the periodic jobs.
type Janitor struct {
	DB      *pgxpool.Pool
	Svc     *service.Service
	Log     *slog.Logger
	Metrics *metrics.Metrics
	// ReviewHour is the UTC hour of the daily relation review.
	ReviewHour int
	// Tick is the period of the frequent jobs (lock expiry).
	Tick time.Duration

	// History compaction: daily from HistoryHour (in HistoryLocation), when enabled.
	HistoryEnabled  bool
	HistoryHour     int
	HistoryLocation *time.Location

	compacting atomic.Bool
	wg         sync.WaitGroup
}

// Run loops until ctx ends.
func (j *Janitor) Run(ctx context.Context) error {
	tick := j.Tick
	if tick <= 0 {
		tick = 10 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	defer j.wg.Wait()
	for {
		j.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// RunOnce runs whatever is due, if this replica gets the lock.
func (j *Janitor) RunOnce(ctx context.Context) {
	conn, err := j.DB.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	var leader bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, leaderLock).Scan(&leader); err != nil || !leader {
		return
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, leaderLock) }()

	if n, err := j.Svc.ExpireLocks(ctx); err != nil {
		j.Log.Warn("janitor: lock expiry failed", "err", err.Error())
	} else if n > 0 {
		j.Log.Info("janitor: locks expired", "count", n)
	}
	if n, err := j.Svc.ActiveLocks(ctx); err == nil {
		j.Metrics.Locks(n)
	}
	j.job(ctx, "share-data", 5*time.Minute, func() error { return j.Svc.PurgeShareData(ctx) })
	j.job(ctx, "uploads", 15*time.Minute, func() error {
		n, err := j.Svc.PurgeUploads(ctx)
		j.logCount("uploads purged", n)
		return err
	})
	j.job(ctx, "pending-attachments", time.Hour, func() error {
		n, err := j.Svc.PurgePendingAttachments(ctx)
		j.logCount("pending attachments purged", int(n))
		return err
	})
	j.job(ctx, "deleted-documents", time.Hour, func() error {
		n, err := j.Svc.PurgeDeleted(ctx)
		j.logCount("deleted documents purged", n)
		return err
	})
	j.job(ctx, "changelog", 6*time.Hour, func() error {
		n, err := j.Svc.PurgeChangelog(ctx)
		j.logCount("changelog rows purged", int(n))
		return err
	})
	// Weekly sweep of the whole bucket, one page per minute with a cursor in service_state: the
	// objects of compacted versions are deleted by pruned_objects, not by this sweep.
	j.job(ctx, "orphans", 7*24*time.Hour, func() error { return j.Svc.StartOrphanSweep(ctx) })
	j.job(ctx, "orphans-page", time.Minute, func() error {
		n, done, err := j.Svc.PurgeOrphans(ctx)
		j.logCount("orphan objects deleted", n)
		if done && err == nil && n > 0 {
			j.Log.Info("janitor: orphan sweep finished")
		}
		return err
	})
	if n, age, err := j.Svc.PrunedObjectsPending(ctx); err == nil {
		j.Metrics.HistoryPending(n, age)
	}
	j.historyCompaction(ctx)
	if time.Now().UTC().Hour() == j.ReviewHour {
		j.job(ctx, "relation-review", 20*time.Hour, func() error {
			n, err := j.Svc.ReviewRelations(ctx)
			j.logCount("documents with stale relations fixed", n)
			return err
		})
	}
}

// historyCompaction starts the daily compaction in its own goroutine when due. The run takes
// its own advisory lock: a manual `jiku-docs history compact` or another replica never overlap.
func (j *Janitor) historyCompaction(ctx context.Context) {
	if !j.HistoryEnabled || j.HistoryLocation == nil || j.compacting.Load() {
		return
	}
	due, err := j.Svc.JobDueDaily(ctx, "history-compaction", j.HistoryLocation, j.HistoryHour)
	if err != nil || !due {
		return
	}
	j.compacting.Store(true)
	j.wg.Add(1)
	go func() {
		defer j.wg.Done()
		defer j.compacting.Store(false)
		rep, err := j.Svc.CompactHistory(ctx, service.HistoryOptions{})
		if rep == nil || rep.Skipped {
			return
		}
		j.Metrics.HistoryRun(rep.VersionsPruned, rep.BytesFreed, rep.Duration, err == nil && rep.Errors == 0)
		j.Metrics.HistoryPending(rep.ObjectsPending, 0)
	}()
}

func (j *Janitor) job(ctx context.Context, name string, every time.Duration, fn func() error) {
	due, err := j.Svc.JobDue(ctx, name, every)
	if err != nil || !due {
		return
	}
	if err := fn(); err != nil {
		j.Log.Warn("janitor job failed", "job", name, "err", err.Error())
	}
}

func (j *Janitor) logCount(what string, n int) {
	if n > 0 {
		j.Log.Info("janitor: "+what, "count", n)
	}
}
