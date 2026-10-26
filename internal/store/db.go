// Package store owns the PostgreSQL connection, the embedded migrations and the transaction
// helpers. Queries live next to the business logic that uses them (package service); this
// package only provides the plumbing.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// StatementTimeout is the database-side cut of every query: below the 10 s a caller waits,
// so the caller gets an explainable `query_timeout` instead of a mute bus timeout.
const StatementTimeout = 8 * time.Second

// Open creates the pool. The statement timeout is set per connection.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing DATABASE_URL: %w", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.Itoa(int(StatementTimeout / time.Millisecond))
	cfg.ConnConfig.RuntimeParams["application_name"] = "jiku-docs"
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	if cfg.MaxConns < 10 {
		cfg.MaxConns = 10
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to PostgreSQL: %w", err)
	}
	return pool, nil
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		num, _, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("migration %s: name must be NNNN_description.sql", name)
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("migration %s: %w", name, err)
		}
		b, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: name, sql: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// Migrate applies pending migrations, each in its own transaction. Safe to run from several
// processes at once.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (applied int, err error) {
	return MigrateTo(ctx, pool, log, 0)
}

// MigrateTo applies pending migrations up to version upTo (0 = all). Tests use it to load data
// with an older schema and check what a migration does with it.
func MigrateTo(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, upTo int) (applied int, err error) {
	migs, err := loadMigrations()
	if err != nil {
		return 0, err
	}
	// LockMigrations serialises concurrent migrators (two replicas starting at once).
	lock, err := AdvisoryLockWait(ctx, pool, LockMigrations)
	if err != nil {
		return 0, err
	}
	defer lock.Release()
	conn := lock.Conn()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version integer PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return 0, err
	}
	done := map[int]bool{}
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return 0, err
		}
		done[v] = true
	}
	rows.Close()
	for _, m := range migs {
		if done[m.version] || (upTo > 0 && m.version > upTo) {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return applied, err
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			_ = tx.Rollback(ctx)
			return applied, fmt.Errorf("migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.version, m.name); err != nil {
			_ = tx.Rollback(ctx)
			return applied, err
		}
		if err := tx.Commit(ctx); err != nil {
			return applied, err
		}
		applied++
		if log != nil {
			log.Info("migration applied", "migration", m.name)
		}
	}
	return applied, nil
}

// Querier is satisfied by a pool, a connection and a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// InTx runs fn in a transaction, committing on nil and rolling back otherwise.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// IsUniqueViolation reports a unique constraint violation, optionally of a named constraint.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}

// IsStatementTimeout reports PostgreSQL's statement_timeout cancellation (57014), detected by
// code and never by message text.
func IsStatementTimeout(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "57014"
}
