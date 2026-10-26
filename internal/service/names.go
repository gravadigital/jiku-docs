package service

import (
	"context"
	"sync"
	"time"

	"github.com/gravadigital/jiku-docs/internal/coreapi"
)

// Author names (JIKU-WEB-5). The api builds `actor` from claims of the web token that do not
// carry the name, so a person writing from the web arrived without one and every name of what
// it wrote stayed null. Names are resolved with core's `users` on both ends:
//
//   - On write (Dispatch, commands only): an `actor` without `name` nor `username` gets the name
//     of its row in core, so the version, the document and the lock keep the name that person had
//     when writing (the documented rule: names are stored on write and do not follow renames), and
//     events carry it too.
//   - On read: a null name of a row (written before this fix, or while core did not answer) is
//     completed from core in the reply, without writing it back. Reads never write.
//
// A name is a label, never an access decision: when core does not answer the name stays null and
// the request goes on (no `upstream_unavailable`). Lookups are bounded by nameTimeout and, after a
// failure, skipped for nameBackoff, so a core down costs at most one short wait per backoff.
const (
	nameTTL        = 10 * time.Minute
	unknownNameTTL = time.Minute
	nameBackoff    = 30 * time.Second
	nameTimeout    = 2 * time.Second
)

type nameEntry struct {
	name    string // "" when core does not know the id
	expires time.Time
}

// nameCache is the in-memory cache of resolved names. The zero value is ready to use.
type nameCache struct {
	mu      sync.Mutex
	m       map[string]nameEntry
	backoff time.Time
}

func userName(u *coreapi.User) string {
	if u == nil {
		return ""
	}
	if u.Name != "" {
		return u.Name
	}
	return u.Username
}

// resolveNames returns the names core knows for ids (ids without a name are absent). It never
// fails: whatever it cannot resolve in time is left out.
func (s *Service) resolveNames(ctx context.Context, ids []string) map[string]string {
	out := map[string]string{}
	if len(ids) == 0 || s.Core == nil {
		return out
	}
	now := s.Now()
	c := &s.names
	var missing []string
	seen := map[string]bool{}
	c.mu.Lock()
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if e, ok := c.m[id]; ok && now.Before(e.expires) {
			if e.name != "" {
				out[id] = e.name
			}
			continue
		}
		missing = append(missing, id)
	}
	skip := now.Before(c.backoff)
	c.mu.Unlock()
	if len(missing) == 0 || skip {
		return out
	}

	ctx, cancel := context.WithTimeout(ctx, nameTimeout)
	defer cancel()
	found := map[string]string{}
	var err error
	if len(missing) == 1 {
		var u *coreapi.User
		if u, err = s.Core.User(ctx, missing[0]); err == nil {
			found[missing[0]] = userName(u)
		}
	} else {
		// One call for the whole page instead of one per author (cached by coreapi.Cached).
		var users []coreapi.User
		if users, err = s.Core.Users(ctx); err == nil {
			for i := range users {
				found[users[i].ID] = userName(&users[i])
			}
		}
	}

	now = s.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.backoff = now.Add(nameBackoff)
		if s.Log != nil {
			s.Log.Warn("author names: core did not answer, names stay null", "err", err.Error())
		}
		return out
	}
	if c.m == nil {
		c.m = map[string]nameEntry{}
	}
	if len(c.m) > 50_000 {
		for k, e := range c.m {
			if !now.Before(e.expires) {
				delete(c.m, k)
			}
		}
	}
	for id, n := range found {
		if n != "" {
			c.m[id] = nameEntry{name: n, expires: now.Add(nameTTL)}
		}
	}
	for _, id := range missing {
		n := found[id]
		if n == "" {
			c.m[id] = nameEntry{expires: now.Add(unknownNameTTL)}
			continue
		}
		out[id] = n
	}
	return out
}

// fillNames completes, in place, the null names of maps that hold an id and its name under
// pairs of keys {idKey, nameKey} (for example {"updatedBy", "updatedByName"}). Only keys that are
// present with a nil value are filled, so a projection without the name stays as it is.
func (s *Service) fillNames(ctx context.Context, items []map[string]any, pairs ...[2]string) {
	var ids []string
	for _, it := range items {
		for _, p := range pairs {
			if id, ok := it[p[0]].(string); ok && id != "" {
				if v, has := it[p[1]]; has && v == nil {
					ids = append(ids, id)
				}
			}
		}
	}
	if len(ids) == 0 {
		return
	}
	names := s.resolveNames(ctx, ids)
	for _, it := range items {
		for _, p := range pairs {
			id, _ := it[p[0]].(string)
			if v, has := it[p[1]]; has && v == nil {
				if n, ok := names[id]; ok {
					it[p[1]] = n
				}
			}
		}
	}
}

// Pairs of keys of the replies that carry an author id and its name.
var (
	docNamePairs     = [][2]string{{"createdBy", "createdByName"}, {"updatedBy", "updatedByName"}}
	versionNamePairs = [][2]string{{"author", "authorName"}}
	lockNamePairs    = [][2]string{{"holder", "holderName"}}
)
