package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gravadigital/jiku-docs/internal/caller"
	"github.com/gravadigital/jiku-docs/internal/markdown"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/store"
)

// EventActor is who caused an event.
type EventActor struct {
	ID   string  `json:"id"`
	Name *string `json:"name"`
	Via  string  `json:"via"`
}

// Envelope is the event as stored in the changelog and published on JIKU_DOCS_EVENTS. It never
// carries document content nor presigned URLs.
type Envelope struct {
	EventID       string         `json:"eventId"`
	Seq           int64          `json:"seq"`
	Type          string         `json:"type"`
	Version       string         `json:"version"`
	OccurredAt    string         `json:"occurredAt"`
	CorrelationID string         `json:"correlationId"`
	Actor         EventActor     `json:"actor"`
	Entity        map[string]any `json:"entity"`
	Snapshot      map[string]any `json:"snapshot,omitempty"`
	Changes       map[string]any `json:"changes,omitempty"`
}

// event is what a command emits, before it has a seq.
type event struct {
	Type       string
	Entity     map[string]any
	Snapshot   map[string]any
	Changes    map[string]any
	FolderID   string
	DocumentID string
	Via        string
}

// emitter writes events in the command's transaction.
type emitter struct {
	tx     pgx.Tx
	c      *caller.Caller
	corr   string
	now    time.Time
	locked bool
}

func (s *Service) emitter(tx pgx.Tx, c *caller.Caller, corr string) *emitter {
	if corr == "" {
		corr = protocol.NewULID()
	}
	return &emitter{tx: tx, c: c, corr: corr, now: s.now()}
}

func (e *emitter) emit(ctx context.Context, ev event) error {
	if !e.locked {
		// store.LockChangelog serialises the changelog inserts of concurrent transactions, so
		// `seq` order is commit order: a reader of `documents.changes` (or the publisher) never
		// skips a seq that a slower transaction commits later. Transaction-scoped: it ends with
		// the commit or the rollback.
		if _, err := e.tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(store.LockChangelog)); err != nil {
			return err
		}
		e.locked = true
	}
	via := ev.Via
	if via == "" || e.c.IsService() {
		via = e.c.DefaultVia(via)
	}
	var name *string
	if e.c.Name != "" {
		n := e.c.Name
		name = &n
	}
	env := Envelope{
		EventID: protocol.NewULID(), Type: ev.Type, Version: protocol.EventsVersion,
		OccurredAt: protocol.FormatTime(e.now), CorrelationID: e.corr,
		Actor:  EventActor{ID: e.c.ID, Name: name, Via: via},
		Entity: ev.Entity, Snapshot: ev.Snapshot, Changes: ev.Changes,
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return err
	}
	entityType, _ := ev.Entity["type"].(string)
	entityID, _ := ev.Entity["id"].(string)
	_, err = e.tx.Exec(ctx, `INSERT INTO changelog (event_id, type, entity_type, entity_id, folder_id, document_id, payload, occurred_at)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7, $8)`,
		env.EventID, env.Type, entityType, entityID, ev.FolderID, ev.DocumentID, payload, e.now)
	return err
}

// docState is the metadata snapshot of a document after a change.
type docState struct {
	doc       *docRow
	relations markdown.Relations
	topics    []string
	lock      *lockRow
}

func (s *Service) loadDocState(ctx context.Context, tx pgx.Tx, id string) (*docState, error) {
	d, err := getDoc(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	rels, err := loadRelations(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	topics, err := loadTopics(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	lock, err := loadLock(ctx, tx, id, s.now())
	if err != nil {
		return nil, err
	}
	return &docState{doc: d, relations: relationsView(rels), topics: topics, lock: lock}, nil
}

func (st *docState) entity() map[string]any {
	return map[string]any{"type": "document", "id": st.doc.ID, "folderId": st.doc.FolderID, "path": st.doc.Path}
}

func (st *docState) snapshot() map[string]any {
	var lock any
	if st.lock != nil {
		lock = st.lock.view()
	}
	return map[string]any{
		"title": st.doc.Title, "version": st.doc.Version, "checksum": st.doc.Checksum,
		"sizeBytes": st.doc.Size, "visibility": st.doc.Visibility, "folderId": st.doc.FolderID,
		"path": st.doc.Path, "relations": st.relations, "topics": st.topics,
		"deleted": st.doc.DeletedAt != nil, "lock": lock,
	}
}

func (e *emitter) docEvent(ctx context.Context, typ string, st *docState, changes map[string]any, via string) error {
	return e.emit(ctx, event{
		Type: typ, Entity: st.entity(), Snapshot: st.snapshot(), Changes: changes,
		FolderID: st.doc.FolderID, DocumentID: st.doc.ID, Via: via,
	})
}

// diffRelations returns {added, removed} with only the non-empty kinds.
func diffRelations(before, after markdown.Relations) map[string]any {
	added, removed := map[string][]int64{}, map[string][]int64{}
	for _, k := range markdown.RelationKinds {
		b, a := toSet(before.Get(k.Key)), toSet(after.Get(k.Key))
		for _, id := range after.Get(k.Key) {
			if !b[id] {
				added[k.Key] = append(added[k.Key], id)
			}
		}
		for _, id := range before.Get(k.Key) {
			if !a[id] {
				removed[k.Key] = append(removed[k.Key], id)
			}
		}
	}
	if len(added) == 0 && len(removed) == 0 {
		return nil
	}
	out := map[string]any{}
	if len(added) > 0 {
		out["added"] = added
	}
	if len(removed) > 0 {
		out["removed"] = removed
	}
	return out
}

func toSet(ids []int64) map[int64]bool {
	m := make(map[int64]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func diffStrings(before, after []string) map[string]any {
	b, a := map[string]bool{}, map[string]bool{}
	for _, x := range before {
		b[x] = true
	}
	for _, x := range after {
		a[x] = true
	}
	var added, removed []string
	for _, x := range after {
		if !b[x] {
			added = append(added, x)
		}
	}
	for _, x := range before {
		if !a[x] {
			removed = append(removed, x)
		}
	}
	if len(added) == 0 && len(removed) == 0 {
		return nil
	}
	out := map[string]any{}
	if len(added) > 0 {
		out["added"] = added
	}
	if len(removed) > 0 {
		out["removed"] = removed
	}
	return out
}

func fromTo(from, to any) map[string]any { return map[string]any{"from": from, "to": to} }
