package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/gravadigital/jiku-docs/internal/acl"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/store"
	"github.com/gravadigital/jiku-docs/internal/validate"
)

var folderNameRe = regexp.MustCompile(`^[^/\\]+$`)

func validFolderName(o *validate.Object, key string, required bool) (string, bool) {
	name, ok := o.String(key, required, validate.MinLen(1), validate.MaxLen(120), validate.Pattern(folderNameRe))
	if !ok {
		return "", false
	}
	bad := name != strings.TrimSpace(name) || name == "." || name == ".."
	for _, r := range name {
		if unicode.IsControl(r) {
			bad = true
		}
	}
	if bad {
		o.Fail(protocol.InvalidField(key, name, nil, "El nombre de carpeta no puede ser . ni .., ni empezar o terminar con espacios ni tener caracteres de control"))
		return "", false
	}
	return name, true
}

type folderRow struct {
	ID, ParentID, Name, Path             string
	InheritACL, ExternalBlocked, Sharing bool
}

func getFolder(ctx context.Context, q store.Querier, id string) (*folderRow, bool, error) {
	f := &folderRow{}
	var deleted bool
	err := q.QueryRow(ctx, `SELECT id, COALESCE(parent_id, ''), name, path, inherit_acl, external_blocked, sharing_blocked,
		deleted_at IS NOT NULL FROM folders WHERE id = $1`, id).
		Scan(&f.ID, &f.ParentID, &f.Name, &f.Path, &f.InheritACL, &f.ExternalBlocked, &f.Sharing, &deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return f, deleted, nil
}

func (f *folderRow) snapshot() map[string]any {
	var parent any
	if f.ParentID != "" {
		parent = f.ParentID
	}
	return map[string]any{"name": f.Name, "parentId": parent, "path": f.Path, "inheritAcl": f.InheritACL,
		"externalBlocked": f.ExternalBlocked, "sharingBlocked": f.Sharing}
}

func (f *folderRow) entity() map[string]any {
	return map[string]any{"type": "folder", "id": f.ID, "path": f.Path}
}

// requireFolderPerm: not readable = folder_not_found, readable without min = permission_denied.
func requireFolderPerm(ev *acl.Evaluator, tree *acl.Tree, id string, min acl.Perm) error {
	if _, ok := tree.Get(id); !ok {
		return protocol.E(protocol.CodeFolderNotFound)
	}
	p := ev.Permission(id)
	if p < acl.Read {
		return protocol.E(protocol.CodeFolderNotFound)
	}
	if p < min {
		return protocol.E(protocol.CodePermissionDenied)
	}
	return nil
}

func (s *Service) foldersNew(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	o := validate.ParseObject(c.Payload, "", "id", "parentId", "name", "inheritAcl", "externalBlocked", "sharingBlocked")
	id, _ := o.ULID("id", true)
	parentID, _ := o.ULID("parentId", true)
	name, _ := validFolderName(o, "name", true)
	inherit := o.Bool("inheritAcl", true)
	extBlocked := o.Bool("externalBlocked", false)
	sharing := o.Bool("sharingBlocked", false)
	if err := o.Err(); err != nil {
		return nil, err
	}
	return s.CreateFolder(ctx, c, id, parentID, name, inherit, extBlocked, sharing)
}

// CreateFolder creates a folder (also used in-process by the importer).
func (s *Service) CreateFolder(ctx context.Context, c *Call, id, parentID, name string, inherit, extBlocked, sharing bool) (map[string]any, error) {
	var out map[string]any
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		tree, err := treeTx(ctx, tx)
		if err != nil {
			return err
		}
		ev := tree.For(c.Caller.Principal())
		if err := requireFolderPerm(ev, tree, parentID, acl.Manage); err != nil {
			return err
		}
		existing, deleted, err := getFolder(ctx, tx, id)
		if err != nil {
			return err
		}
		if existing != nil {
			if !deleted && existing.ParentID == parentID && strings.EqualFold(existing.Name, name) &&
				existing.InheritACL == inherit && existing.ExternalBlocked == extBlocked && existing.Sharing == sharing {
				out = map[string]any{"id": existing.ID, "path": existing.Path}
				return nil
			}
			return protocol.Ef(protocol.CodeAlreadyExists, "Ya existe una carpeta con ese id")
		}
		parent, _ := tree.Get(parentID)
		f := &folderRow{ID: id, ParentID: parentID, Name: name, Path: parent.Path + name + "/",
			InheritACL: inherit, ExternalBlocked: extBlocked, Sharing: sharing}
		now := s.now()
		_, err = tx.Exec(ctx, `INSERT INTO folders (id, parent_id, name, path, inherit_acl, external_blocked, sharing_blocked,
			created_by, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`,
			f.ID, f.ParentID, f.Name, f.Path, f.InheritACL, f.ExternalBlocked, f.Sharing, c.Caller.ID, now)
		if store.IsUniqueViolation(err, "") {
			return protocol.Ef(protocol.CodeAlreadyExists, "Ya existe una carpeta con ese nombre en la carpeta padre")
		}
		if err != nil {
			return err
		}
		if err := keepManager(ctx, tx, c, f.ID, f.InheritACL); err != nil {
			return err
		}
		if err := bumpTree(ctx, tx); err != nil {
			return err
		}
		em := s.emitter(tx, c.Caller, c.CorrelationID)
		if err := em.emit(ctx, event{Type: "folder.created", Entity: f.entity(), Snapshot: f.snapshot(), FolderID: f.ID}); err != nil {
			return err
		}
		out = map[string]any{"id": f.ID, "path": f.Path}
		return nil
	})
	return out, err
}

func (s *Service) foldersEdit(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	id := c.Params["folderId"]
	o := validate.ParseObject(c.Payload, "", "name", "parentId", "inheritAcl", "externalBlocked", "sharingBlocked")
	var name, parentID string
	var hasName, hasParent bool
	if o.Has("name") {
		name, hasName = validFolderName(o, "name", true)
	}
	if o.Has("parentId") {
		parentID, hasParent = o.ULID("parentId", true)
	}
	inherit := o.OptBool("inheritAcl")
	ext := o.OptBool("externalBlocked")
	sharing := o.OptBool("sharingBlocked")
	if err := o.Err(); err != nil {
		return nil, err
	}
	if len(o.Keys()) == 0 {
		return nil, protocol.InvalidField("payload", nil, []string{"name", "parentId", "inheritAcl", "externalBlocked", "sharingBlocked"},
			"Indicá al menos un campo a modificar")
	}
	var out map[string]any
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		tree, err := treeTx(ctx, tx)
		if err != nil {
			return err
		}
		ev := tree.For(c.Caller.Principal())
		if err := requireFolderPerm(ev, tree, id, acl.Manage); err != nil {
			return err
		}
		cur, _, err := getFolder(ctx, tx, id)
		if err != nil {
			return err
		}
		next := *cur
		changes := map[string]any{}
		if id == protocol.RootFolderID && (hasName || hasParent || (inherit != nil && *inherit)) {
			return protocol.InvalidField("payload", nil, nil, "La carpeta raíz no se puede renombrar, mover ni heredar permisos")
		}
		if hasName && name != cur.Name {
			next.Name = name
			changes["name"] = fromTo(cur.Name, name)
		}
		if hasParent && parentID != cur.ParentID {
			if err := requireFolderPerm(ev, tree, parentID, acl.Manage); err != nil {
				return err
			}
			if tree.IsSelfOrDescendant(parentID, id) {
				return protocol.InvalidField("parentId", parentID, nil, "No se puede mover una carpeta dentro de sí misma")
			}
			next.ParentID = parentID
			changes["parentId"] = fromTo(cur.ParentID, parentID)
		}
		if inherit != nil && *inherit != cur.InheritACL {
			next.InheritACL = *inherit
			changes["inheritAcl"] = fromTo(cur.InheritACL, *inherit)
		}
		if ext != nil && *ext != cur.ExternalBlocked {
			next.ExternalBlocked = *ext
			changes["externalBlocked"] = fromTo(cur.ExternalBlocked, *ext)
		}
		if sharing != nil && *sharing != cur.Sharing {
			next.Sharing = *sharing
			changes["sharingBlocked"] = fromTo(cur.Sharing, *sharing)
		}
		if next.ParentID != cur.ParentID || next.Name != cur.Name {
			parent, _ := tree.Get(next.ParentID)
			next.Path = parent.Path + next.Name + "/"
			changes["path"] = fromTo(cur.Path, next.Path)
		}
		out = map[string]any{"id": id, "path": next.Path}
		if len(changes) == 0 {
			return nil
		}
		now := s.now()
		_, err = tx.Exec(ctx, `UPDATE folders SET name = $2, parent_id = NULLIF($3, ''), path = $4, inherit_acl = $5,
			external_blocked = $6, sharing_blocked = $7, updated_at = $8 WHERE id = $1`,
			id, next.Name, next.ParentID, next.Path, next.InheritACL, next.ExternalBlocked, next.Sharing, now)
		if store.IsUniqueViolation(err, "") {
			return protocol.Ef(protocol.CodeAlreadyExists, "Ya existe una carpeta con ese nombre en el destino")
		}
		if err != nil {
			return err
		}
		if inherit != nil && !*inherit && cur.InheritACL {
			if err := keepManager(ctx, tx, c, id, false); err != nil {
				return err
			}
		}
		if next.Path != cur.Path {
			subtree := tree.Subtree(id)
			if _, err := tx.Exec(ctx, `UPDATE folders SET path = $1 || substr(path, length($2) + 1), updated_at = $4
				WHERE id = ANY($3) AND id <> $5 AND deleted_at IS NULL`, next.Path, cur.Path, subtree, now, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE documents SET path = $1 || substr(path, length($2) + 1)
				WHERE folder_id = ANY($3) AND left(path, length($2)) = $2`, next.Path, cur.Path, subtree); err != nil {
				return err
			}
		}
		if err := bumpTree(ctx, tx); err != nil {
			return err
		}
		em := s.emitter(tx, c.Caller, c.CorrelationID)
		return em.emit(ctx, event{Type: "folder.updated", Entity: next.entity(), Snapshot: next.snapshot(), Changes: changes, FolderID: id})
	})
	return out, err
}

// keepManager avoids a self-lockout: a person who creates a folder (or turns off inheritance)
// without inheritance keeps an explicit manage rule, so the folder does not become unreachable
// for everyone but services.
func keepManager(ctx context.Context, tx pgx.Tx, c *Call, folderID string, inherit bool) error {
	if inherit || c.Caller.IsService() {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO folder_rules (folder_id, principal_type, principal, permission)
		VALUES ($1, 'user', $2, 'manage') ON CONFLICT (folder_id, principal_type, principal) DO UPDATE SET permission = 'manage'`,
		folderID, c.Caller.ID)
	return err
}

func (s *Service) foldersDelete(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	id := c.Params["folderId"]
	o := validate.ParseObject(c.Payload, "")
	if err := o.Err(); err != nil {
		return nil, err
	}
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM folders WHERE id = $1 FOR UPDATE`, id); err != nil {
			return err
		}
		cur, deleted, err := getFolder(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur == nil {
			return protocol.E(protocol.CodeFolderNotFound)
		}
		if deleted {
			return nil // deleting what is already deleted is success
		}
		tree, err := treeTx(ctx, tx)
		if err != nil {
			return err
		}
		if err := requireFolderPerm(tree.For(c.Caller.Principal()), tree, id, acl.Manage); err != nil {
			return err
		}
		if id == protocol.RootFolderID {
			return protocol.InvalidField("folderId", id, nil, "La carpeta raíz no se puede borrar")
		}
		var busy bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM folders WHERE parent_id = $1 AND deleted_at IS NULL)
			OR EXISTS (SELECT 1 FROM documents WHERE folder_id = $1 AND deleted_at IS NULL)`, id).Scan(&busy); err != nil {
			return err
		}
		if busy {
			return protocol.E(protocol.CodeFolderNotEmpty)
		}
		if _, err := tx.Exec(ctx, `UPDATE folders SET deleted_at = $2, updated_at = $2 WHERE id = $1`, id, s.now()); err != nil {
			return err
		}
		if err := bumpTree(ctx, tx); err != nil {
			return err
		}
		em := s.emitter(tx, c.Caller, c.CorrelationID)
		return em.emit(ctx, event{Type: "folder.deleted", Entity: cur.entity(), Snapshot: cur.snapshot(), FolderID: id})
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id}, nil
}

func (s *Service) foldersACLReplace(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	id := c.Params["folderId"]
	o := validate.ParseObject(c.Payload, "", "rules")
	items, _ := o.Array("rules", true, 100)
	if err := o.Err(); err != nil {
		return nil, err
	}
	rules := make([]acl.Rule, 0, len(items))
	seen := map[string]bool{}
	for i, raw := range items {
		ro := validate.ParseObject(raw, "rules["+itoa(i)+"]", "principalType", "principal", "permission")
		pt, _ := ro.Enum("principalType", true, "role", "user")
		pr, _ := ro.String("principal", true, validate.MinLen(1), validate.MaxLen(200))
		perm, _ := ro.Enum("permission", true, "read", "write", "manage")
		if err := ro.Err(); err != nil {
			return nil, err
		}
		if !validate.IsObject(raw) {
			return nil, protocol.InvalidField("rules", nil, nil, "Cada regla tiene que ser un objeto")
		}
		key := pt + "\x00" + pr
		if seen[key] {
			return nil, protocol.InvalidField("rules", pr, nil, "La regla para %s %q está repetida", pt, pr)
		}
		seen[key] = true
		rules = append(rules, acl.Rule{PrincipalType: pt, Principal: pr, Permission: perm})
	}
	return s.ReplaceACL(ctx, c, id, rules)
}

// ReplaceACL replaces the rules of a folder (also used in-process by the importer).
func (s *Service) ReplaceACL(ctx context.Context, c *Call, id string, rules []acl.Rule) (map[string]any, error) {
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		tree, err := treeTx(ctx, tx)
		if err != nil {
			return err
		}
		if err := requireFolderPerm(tree.For(c.Caller.Principal()), tree, id, acl.Manage); err != nil {
			return err
		}
		f, _ := tree.Get(id)
		before := f.Rules
		if before == nil {
			before = []acl.Rule{}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM folder_rules WHERE folder_id = $1`, id); err != nil {
			return err
		}
		for _, r := range rules {
			if _, err := tx.Exec(ctx, `INSERT INTO folder_rules (folder_id, principal_type, principal, permission) VALUES ($1,$2,$3,$4)`,
				id, r.PrincipalType, r.Principal, r.Permission); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE folders SET updated_at = $2 WHERE id = $1`, id, s.now()); err != nil {
			return err
		}
		if err := bumpTree(ctx, tx); err != nil {
			return err
		}
		cur, _, err := getFolder(ctx, tx, id)
		if err != nil {
			return err
		}
		em := s.emitter(tx, c.Caller, c.CorrelationID)
		return em.emit(ctx, event{Type: "folder.acl.changed", Entity: cur.entity(), Snapshot: cur.snapshot(),
			Changes: map[string]any{"acl": fromTo(before, rules)}, FolderID: id})
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "rules": rules}, nil
}
