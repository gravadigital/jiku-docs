// Package outbox publishes the changelog: every confirmed change was written to `changelog` in
// the transaction of its command; from there it goes to the JetStream stream JIKU_DOCS_EVENTS
// (Nats-Msg-Id = eventId, deduplicated) and to the `docs-notify` subject of every person
// allowed to read it. Woken by LISTEN/NOTIFY, with a 5 s sweep as safety net.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/gravadigital/jiku-docs/internal/metrics"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/service"
	"github.com/gravadigital/jiku-docs/internal/store"
)

// Stream parameters (section 8).
const (
	StreamName      = "JIKU_DOCS_EVENTS"
	StreamMaxAge    = 30 * 24 * time.Hour
	DuplicateWindow = 2 * time.Minute
	SweepInterval   = 5 * time.Second
	batchSize       = 200
)

// StreamConfig is the configuration of JIKU_DOCS_EVENTS.
func StreamConfig(instance string) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:        StreamName,
		Description: "jiku-docs: documentation events (outbox)",
		Subjects:    []string{protocol.EventsWildcard(instance)},
		Retention:   jetstream.LimitsPolicy,
		MaxAge:      StreamMaxAge,
		Duplicates:  DuplicateWindow,
		Storage:     jetstream.FileStorage,
		Discard:     jetstream.DiscardOld,
	}
}

// EnsureStream creates or updates the stream, idempotently.
func EnsureStream(ctx context.Context, js jetstream.JetStream, instance string) error {
	_, err := js.CreateOrUpdateStream(ctx, StreamConfig(instance))
	return err
}

// Publisher drains the changelog.
type Publisher struct {
	DB       *pgxpool.Pool
	JS       jetstream.JetStream
	NC       *nats.Conn
	Svc      *service.Service
	Instance string
	Log      *slog.Logger
	Metrics  *metrics.Metrics
}

// Run publishes until ctx ends. Only one replica publishes at a time (advisory lock held on the
// listening connection), so events leave in seq order.
func (p *Publisher) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		if err := p.runLeader(ctx); err != nil && ctx.Err() == nil {
			p.Log.Warn("outbox publisher restarting", "err", err.Error())
			select {
			case <-ctx.Done():
			case <-time.After(SweepInterval):
			}
		}
	}
	return nil
}

func (p *Publisher) runLeader(ctx context.Context) error {
	var lock *store.AdvisoryLock
	for {
		var err error
		if lock, err = store.TryAdvisoryLock(ctx, p.DB, store.LockOutbox); err != nil {
			return err
		}
		if lock != nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(SweepInterval):
		}
	}
	defer lock.Release()
	conn := lock.Conn()
	defer func() { _, _ = conn.Exec(context.Background(), `UNLISTEN *`) }()
	if _, err := conn.Exec(ctx, `LISTEN jiku_docs_changelog`); err != nil {
		return err
	}
	for {
		if _, err := p.Drain(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			p.Log.Warn("outbox drain failed", "err", err.Error())
		}
		wctx, cancel := context.WithTimeout(ctx, SweepInterval)
		_, err := conn.Conn().WaitForNotification(wctx)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}
}

type row struct {
	seq      int64
	eventID  string
	typ      string
	payload  []byte
	occurred time.Time
}

// Drain publishes every unpublished row, in seq order. It stops at the first stream failure:
// the row stays unpublished and the next pass retries it (the Msg-Id dedupes a double publish).
func (p *Publisher) Drain(ctx context.Context) (int, error) {
	total := 0
	for {
		rows, err := p.DB.Query(ctx, `SELECT seq, event_id, type, payload, occurred_at FROM changelog
			WHERE published_at IS NULL ORDER BY seq LIMIT $1`, batchSize)
		if err != nil {
			return total, err
		}
		var batch []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.seq, &r.eventID, &r.typ, &r.payload, &r.occurred); err != nil {
				rows.Close()
				return total, err
			}
			batch = append(batch, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return total, err
		}
		if len(batch) > 0 {
			p.Metrics.OutboxAge(time.Since(batch[0].occurred))
		} else {
			p.Metrics.OutboxAge(0)
		}
		for _, r := range batch {
			if err := p.publish(ctx, r); err != nil {
				return total, err
			}
			if _, err := p.DB.Exec(ctx, `UPDATE changelog SET published_at = now() WHERE seq = $1`, r.seq); err != nil {
				return total, err
			}
			total++
			p.Metrics.Published(1)
		}
		if len(batch) < batchSize {
			return total, nil
		}
	}
}

func (p *Publisher) publish(ctx context.Context, r row) error {
	var env map[string]any
	if err := json.Unmarshal(r.payload, &env); err != nil {
		return err
	}
	env["seq"] = r.seq
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := p.JS.Publish(pctx, protocol.EventSubject(p.Instance, r.typ), data, jetstream.WithMsgID(r.eventID)); err != nil {
		return err
	}
	// Notifications are plain NATS and best effort: a person who misses one catches up with
	// documents.changes. A failure to compute them never blocks the stream.
	notes, err := p.Svc.Notifications(ctx, env)
	if err != nil {
		p.Log.Warn("notification fan-out skipped", "eventId", r.eventID, "type", r.typ, "err", err.Error())
		return nil
	}
	for _, n := range notes {
		if err := p.NC.Publish(protocol.NotifySubject(p.Instance, n.UserID, n.Type), n.Body); err != nil {
			p.Log.Warn("notification failed", "eventId", r.eventID, "err", err.Error())
		}
	}
	p.Metrics.Notified(len(notes))
	return nil
}
