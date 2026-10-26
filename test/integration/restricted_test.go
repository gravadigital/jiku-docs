package integration

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/gravadigital/jiku-docs/internal/protocol"
	tu "github.com/gravadigital/jiku-docs/internal/testutil"
)

// P-1, the owner's guarantee: a `user` or an external person never reaches a `restricted`
// document, by any path. One test per path, then a property test over random documents.

// restrictedWorld is a folder with one restricted document (visible to nobody but admins and
// services) that has every kind of data attached: versions, topic, relations, an attachment,
// a revoked link, a lock and search chunks.
type restrictedWorld struct {
	folder, doc, att, shareToken, shareID string
	secret                                string // a word only the restricted document contains
}

func newRestrictedWorld(e *tu.Env) restrictedWorld {
	e.T.Helper()
	w := restrictedWorld{folder: e.Folder("", "cerrada", nil), secret: "zafiroconfidencial"}
	head := "---\ntitle: Sueldos " + w.secret + "\ntopics: [rrhh-secreto]\njiku:\n  projects: [12]\n---\n"
	// Created public (linked to the external's project), shared, then made restricted by an
	// admin: the link must die and every trace must disappear for user and external.
	pub := strings.Replace(head, "---\ntitle", "---\nvisibility: public\ntitle", 1) + "# Sueldos\n\nEl " + w.secret + " cobra mucho.\n"
	w.doc = e.Doc(tu.Admin, w.folder, "sueldos.md", pub)
	w.att = attach(e, tu.Admin, w.doc, "planilla.png", []byte("png-secreto"))
	exp := time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	w.shareID = tu.ID()
	sh := e.OK(e.Cmd(tu.Admin, "shares.new", map[string]any{"id": w.shareID, "documentId": w.doc, "mode": "latest", "expiresAt": exp}))
	w.shareToken = sh["token"].(string)
	e.OK(e.Q(tu.APIUser, "shares.resolve", map[string]any{"token": w.shareToken})) // works while public
	res := strings.Replace(head, "---\ntitle", "---\nvisibility: restricted\ntitle", 1) + "# Sueldos\n\nEl " + w.secret + " cobra mucho.\n\n![p](http://web.test/docs/attachments/" + w.att + ")\n"
	e.OK(e.Cmd(tu.Admin, "documents."+w.doc+".update", map[string]any{"baseVersion": 1, "content": res, "checksum": tu.Sum(res)}))
	e.OK(e.Cmd(tu.Admin, "documents."+w.doc+".lock", map[string]any{"sessionId": tu.ID(), "via": "human", "ttlSeconds": 300}))
	e.Eventually("restricted indexed", func() bool {
		return len(e.Q(tu.Admin, "documents.search", map[string]any{"query": w.secret}).Items()) == 1
	})
	return w
}

// outsiders are the identities that must never see a restricted document: a user and an
// external person, on the bus and through the api with actor.
func outsiders() []struct {
	name, user string
	actor      map[string]any
} {
	return []struct {
		name, user string
		actor      map[string]any
	}{
		{"user", tu.User, nil},
		{"external", tu.External, nil},
		{"api+user", tu.APIUser, actor(tu.User, "user")},
		{"api+external", tu.APIUser, actor(tu.External, "external-user")},
	}
}

func with(actor map[string]any, p map[string]any) map[string]any {
	if p == nil {
		p = map[string]any{}
	}
	if actor != nil {
		p["actor"] = actor
	}
	return p
}

// assertClean fails when a reply carries the id, the title or the secret of the document.
func (w restrictedWorld) assertClean(t *testing.T, what string, r tu.Resp) {
	t.Helper()
	raw := string(r.Raw)
	for _, s := range []string{w.doc, w.secret, "Sueldos", "rrhh-secreto", w.att} {
		if strings.Contains(raw, s) {
			t.Fatalf("%s leaks %q: %s", what, s, raw)
		}
	}
}

func TestRestrictedInvisiblePerPath(t *testing.T) {
	e := tu.NewEnv(t)
	w := newRestrictedWorld(e)
	// An admin and a service see it.
	for _, who := range []string{tu.Admin, tu.Connector} {
		if n := len(e.Q(who, "documents.list", map[string]any{"filter": map[string]any{"visibility": "restricted"}}).Items()); n != 1 {
			t.Fatalf("%s sees %d restricted", who, n)
		}
	}
	for _, o := range outsiders() {
		t.Run(o.name, func(t *testing.T) {
			q := func(method string, p map[string]any) tu.Resp { return e.Q(o.user, method, with(o.actor, p)) }
			// documents.list, its count, count only and the visibility filter (empty, not an error).
			for _, p := range []map[string]any{nil, {"count": true}, {"count": "only"},
				{"filter": map[string]any{"visibility": "restricted"}, "count": true},
				{"filter": map[string]any{"projectId": 12}, "count": true},
				{"filter": map[string]any{"id": w.doc}}, {"filter": map[string]any{"topic": "rrhh-secreto"}, "count": true},
				{"filter": map[string]any{"deleted": true}}, {"filter": map[string]any{"hasProject": true}, "include": []string{"relations", "topics", "lock"}}} {
				r := q("documents.list", p)
				e.OK(r)
				w.assertClean(t, "documents.list", r)
				if pg, ok := r.M()["page"].(map[string]any)["total"]; ok && p["filter"] != nil {
					if f := p["filter"].(map[string]any); f["visibility"] != nil || f["id"] != nil || f["topic"] != nil {
						if pg.(float64) != 0 {
							t.Fatalf("count of %v is %v", f, pg)
						}
					}
				}
			}
			// documents.get, with and without version, with every include.
			e.Fail(q("documents.get", map[string]any{"id": w.doc}), protocol.CodeDocumentNotFound)
			e.Fail(q("documents.get", map[string]any{"id": w.doc, "version": 1}), protocol.CodeDocumentNotFound)
			e.Fail(q("documents.get", map[string]any{"id": w.doc, "version": 2, "include": []string{"content", "attachments"}}), protocol.CodeDocumentNotFound)
			// documents.search and its fragments.
			for _, text := range []string{w.secret, "sueldos", "cobra mucho"} {
				r := q("documents.search", map[string]any{"query": text})
				w.assertClean(t, "documents.search", r)
				if len(r.Items()) != 0 {
					t.Fatalf("search %q: %s", text, r.Raw)
				}
			}
			// documents.changes (also the events of when it was public).
			r := q("documents.changes", map[string]any{"sinceSeq": 0, "limit": 500})
			w.assertClean(t, "documents.changes", r)
			// documents.versions.list.
			e.Fail(q("documents.versions.list", map[string]any{"filter": map[string]any{"documentId": w.doc}}), protocol.CodeDocumentNotFound)
			// topics.list and its counts.
			r = q("topics.list", nil)
			w.assertClean(t, "topics.list", r)
			// attachments.list.
			e.Fail(q("attachments.list", map[string]any{"filter": map[string]any{"documentId": w.doc}}), protocol.CodeDocumentNotFound)
			// shares.list.
			e.Fail(q("shares.list", map[string]any{"filter": map[string]any{"documentId": w.doc}}), protocol.CodeDocumentNotFound)
			// documents.suggest-relations.
			e.Fail(q("documents.suggest-relations", map[string]any{"id": w.doc}), protocol.CodeDocumentNotFound)
			// meta.describe holds no data.
			w.assertClean(t, "meta.describe", q("meta.describe", nil))
			// Commands: attachments.*, shares.*, and every write on the document.
			cmd := func(method string, p map[string]any) tu.Resp { return e.CmdRaw(o.user, method, with(o.actor, p)) }
			notFound := protocol.CodeDocumentNotFound
			if strings.Contains(o.name, "external") {
				notFound = protocol.CodeCallerNotAuthorized // externals never write
			}
			for _, c := range []struct {
				method string
				p      map[string]any
				code   string
			}{
				{"attachments." + w.att + ".request-download", nil, protocol.CodeAttachmentNotFound},
				{"attachments." + w.att + ".confirm", nil, map[bool]string{true: protocol.CodeCallerNotAuthorized, false: protocol.CodeAttachmentNotFound}[strings.Contains(o.name, "external")]},
				{"attachments." + w.att + ".delete", nil, map[bool]string{true: protocol.CodeCallerNotAuthorized, false: protocol.CodeAttachmentNotFound}[strings.Contains(o.name, "external")]},
				{"attachments.request-upload", map[string]any{"id": tu.ID(), "documentId": w.doc, "fileName": "a.png", "mimeType": "image/png", "fileSize": 4, "checksum": tu.Sum("abcd")}, notFound},
				{"shares.new", map[string]any{"id": tu.ID(), "documentId": w.doc, "mode": "latest", "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, notFound},
				{"shares." + w.shareID + ".revoke", nil, map[bool]string{true: protocol.CodeCallerNotAuthorized, false: protocol.CodeShareNotFound}[strings.Contains(o.name, "external")]},
				{"documents." + w.doc + ".update", map[string]any{"baseVersion": 2, "content": "x", "checksum": tu.Sum("x")}, notFound},
				{"documents." + w.doc + ".move", map[string]any{"baseVersion": 2, "fileName": "x.md"}, notFound},
				{"documents." + w.doc + ".delete", map[string]any{"baseVersion": 2}, notFound},
				{"documents." + w.doc + ".restore", map[string]any{"fromVersion": 1, "baseVersion": 2}, notFound},
				{"documents." + w.doc + ".lock", map[string]any{"sessionId": tu.ID(), "via": "human"}, notFound},
				{"documents." + w.doc + ".unlock", map[string]any{"sessionId": tu.ID(), "force": true}, notFound},
				{"documents." + w.doc + ".relations.update", map[string]any{"baseVersion": 2, "add": map[string]any{"projects": []int{31}}}, notFound},
			} {
				r := cmd(c.method, c.p)
				e.Fail(r, c.code)
				w.assertClean(t, c.method, r)
			}
		})
	}
	// The public route of a link created before the restriction: dead, also for attachments.
	e.Fail(e.Q(tu.APIUser, "shares.resolve", map[string]any{"token": w.shareToken}), protocol.CodeShareNotFound)
	e.Fail(e.Q(tu.APIUser, "shares.attachment", map[string]any{"token": w.shareToken, "attachmentId": w.att}), protocol.CodeShareNotFound)
	e.WaitEvent("share.revoked", func(ev tu.Event) bool { return ev.Body["changes"].(map[string]any)["reason"] == "restricted" })
	sh := e.Q(tu.Admin, "shares.list", map[string]any{"filter": map[string]any{"documentId": w.doc}}).Items()
	if len(sh) != 1 || sh[0]["status"] != "revoked" || sh[0]["revokeReason"] != "restricted" {
		t.Fatalf("link of a restricted document: %v", sh)
	}
	// An admin cannot create a link of a restricted document.
	e.Fail(e.Cmd(tu.Admin, "shares.new", map[string]any{"id": tu.ID(), "documentId": w.doc, "mode": "latest",
		"expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}), protocol.CodeSharingBlocked)
	// docs-notify: after the restriction, only admins hear about the document. The users got an
	// access.changed without detail; the external (who read it while public) too.
	e.OK(e.Cmd(tu.Admin, "documents."+w.doc+".unlock", map[string]any{"sessionId": tu.ID(), "force": true}))
	e.Eventually("access.changed to user and external", func() bool {
		seen := map[string]bool{}
		for _, n := range e.Notes() {
			if n.Type == "access.changed" {
				seen[n.UserID] = true
			}
		}
		return seen[tu.User] && seen[tu.User2] && seen[tu.External] && !seen[tu.Admin]
	})
	e.Eventually("unlock notified to the admin", func() bool {
		for _, n := range e.Notes() {
			if n.UserID == tu.Admin && n.Type == "document.unlocked" {
				return true
			}
		}
		return false
	})
	time.Sleep(300 * time.Millisecond)
	for _, n := range e.Notes() {
		if n.UserID == tu.Admin || n.Type == "access.changed" {
			continue
		}
		if snap, _ := n.Body["snapshot"].(map[string]any); snap["visibility"] == "restricted" {
			t.Fatalf("%s notified of the restricted document: %v", n.UserID, n.Type)
		}
	}
}

// Visibility rules: who sets or removes restricted, and public for any internal person.
func TestRestrictedWhoChangesVisibility(t *testing.T) {
	e := tu.NewEnv(t)
	f := writable(e)
	res := "---\nvisibility: restricted\n---\n# R\n"
	pub := "---\nvisibility: public\n---\n# P\n"
	// new: a user cannot create it restricted; public is fine (it used to need manage).
	e.Fail(e.Cmd(tu.User, "documents.new", map[string]any{"id": tu.ID(), "folderId": f, "fileName": "r.md", "content": res, "checksum": tu.Sum(res)}), protocol.CodePermissionDenied)
	p := e.Doc(tu.User, f, "p.md", pub)
	// update: a user cannot make it restricted; an admin can, and can take it back.
	e.Fail(e.Cmd(tu.User, "documents."+p+".update", map[string]any{"baseVersion": 1, "content": res, "checksum": tu.Sum(res)}), protocol.CodePermissionDenied)
	e.OK(e.Cmd(tu.Admin, "documents."+p+".update", map[string]any{"baseVersion": 1, "content": res, "checksum": tu.Sum(res)}))
	e.Fail(e.Q(tu.User, "documents.get", map[string]any{"id": p}), protocol.CodeDocumentNotFound)
	back := "---\nvisibility: internal\n---\n# P\n"
	e.OK(e.Cmd(tu.Admin, "documents."+p+".update", map[string]any{"baseVersion": 2, "content": back, "checksum": tu.Sum(back)}))
	e.OK(e.Q(tu.User, "documents.get", map[string]any{"id": p}))
	// restore: a user cannot restore the version that was restricted.
	e.Fail(e.Cmd(tu.User, "documents."+p+".restore", map[string]any{"fromVersion": 2, "baseVersion": 3}), protocol.CodePermissionDenied)
	e.OK(e.Cmd(tu.User, "documents."+p+".restore", map[string]any{"fromVersion": 1, "baseVersion": 3}))
	// relations.update keeps the visibility of the content: a user may assign on an internal one.
	e.OK(e.Cmd(tu.User, "documents."+p+".relations.update", map[string]any{"baseVersion": 4, "add": map[string]any{"projects": []int{31}}}))
	// The service may set restricted too (importer, connectors).
	sid := tu.ID()
	e.OK(e.Cmd(tu.Connector, "documents.new", map[string]any{"id": sid, "folderId": f, "fileName": "s.md", "content": res, "checksum": tu.Sum(res)}))
	// unlock force: admins only.
	l := e.Doc(tu.User, f, "l.md", "l")
	e.OK(e.Cmd(tu.User, "documents."+l+".lock", map[string]any{"sessionId": tu.ID(), "via": "human"}))
	e.Fail(e.Cmd(tu.User2, "documents."+l+".unlock", map[string]any{"sessionId": tu.ID(), "force": true}), protocol.CodePermissionDenied)
	e.OK(e.Cmd(tu.Admin, "documents."+l+".unlock", map[string]any{"sessionId": tu.ID(), "force": true}))
}

// Property: random documents in the three visibilities, with random topics, relations and
// titles; no reply to a user or an external person ever carries the id, the title or a fragment
// of a restricted one.
func TestRestrictedNeverLeaksProperty(t *testing.T) {
	e := tu.NewEnv(t)
	seed := time.Now().UnixNano()
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewSource(seed))
	folders := []string{e.Folder("", "a", nil), e.Folder("", "b", map[string]any{"inheritAcl": false}), e.Folder("", "c", map[string]any{"externalBlocked": true})}
	visibilities := []string{"internal", "public", "restricted"}
	topics := []string{"comun", "infra", "rrhh"}
	type doc struct{ id, vis, title, marker string }
	var docs []doc
	restricted := map[string]doc{}
	for i := 0; i < 30; i++ {
		vis := visibilities[rng.Intn(3)]
		marker := fmt.Sprintf("marca%dx%d", i, rng.Intn(1_000_000))
		title := fmt.Sprintf("Documento %s", marker)
		var rel []string
		if rng.Intn(3) > 0 {
			rel = append(rel, "  projects: ["+[]string{"12", "31", "12, 31"}[rng.Intn(3)]+"]")
		}
		if rng.Intn(3) == 0 {
			rel = append(rel, "  requirements: [812]")
		}
		if rng.Intn(4) == 0 {
			rel = append(rel, "  clients: [4]")
		}
		content := "---\ntitle: " + title + "\nvisibility: " + vis + "\ntopics: [" + topics[rng.Intn(3)] + ", t-" + marker + "]\n"
		if len(rel) > 0 {
			content += "jiku:\n" + strings.Join(rel, "\n") + "\n"
		}
		content += "---\n# " + title + "\n\nTexto compartido aduana y el " + marker + " propio.\n"
		author := tu.Admin
		if vis != "restricted" && rng.Intn(2) == 0 {
			author = tu.User
		}
		id := e.Doc(author, folders[rng.Intn(3)], fmt.Sprintf("d%d.md", i), content)
		d := doc{id, vis, title, marker}
		docs = append(docs, d)
		if vis == "restricted" {
			restricted[id] = d
		}
		if rng.Intn(4) == 0 && vis != "restricted" {
			// Some documents become restricted later, with their history.
			upd := strings.Replace(content, "visibility: "+vis, "visibility: restricted", 1)
			e.OK(e.Cmd(tu.Admin, "documents."+id+".update", map[string]any{"baseVersion": 1, "content": upd, "checksum": tu.Sum(upd)}))
			restricted[id] = doc{id, "restricted", title, marker}
		}
	}
	if len(restricted) == 0 {
		t.Skip("the seed made no restricted document")
	}
	e.Eventually("indexed", func() bool {
		return len(e.Q(tu.Admin, "documents.search", map[string]any{"query": "aduana", "limit": 50}).Items()) == len(docs)
	})
	clean := func(who, what string, r tu.Resp) {
		t.Helper()
		raw := string(r.Raw)
		for _, d := range restricted {
			for _, s := range []string{d.id, d.title, d.marker} {
				if strings.Contains(raw, s) {
					t.Fatalf("%s: %s leaks %q of a restricted document (seed %d): %s", who, what, s, seed, raw)
				}
			}
		}
	}
	for _, o := range outsiders() {
		q := func(method string, p map[string]any) tu.Resp { return e.Q(o.user, method, with(o.actor, p)) }
		for _, p := range []map[string]any{nil, {"count": true, "include": []string{"relations", "topics", "lock"}},
			{"filter": map[string]any{"projectId": []int{12, 31}}, "fields": []string{"id", "title", "path", "createdByName", "updatedByName", "clients", "projects"}},
			{"filter": map[string]any{"hasProject": false}}, {"filter": map[string]any{"hasClient": true}}, {"filter": map[string]any{"hasTopic": true}},
			{"filter": map[string]any{"visibility": "restricted"}, "count": "only"}, {"filter": map[string]any{"deleted": true}},
			{"filter": map[string]any{"or": []any{map[string]any{"topic": "rrhh"}, map[string]any{"requirementId": 812}}}}} {
			clean(o.name, "documents.list", q("documents.list", p))
		}
		for _, text := range []string{"aduana", "documento", "texto compartido"} {
			clean(o.name, "documents.search", q("documents.search", map[string]any{"query": text, "limit": 50}))
			clean(o.name, "documents.search", q("documents.search", map[string]any{"query": text, "limit": 50, "filter": map[string]any{"projectId": 12}}))
		}
		clean(o.name, "documents.changes", q("documents.changes", map[string]any{"sinceSeq": 0, "limit": 500}))
		clean(o.name, "topics.list", q("topics.list", map[string]any{"page": map[string]any{"limit": 200}}))
		for _, d := range restricted {
			clean(o.name, "search marker", q("documents.search", map[string]any{"query": d.marker}))
			for _, m := range []struct {
				method string
				p      map[string]any
			}{
				{"documents.get", map[string]any{"id": d.id}}, {"documents.get", map[string]any{"id": d.id, "version": 1}},
				{"documents.versions.list", map[string]any{"filter": map[string]any{"documentId": d.id}}},
				{"attachments.list", map[string]any{"filter": map[string]any{"documentId": d.id}}},
				{"shares.list", map[string]any{"filter": map[string]any{"documentId": d.id}}},
				{"documents.suggest-relations", map[string]any{"id": d.id}},
			} {
				r := q(m.method, m.p)
				e.Fail(r, protocol.CodeDocumentNotFound)
			}
		}
	}
	// Topic counts for a user equal the non-restricted documents with that topic.
	adminTopics := map[string]float64{}
	for _, it := range e.Q(tu.Admin, "topics.list", map[string]any{"filter": map[string]any{"prefix": "comun"}}).Items() {
		adminTopics[it["topic"].(string)] = it["documents"].(float64)
	}
	userTopics := map[string]float64{}
	for _, it := range e.Q(tu.User, "topics.list", map[string]any{"filter": map[string]any{"prefix": "comun"}}).Items() {
		userTopics[it["topic"].(string)] = it["documents"].(float64)
	}
	n := e.Q(tu.User, "documents.list", map[string]any{"filter": map[string]any{"topic": "comun"}, "count": "only"}).M()["page"].(map[string]any)["total"].(float64)
	if userTopics["comun"] != n || n > adminTopics["comun"] {
		t.Fatalf("topic counts: user %v (list %v), admin %v", userTopics["comun"], n, adminTopics["comun"])
	}
	// docs-notify of every restricted document reached admins only.
	time.Sleep(500 * time.Millisecond)
	for _, note := range e.Notes() {
		if note.UserID == tu.Admin || note.Type == "access.changed" {
			continue
		}
		ent, _ := note.Body["entity"].(map[string]any)
		d, ok := restricted[fmt.Sprint(ent["id"])]
		if !ok {
			continue
		}
		if snap, _ := note.Body["snapshot"].(map[string]any); snap["visibility"] == "restricted" {
			t.Fatalf("%s notified of restricted %s (seed %d)", note.UserID, d.id, seed)
		}
	}
}
