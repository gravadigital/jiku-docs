package acl

import "testing"

func tree() *Tree {
	return NewTree(1, []*Folder{
		{ID: "root", Path: "/", Rules: []Rule{{"role", "admin", "manage"}}},
		{ID: "clientes", ParentID: "root", Path: "/clientes/", InheritACL: true, Rules: []Rule{{"role", "user", "read"}}},
		{ID: "acme", ParentID: "clientes", Path: "/clientes/acme/", InheritACL: true, Rules: []Rule{{"user", "u1", "write"}}},
		{ID: "infra", ParentID: "root", Path: "/infra/", InheritACL: false, ExternalBlocked: true, SharingBlocked: true,
			Rules: []Rule{{"user", "u2", "manage"}}},
		{ID: "srv", ParentID: "infra", Path: "/infra/srv/", InheritACL: true},
	})
}

func TestPermission(t *testing.T) {
	tr := tree()
	cases := []struct {
		p      Principal
		folder string
		want   Perm
	}{
		{Principal{UserID: "a", Roles: []string{"admin"}}, "acme", Manage},
		{Principal{UserID: "a", Roles: []string{"admin"}}, "infra", None}, // inheritAcl=false cuts the root rule
		{Principal{UserID: "u1", Roles: []string{"user"}}, "acme", Write},
		{Principal{UserID: "u1", Roles: []string{"user"}}, "clientes", Read},
		{Principal{UserID: "u3", Roles: []string{"user"}}, "acme", Read},
		{Principal{UserID: "u2", Roles: []string{"user"}}, "srv", Manage},
		{Principal{UserID: "u9"}, "acme", None},
		{Principal{All: true}, "srv", Manage},
		{Principal{All: true}, "missing", None},
	}
	for _, c := range cases {
		if got := tr.For(c.p).Permission(c.folder); got != c.want {
			t.Errorf("%+v on %s: %v, want %v", c.p, c.folder, got, c.want)
		}
	}
}

func TestInheritedFlags(t *testing.T) {
	tr := tree()
	if !tr.ExternalBlocked("srv") || !tr.SharingBlocked("srv") || tr.SharingBlocked("acme") {
		t.Error("flags must be inherited from ancestors")
	}
	if !tr.IsSelfOrDescendant("acme", "clientes") || tr.IsSelfOrDescendant("clientes", "acme") {
		t.Error("descendant check")
	}
	if got := tr.Subtree("clientes"); len(got) != 2 {
		t.Errorf("subtree %v", got)
	}
	if f, ok := tr.ByPath("/infra/srv"); !ok || f.ID != "srv" {
		t.Error("by path")
	}
}
