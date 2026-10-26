package integration

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gravadigital/jiku-docs/internal/janitor"
	"github.com/gravadigital/jiku-docs/internal/logging"
	"github.com/gravadigital/jiku-docs/internal/service"
	"github.com/gravadigital/jiku-docs/internal/store"
	tu "github.com/gravadigital/jiku-docs/internal/testutil"
)

// janitorLockHolders counts the sessions that hold the janitor's advisory lock. A closed
// connection's backend exits asynchronously, so it waits up to 2 s for the count to reach 0.
func janitorLockHolders(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var n int
		if err := db.QueryRow(context.Background(), `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'
			AND ((classid::bigint << 32) | objid::bigint) = $1 AND granted`, int64(store.LockJanitor)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 || time.Now().After(deadline) {
			return n
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The janitor runs on the pool every component shares. A statement that fails after taking the
// session lock (statement_timeout under load: what happened in the local instance) left the lock on
// a pooled connection; that connection went on serving other components (the outbox kept it for
// its LISTEN) and the janitor never got the lock again. Now a failure closes the connection, so
// the session and its lock end with it, and later runs work.
func TestJanitorLockSurvivesFailuresOnSharedPool(t *testing.T) {
	e := tu.NewEnv(t, tu.Options{NoJanitor: true})
	ctx := context.Background()

	// pg_try_advisory_lock that takes the lock and then fails the statement, once, the way a
	// statement_timeout does. Reached through the search_path of the janitor's pool only.
	if _, err := e.DB.Exec(ctx, `CREATE SCHEMA fault;
		CREATE SEQUENCE fault.calls;
		CREATE FUNCTION fault.pg_try_advisory_lock(k bigint) RETURNS boolean LANGUAGE plpgsql AS $$
		DECLARE got boolean;
		BEGIN
			got := pg_catalog.pg_try_advisory_lock(k);
			IF k = `+strconv.FormatInt(int64(store.LockJanitor), 10)+` AND nextval('fault.calls') = 1 THEN
				RAISE EXCEPTION 'canceling statement due to statement timeout' USING ERRCODE = 'query_canceled';
			END IF;
			RETURN got;
		END $$;`); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(e.DB.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = "fault, pg_catalog, public"
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	svc := service.New(pool, e.Blob, e.Cached, logging.Discard(), e.Settings)
	j := &janitor.Janitor{DB: pool, Svc: svc, Log: logging.Discard(), ReviewHour: -1}

	j.RunOnce(ctx) // the lock statement fails after taking the lock
	if n := janitorLockHolders(t, e.DB); n != 0 {
		t.Errorf("after a failed run, %d session(s) still hold the janitor lock", n)
	}

	// Other components reuse the pool; one of them (the outbox, for its LISTEN) keeps whichever
	// connection it gets. Worst case: the one the failed run handed back.
	var held []*pgxpool.Conn
	for i := 0; i < int(cfg.MaxConns); i++ {
		c, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	var keep *pgxpool.Conn
	for _, c := range held {
		var holds bool
		if err := c.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = pg_backend_pid()
			AND locktype = 'advisory' AND ((classid::bigint << 32) | objid::bigint) = $1)`, int64(store.LockJanitor)).Scan(&holds); err != nil {
			t.Fatal(err)
		}
		if holds && keep == nil {
			keep = c
			continue
		}
		c.Release()
	}
	if keep == nil {
		keep, err = pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	defer keep.Release()
	if _, err := keep.Exec(ctx, `LISTEN jiku_docs_changelog`); err != nil {
		t.Fatal(err)
	}

	// Several janitor runs on the same pool: they must take the lock and do their jobs.
	for i := 0; i < 3; i++ {
		j.RunOnce(ctx)
		time.Sleep(10 * time.Millisecond)
	}
	var ran int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM service_state WHERE key = 'job:share-data'`).Scan(&ran); err != nil {
		t.Fatal(err)
	}
	if ran != 1 {
		t.Fatal("the janitor never ran again: its lock is held by a connection of the pool")
	}
	if n := janitorLockHolders(t, e.DB); n != 0 {
		t.Fatalf("after the runs, %d session(s) still hold the janitor lock", n)
	}
}

// store.AdvisoryLock: one holder at a time, released on its own connection even on a panic,
// and a session that took the lock twice is closed rather than handed back holding it.
func TestAdvisoryLockReleasedOnItsConnection(t *testing.T) {
	e := tu.NewEnv(t, tu.Options{NoJanitor: true})
	ctx := context.Background()
	key := store.LockJanitor
	l, err := store.TryAdvisoryLock(ctx, e.DB, key)
	if err != nil || l == nil {
		t.Fatalf("first lock: %v %v", l, err)
	}
	if other, err := store.TryAdvisoryLock(ctx, e.DB, key); err != nil || other != nil {
		t.Fatalf("a second holder: %v %v", other, err)
	}
	l.Release()
	l.Release() // twice is harmless
	if n := janitorLockHolders(t, e.DB); n != 0 {
		t.Fatalf("%d holder(s) after Release", n)
	}

	func() {
		defer func() { _ = recover() }()
		l, err := store.TryAdvisoryLock(ctx, e.DB, key)
		if err != nil || l == nil {
			t.Fatalf("lock: %v %v", l, err)
		}
		defer l.Release()
		panic("job blew up")
	}()
	if n := janitorLockHolders(t, e.DB); n != 0 {
		t.Fatalf("%d holder(s) after a panic", n)
	}

	l, err = store.TryAdvisoryLock(ctx, e.DB, key)
	if err != nil || l == nil {
		t.Fatalf("lock: %v %v", l, err)
	}
	if _, err := l.Conn().Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(key)); err != nil { // reentrant: count 2
		t.Fatal(err)
	}
	l.Release()
	if n := janitorLockHolders(t, e.DB); n != 0 {
		t.Fatalf("%d holder(s) after releasing a lock taken twice", n)
	}
}
