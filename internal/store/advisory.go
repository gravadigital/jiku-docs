package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LockKey is a PostgreSQL advisory lock key. Every key of the service lives here, so two
// components never share one by accident.
type LockKey int64

// Advisory lock keys.
const (
	LockMigrations        LockKey = 7_301_000_001 // schema migrations (session)
	LockChangelog         LockKey = 7_301_000_002 // changelog inserts in seq order (transaction)
	LockOutbox            LockKey = 7_301_000_003 // outbox publisher leader (session)
	LockJanitor           LockKey = 7_301_000_004 // janitor leader (session)
	LockHistoryCompaction LockKey = 7_301_000_005 // history compaction (session)
	LockMigrationP1       LockKey = 7_301_000_006 // P-1 visibility migration (session)
)

// AdvisoryLock is a session-level advisory lock held on a connection taken out of the pool for
// as long as the lock lives. The lock is released on that same connection. If the lock or the
// unlock statement fails, the connection is closed instead of going back to the pool: a session
// lock survives the failure of the statement that took it (a statement_timeout after the lock
// was granted, say), and a pooled session holding it would keep it forever while serving other
// components.
type AdvisoryLock struct {
	conn *pgxpool.Conn
	key  LockKey
}

// TryAdvisoryLock takes key without waiting. It returns (nil, nil) when another session holds it.
func TryAdvisoryLock(ctx context.Context, pool *pgxpool.Pool, key LockKey) (*AdvisoryLock, error) {
	return acquireAdvisory(ctx, pool, key, true)
}

// AdvisoryLockWait takes key, waiting for it.
func AdvisoryLockWait(ctx context.Context, pool *pgxpool.Pool, key LockKey) (*AdvisoryLock, error) {
	return acquireAdvisory(ctx, pool, key, false)
}

func acquireAdvisory(ctx context.Context, pool *pgxpool.Pool, key LockKey, try bool) (*AdvisoryLock, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	// From here on the session may hold the lock (granted, then the statement failed), so any
	// way out but success discards the connection, panics included.
	ok := false
	defer func() {
		if !ok {
			discard(conn)
		}
	}()
	got := true
	if try {
		err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, int64(key)).Scan(&got)
	} else {
		_, err = conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(key))
	}
	if err != nil {
		return nil, fmt.Errorf("advisory lock %d: %w", key, err)
	}
	ok = true
	if !got {
		conn.Release() // never taken: the connection is clean
		return nil, nil
	}
	return &AdvisoryLock{conn: conn, key: key}, nil
}

// Conn is the connection that holds the lock, for work that must run on it (LISTEN, the
// migrations). It is valid until Release.
func (l *AdvisoryLock) Conn() *pgxpool.Conn { return l.conn }

// Release unlocks on the connection that holds the lock and returns it to the pool; if the
// unlock fails or finds nothing to release, the connection is closed. Safe to defer (it runs on
// panics) and to call twice.
func (l *AdvisoryLock) Release() {
	if l == nil || l.conn == nil {
		return
	}
	conn := l.conn
	l.conn = nil
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var released bool
	if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, int64(l.key)).Scan(&released); err != nil || !released {
		discard(conn)
		return
	}
	// A session may hold a lock more than once; ours must be gone, or the connection goes too.
	var still bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = pg_backend_pid()
		AND locktype = 'advisory')`).Scan(&still); err != nil || still {
		discard(conn)
		return
	}
	conn.Release()
}

// discard closes the session (ending every lock it holds) and hands the dead connection back,
// which the pool destroys.
func discard(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Conn().Close(ctx)
	conn.Release()
}
