package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/gravadigital/jiku-docs/internal/acl"
	"github.com/gravadigital/jiku-docs/internal/caller"
)

// Notification is one message to a person: who, which notify type, and the body.
type Notification struct {
	UserID string
	Type   string
	Body   []byte
}

// Notifications computes the per-person fan-out of a published event (section 8):
//   - document.* and attachment.*: every person with read on the document at this moment,
//     connected or not, with the envelope minus snapshot.relations and changes.relations;
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
	tree, err := s.Tree(ctx)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(stripRelations(env))
	if err != nil {
		return nil, err
	}
	var out []Notification
	for _, p := range people {
		v := &viewer{c: p, tree: tree, ev: tree.For(p.Principal())}
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
	body, err := json.Marshal(map[string]any{
		"eventId": env["eventId"], "seq": env["seq"], "type": "access.changed", "version": env["version"],
		"occurredAt": env["occurredAt"],
	})
	if err != nil {
		return nil, err
	}
	var out []Notification
	for _, p := range people {
		if p.Class == caller.PersonInternal {
			out = append(out, Notification{UserID: p.ID, Type: "access.changed", Body: body})
		}
	}
	return out, nil
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
