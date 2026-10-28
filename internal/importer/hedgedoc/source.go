// Package hedgedoc imports the notes of a HedgeDoc 1.x database (decision D-9): it reads a MariaDB
// loaded with the dump, applies a mapping sheet (note -> folder; unmapped notes go to
// /imported/, readable by administrators only), creates each document as the jiku-docs service
// with its original dates and author, uploads the images as attachments through core's file
// service (D-3), rewrites links and reports notes that look like they contain secrets.
//
// The dump and the note contents never leave `.local/`, and are never logged: logs carry note
// ids and counts only.
package hedgedoc

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // MariaDB driver
)

// Note is one HedgeDoc note with what the import needs.
type Note struct {
	ID         string
	ShortID    string
	Alias      string
	Title      string
	Content    string
	Permission string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Owner      string // display name of the owner, "" for anonymous notes
	LastEditor string
}

// Source yields the notes to import.
type Source interface {
	Notes(ctx context.Context) ([]Note, error)
}

// MySQLSource reads a HedgeDoc database.
type MySQLSource struct{ DSN string }

type profile struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
}

func displayName(profileJSON, email sql.NullString) string {
	if profileJSON.Valid && profileJSON.String != "" {
		var p profile
		if json.Unmarshal([]byte(profileJSON.String), &p) == nil {
			if p.DisplayName != "" {
				return p.DisplayName
			}
			if p.Username != "" {
				return p.Username
			}
		}
	}
	if email.Valid && email.String != "" {
		local, _, _ := strings.Cut(email.String, "@")
		return local
	}
	return ""
}

// Notes reads every live note, oldest first.
func (s MySQLSource) Notes(ctx context.Context) ([]Note, error) {
	db, err := sql.Open("mysql", s.DSN)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT n.id, COALESCE(n.shortid, ''), COALESCE(n.alias, ''), COALESCE(n.title, ''),
		COALESCE(n.content, ''), COALESCE(n.permission, ''), n.createdAt, COALESCE(n.lastchangeAt, n.updatedAt, n.createdAt),
		o.profile, o.email, e.profile, e.email
		FROM Notes n
		LEFT JOIN Users o ON o.id = n.ownerId
		LEFT JOIN Users e ON e.id = n.lastchangeuserId
		WHERE n.deletedAt IS NULL
		ORDER BY n.createdAt, n.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var n Note
		var created, updated sql.NullTime
		var op, oe, ep, ee sql.NullString
		if err := rows.Scan(&n.ID, &n.ShortID, &n.Alias, &n.Title, &n.Content, &n.Permission, &created, &updated,
			&op, &oe, &ep, &ee); err != nil {
			return nil, err
		}
		if created.Valid {
			n.CreatedAt = created.Time.UTC()
		}
		n.UpdatedAt = n.CreatedAt
		if updated.Valid {
			n.UpdatedAt = updated.Time.UTC()
		}
		n.Owner = displayName(op, oe)
		n.LastEditor = displayName(ep, ee)
		out = append(out, n)
	}
	return out, rows.Err()
}
