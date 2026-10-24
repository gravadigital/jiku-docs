// Package coreapi is everything jiku-docs asks Jiku's core, over `jiku-queries` and
// `jiku-commands`: who a caller is, which projects an external user sees, whether related
// entities exist, and the file service that holds attachments (D-3).
//
// jiku-docs never touches Jiku's database. When core does not answer, the operations that need
// it fail with `upstream_unavailable` — access is never granted by default.
package coreapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Reply is core's envelope, decoded.
type Reply struct {
	Status       string          `json:"status"`
	ErrorCode    string          `json:"errorCode,omitempty"`
	ErrorMessage string          `json:"errorMessage,omitempty"`
	Data         json.RawMessage `json:"data,omitempty"`
}

// Transport sends one request to core and returns its envelope. A transport error (timeout,
// no responders, disconnected) is returned as error; a `failure` envelope is not an error here.
type Transport interface {
	Request(ctx context.Context, service, method string, payload any) (*Reply, error)
}

// Service names of core.
const (
	Queries  = "jiku-queries"
	Commands = "jiku-commands"
)

// ErrUnavailable means core did not answer usably: the caller maps it to `upstream_unavailable`.
var ErrUnavailable = errors.New("core unavailable")

// Error is a failure envelope returned by core.
type Error struct {
	Method string
	Code   string
	Msg    string
}

func (e *Error) Error() string { return fmt.Sprintf("core %s: %s: %s", e.Method, e.Code, e.Msg) }

// IsCode reports a core failure with that code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// User is a row of core's `users`, with the two includables jiku-docs needs to classify a caller.
type User struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Username     string   `json:"username"`
	Roles        []string `json:"roles"`
	IdentityType string   `json:"identityType"`
}

// Entity is a related Jiku entity: its id and, for requirements and tasks, its project.
type Entity struct {
	ID        int64
	ProjectID int64
}

// FileTicket is the reply of `files.request-upload`.
type FileTicket struct {
	ID        int64  `json:"id"`
	UploadURL string `json:"uploadUrl"`
	ExpiresIn int    `json:"expiresIn"`
}

// DownloadTicket is the reply of `files.{fileId}.request-download`.
type DownloadTicket struct {
	DownloadURL string `json:"downloadUrl"`
	ExpiresIn   int    `json:"expiresIn"`
	FileName    string `json:"fileName"`
	MimeType    string `json:"mimeType"`
	FileSize    int64  `json:"fileSize"`
}

// API is what the service needs from core.
type API interface {
	User(ctx context.Context, id string) (*User, error) // nil, nil when core has no row
	Users(ctx context.Context) ([]User, error)
	ProjectPermissions(ctx context.Context, userID string) ([]int64, error)
	Entities(ctx context.Context, entityType string, ids []int64) (map[int64]Entity, error)
	RequestUpload(ctx context.Context, fileName, mimeType string, size int64, checksumHex string) (*FileTicket, error)
	RequestDownload(ctx context.Context, fileID int64, disposition string) (*DownloadTicket, error)
}

// Client implements API over a transport.
type Client struct {
	t Transport
	// UserIncludes are the includables asked to users.list. Core must declare `roles` (and
	// ideally `identityType`) for connector-class callers; see the README.
	UserIncludes []string
	// Timeout of each request.
	Timeout time.Duration
}

// New builds a client.
func New(t Transport, userIncludes []string, timeout time.Duration) *Client {
	if len(userIncludes) == 0 {
		userIncludes = []string{"roles", "identityType"}
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{t: t, UserIncludes: userIncludes, Timeout: timeout}
}

func (c *Client) call(ctx context.Context, service, method string, payload, dest any) error {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	reply, err := c.t.Request(ctx, service, method, payload)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUnavailable, method, err)
	}
	if reply.Status != "success" {
		if reply.ErrorCode == "" {
			return fmt.Errorf("%w: %s: malformed reply", ErrUnavailable, method)
		}
		return &Error{Method: method, Code: reply.ErrorCode, Msg: reply.ErrorMessage}
	}
	if dest == nil {
		return nil
	}
	if err := json.Unmarshal(reply.Data, dest); err != nil {
		return fmt.Errorf("%w: %s: decoding the reply: %w", ErrUnavailable, method, err)
	}
	return nil
}

type page struct {
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor,omitempty"`
}

type listPayload struct {
	Filter  map[string]any `json:"filter,omitempty"`
	Fields  []string       `json:"fields,omitempty"`
	Include []string       `json:"include,omitempty"`
	Page    page           `json:"page"`
}

type listReply[T any] struct {
	Items []T `json:"items"`
	Page  struct {
		Cursor string `json:"cursor"`
	} `json:"page"`
}

func listAll[T any](ctx context.Context, c *Client, method string, p listPayload) ([]T, error) {
	var out []T
	for i := 0; i < 1000; i++ {
		var r listReply[T]
		if err := c.call(ctx, Queries, method, p, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Items...)
		if r.Page.Cursor == "" {
			return out, nil
		}
		p.Page.Cursor = r.Page.Cursor
	}
	return nil, fmt.Errorf("%w: %s: too many pages", ErrUnavailable, method)
}

func (c *Client) normalizeUser(u *User) {
	if u.IdentityType == "" {
		u.IdentityType = "person"
		for _, r := range u.Roles {
			if r == "internal-app" || r == "core" {
				u.IdentityType = "service"
			}
		}
	}
}

// User fetches one user by its Zitadel `sub`.
func (c *Client) User(ctx context.Context, id string) (*User, error) {
	users, err := listAll[User](ctx, c, "users.list", listPayload{
		Filter: map[string]any{"id": id}, Include: c.UserIncludes, Page: page{Limit: 1},
	})
	if err != nil {
		return nil, err
	}
	for i := range users {
		if users[i].ID == id {
			c.normalizeUser(&users[i])
			return &users[i], nil
		}
	}
	return nil, nil
}

// Users lists every user, for the notification fan-out.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	users, err := listAll[User](ctx, c, "users.list", listPayload{Include: c.UserIncludes, Page: page{Limit: 200}})
	if err != nil {
		return nil, err
	}
	for i := range users {
		c.normalizeUser(&users[i])
	}
	return users, nil
}

// ProjectPermissions lists the projects an external user may see.
func (c *Client) ProjectPermissions(ctx context.Context, userID string) ([]int64, error) {
	type row struct {
		ProjectID int64 `json:"projectId"`
	}
	rows, err := listAll[row](ctx, c, "project-permissions.list", listPayload{
		Filter: map[string]any{"userId": userID}, Fields: []string{"id", "projectId"}, Page: page{Limit: 200},
	})
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		if !seen[r.ProjectID] {
			seen[r.ProjectID] = true
			out = append(out, r.ProjectID)
		}
	}
	return out, nil
}

var entityResources = map[string]struct {
	method string
	fields []string
}{
	"client":      {"clients.list", []string{"id"}},
	"project":     {"projects.list", []string{"id"}},
	"requirement": {"requirements.list", []string{"id", "projectId"}},
	"task":        {"tasks.list", []string{"id", "projectId"}},
}

// Entities checks which of the ids exist, and resolves the project of requirements and tasks.
func (c *Client) Entities(ctx context.Context, entityType string, ids []int64) (map[int64]Entity, error) {
	res, ok := entityResources[entityType]
	if !ok {
		return nil, fmt.Errorf("unknown entity type %q", entityType)
	}
	out := map[int64]Entity{}
	type row struct {
		ID        int64  `json:"id"`
		ProjectID *int64 `json:"projectId"`
	}
	for start := 0; start < len(ids); start += 200 {
		end := start + 200
		if end > len(ids) {
			end = len(ids)
		}
		rows, err := listAll[row](ctx, c, res.method, listPayload{
			Filter: map[string]any{"id": ids[start:end]}, Fields: res.fields, Page: page{Limit: 200},
		})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			e := Entity{ID: r.ID}
			if entityType == "project" {
				e.ProjectID = r.ID
			} else if r.ProjectID != nil {
				e.ProjectID = *r.ProjectID
			}
			out[r.ID] = e
		}
	}
	return out, nil
}

// RequestUpload asks core for an upload ticket. jiku-docs acts as itself (no actor): the file is
// owned by the jiku_docs machine user.
func (c *Client) RequestUpload(ctx context.Context, fileName, mimeType string, size int64, checksumHex string) (*FileTicket, error) {
	var t FileTicket
	err := c.call(ctx, Commands, "files.request-upload", map[string]any{
		"fileName": fileName, "mimeType": mimeType, "fileSize": size, "checksum": checksumHex,
	}, &t)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// RequestDownload asks core for a download URL of a file.
func (c *Client) RequestDownload(ctx context.Context, fileID int64, disposition string) (*DownloadTicket, error) {
	if disposition == "" {
		disposition = "inline"
	}
	var t DownloadTicket
	method := "files." + strconv.FormatInt(fileID, 10) + ".request-download"
	if err := c.call(ctx, Commands, method, map[string]any{"disposition": disposition}, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// ParseIncludes reads a comma separated list (CORE_USER_INCLUDES).
func ParseIncludes(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
