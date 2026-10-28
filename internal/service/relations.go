package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/gravadigital/jiku-docs/internal/acl"
	"github.com/gravadigital/jiku-docs/internal/coreapi"
	"github.com/gravadigital/jiku-docs/internal/markdown"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/validate"
)

// ---- documents.{id}.relations.update ----------------------------------------------------------

// maxRelationIDs is the limit of ids per kind in add or remove.
const maxRelationIDs = 100

// readRelationSet reads `{clients?, projects?, requirements?, tasks?}` of positive ids.
func readRelationSet(o *validate.Object, key string) (markdown.Relations, bool, error) {
	var rel markdown.Relations
	keys := make([]string, len(markdown.RelationKinds))
	for i, k := range markdown.RelationKinds {
		keys[i] = k.Key
	}
	if !o.Has(key) {
		return rel, false, nil
	}
	child, ok := o.Object(key, false, keys...)
	if err := o.Err(); err != nil || !ok {
		return rel, false, err
	}
	for _, k := range markdown.RelationKinds {
		arr, ok := child.Array(k.Key, false, maxRelationIDs)
		if err := child.Err(); err != nil {
			return rel, false, err
		}
		if !ok {
			continue
		}
		ids := make([]int64, 0, len(arr))
		for _, el := range arr {
			n, ok := validate.ElemInt(el)
			if !ok || n <= 0 {
				return rel, false, protocol.InvalidField(validate.Join(child.Path(), k.Key), validate.Value(el), nil,
					"%q must be a list of positive ids", validate.Join(child.Path(), k.Key))
			}
			ids = append(ids, n)
		}
		rel.Set(k.Key, ids)
	}
	return rel.Normalize(), true, nil
}

func (s *Service) documentsRelationsUpdate(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	id := c.Params["documentId"]
	// No `via`: the origin comes from the caller class, as in the rest of the commands.
	o := validate.ParseObject(c.Payload, "", "baseVersion", "add", "remove", "sessionId")
	base, _ := o.Int("baseVersion", true, 1, 1<<31-1)
	sessionID, _ := o.ULID("sessionId", false)
	if err := o.Err(); err != nil {
		return nil, err
	}
	add, hasAdd, err := readRelationSet(o, "add")
	if err != nil {
		return nil, err
	}
	remove, hasRemove, err := readRelationSet(o, "remove")
	if err != nil {
		return nil, err
	}
	if (!hasAdd || add.Empty()) && (!hasRemove || remove.Empty()) {
		return nil, protocol.InvalidField("payload", nil, []string{"add", "remove"}, "Give at least one relation in add or remove")
	}
	for _, k := range markdown.RelationKinds {
		rm := toSet(remove.Get(k.Key))
		for _, x := range add.Get(k.Key) {
			if rm[x] {
				return nil, protocol.InvalidField("remove."+k.Key, x, nil, "The id %d is in both add and remove", x)
			}
		}
	}
	return s.UpdateRelations(ctx, c, id, int(base), add, remove, sessionID, "")
}

// UpdateRelations rewrites the `jiku` block of a document's frontmatter: (current ∪ add) \ remove,
// plus the client of each added project when it is missing. It writes a version (kind update)
// with an automatic message. Nothing to change is success with the current version.
func (s *Service) UpdateRelations(ctx context.Context, c *Call, id string, base int, add, remove markdown.Relations, sessionID, via string) (map[string]any, error) {
	d, err := getDoc(ctx, s.DB, id, false)
	if err != nil {
		return nil, err
	}
	if d == nil || d.DeletedAt != nil {
		return nil, protocol.E(protocol.CodeDocumentNotFound)
	}
	if _, err := docWritePerm(c.Caller, d, acl.Write); err != nil {
		return nil, err
	}
	var key string
	if err := s.DB.QueryRow(ctx, `SELECT storage_key FROM document_versions WHERE document_id = $1 AND version = $2`, id, d.Version).Scan(&key); err != nil {
		return nil, err
	}
	raw, err := s.Blob.Get(ctx, key, MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	fm, _, ferr := markdown.Parse(string(raw))
	if ferr != nil {
		return nil, protocol.Ef(protocol.CodeFrontmatterInvalid, "%s", ferr.Error()).
			With("line", ferr.Line).With("field", ferr.Field).With("reason", ferr.Reason)
	}
	cur := fm.Relations
	// Assigning a project assigns its client too, when the document does not name it.
	var projects map[int64]coreapi.Project
	clients := map[int64]string{}
	if len(add.Projects) > 0 || len(remove.Projects) > 0 {
		if projects, err = s.projectIndex(ctx); err != nil {
			return nil, err
		}
	}
	for _, pid := range add.Projects {
		if p, ok := projects[pid]; ok && p.ClientID != nil && *p.ClientID > 0 && !toSet(remove.Clients)[*p.ClientID] {
			add.Clients = append(add.Clients, *p.ClientID)
		}
	}
	next := markdown.Relations{}
	for _, k := range markdown.RelationKinds {
		rm := toSet(remove.Get(k.Key))
		var ids []int64
		for _, x := range append(append([]int64{}, cur.Get(k.Key)...), add.Get(k.Key)...) {
			if !rm[x] {
				ids = append(ids, x)
			}
		}
		next.Set(k.Key, ids)
	}
	next = next.Normalize()
	diff := diffRelations(cur, next)
	if diff == nil {
		return map[string]any{"id": id, "version": d.Version, "relations": next}, nil
	}
	if len(add.Clients) > 0 || len(remove.Clients) > 0 {
		if cl, err := s.Core.Clients(ctx); err == nil {
			for _, x := range cl {
				clients[x.ID] = x.Name
			}
		}
	}
	edited, err := markdown.SetRelations(string(raw), next)
	if err != nil {
		return nil, protocol.Ef(protocol.CodeFrontmatterInvalid, "The frontmatter cannot be edited: %s", err.Error())
	}
	message := relationsMessage(diff, projects, clients)
	out, err := s.UpdateDocument(ctx, c, id, base, &contentInput{raw: []byte(edited)}, sessionID, via, message)
	if err != nil {
		return nil, err
	}
	res := map[string]any{"id": id, "version": out["version"], "relations": next}
	if w, ok := out["warnings"]; ok {
		res["warnings"] = w
	}
	return res, nil
}

func (s *Service) projectIndex(ctx context.Context) (map[int64]coreapi.Project, error) {
	list, err := s.Core.Projects(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]coreapi.Project, len(list))
	for _, p := range list {
		out[p.ID] = p
	}
	return out, nil
}

// relationsMessage is the automatic message of the version: "Project assigned: EXO -
// Videoconferencia; Client assigned: EXO".
func relationsMessage(diff map[string]any, projects map[int64]coreapi.Project, clients map[int64]string) string {
	labels := map[string][2]string{
		"clients": {"Client assigned", "Client removed"}, "projects": {"Project assigned", "Project removed"},
		"requirements": {"Requirement linked", "Requirement unlinked"}, "tasks": {"Task linked", "Task unlinked"},
	}
	name := func(kind string, id int64) string {
		switch kind {
		case "projects":
			if p, ok := projects[id]; ok && p.Name != "" {
				return p.Name
			}
		case "clients":
			if n := clients[id]; n != "" {
				return n
			}
		}
		return "#" + strconv.FormatInt(id, 10)
	}
	var parts []string
	for i, side := range []string{"added", "removed"} {
		m, _ := diff[side].(map[string][]int64)
		for _, k := range markdown.RelationKinds {
			for _, x := range m[k.Key] {
				parts = append(parts, labels[k.Key][i]+": "+name(k.Key, x))
			}
		}
	}
	msg := strings.Join(parts, "; ")
	if r := []rune(msg); len(r) > 500 {
		msg = string(r[:497]) + "..."
	}
	return msg
}

// ---- documents.suggest-relations ---------------------------------------------------------------

// maxSuggestions is how many candidates documents.suggest-relations returns.
const maxSuggestions = 5

// Weights of a match: the title weighs more than the content.
const (
	titleWeight       = 0.7
	contentWeightEach = 0.06
	contentMaxHits    = 5
	clientTitleBonus  = 0.1
	clientBodyBonus   = 0.05
)

// stopTerms never make a candidate on their own: they are too common in names. They are Spanish
// words because the names of clients and projects in Jiku are Spanish.
var stopTerms = map[string]bool{"proyecto": true, "general": true, "interno": true, "cliente": true, "sistema": true,
	"app": true, "web": true, "soporte": true, "mantenimiento": true, "desarrollo": true}

func (s *Service) documentsSuggestRelations(ctx context.Context, c *Call) (any, error) {
	o := validate.ParseObject(c.Payload, "", "id")
	id, _ := o.ULID("id", true)
	if err := o.Err(); err != nil {
		return nil, err
	}
	v, err := s.viewer(ctx, c.Caller)
	if err != nil {
		return nil, err
	}
	d, _, err := s.visibleDoc(ctx, s.DB, v, id, false)
	if err != nil {
		return nil, err
	}
	// Suggesting relations is part of assigning them, an internal task; an external person would
	// learn the names of projects it has no permission on.
	if v.c.IsExternal() {
		return nil, protocol.E(protocol.CodePermissionDenied)
	}
	var key string
	if err := s.DB.QueryRow(ctx, `SELECT storage_key FROM document_versions WHERE document_id = $1 AND version = $2`, id, d.Version).Scan(&key); err != nil {
		return nil, err
	}
	raw, err := s.Blob.Get(ctx, key, MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	fm, body, ferr := markdown.Parse(string(raw))
	if ferr != nil {
		body = markdown.StripFrontmatter(string(raw))
	}
	projects, err := s.Core.Projects(ctx)
	if err != nil {
		return nil, err
	}
	clientList, err := s.Core.Clients(ctx)
	if err != nil {
		return nil, err
	}
	clients := map[int64]string{}
	for _, cl := range clientList {
		clients[cl.ID] = cl.Name
	}
	return map[string]any{"items": suggest(d.Title, body, projects, clients, toSet(fm.Relations.Projects), toSet(fm.Relations.Clients))}, nil
}

// suggest scores every project by mentions of its name, the parts of its name ("EXO -
// Videoconferencia" -> "EXO", "Videoconferencia") or its code in the title and the body. A
// client named in the text without any of its projects matching is a client-only candidate,
// with projectId null.
func suggest(title, body string, projects []coreapi.Project, clients map[int64]string, linked, linkedClients map[int64]bool) []map[string]any {
	t, b := " "+normalizeText(title)+" ", " "+normalizeText(body)+" "
	type cand struct {
		p      coreapi.Project
		score  float64
		reason string
	}
	var out []cand
	for _, p := range projects {
		if linked[p.ID] {
			continue
		}
		best, reason := 0.0, ""
		for _, term := range projectTerms(p) {
			n := normalizeText(term)
			if !usefulTerm(n) {
				continue
			}
			needle := " " + n + " "
			score := 0.0
			inTitle := strings.Contains(t, needle)
			hits := strings.Count(b, needle)
			if inTitle {
				score += titleWeight
			}
			score += contentWeightEach * float64(min(hits, contentMaxHits))
			if score > best {
				best = score
				switch {
				case inTitle:
					reason = "The title mentions “" + term + "”"
				case hits == 1:
					reason = "The content mentions “" + term + "” (1 time)"
				default:
					reason = fmt.Sprintf("The content mentions “%s” (%d times)", term, hits)
				}
			}
		}
		if best == 0 {
			continue
		}
		if p.ClientID != nil {
			if cn := normalizeText(clients[*p.ClientID]); usefulTerm(cn) {
				switch {
				case strings.Contains(t, " "+cn+" "):
					best += clientTitleBonus
				case strings.Contains(b, " "+cn+" "):
					best += clientBodyBonus
				}
			}
		}
		out = append(out, cand{p: p, score: math.Min(1, math.Round(best*100)/100), reason: reason})
	}
	covered := map[int64]bool{}
	for _, c := range out {
		if c.p.ClientID != nil {
			covered[*c.p.ClientID] = true
		}
	}
	for cid, name := range clients {
		if linkedClients[cid] || covered[cid] {
			continue
		}
		n := normalizeText(name)
		if !usefulTerm(n) {
			continue
		}
		needle := " " + n + " "
		score, hits := 0.0, strings.Count(b, needle)
		reason := ""
		if strings.Contains(t, needle) {
			score, reason = titleWeight/2, "The title mentions the client “"+name+"”"
		} else if hits > 0 {
			score, reason = contentWeightEach*float64(min(hits, contentMaxHits)), fmt.Sprintf("The content mentions the client “%s”", name)
		}
		if score == 0 {
			continue
		}
		id := cid
		out = append(out, cand{p: coreapi.Project{ClientID: &id}, score: math.Round(score*100) / 100, reason: reason})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].p.ID != out[j].p.ID {
			return out[i].p.ID < out[j].p.ID
		}
		return *out[i].p.ClientID < *out[j].p.ClientID
	})
	if len(out) > maxSuggestions {
		out = out[:maxSuggestions]
	}
	items := make([]map[string]any, len(out))
	for i, c := range out {
		var client any
		if c.p.ClientID != nil {
			client = *c.p.ClientID
		}
		var project any
		if c.p.ID != 0 {
			project = c.p.ID
		}
		items[i] = map[string]any{"projectId": project, "clientId": client, "score": c.score, "reason": c.reason}
	}
	return items
}

func projectTerms(p coreapi.Project) []string {
	terms := []string{strings.TrimSpace(p.Name)}
	for _, sep := range []string{" - ", " – ", " — ", ":", "/", "|"} {
		if strings.Contains(p.Name, sep) {
			for _, part := range strings.Split(p.Name, sep) {
				if part = strings.TrimSpace(part); part != "" {
					terms = append(terms, part)
				}
			}
		}
	}
	if c := strings.TrimSpace(p.Code); c != "" {
		terms = append(terms, c)
	}
	return terms
}

func usefulTerm(n string) bool {
	if len([]rune(n)) < 3 || stopTerms[n] {
		return false
	}
	for _, r := range n {
		if !unicode.IsDigit(r) && r != ' ' {
			return true
		}
	}
	return false // only digits: too ambiguous
}

// normalizeText lowers, strips accents and turns everything that is not a letter or a digit
// into a single space: "Integración-Móvil" -> "integracion movil".
func normalizeText(s string) string {
	var b strings.Builder
	space := false
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		default:
			if b.Len() > 0 && !space {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}
