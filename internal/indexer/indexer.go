// Package indexer keeps the full-text catalog: it consumes JIKU_DOCS_EVENTS with a durable of its
// own, splits each document version by headings and stores weighted `tsvector`s (title and
// headings A, body B, configuration `es_unaccent`). The index is derived data: `jiku-docs
// reindex` rebuilds it from scratch.
package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/gravadigital/jiku-docs/internal/blob"
	"github.com/gravadigital/jiku-docs/internal/markdown"
	"github.com/gravadigital/jiku-docs/internal/metrics"
	"github.com/gravadigital/jiku-docs/internal/store"
)

// Durable is the consumer name of the indexer.
const Durable = "jiku-docs-indexer"

// Indexer indexes documents.
type Indexer struct {
	DB      *pgxpool.Pool
	Blob    blob.Store
	Log     *slog.Logger
	Metrics *metrics.Metrics
}

// Run consumes the document events until ctx ends.
func (ix *Indexer) Run(ctx context.Context, js jetstream.JetStream, stream, instance string) error {
	for ctx.Err() == nil {
		err := ix.consume(ctx, js, stream, instance)
		if err != nil && ctx.Err() == nil {
			ix.Log.Warn("indexer consumer restarting", "err", err.Error())
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}
	return nil
}

func (ix *Indexer) consume(ctx context.Context, js jetstream.JetStream, stream, instance string) error {
	cons, err := js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:       Durable,
		Description:   "jiku-docs: indexador de búsqueda",
		FilterSubject: instance + ".docs-events.v1.document.>",
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckWait:       2 * time.Minute,
		MaxAckPending: 100,
	})
	if err != nil {
		return err
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		if err := ix.Handle(ctx, msg.Data()); err != nil {
			ix.Log.Warn("indexing failed, will retry", "subject", msg.Subject(), "err", err.Error())
			_ = msg.NakWithDelay(10 * time.Second)
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		return err
	}
	defer cc.Stop()
	<-ctx.Done()
	return nil
}

type envelope struct {
	Type       string `json:"type"`
	OccurredAt string `json:"occurredAt"`
	Entity     struct {
		ID string `json:"id"`
	} `json:"entity"`
	Snapshot struct {
		Version int `json:"version"`
	} `json:"snapshot"`
}

// Handle processes one event.
func (ix *Indexer) Handle(ctx context.Context, data []byte) error {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		ix.Log.Warn("indexer: discarding malformed event", "err", err.Error())
		return nil
	}
	if t, err := time.Parse(time.RFC3339Nano, env.OccurredAt); err == nil {
		ix.Metrics.IndexerLag(time.Since(t))
	}
	switch env.Type {
	case "document.deleted":
		return ix.Remove(ctx, env.Entity.ID)
	case "document.created", "document.updated", "document.restored":
		return ix.IndexDocument(ctx, env.Entity.ID, false)
	}
	return nil // moved, locked, unlocked: the indexed text does not change
}

// Remove deletes the fragments of a document.
func (ix *Indexer) Remove(ctx context.Context, docID string) error {
	return store.InTx(ctx, ix.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM index_chunks WHERE document_id = $1`, docID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM index_status WHERE document_id = $1`, docID)
		return err
	})
}

// IndexDocument indexes the current version of a document. Unless force, a version already
// indexed (or a newer one) is skipped, so replays and out-of-order events are harmless.
func (ix *Indexer) IndexDocument(ctx context.Context, docID string, force bool) error {
	var title, key string
	var version int
	var deleted bool
	err := ix.DB.QueryRow(ctx, `SELECT d.title, d.current_version, d.deleted_at IS NOT NULL, v.storage_key
		FROM documents d JOIN document_versions v ON v.document_id = d.id AND v.version = d.current_version
		WHERE d.id = $1`, docID).Scan(&title, &version, &deleted, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return ix.Remove(ctx, docID)
	}
	if err != nil {
		return err
	}
	if deleted {
		return ix.Remove(ctx, docID)
	}
	if !force {
		var indexed int
		err := ix.DB.QueryRow(ctx, `SELECT version FROM index_status WHERE document_id = $1`, docID).Scan(&indexed)
		if err == nil && indexed >= version {
			return nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	content, err := ix.Blob.Get(ctx, key, 6*1024*1024)
	if err != nil {
		return fmt.Errorf("reading %s v%d: %w", docID, version, err)
	}
	chunks := markdown.Chunks(markdown.StripFrontmatter(string(content)))
	err = store.InTx(ctx, ix.DB, func(tx pgx.Tx) error {
		// Re-check under the row lock: a newer version may have been indexed meanwhile.
		var current int
		if err := tx.QueryRow(ctx, `SELECT current_version FROM documents WHERE id = $1 FOR SHARE`, docID).Scan(&current); err != nil {
			return err
		}
		if current != version {
			return nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM index_chunks WHERE document_id = $1`, docID); err != nil {
			return err
		}
		for i, ch := range chunks {
			_, err := tx.Exec(ctx, `INSERT INTO index_chunks (document_id, version, chunk_no, heading_path, body, tsv)
				VALUES ($1, $2, $3, $4, $5,
					setweight(to_tsvector('es_unaccent', $6 || ' ' || array_to_string($4::text[], ' ')), 'A') ||
					setweight(to_tsvector('es_unaccent', $5), 'B'))`,
				docID, version, i, ch.HeadingPath, ch.Body, title)
			if err != nil {
				return err
			}
		}
		if len(chunks) == 0 {
			// A document without text is still findable by its title.
			if _, err := tx.Exec(ctx, `INSERT INTO index_chunks (document_id, version, chunk_no, heading_path, body, tsv)
				VALUES ($1, $2, 0, '{}', '', setweight(to_tsvector('es_unaccent', $3), 'A'))`, docID, version, title); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO index_status (document_id, version, indexed_at, doc_tsv)
			VALUES ($1, $2, now(), (SELECT COALESCE(tsvector_agg(tsv ORDER BY chunk_no), ''::tsvector) FROM index_chunks WHERE document_id = $1))
			ON CONFLICT (document_id) DO UPDATE SET version = EXCLUDED.version, indexed_at = EXCLUDED.indexed_at, doc_tsv = EXCLUDED.doc_tsv`, docID, version)
		return err
	})
	if err == nil {
		ix.Metrics.Indexed()
	}
	return err
}

// Reindex rebuilds the whole index. It runs in-process over every live document instead of
// injecting synthetic events in the stream, which other consumers would see.
func (ix *Indexer) Reindex(ctx context.Context, progress func(done, total int)) error {
	rows, err := ix.DB.Query(ctx, `SELECT id FROM documents WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if _, err := ix.DB.Exec(ctx, `DELETE FROM index_chunks WHERE document_id IN (SELECT id FROM documents WHERE deleted_at IS NOT NULL)`); err != nil {
		return err
	}
	for i, id := range ids {
		if err := ix.IndexDocument(ctx, id, true); err != nil {
			return fmt.Errorf("document %s: %w", id, err)
		}
		if progress != nil {
			progress(i+1, len(ids))
		}
	}
	return nil
}
