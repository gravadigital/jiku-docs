package coreapi

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"
)

// TTLs of the cached data (specification, section 4).
const (
	UserTTL        = 60 * time.Second
	UnknownUserTTL = 10 * time.Second
	ProjectsTTL    = 60 * time.Second
	EntityTTL      = 5 * time.Minute
)

type entry[T any] struct {
	value   T
	expires time.Time
}

type ttlMap[T any] struct {
	mu sync.Mutex
	m  map[string]entry[T]
}

func (c *ttlMap[T]) get(key string, now time.Time) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || now.After(e.expires) {
		var zero T
		return zero, false
	}
	return e.value, true
}

func (c *ttlMap[T]) put(key string, v T, ttl time.Duration, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]entry[T]{}
	}
	if len(c.m) > 50_000 {
		for k, e := range c.m {
			if now.After(e.expires) {
				delete(c.m, k)
			}
		}
	}
	c.m[key] = entry[T]{value: v, expires: now.Add(ttl)}
}

// Cached wraps an API with the TTLs of the specification. Cached data keeps being used until it
// expires, also while core is down; nothing is ever granted on a miss with core down.
type Cached struct {
	API
	Now func() time.Time

	users    ttlMap[*User]
	all      ttlMap[[]User]
	projects ttlMap[[]int64]
	entities ttlMap[Entity]
	catalog  ttlMap[any]
}

// NewCached wraps api.
func NewCached(api API) *Cached { return &Cached{API: api, Now: time.Now} }

func (c *Cached) User(ctx context.Context, id string) (*User, error) {
	now := c.Now()
	if u, ok := c.users.get(id, now); ok {
		return u, nil
	}
	u, err := c.API.User(ctx, id)
	if err != nil {
		return nil, err
	}
	ttl := UserTTL
	if u == nil {
		ttl = UnknownUserTTL
	}
	c.users.put(id, u, ttl, now)
	return u, nil
}

func (c *Cached) Users(ctx context.Context) ([]User, error) {
	now := c.Now()
	if u, ok := c.all.get("", now); ok {
		return u, nil
	}
	u, err := c.API.Users(ctx)
	if err != nil {
		return nil, err
	}
	c.all.put("", u, UserTTL, now)
	for i := range u {
		c.users.put(u[i].ID, &u[i], UserTTL, now)
	}
	return u, nil
}

func (c *Cached) ProjectPermissions(ctx context.Context, userID string) ([]int64, error) {
	now := c.Now()
	if p, ok := c.projects.get(userID, now); ok {
		return p, nil
	}
	p, err := c.API.ProjectPermissions(ctx, userID)
	if err != nil {
		return nil, err
	}
	sort.Slice(p, func(i, j int) bool { return p[i] < p[j] })
	c.projects.put(userID, p, ProjectsTTL, now)
	return p, nil
}

// Entities caches positives for 5 minutes. A miss is never cached, so an entity created a
// moment ago can be linked right away.
func (c *Cached) Entities(ctx context.Context, entityType string, ids []int64) (map[int64]Entity, error) {
	now := c.Now()
	out := map[int64]Entity{}
	var missing []int64
	for _, id := range ids {
		if e, ok := c.entities.get(entityType+":"+strconv.FormatInt(id, 10), now); ok {
			out[id] = e
		} else {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	found, err := c.API.Entities(ctx, entityType, missing)
	if err != nil {
		return nil, err
	}
	for id, e := range found {
		out[id] = e
		c.entities.put(entityType+":"+strconv.FormatInt(id, 10), e, EntityTTL, now)
	}
	return out, nil
}

// Projects caches the list of projects for EntityTTL.
func (c *Cached) Projects(ctx context.Context) ([]Project, error) {
	now := c.Now()
	if v, ok := c.catalog.get("projects", now); ok {
		return v.([]Project), nil
	}
	p, err := c.API.Projects(ctx)
	if err != nil {
		return nil, err
	}
	c.catalog.put("projects", p, EntityTTL, now)
	return p, nil
}

// Clients caches the list of clients for EntityTTL.
func (c *Cached) Clients(ctx context.Context) ([]JikuClient, error) {
	now := c.Now()
	if v, ok := c.catalog.get("clients", now); ok {
		return v.([]JikuClient), nil
	}
	cl, err := c.API.Clients(ctx)
	if err != nil {
		return nil, err
	}
	c.catalog.put("clients", cl, EntityTTL, now)
	return cl, nil
}

// EntitiesFresh bypasses the cache (the daily relation review must see deletions).
func (c *Cached) EntitiesFresh(ctx context.Context, entityType string, ids []int64) (map[int64]Entity, error) {
	return c.API.Entities(ctx, entityType, ids)
}
