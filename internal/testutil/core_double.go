package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/gravadigital/jiku-docs/internal/blob"
	"github.com/gravadigital/jiku-docs/internal/coreapi"
)

// CoreDouble answers the part of core jiku-docs uses, as strictly as core does: an undeclared
// key, filter, field or include is invalid_fields. It also checks that jiku-docs never sends
// `actor` or identity fields to core.
type CoreDouble struct {
	mu           sync.Mutex
	Users        map[string]coreapi.User
	ProjectPerms map[string][]int64
	Clients      map[int64]bool
	Projects     map[int64]bool
	Requirements map[int64]int64 // id -> project
	Tasks        map[int64]int64 // id -> project
	// ProjectInfo and ClientNames are the names (and code and client) projects.list and
	// clients.list return when asked for those fields.
	ProjectInfo map[int64]coreapi.Project
	ClientNames map[int64]string
	// Down makes the double stop answering (core unavailable).
	Down bool
	// MaxFileSize and AllowedMimes are core's upload policy (system_settings).
	MaxFileSize  int64
	AllowedMimes []string
	Files        map[int64]coreFile
	nextFile     int64
	Blob         *blob.S3
	Calls        []string
	subs         []*nats.Subscription
}

type coreFile struct {
	key, name, mime string
	size            int64
	uploadedBy      string
}

// NewCoreDouble returns a double with an empty world.
func NewCoreDouble(b *blob.S3) *CoreDouble {
	return &CoreDouble{
		Users: map[string]coreapi.User{}, ProjectPerms: map[string][]int64{}, Clients: map[int64]bool{},
		Projects: map[int64]bool{}, Requirements: map[int64]int64{}, Tasks: map[int64]int64{},
		ProjectInfo: map[int64]coreapi.Project{}, ClientNames: map[int64]string{},
		MaxFileSize: 10 << 20, AllowedMimes: []string{"image/png", "image/jpeg", "application/pdf", "text/plain"},
		Files: map[int64]coreFile{}, Blob: b,
	}
}

// SetDown toggles availability.
func (d *CoreDouble) SetDown(down bool) {
	d.mu.Lock()
	d.Down = down
	d.mu.Unlock()
}

// SetUser adds or replaces a user while the double is serving (the map is read by its handlers).
func (d *CoreDouble) SetUser(u coreapi.User) {
	d.mu.Lock()
	d.Users[u.ID] = u
	d.mu.Unlock()
}

// SetProject adds or replaces a project with its name and client while the double is serving.
func (d *CoreDouble) SetProject(p coreapi.Project) {
	d.mu.Lock()
	d.Projects[p.ID] = true
	d.ProjectInfo[p.ID] = p
	d.mu.Unlock()
}

// SetClient adds or replaces a client while the double is serving.
func (d *CoreDouble) SetClient(id int64, name string) {
	d.mu.Lock()
	d.Clients[id] = true
	d.ClientNames[id] = name
	d.mu.Unlock()
}

// User reads a user while the double is serving.
func (d *CoreDouble) User(id string) coreapi.User {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.Users[id]
}

// CallCount counts the calls to a method.
func (d *CoreDouble) CallCount(method string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.Calls {
		if c == method {
			n++
		}
	}
	return n
}

// Start subscribes to core's two planes of an instance.
func (d *CoreDouble) Start(nc *nats.Conn, instance string) error {
	for _, svc := range []string{"jiku-queries", "jiku-commands"} {
		svc := svc
		sub, err := nc.Subscribe(instance+".*."+svc+".v1.>", func(m *nats.Msg) { d.handle(svc, m) })
		if err != nil {
			return err
		}
		d.subs = append(d.subs, sub)
	}
	return nc.Flush()
}

// Stop unsubscribes.
func (d *CoreDouble) Stop() {
	for _, s := range d.subs {
		_ = s.Unsubscribe()
	}
}

type reply struct {
	Status       string         `json:"status"`
	Data         any            `json:"data,omitempty"`
	ErrorCode    string         `json:"errorCode,omitempty"`
	ErrorMessage string         `json:"errorMessage,omitempty"`
	ErrorDetails map[string]any `json:"errorDetails,omitempty"`
}

func fail(code, msg string) reply {
	return reply{Status: "failure", ErrorCode: code, ErrorMessage: msg}
}

func (d *CoreDouble) handle(svc string, m *nats.Msg) {
	parts := strings.SplitN(m.Subject, ".", 5)
	if len(parts) != 5 {
		return
	}
	method := parts[4]
	d.mu.Lock()
	down := d.Down
	d.Calls = append(d.Calls, method)
	d.mu.Unlock()
	if down {
		return // a dead core never answers: the caller times out
	}
	var payload map[string]json.RawMessage
	if len(m.Data) > 0 {
		if err := json.Unmarshal(m.Data, &payload); err != nil {
			d.respond(m, fail("invalid_fields", "malformed"))
			return
		}
	}
	if _, ok := payload["actor"]; ok {
		d.respond(m, fail("invalid_fields", "actor is reserved to the api"))
		return
	}
	var r reply
	if svc == "jiku-queries" {
		r = d.query(method, payload)
	} else {
		r = d.command(method, payload, parts[1])
	}
	d.respond(m, r)
}

func (d *CoreDouble) respond(m *nats.Msg, r reply) {
	b, _ := json.Marshal(r)
	_ = m.Respond(b)
}

type sheet struct {
	fields, includes, filters []string
}

var sheets = map[string]sheet{
	"users.list":               {[]string{"id", "name", "username", "createdAt", "updatedAt"}, []string{"email", "person", "roles", "identityType"}, []string{"id", "username", "email", "q"}},
	"project-permissions.list": {[]string{"id", "userId", "projectId", "createdAt"}, []string{"user", "project"}, []string{"projectId", "userId"}},
	"clients.list":             {[]string{"id", "name", "createdAt", "updatedAt"}, nil, []string{"id", "name", "createdAt", "q"}},
	"projects.list":            {[]string{"id", "code", "name", "clientId", "createdAt"}, nil, []string{"id", "clientId"}},
	"requirements.list":        {[]string{"id", "title", "projectId", "createdAt"}, nil, []string{"id", "projectId"}},
	"tasks.list":               {[]string{"id", "title", "projectId", "createdAt"}, nil, []string{"id", "projectId"}},
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (d *CoreDouble) query(method string, p map[string]json.RawMessage) reply {
	sh, ok := sheets[method]
	if !ok {
		return fail("unknown_command", method)
	}
	for k := range p {
		if !contains([]string{"filter", "sort", "page", "fields", "include", "count"}, k) {
			return fail("invalid_fields", "key "+k)
		}
	}
	var filter map[string]json.RawMessage
	_ = json.Unmarshal(p["filter"], &filter)
	for k := range filter {
		if !contains(sh.filters, k) {
			return fail("invalid_fields", "filter "+k)
		}
	}
	var fields, includes []string
	_ = json.Unmarshal(p["fields"], &fields)
	_ = json.Unmarshal(p["include"], &includes)
	for _, f := range fields {
		if !contains(sh.fields, f) && !contains(sh.includes, f) {
			return fail("invalid_fields", "field "+f)
		}
	}
	for _, f := range includes {
		if !contains(sh.includes, f) {
			return fail("invalid_fields", "include "+f)
		}
	}
	var pg struct {
		Limit  int    `json:"limit"`
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(p["page"], &pg)
	if pg.Limit <= 0 {
		pg.Limit = 50
	}
	if pg.Limit > 200 {
		pg.Limit = 200
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var items []map[string]any
	switch method {
	case "users.list":
		ids := strList(filter["id"])
		keys := make([]string, 0, len(d.Users))
		for k := range d.Users {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			u := d.Users[k]
			if ids != nil && !contains(ids, u.ID) {
				continue
			}
			it := map[string]any{"id": u.ID, "name": u.Name, "username": u.Username, "createdAt": "2026-01-01T00:00:00.000Z"}
			if contains(includes, "roles") {
				it["roles"] = u.Roles
			}
			if contains(includes, "identityType") {
				it["identityType"] = u.IdentityType
			}
			items = append(items, it)
		}
	case "project-permissions.list":
		var user string
		_ = json.Unmarshal(filter["userId"], &user)
		for i, pid := range d.ProjectPerms[user] {
			items = append(items, map[string]any{"id": i + 1, "userId": user, "projectId": pid})
		}
	default:
		ids := intList(filter["id"])
		var world map[int64]int64
		switch method {
		case "clients.list", "projects.list":
			world = map[int64]int64{}
			src := d.Clients
			if method == "projects.list" {
				src = d.Projects
			}
			for id := range src {
				world[id] = id
			}
		case "requirements.list":
			world = d.Requirements
		case "tasks.list":
			world = d.Tasks
		}
		if ids == nil && filter["id"] == nil {
			for id := range world {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		}
		for _, id := range ids {
			if pid, ok := world[id]; ok {
				it := map[string]any{"id": id}
				if method == "requirements.list" || method == "tasks.list" {
					it["projectId"] = pid
				}
				switch method {
				case "projects.list":
					info := d.ProjectInfo[id]
					if contains(fields, "name") {
						it["name"] = info.Name
					}
					if contains(fields, "code") {
						it["code"] = info.Code
					}
					if contains(fields, "clientId") {
						it["clientId"] = info.ClientID
					}
				case "clients.list":
					if contains(fields, "name") {
						it["name"] = d.ClientNames[id]
					}
				}
				items = append(items, it)
			}
		}
	}
	// Keyset by position, enough for a double.
	start := 0
	if pg.Cursor != "" {
		start, _ = strconv.Atoi(pg.Cursor)
	}
	if start > len(items) {
		start = len(items)
	}
	end := start + pg.Limit
	page := map[string]any{"limit": pg.Limit}
	if end < len(items) {
		page["cursor"] = strconv.Itoa(end)
	} else {
		end = len(items)
	}
	out := items[start:end]
	if out == nil {
		out = []map[string]any{}
	}
	page["returned"] = len(out)
	return reply{Status: "success", Data: map[string]any{"items": out, "page": page}}
}

func strList(raw json.RawMessage) []string {
	if raw == nil {
		return nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var many []string
	_ = json.Unmarshal(raw, &many)
	return many
}

func intList(raw json.RawMessage) []int64 {
	if raw == nil {
		return nil
	}
	var one int64
	if json.Unmarshal(raw, &one) == nil {
		return []int64{one}
	}
	var many []int64
	_ = json.Unmarshal(raw, &many)
	return many
}

func (d *CoreDouble) command(method string, p map[string]json.RawMessage, caller string) reply {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if method == "files.request-upload" {
		for k := range p {
			if !contains([]string{"uploader", "fileName", "mimeType", "fileSize", "checksum"}, k) {
				return fail("invalid_fields", "key "+k)
			}
		}
		var in struct {
			FileName string `json:"fileName"`
			MimeType string `json:"mimeType"`
			FileSize int64  `json:"fileSize"`
		}
		b, _ := json.Marshal(p)
		_ = json.Unmarshal(b, &in)
		d.mu.Lock()
		if !contains(d.AllowedMimes, in.MimeType) {
			d.mu.Unlock()
			return fail("file_type_not_allowed", "tipo no permitido")
		}
		if in.FileSize > d.MaxFileSize {
			d.mu.Unlock()
			return fail("file_too_large", "muy grande")
		}
		d.nextFile++
		id := d.nextFile
		key := fmt.Sprintf("core/f/%d", id)
		d.Files[id] = coreFile{key: key, name: in.FileName, mime: in.MimeType, size: in.FileSize, uploadedBy: caller}
		d.mu.Unlock()
		u, err := d.Blob.PresignPut(ctx, key, 5*time.Minute)
		if err != nil {
			return fail("internal_error", err.Error())
		}
		return reply{Status: "success", Data: map[string]any{"id": id, "uploadUrl": u, "expiresIn": 300}}
	}
	parts := strings.Split(method, ".")
	if len(parts) == 3 && parts[0] == "files" && parts[2] == "request-download" {
		for k := range p {
			if k != "disposition" {
				return fail("invalid_fields", "key "+k)
			}
		}
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		d.mu.Lock()
		f, ok := d.Files[id]
		d.mu.Unlock()
		if !ok {
			return fail("file_not_found", "no existe")
		}
		u, err := d.Blob.PresignGet(ctx, f.key, 5*time.Minute, "")
		if err != nil {
			return fail("internal_error", err.Error())
		}
		return reply{Status: "success", Data: map[string]any{"downloadUrl": u, "expiresIn": 300, "fileName": f.name, "mimeType": f.mime, "fileSize": f.size}}
	}
	return fail("unknown_command", method)
}
