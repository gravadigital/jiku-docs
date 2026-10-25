// Package service is jiku-docs' business logic: the 18 commands and 13 queries, authorisation,
// versioning, locks, content validation and the changelog that feeds the outbox. It is
// transport-agnostic: package bus feeds it NATS micro requests, and the importer calls it
// in-process as the service identity.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gravadigital/jiku-docs/internal/acl"
	"github.com/gravadigital/jiku-docs/internal/blob"
	"github.com/gravadigital/jiku-docs/internal/caller"
	"github.com/gravadigital/jiku-docs/internal/coreapi"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/store"
	"github.com/gravadigital/jiku-docs/internal/validate"
)

// Settings are the integration values the logic needs.
type Settings struct {
	Instance    string
	ServiceName string
	// APIUserID is the user id of Jiku's api: the only caller that may send `actor` and use
	// shares.resolve / shares.attachment.
	APIUserID string
	// SelfUserID is the user id of jiku-docs' own machine user (the importer acts as it).
	SelfUserID string
	// WebURL builds attachment links: {WebURL}/docs/attachments/{id}.
	WebURL string
	// PublicURL builds share links: {PublicURL}/p/docs/{token}.
	PublicURL string
	// SecretKey derives the share retry cipher key and the IP hash key.
	SecretKey []byte
}

// Hooks observe the dispatcher, for metrics.
type Hooks struct {
	Request func(plane, method, errorCode string, d time.Duration)
}

// Service is the business logic.
type Service struct {
	DB    *pgxpool.Pool
	Blob  blob.Store
	Core  coreapi.API
	Log   *slog.Logger
	Set   Settings
	HTTP  *http.Client
	Hooks Hooks
	Now   func() time.Time
	// PageBudget is the byte budget of a list page (half the NATS max_payload, like core).
	PageBudget int

	treeMu sync.Mutex
	tree   *acl.Tree

	epOnce            sync.Once
	commands, queries []Endpoint
}

// New builds the service.
func New(db *pgxpool.Pool, b blob.Store, core coreapi.API, log *slog.Logger, set Settings) *Service {
	return &Service{
		DB: db, Blob: b, Core: core, Log: log, Set: set,
		HTTP: &http.Client{Timeout: 60 * time.Second},
		Now:  protocol.Now,
	}
}

func (s *Service) now() time.Time { return s.Now().UTC().Truncate(time.Millisecond) }

// ---- endpoints and dispatch ----------------------------------------------------------------

// Call is one request after the caller was resolved.
type Call struct {
	Caller        *caller.Caller
	Method        string
	Params        map[string]string
	Payload       json.RawMessage // without `actor`
	CorrelationID string
}

// Endpoint is one method of a plane.
type Endpoint struct {
	Pattern string
	// APIOnly: only the api, without actor (shares.resolve, shares.attachment).
	APIOnly bool
	Handle  func(ctx context.Context, c *Call) (any, error)
}

// Request is what a transport hands to Dispatch.
type Request struct {
	Plane         string
	Subject       string
	Data          []byte
	CorrelationID string
}

// Dispatch runs one request and never panics: any failure becomes a `failure` envelope, because
// on the other side there is a request waiting and a mute timeout explains nothing.
func (s *Service) Dispatch(ctx context.Context, req Request) (reply protocol.Reply) {
	start := time.Now()
	method, callerID, code := "", "", ""
	var effective string
	defer func() {
		if r := recover(); r != nil {
			s.Log.Error("panic in handler", "method", method, "panic", r)
			reply = protocol.Failure(protocol.E(protocol.CodeInternalError))
			code = protocol.CodeInternalError
		}
		d := time.Since(start)
		attrs := []any{"plane", req.Plane, "method", method, "caller", callerID,
			"correlationId", req.CorrelationID, "durationMs", d.Milliseconds()}
		if effective != "" && effective != callerID {
			attrs = append(attrs, "actor", effective)
		}
		if code != "" {
			attrs = append(attrs, "errorCode", code)
			s.Log.Info("request failed", attrs...)
		} else {
			s.Log.Info("request", attrs...)
		}
		if s.Hooks.Request != nil {
			s.Hooks.Request(req.Plane, patternOr(method), code, d)
		}
	}()

	ps, ok := protocol.ParseSubject(req.Subject)
	if !ok {
		code = protocol.CodeInvalidFields
		return protocol.Failure(protocol.E(protocol.CodeInvalidFields))
	}
	callerID = ps.UserID
	s.epOnce.Do(func() { s.commands, s.queries = s.Commands(), s.Queries() })
	endpoints := s.queries
	if req.Plane == protocol.PlaneCommands {
		endpoints = s.commands
	}
	var ep *Endpoint
	var params map[string]string
	for i := range endpoints {
		if p, ok := protocol.MatchPattern(endpoints[i].Pattern, ps.Method); ok {
			ep, params = &endpoints[i], p
			break
		}
	}
	if ep == nil {
		code = "unknown_command"
		return protocol.Reply{Status: protocol.StatusFailure, ErrorCode: "unknown_command", ErrorMessage: "Método desconocido"}
	}
	method = ep.Pattern
	for k, v := range params {
		if !protocol.IsULID(v) {
			code = protocol.CodeInvalidFields
			return protocol.Failure(protocol.InvalidField(k, v, nil, "El id del subject tiene que ser un ULID"))
		}
	}

	payload, actor, err := s.splitActor(req.Data, callerID)
	if err == nil && ep.APIOnly && callerID != s.Set.APIUserID {
		err = protocol.E(protocol.CodeCallerNotAuthorized)
	}
	var c *caller.Caller
	if err == nil {
		c, err = s.ResolveCaller(ctx, callerID, actor)
	}
	if err == nil {
		effective = c.ID
		var data any
		data, err = ep.Handle(ctx, &Call{Caller: c, Method: ps.Method, Params: params, Payload: payload, CorrelationID: req.CorrelationID})
		if err == nil {
			return protocol.Success(data)
		}
	}
	err = s.translate(err, method)
	code = protocol.CodeOf(err)
	return protocol.Failure(err)
}

func patternOr(m string) string {
	if m == "" {
		return "unknown"
	}
	return m
}

// translate maps infrastructure errors to the catalog.
func (s *Service) translate(err error, method string) error {
	var pe *protocol.Error
	switch {
	case errors.As(err, &pe):
		return pe
	case store.IsStatementTimeout(err):
		return protocol.E(protocol.CodeQueryTimeout)
	case errors.Is(err, coreapi.ErrUnavailable):
		s.Log.Warn("core unavailable", "method", method, "err", err.Error())
		return protocol.E(protocol.CodeUpstreamUnavailable)
	case errors.Is(err, context.DeadlineExceeded):
		s.Log.Error("deadline exceeded", "method", method)
		return protocol.E(protocol.CodeInternalError)
	default:
		var ce *coreapi.Error
		if errors.As(err, &ce) {
			s.Log.Warn("core failure", "method", method, "coreMethod", ce.Method, "coreCode", ce.Code)
			return protocol.E(protocol.CodeUpstreamUnavailable)
		}
		s.Log.Error("internal error", "method", method, "err", err.Error())
		return protocol.E(protocol.CodeInternalError)
	}
}

// Actor is the person the api acts for (D-11): the same envelope the api sends to core, built
// from the already verified web token. Only the api may send it.
type Actor struct {
	ID       string
	Roles    []string
	Name     string
	Username string
	Email    string
}

// splitActor validates the top level of the payload, extracts `actor` (only the api may send
// it) and returns the payload without it.
func (s *Service) splitActor(data []byte, callerID string) (json.RawMessage, *Actor, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return json.RawMessage("{}"), nil, nil
	}
	if trimmed[0] != '{' {
		return nil, nil, protocol.InvalidField("payload", nil, nil, "Se esperaba un objeto JSON")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &m); err != nil {
		return nil, nil, protocol.InvalidField("payload", nil, nil, "JSON mal formado")
	}
	raw, ok := m["actor"]
	if !ok {
		return trimmed, nil, nil
	}
	if callerID != s.Set.APIUserID {
		return nil, nil, protocol.InvalidField("actor", nil, nil, "Solo la api de Jiku puede enviar actor: la identidad sale del subject")
	}
	if !validate.IsObject(raw) {
		return nil, nil, protocol.InvalidField("actor", nil, nil, "actor tiene que ser { id, roles, name?, username?, email? }")
	}
	o := validate.ParseObject(raw, "actor", "id", "roles", "name", "username", "email")
	a := &Actor{}
	a.ID, _ = o.String("id", true, validate.MinLen(1), validate.MaxLen(200))
	if o.Err() == nil && !o.Has("roles") {
		o.Fail(protocol.InvalidField("actor.roles", nil, nil, "El campo %q es obligatorio", "actor.roles"))
	}
	a.Roles, _ = o.StringList("roles", nil, 50)
	a.Name, _ = o.String("name", false, validate.MaxLen(500))
	a.Username, _ = o.String("username", false, validate.MaxLen(500))
	a.Email, _ = o.String("email", false, validate.MaxLen(500))
	if err := o.Err(); err != nil {
		return nil, nil, err
	}
	delete(m, "actor")
	rest, _ := json.Marshal(m)
	return rest, a, nil
}

// ResolveCaller classifies the caller (section 4): the api without actor is a service; with
// actor, the class comes from `actor.roles` (D-11: the roles of the verified web token, fresher
// than core's `users` mirror) and `users.list` is not consulted. Any other caller is classified
// by its row in core's `users`.
func (s *Service) ResolveCaller(ctx context.Context, subjectID string, actor *Actor) (*caller.Caller, error) {
	if subjectID == s.Set.APIUserID && s.Set.APIUserID != "" {
		if actor == nil {
			return &caller.Caller{SubjectID: subjectID, ViaAPI: true, ID: subjectID, Class: caller.Service, Name: "api"}, nil
		}
		class, ok := caller.ClassFor(actor.Roles, "person")
		if !ok || class == caller.Service {
			return nil, protocol.Ef(protocol.CodeCallerNotAuthorized, "El actor tiene que ser una persona con un rol de producto")
		}
		name := actor.Name
		if name == "" {
			name = actor.Username
		}
		return &caller.Caller{SubjectID: subjectID, ViaAPI: true, ID: actor.ID, Name: name, Class: class, Roles: actor.Roles}, nil
	}
	u, err := s.Core.User(ctx, subjectID)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, protocol.E(protocol.CodeUnknownCaller)
	}
	class, ok := caller.ClassFor(u.Roles, u.IdentityType)
	if !ok {
		return nil, protocol.E(protocol.CodeCallerNotAuthorized)
	}
	return &caller.Caller{SubjectID: subjectID, ID: u.ID, Name: u.Name, Class: class, Roles: u.Roles}, nil
}

// SelfCaller is jiku-docs acting as itself (importer, relation review).
func (s *Service) SelfCaller() *caller.Caller {
	return &caller.Caller{SubjectID: s.Set.SelfUserID, ID: s.Set.SelfUserID, Name: "jiku-docs", Class: caller.Service}
}

// ---- folder tree ---------------------------------------------------------------------------

// Tree returns the live folder tree, cached by the version in `tree_state`.
func (s *Service) Tree(ctx context.Context) (*acl.Tree, error) {
	var version int64
	if err := s.DB.QueryRow(ctx, `SELECT version FROM tree_state WHERE id = 1`).Scan(&version); err != nil {
		return nil, err
	}
	s.treeMu.Lock()
	cached := s.tree
	s.treeMu.Unlock()
	if cached != nil && cached.Version == version {
		return cached, nil
	}
	t, err := loadTree(ctx, s.DB, version)
	if err != nil {
		return nil, err
	}
	s.treeMu.Lock()
	if s.tree == nil || s.tree.Version <= version {
		s.tree = t
	}
	s.treeMu.Unlock()
	return t, nil
}

// treeTx loads the tree inside a transaction (sees its own uncommitted changes). Never cached:
// a rolled-back version number could be reused by another transaction.
func treeTx(ctx context.Context, tx pgx.Tx) (*acl.Tree, error) {
	var version int64
	if err := tx.QueryRow(ctx, `SELECT version FROM tree_state WHERE id = 1`).Scan(&version); err != nil {
		return nil, err
	}
	return loadTree(ctx, tx, version)
}

func loadTree(ctx context.Context, q store.Querier, version int64) (*acl.Tree, error) {
	rows, err := q.Query(ctx, `SELECT id, COALESCE(parent_id, ''), name, path, inherit_acl, external_blocked,
		sharing_blocked, created_by, created_at FROM folders WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	byID := map[string]*acl.Folder{}
	var list []*acl.Folder
	for rows.Next() {
		f := &acl.Folder{}
		if err := rows.Scan(&f.ID, &f.ParentID, &f.Name, &f.Path, &f.InheritACL, &f.ExternalBlocked,
			&f.SharingBlocked, &f.CreatedBy, &f.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		byID[f.ID] = f
		list = append(list, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rrows, err := q.Query(ctx, `SELECT r.folder_id, r.principal_type, r.principal, r.permission
		FROM folder_rules r JOIN folders f ON f.id = r.folder_id WHERE f.deleted_at IS NULL
		ORDER BY r.folder_id, r.principal_type, r.principal`)
	if err != nil {
		return nil, err
	}
	for rrows.Next() {
		var fid string
		var r acl.Rule
		if err := rrows.Scan(&fid, &r.PrincipalType, &r.Principal, &r.Permission); err != nil {
			rrows.Close()
			return nil, err
		}
		if f, ok := byID[fid]; ok {
			f.Rules = append(f.Rules, r)
		}
	}
	rrows.Close()
	return acl.NewTree(version, list), rrows.Err()
}

func bumpTree(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `UPDATE tree_state SET version = version + 1 WHERE id = 1`)
	return err
}

// requireWriter rejects callers that can never write (external persons).
func requireWriter(c *caller.Caller) error {
	if c.IsExternal() {
		return protocol.E(protocol.CodeCallerNotAuthorized)
	}
	return nil
}

// args accumulates positional SQL parameters.
type args []any

func (a *args) add(v any) string {
	*a = append(*a, v)
	return "$" + itoa(len(*a))
}
