package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gravadigital/jiku-docs/internal/coreapi"
)

// fakeCore answers users from a map and counts the calls; down makes every call fail.
type fakeCore struct {
	coreapi.API
	users               map[string]coreapi.User
	down                bool
	userCalls, allCalls int
}

func (f *fakeCore) User(_ context.Context, id string) (*coreapi.User, error) {
	f.userCalls++
	if f.down {
		return nil, coreapi.ErrUnavailable
	}
	if u, ok := f.users[id]; ok {
		return &u, nil
	}
	return nil, nil
}

func (f *fakeCore) Users(context.Context) ([]coreapi.User, error) {
	f.allCalls++
	if f.down {
		return nil, errors.New("no responders")
	}
	var out []coreapi.User
	for _, u := range f.users {
		out = append(out, u)
	}
	return out, nil
}

func namesService() (*Service, *fakeCore, *time.Time) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	core := &fakeCore{users: map[string]coreapi.User{
		"ana":   {ID: "ana", Name: "Ana Pérez"},
		"bruno": {ID: "bruno", Username: "bruno"},
	}}
	s := &Service{Core: core, Now: func() time.Time { return now }}
	return s, core, &now
}

func TestResolveNamesCachesHitsAndMisses(t *testing.T) {
	s, core, now := namesService()
	ctx := context.Background()
	if got := s.resolveNames(ctx, []string{"ana"}); got["ana"] != "Ana Pérez" || core.userCalls != 1 {
		t.Fatalf("first lookup: %v, %d calls", got, core.userCalls)
	}
	if got := s.resolveNames(ctx, []string{"ana", "ana"}); got["ana"] != "Ana Pérez" || core.userCalls != 1 {
		t.Fatalf("cached lookup: %v, %d calls", got, core.userCalls)
	}
	// Several ids: one users.list, the username when there is no name, unknown ids absent.
	got := s.resolveNames(ctx, []string{"ana", "bruno", "nadie", "otro"})
	if got["bruno"] != "bruno" || got["ana"] != "Ana Pérez" || len(got) != 2 || core.allCalls != 1 {
		t.Fatalf("batch lookup: %v, %d calls", got, core.allCalls)
	}
	// Unknown ids are cached for a short while, then asked again.
	s.resolveNames(ctx, []string{"nadie"})
	if core.userCalls != 1 {
		t.Fatalf("unknown id asked again before its TTL: %d", core.userCalls)
	}
	*now = now.Add(unknownNameTTL + time.Second)
	core.users["nadie"] = coreapi.User{ID: "nadie", Name: "Nueva"}
	if got := s.resolveNames(ctx, []string{"nadie"}); got["nadie"] != "Nueva" {
		t.Fatalf("unknown id after its TTL: %v", got)
	}
	// Known names live for nameTTL.
	core.users["ana"] = coreapi.User{ID: "ana", Name: "Ana P."}
	if got := s.resolveNames(ctx, []string{"ana"}); got["ana"] != "Ana Pérez" {
		t.Fatalf("name refreshed before its TTL: %v", got)
	}
	*now = now.Add(nameTTL)
	if got := s.resolveNames(ctx, []string{"ana"}); got["ana"] != "Ana P." {
		t.Fatalf("name after its TTL: %v", got)
	}
}

func TestResolveNamesCoreDownBacksOff(t *testing.T) {
	s, core, now := namesService()
	ctx := context.Background()
	core.down = true
	if got := s.resolveNames(ctx, []string{"ana"}); len(got) != 0 || core.userCalls != 1 {
		t.Fatalf("core down: %v, %d calls", got, core.userCalls)
	}
	// Within the backoff core is not asked; nothing is cached as unknown either.
	if got := s.resolveNames(ctx, []string{"ana", "bruno"}); len(got) != 0 || core.userCalls != 1 || core.allCalls != 0 {
		t.Fatalf("within backoff: %v, %d/%d calls", got, core.userCalls, core.allCalls)
	}
	core.down = false
	*now = now.Add(nameBackoff + time.Second)
	if got := s.resolveNames(ctx, []string{"ana"}); got["ana"] != "Ana Pérez" {
		t.Fatalf("after backoff: %v", got)
	}
}

func TestFillNames(t *testing.T) {
	s, core, _ := namesService()
	items := []map[string]any{
		{"createdBy": "ana", "createdByName": nil, "updatedBy": "bruno", "updatedByName": "Stored"},
		{"createdBy": "nadie", "createdByName": nil, "updatedBy": "ana"}, // updatedByName not projected
		{"updatedBy": "", "updatedByName": nil},
	}
	s.fillNames(context.Background(), items, docNamePairs...)
	if items[0]["createdByName"] != "Ana Pérez" || items[0]["updatedByName"] != "Stored" {
		t.Errorf("item 0: %v", items[0])
	}
	if items[1]["createdByName"] != nil {
		t.Errorf("unknown id got a name: %v", items[1])
	}
	if _, ok := items[1]["updatedByName"]; ok {
		t.Errorf("a key that was not projected was added: %v", items[1])
	}
	if items[2]["updatedByName"] != nil {
		t.Errorf("empty id got a name: %v", items[2])
	}
	// Nothing to fill: core is not asked.
	calls := core.userCalls + core.allCalls
	s.fillNames(context.Background(), []map[string]any{{"holder": "ana", "holderName": "Ana"}}, lockNamePairs...)
	if core.userCalls+core.allCalls != calls {
		t.Errorf("core asked with every name present")
	}
}
