// Package acl computes effective permissions over the folder tree.
//
// A rule is folder + principal (a role or a user id) + permission. The effective permission of a
// person on a folder is the maximum among the rules of the folder that match them and, when the
// folder inherits, the effective rules of its parent, recursively. No rules, no access. There
// are no deny rules: a subfolder is restricted with inheritAcl = false and explicit rules.
package acl

import (
	"sort"
	"strings"
	"time"
)

// Perm is a permission level. Each level includes the previous ones.
type Perm int

const (
	None Perm = iota
	Read
	Write
	Manage
)

func (p Perm) String() string {
	switch p {
	case Read:
		return "read"
	case Write:
		return "write"
	case Manage:
		return "manage"
	}
	return ""
}

// ParsePerm reads a stored permission.
func ParsePerm(s string) Perm {
	switch s {
	case "read":
		return Read
	case "write":
		return Write
	case "manage":
		return Manage
	}
	return None
}

// Rule is one ACL entry.
type Rule struct {
	PrincipalType string `json:"principalType"`
	Principal     string `json:"principal"`
	Permission    string `json:"permission"`
}

// Folder is a node of the tree with its own rules.
type Folder struct {
	ID              string
	ParentID        string // "" for the root
	Name            string
	Path            string
	InheritACL      bool
	ExternalBlocked bool
	SharingBlocked  bool
	CreatedBy       string
	CreatedAt       time.Time
	Rules           []Rule
}

// Principal is who asks: a user id and its roles. All grants manage everywhere (service class).
type Principal struct {
	UserID string
	Roles  []string
	All    bool
}

// Tree is an immutable snapshot of the live folders and their rules.
type Tree struct {
	Version  int64
	Folders  map[string]*Folder
	Children map[string][]string
}

// NewTree indexes folders.
func NewTree(version int64, folders []*Folder) *Tree {
	t := &Tree{Version: version, Folders: map[string]*Folder{}, Children: map[string][]string{}}
	for _, f := range folders {
		t.Folders[f.ID] = f
	}
	for _, f := range folders {
		if f.ParentID != "" {
			t.Children[f.ParentID] = append(t.Children[f.ParentID], f.ID)
		}
	}
	for k := range t.Children {
		sort.Strings(t.Children[k])
	}
	return t
}

// Get returns a live folder.
func (t *Tree) Get(id string) (*Folder, bool) {
	f, ok := t.Folders[id]
	return f, ok
}

func ruleMatches(r Rule, p Principal) bool {
	switch r.PrincipalType {
	case "user":
		return r.Principal == p.UserID
	case "role":
		for _, role := range p.Roles {
			if role == r.Principal {
				return true
			}
		}
	}
	return false
}

// Evaluator memoises effective permissions of one principal over a tree. Build one per request.
type Evaluator struct {
	tree *Tree
	p    Principal
	memo map[string]Perm
}

// For returns an evaluator for a principal.
func (t *Tree) For(p Principal) *Evaluator {
	return &Evaluator{tree: t, p: p, memo: map[string]Perm{}}
}

// Permission is the effective permission on a folder. A missing folder is None.
func (e *Evaluator) Permission(folderID string) Perm {
	if e.p.All {
		if _, ok := e.tree.Folders[folderID]; ok {
			return Manage
		}
		return None
	}
	if p, ok := e.memo[folderID]; ok {
		return p
	}
	f, ok := e.tree.Folders[folderID]
	if !ok {
		return None
	}
	e.memo[folderID] = None // cycle guard; the tree cannot have cycles, but never loop forever
	best := None
	for _, r := range f.Rules {
		if ruleMatches(r, e.p) {
			if perm := ParsePerm(r.Permission); perm > best {
				best = perm
			}
		}
	}
	if f.InheritACL && f.ParentID != "" {
		if parent := e.Permission(f.ParentID); parent > best {
			best = parent
		}
	}
	e.memo[folderID] = best
	return best
}

// Folders returns the ids of the folders where the principal has at least min.
func (e *Evaluator) Folders(min Perm) []string {
	out := make([]string, 0, len(e.tree.Folders))
	for id := range e.tree.Folders {
		if e.Permission(id) >= min {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// ExternalBlocked is the effective flag: the folder or any ancestor.
func (t *Tree) ExternalBlocked(id string) bool {
	for f, ok := t.Folders[id]; ok; f, ok = t.Folders[f.ParentID] {
		if f.ExternalBlocked {
			return true
		}
		if f.ParentID == "" {
			break
		}
	}
	return false
}

// SharingBlocked is the effective flag: the folder or any ancestor.
func (t *Tree) SharingBlocked(id string) bool {
	for f, ok := t.Folders[id]; ok; f, ok = t.Folders[f.ParentID] {
		if f.SharingBlocked {
			return true
		}
		if f.ParentID == "" {
			break
		}
	}
	return false
}

// IsSelfOrDescendant reports whether id is ancestor or lies below it.
func (t *Tree) IsSelfOrDescendant(id, ancestor string) bool {
	for f, ok := t.Folders[id]; ok; f, ok = t.Folders[f.ParentID] {
		if f.ID == ancestor {
			return true
		}
		if f.ParentID == "" {
			break
		}
	}
	return false
}

// NotExternalBlocked returns the folders a client may see documents in.
func (t *Tree) NotExternalBlocked() []string {
	out := make([]string, 0, len(t.Folders))
	for id := range t.Folders {
		if !t.ExternalBlocked(id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Subtree returns the folder and all its live descendants.
func (t *Tree) Subtree(id string) []string {
	var out []string
	var walk func(string)
	walk = func(x string) {
		out = append(out, x)
		for _, c := range t.Children[x] {
			walk(c)
		}
	}
	if _, ok := t.Folders[id]; ok {
		walk(id)
	}
	return out
}

// ByPath finds a live folder by its path.
func (t *Tree) ByPath(path string) (*Folder, bool) {
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	for _, f := range t.Folders {
		if f.Path == path {
			return f, true
		}
	}
	return nil, false
}
