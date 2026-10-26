package testutil

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/gravadigital/jiku-docs/internal/app"
	"github.com/gravadigital/jiku-docs/internal/blob"
	"github.com/gravadigital/jiku-docs/internal/contract"
	"github.com/gravadigital/jiku-docs/internal/coreapi"
	"github.com/gravadigital/jiku-docs/internal/logging"
	"github.com/gravadigital/jiku-docs/internal/outbox"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/service"
	"github.com/gravadigital/jiku-docs/internal/store"
)

// Identities of the test world.
const (
	Instance  = "dev"
	APIUser   = "390014149763006466" // the api (JIKU_API_USER_ID)
	DocsUser  = "396551947454251010" // jiku_docs machine user: internal-app + docs
	Admin     = "396554146460729346" // admin
	User      = "396554027241832450" // user
	User2     = "500000000000000002" // another user
	External  = "396554218317545474" // external-user, sees project 12
	Connector = "500000000000000003" // another internal-app
	Observer  = "500000000000000004" // bus-observer: no access to jiku-docs
	Ghost     = "500000000000000009" // not in core's users
)

// Env is one isolated jiku-docs: its own database, bucket and NATS server.
type Env struct {
	T        *testing.T
	DB       *pgxpool.Pool
	Blob     *blob.S3
	CoreBlob *blob.S3
	NS       *server.Server
	NC       *nats.Conn // the service connection
	Client   *nats.Conn // the callers' connection
	Core     *CoreDouble
	Cached   *coreapi.Cached
	Inst     *app.Instance
	Contract *contract.Validator
	Settings service.Settings

	mu     sync.Mutex
	events []Event
	notes  []Note
}

// Event is a message seen on JIKU_DOCS_EVENTS.
type Event struct {
	Subject string
	MsgID   string
	Body    map[string]any
	Raw     []byte
}

// Note is a docs-notify message seen by a person.
type Note struct {
	UserID string
	Type   string
	Body   map[string]any
}

// Options tune an environment.
type Options struct {
	NoIndexer   bool
	NoJanitor   bool
	JanitorTick time.Duration
	CoreTimeout time.Duration
	NoKV        bool
}

var contractOnce sync.Once
var contractV *contract.Validator

// NewEnv starts an environment and registers its cleanup.
func NewEnv(t *testing.T, opts ...Options) *Env {
	t.Helper()
	if SkipIntegration() {
		t.Skip("JIKU_DOCS_SKIP_INTEGRATION=1")
	}
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	c, err := Shared()
	if err != nil {
		t.Fatalf("containers: %v (set JIKU_DOCS_SKIP_INTEGRATION=1 to skip integration tests without Docker)", err)
	}
	ctx := context.Background()
	e := &Env{T: t}
	contractOnce.Do(func() {
		root, err := contract.RepoRoot()
		if err == nil {
			contractV, err = contract.Load(root)
		}
		if err != nil {
			panic(err)
		}
	})
	e.Contract = contractV

	dsn, err := c.NewDatabase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e.DB, err = store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.DB.Close)
	if _, err := store.Migrate(ctx, e.DB, nil); err != nil {
		t.Fatal(err)
	}
	bucket := "jd-" + randHex(6)
	e.Blob, err = blob.New(blob.Config{Endpoint: c.MinIOEndpoint, Region: "us-east-1", Bucket: bucket,
		AccessKey: c.MinIOUser, SecretKey: c.MinIOPassword, PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Blob.EnsureBucket(ctx, "us-east-1"); err != nil {
		t.Fatal(err)
	}
	if err := e.Blob.EnsureLifecycle(ctx, blob.DefaultTrashDays); err != nil { // what `jiku-docs storage init` does
		t.Fatal(err)
	}
	e.CoreBlob, err = blob.New(blob.Config{Endpoint: c.MinIOEndpoint, Region: "us-east-1", Bucket: "core-" + randHex(6),
		AccessKey: c.MinIOUser, SecretKey: c.MinIOPassword, PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.CoreBlob.EnsureBucket(ctx, "us-east-1"); err != nil {
		t.Fatal(err)
	}

	e.NS, err = server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(),
		NoLog: true, NoSigs: true, MaxPayload: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	go e.NS.Start()
	if !e.NS.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats-server not ready")
	}
	t.Cleanup(e.NS.Shutdown)
	e.NC, err = nats.Connect(e.NS.ClientURL(), nats.CustomInboxPrefix(protocol.InboxPrefix(DocsUser)), nats.Name("jiku-docs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.NC.Close)
	e.Client, err = nats.Connect(e.NS.ClientURL(), nats.Name("callers"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Client.Close)

	e.Core = NewCoreDouble(e.CoreBlob)
	e.seedWorld()
	coreNC, err := nats.Connect(e.NS.ClientURL(), nats.Name("core-double"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(coreNC.Close)
	if err := e.Core.Start(coreNC, Instance); err != nil {
		t.Fatal(err)
	}

	js, err := jetstream.New(e.Client)
	if err != nil {
		t.Fatal(err)
	}
	if !o.NoKV {
		// What Jiku's deployment does (D-6): the KV exists before any module.
		if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "JIKU_MODULES", History: 1}); err != nil {
			t.Fatal(err)
		}
	}
	timeout := o.CoreTimeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	e.Cached = coreapi.NewCached(coreapi.New(coreapi.NATSTransport{Conn: e.NC, Instance: Instance, UserID: DocsUser}, nil, timeout))
	e.Settings = service.Settings{
		Instance: Instance, ServiceName: "jiku-docs", APIUserID: APIUser, SelfUserID: DocsUser,
		WebURL: "http://web.test", PublicURL: "http://public.test", SecretKey: []byte(strings.Repeat("k", 32)),
	}
	e.Inst, err = app.Start(ctx, app.Options{
		NC: e.NC, DB: e.DB, Blob: e.Blob, Core: e.Cached, Log: logging.Discard(), Settings: e.Settings, Version: "test",
		Indexer: !o.NoIndexer, Janitor: !o.NoJanitor, Outbox: true, Register: !o.NoKV, JanitorTick: o.JanitorTick, ReviewHour: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Inst.Stop)
	e.captureEvents(js)
	e.captureNotifications()
	return e
}

func (e *Env) seedWorld() {
	add := func(id, name string, idType string, roles ...string) {
		e.Core.Users[id] = coreapi.User{ID: id, Name: name, Username: strings.ToLower(name), Roles: roles, IdentityType: idType}
	}
	add(APIUser, "api", "service", "internal-app")
	add(DocsUser, "jiku-docs", "service", "internal-app", "docs")
	add(Admin, "Admin", "person", "admin")
	add(User, "Usuario", "person", "user")
	add(User2, "Usuaria", "person", "user")
	add(External, "Cliente", "person", "external-user")
	add(Connector, "Conector", "service", "internal-app")
	add(Observer, "Observer", "person", "bus-observer")
	e.Core.ProjectPerms[External] = []int64{12}
	four := int64(4)
	e.Core.Clients[4] = true
	e.Core.ClientNames[4] = "Acmé S.A."
	e.Core.Projects[12], e.Core.Projects[31] = true, true
	e.Core.ProjectInfo[12] = coreapi.Project{ID: 12, Code: "VC", Name: "EXO - Videoconferencia", ClientID: &four}
	e.Core.ProjectInfo[31] = coreapi.Project{ID: 31, Code: "PZT", Name: "Portal Zeta"}
	e.Core.Requirements[812] = 12
	e.Core.Tasks[900] = 31
}

func (e *Env) captureEvents(js jetstream.JetStream) {
	ctx := context.Background()
	cons, err := js.OrderedConsumer(ctx, outbox.StreamName, jetstream.OrderedConsumerConfig{})
	if err != nil {
		e.T.Fatal(err)
	}
	cc, err := cons.Consume(func(m jetstream.Msg) {
		var body map[string]any
		_ = json.Unmarshal(m.Data(), &body)
		typ := strings.TrimPrefix(m.Subject(), Instance+".docs-events.v1.")
		if err := e.Contract.ValidateEvent(typ, m.Data()); err != nil {
			e.T.Errorf("event %s breaks the contract: %v", typ, err)
		}
		if body["type"] != typ {
			e.T.Errorf("event type %v does not match its subject %s", body["type"], m.Subject())
		}
		e.mu.Lock()
		e.events = append(e.events, Event{Subject: m.Subject(), MsgID: m.Headers().Get(jetstream.MsgIDHeader), Body: body, Raw: m.Data()})
		e.mu.Unlock()
	})
	if err != nil {
		e.T.Fatal(err)
	}
	e.T.Cleanup(cc.Stop)
}

func (e *Env) captureNotifications() {
	sub, err := e.Client.Subscribe(Instance+".*.docs-notify.v1.>", func(m *nats.Msg) {
		parts := strings.SplitN(m.Subject, ".", 5)
		typ := parts[4]
		if err := e.Contract.ValidateNotification(typ, m.Data); err != nil {
			e.T.Errorf("notification %s breaks the contract: %v", typ, err)
		}
		var body map[string]any
		_ = json.Unmarshal(m.Data, &body)
		e.mu.Lock()
		e.notes = append(e.notes, Note{UserID: parts[1], Type: typ, Body: body})
		e.mu.Unlock()
	})
	if err != nil {
		e.T.Fatal(err)
	}
	e.T.Cleanup(func() { _ = sub.Unsubscribe() })
}

// Events returns the events seen so far.
func (e *Env) Events() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Event{}, e.events...)
}

// Notes returns the notifications seen so far.
func (e *Env) Notes() []Note {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Note{}, e.notes...)
}

// WaitEvent waits for an event of a type matching pred.
func (e *Env) WaitEvent(typ string, pred func(Event) bool) Event {
	e.T.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range e.Events() {
			if ev.Body["type"] == typ && (pred == nil || pred(ev)) {
				return ev
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.T.Fatalf("event %s not seen; seen: %d events", typ, len(e.Events()))
	return Event{}
}

// Eventually polls cond until true or 10 s.
func (e *Env) Eventually(what string, cond func() bool) {
	e.T.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	e.T.Fatalf("timed out waiting for %s", what)
}

// Resp is a decoded reply.
type Resp struct {
	Status       string          `json:"status"`
	Data         json.RawMessage `json:"data"`
	ErrorCode    string          `json:"errorCode"`
	ErrorMessage string          `json:"errorMessage"`
	ErrorDetails map[string]any  `json:"errorDetails"`
	Raw          []byte          `json:"-"`
	Header       nats.Header     `json:"-"`
}

// M decodes data as an object.
func (r Resp) M() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(r.Data, &m)
	return m
}

// Items decodes data.items.
func (r Resp) Items() []map[string]any {
	var d struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(r.Data, &d)
	return d.Items
}

func (e *Env) do(user, plane, method string, payload any, validateRequest bool) Resp {
	e.T.Helper()
	var body []byte
	switch p := payload.(type) {
	case nil:
		body = []byte("{}")
	case string:
		body = []byte(p)
	case []byte:
		body = p
	default:
		var err error
		if body, err = json.Marshal(p); err != nil {
			e.T.Fatal(err)
		}
	}
	if validateRequest {
		if err := e.Contract.ValidateRequest(plane, method, body); err != nil {
			e.T.Fatalf("the test sends a request outside the contract: %v", err)
		}
	}
	subject := protocol.RequestSubject(Instance, user, "jiku-docs-"+plane, method)
	msg, err := e.Client.Request(subject, body, 15*time.Second)
	if err != nil {
		e.T.Fatalf("%s: %v", method, err)
	}
	var r Resp
	if err := json.Unmarshal(msg.Data, &r); err != nil {
		e.T.Fatalf("%s: not an envelope: %s", method, msg.Data)
	}
	r.Raw, r.Header = msg.Data, msg.Header
	if err := e.Contract.ValidateReply(plane, method, msg.Data); err != nil {
		e.T.Errorf("the reply of %s breaks the contract: %v", method, err)
	}
	if r.Status == "failure" && msg.Header.Get("Nats-Service-Error-Code") == "" {
		e.T.Errorf("%s: failure without Nats-Service-Error headers", method)
	}
	return r
}

// Cmd sends a contract-valid command.
func (e *Env) Cmd(user, method string, payload any) Resp {
	e.T.Helper()
	return e.do(user, protocol.PlaneCommands, method, payload, true)
}

// CmdRaw sends a command without validating the request (negative tests).
func (e *Env) CmdRaw(user, method string, payload any) Resp {
	e.T.Helper()
	return e.do(user, protocol.PlaneCommands, method, payload, false)
}

// Q sends a contract-valid query.
func (e *Env) Q(user, method string, payload any) Resp {
	e.T.Helper()
	return e.do(user, protocol.PlaneQueries, method, payload, true)
}

// QRaw sends a query without validating the request.
func (e *Env) QRaw(user, method string, payload any) Resp {
	e.T.Helper()
	return e.do(user, protocol.PlaneQueries, method, payload, false)
}

// OK asserts success and returns data as a map.
func (e *Env) OK(r Resp) map[string]any {
	e.T.Helper()
	if r.Status != "success" {
		e.T.Fatalf("expected success, got %s: %s %v", r.ErrorCode, r.ErrorMessage, r.ErrorDetails)
	}
	return r.M()
}

// Fail asserts a failure code.
func (e *Env) Fail(r Resp, code string) Resp {
	e.T.Helper()
	if r.Status != "failure" || r.ErrorCode != code {
		e.T.Fatalf("expected %s, got %s %s %s", code, r.Status, r.ErrorCode, r.Raw)
	}
	return r
}

// Sum is the checksum of a content.
func Sum(content string) string {
	s := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(s[:])
}

// ID returns a fresh ULID.
func ID() string { return protocol.NewULID() }

// Folder creates a folder as admin under parent (root when "") and returns its id.
func (e *Env) Folder(parent, name string, extra map[string]any) string {
	e.T.Helper()
	if parent == "" {
		parent = protocol.RootFolderID
	}
	id := ID()
	p := map[string]any{"id": id, "parentId": parent, "name": name}
	for k, v := range extra {
		p[k] = v
	}
	e.OK(e.Cmd(Admin, "folders.new", p))
	return id
}

// ACL replaces the rules of a folder as admin.
func (e *Env) ACL(folder string, rules ...map[string]any) {
	e.T.Helper()
	if rules == nil {
		rules = []map[string]any{}
	}
	e.OK(e.Cmd(Admin, "folders."+folder+".acl.replace", map[string]any{"rules": rules}))
}

// Rule builds an ACL rule.
func Rule(kind, principal, perm string) map[string]any {
	return map[string]any{"principalType": kind, "principal": principal, "permission": perm}
}

// Doc creates a document as user and returns its id.
func (e *Env) Doc(user, folder, fileName, content string) string {
	e.T.Helper()
	id := ID()
	e.OK(e.Cmd(user, "documents.new", map[string]any{"id": id, "folderId": folder, "fileName": fileName,
		"content": content, "checksum": Sum(content)}))
	return id
}
