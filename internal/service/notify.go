package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/gravadigital/jiku-docs/internal/acl"
	"github.com/gravadigital/jiku-docs/internal/caller"
	"github.com/gravadigital/jiku-docs/internal/markdown"
)

// Notification is one message to a person: who, which notify type, and the body.
type Notification struct {
	UserID string
	Type   string
	Body   []byte
}

// Notifications computes the per-person fan-out of a published event (section 8):
//   - document.* and attachment.*: every person with read on the document at this moment,
//     connected or not, with the envelope minus snapshot.relations and changes.relations
//     (a restricted document: only admins). A document that became restricted also sends
//     access.changed, without detail, to the users who stop reading it;
//   - folder.acl.changed and folder.updated: every internal person, as `access.changed`
//     without detail — the signal to reconcile;
//   - share.*, folder.created, folder.deleted: nobody (stream only).
func (s *Service) Notifications(ctx context.Context, env map[string]any) ([]Notification, error) {
	typ, _ := env["type"].(string)
	entity, _ := env["entity"].(map[string]any)
	switch {
	case typ == "folder.acl.changed" || typ == "folder.updated":
		return s.accessChanged(ctx, env)
	case strings.HasPrefix(typ, "document."), strings.HasPrefix(typ, "attachment."):
	default:
		return nil, nil
	}
	docID, _ := entity["id"].(string)
	if strings.HasPrefix(typ, "attachment.") {
		docID, _ = entity["documentId"].(string)
	}
	d, err := getDoc(ctx, s.DB, docID, false)
	if err != nil || d == nil {
		return nil, err
	}
	people, err := s.people(ctx)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(stripRelations(env))
	if err != nil {
		return nil, err
	}
	var out []Notification
	if becameRestricted(env) {
		// Persons who stop reading the document get no detail of it: an access.changed (without
		// ids) is the signal for their sync daemon to reconcile and drop it.
		ac, err := s.accessChangedFor(env, people, func(p *caller.Caller) bool { return p.Class == caller.PersonInternal && !p.IsAdmin() })
		if err != nil {
			return nil, err
		}
		out = append(out, ac...)
	}
	if lostPublic(env) {
		// Externals who read it while it was public and no longer do: same signal, no detail.
		var related []int64
		rows, err := s.DB.Query(ctx, `SELECT DISTINCT project_id FROM document_relations WHERE document_id = $1 AND project_id IS NOT NULL`, d.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p int64
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return nil, err
			}
			related = append(related, p)
		}
		rows.Close()
		for _, p := range people {
			if !p.IsExternal() || len(related) == 0 {
				continue
			}
			projects, err := s.Core.ProjectPermissions(ctx, p.ID)
			if err != nil {
				return nil, err
			}
			if intersects(projects, related) {
				ac, err := s.accessChangedFor(env, []*caller.Caller{p}, func(*caller.Caller) bool { return true })
				if err != nil {
					return nil, err
				}
				out = append(out, ac...)
			}
		}
	}
	for _, p := range people {
		v := &viewer{c: p}
		if p.IsExternal() {
			if d.Visibility != "public" || d.DeletedAt != nil {
				continue
			}
			projects, err := s.Core.ProjectPermissions(ctx, p.ID)
			if err != nil {
				return nil, err
			}
			v.projects = projects
		}
		perm, err := v.docPerm(ctx, s.DB, d)
		if err != nil {
			return nil, err
		}
		if perm >= acl.Read {
			out = append(out, Notification{UserID: p.ID, Type: typ, Body: body})
		}
	}
	return out, nil
}

func (s *Service) accessChanged(ctx context.Context, env map[string]any) ([]Notification, error) {
	people, err := s.people(ctx)
	if err != nil {
		return nil, err
	}
	return s.accessChangedFor(env, people, func(p *caller.Caller) bool { return p.Class == caller.PersonInternal })
}

func (s *Service) accessChangedFor(env map[string]any, people []*caller.Caller, who func(*caller.Caller) bool) ([]Notification, error) {
	body, err := json.Marshal(map[string]any{
		"eventId": env["eventId"], "seq": env["seq"], "type": "access.changed", "version": env["version"],
		"occurredAt": env["occurredAt"],
	})
	if err != nil {
		return nil, err
	}
	var out []Notification
	for _, p := range people {
		if who(p) {
			out = append(out, Notification{UserID: p.ID, Type: "access.changed", Body: body})
		}
	}
	return out, nil
}

// lostPublic reports a document event whose visibility changed from public to another one.
func lostPublic(env map[string]any) bool {
	ch, _ := env["changes"].(map[string]any)
	vis, _ := ch["visibility"].(map[string]any)
	return vis != nil && vis["from"] == markdown.VisibilityPublic && vis["to"] != markdown.VisibilityPublic
}

func intersects(a, b []int64) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// becameRestricted reports a document event whose visibility changed to restricted.
func becameRestricted(env map[string]any) bool {
	ch, _ := env["changes"].(map[string]any)
	vis, _ := ch["visibility"].(map[string]any)
	return vis != nil && vis["to"] == markdown.VisibilityRestricted
}

// people are core's users that are persons with a product role.
func (s *Service) people(ctx context.Context) ([]*caller.Caller, error) {
	users, err := s.Core.Users(ctx)
	if err != nil {
		return nil, err
	}
	var out []*caller.Caller
	for _, u := range users {
		class, ok := caller.ClassFor(u.Roles, u.IdentityType)
		if !ok || class == caller.Service {
			continue
		}
		out = append(out, &caller.Caller{ID: u.ID, Name: u.Name, Class: class, Roles: u.Roles})
	}
	return out, nil
}

// stripRelations removes what may reveal projects a person does not see.
func stripRelations(env map[string]any) map[string]any {
	out := make(map[string]any, len(env))
	for k, v := range env {
		out[k] = v
	}
	for _, key := range []string{"snapshot", "changes"} {
		if m, ok := env[key].(map[string]any); ok {
			c := make(map[string]any, len(m))
			for k, v := range m {
				if k != "relations" {
					c[k] = v
				}
			}
			out[key] = c
		}
	}
	return out
}
