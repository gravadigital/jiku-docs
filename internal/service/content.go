package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/gravadigital/jiku-docs/internal/blob"
	"github.com/gravadigital/jiku-docs/internal/caller"
	"github.com/gravadigital/jiku-docs/internal/markdown"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/secrets"
	"github.com/gravadigital/jiku-docs/internal/store"
	"github.com/gravadigital/jiku-docs/internal/validate"
)

// Content limits (section 5).
const (
	MaxInlineBytes   = 256 * 1024
	MaxDocumentBytes = 5 * 1024 * 1024
	PresignTTL       = 300 * time.Second
	UploadTTL        = 24 * time.Hour
)

var (
	checksumRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	fileNameRe = regexp.MustCompile(`^[^/\\]{1,200}\.md$`)
)

// Checksum is the `sha256:<hex>` of a byte slice.
func Checksum(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func checksumHex(c string) string { return strings.TrimPrefix(c, "sha256:") }

// ValidFileName applies the file name rule: `^[^/\\]{1,200}\.md$`, no control characters, no
// surrounding spaces.
func ValidFileName(name string) bool {
	if !fileNameRe.MatchString(name) || name != strings.TrimSpace(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func readFileName(o *validate.Object, key string, required bool) (string, error) {
	if !o.Has(key) && !required {
		return "", nil
	}
	name, ok := o.String(key, required)
	if err := o.Err(); err != nil {
		return "", err
	}
	if !ok || !ValidFileName(name) {
		return "", protocol.E(protocol.CodeFileNameInvalid).With("field", key).With("value", name)
	}
	return name, nil
}

func storageKey(docID string, version int, checksum string) string {
	return fmt.Sprintf("docs/%s/%d-%s.md", docID, version, checksumHex(checksum))
}

// contentInput is the content of a write, as received.
type contentInput struct {
	raw      []byte
	uploadID string // consumed in the transaction when set
}

// readContent reads exactly one of `content` (inline, up to 256 KiB) or `contentUploadId`, and
// verifies the declared checksum against the received bytes.
func (s *Service) readContent(ctx context.Context, c *caller.Caller, o *validate.Object) (*contentInput, error) {
	hasContent, hasUpload := o.Has("content") && !o.IsNull("content"), o.Has("contentUploadId") && !o.IsNull("contentUploadId")
	checksum, _ := o.String("checksum", true, validate.Pattern(checksumRe))
	if err := o.Err(); err != nil {
		return nil, err
	}
	if hasContent == hasUpload {
		return nil, protocol.InvalidField("content", nil, []string{"content", "contentUploadId"},
			"Mandá exactamente uno de content o contentUploadId")
	}
	in := &contentInput{}
	if hasContent {
		text, _ := o.String("content", true)
		if err := o.Err(); err != nil {
			return nil, err
		}
		if len(text) > MaxInlineBytes {
			return nil, protocol.Ef(protocol.CodeContentTooLarge, "El contenido inline admite hasta 256 KiB: usá documents.request-content-upload").
				With("maxInlineBytes", MaxInlineBytes)
		}
		in.raw = []byte(text)
	} else {
		id, _ := o.ULID("contentUploadId", true)
		if err := o.Err(); err != nil {
			return nil, err
		}
		var key, declared string
		err := s.DB.QueryRow(ctx, `SELECT storage_key, checksum FROM content_uploads WHERE id = $1 AND requested_by = $2
			AND consumed_at IS NULL AND expires_at > $3`, id, c.ID, s.now()).Scan(&key, &declared)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, protocol.E(protocol.CodeUploadNotFound)
		}
		if err != nil {
			return nil, err
		}
		b, err := s.Blob.Get(ctx, key, MaxDocumentBytes)
		switch {
		case errors.Is(err, blob.ErrNotFound):
			return nil, protocol.Ef(protocol.CodeUploadNotFound, "Todavía no se subió el contenido a la URL")
		case errors.Is(err, blob.ErrTooLarge):
			return nil, protocol.E(protocol.CodeContentTooLarge).With("maxBytes", MaxDocumentBytes)
		case err != nil:
			return nil, err
		}
		if Checksum(b) != declared {
			return nil, protocol.Ef(protocol.CodeChecksumMismatch, "El contenido subido no coincide con el checksum declarado al pedir la subida")
		}
		in.raw, in.uploadID = b, id
	}
	if got := Checksum(in.raw); got != checksum {
		return nil, protocol.E(protocol.CodeChecksumMismatch).With("computed", got)
	}
	return in, nil
}

// consumeUpload marks a content upload as used; a second use is upload_not_found.
func (s *Service) consumeUpload(ctx context.Context, tx pgx.Tx, in *contentInput) error {
	if in.uploadID == "" {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE content_uploads SET consumed_at = $2 WHERE id = $1 AND consumed_at IS NULL`, in.uploadID, s.now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return protocol.E(protocol.CodeUploadNotFound)
	}
	return nil
}

// prepared is a validated content, ready to be stored.
type prepared struct {
	content   []byte
	checksum  string
	size      int
	fm        markdown.Frontmatter
	title     string
	relations []relRow
	relView   markdown.Relations
	topics    []string
	findings  []secrets.Finding
}

// prepare normalises (UTF-8, LF), parses the frontmatter, validates the relations that are new
// against core and runs the secret detector.
func (s *Service) prepare(ctx context.Context, raw []byte, fileName string, existing []relRow) (*prepared, error) {
	if len(raw) > MaxDocumentBytes {
		return nil, protocol.E(protocol.CodeContentTooLarge).With("maxBytes", MaxDocumentBytes)
	}
	if !utf8.Valid(raw) {
		return nil, protocol.InvalidField("content", nil, nil, "El contenido tiene que ser texto UTF-8")
	}
	content := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	text := string(content)
	fm, body, ferr := markdown.Parse(text)
	if ferr != nil {
		return nil, protocol.Ef(protocol.CodeFrontmatterInvalid, "%s", ferr.Error()).
			With("line", ferr.Line).With("field", ferr.Field).With("reason", ferr.Reason)
	}
	p := &prepared{
		content: content, checksum: Checksum(content), size: len(content), fm: fm,
		title: markdown.Title(fm, body, fileName), relView: fm.Relations, topics: fm.Topics,
		findings: secrets.Scan(text),
	}
	known := map[string]*int64{}
	for _, r := range existing {
		known[r.EntityType+":"+itoa(int(r.EntityID))] = r.ProjectID
	}
	for _, k := range markdown.RelationKinds {
		ids := fm.Relations.Get(k.Key)
		var missing []int64
		for _, id := range ids {
			if pid, ok := known[k.EntityType+":"+itoa(int(id))]; ok {
				p.relations = append(p.relations, relRow{EntityType: k.EntityType, EntityID: id, ProjectID: pid})
			} else {
				missing = append(missing, id)
			}
		}
		if len(missing) == 0 {
			continue
		}
		found, err := s.Core.Entities(ctx, k.EntityType, missing)
		if err != nil {
			return nil, err
		}
		for _, id := range missing {
			e, ok := found[id]
			if !ok {
				return nil, protocol.Ef(protocol.CodeInvalidRelation, "La entidad %s %d no existe o no es visible", k.EntityType, id).
					With("entityType", k.EntityType).With("entityId", id)
			}
			var pid *int64
			if e.ProjectID != 0 {
				v := e.ProjectID
				pid = &v
			}
			p.relations = append(p.relations, relRow{EntityType: k.EntityType, EntityID: id, ProjectID: pid})
		}
	}
	return p, nil
}

func (p *prepared) warnings() []string {
	if len(p.findings) == 0 {
		return nil
	}
	return []string{protocol.WarningPossibleSecret}
}

// writeMeta replaces relations and topics of a document.
func writeMeta(ctx context.Context, tx pgx.Tx, docID string, p *prepared) error {
	if _, err := tx.Exec(ctx, `DELETE FROM document_relations WHERE document_id = $1`, docID); err != nil {
		return err
	}
	for _, r := range p.relations {
		if _, err := tx.Exec(ctx, `INSERT INTO document_relations (document_id, entity_type, entity_id, project_id) VALUES ($1,$2,$3,$4)`,
			docID, r.EntityType, r.EntityID, r.ProjectID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM document_topics WHERE document_id = $1`, docID); err != nil {
		return err
	}
	for _, t := range p.topics {
		if _, err := tx.Exec(ctx, `INSERT INTO document_topics (document_id, topic) VALUES ($1,$2)`, docID, t); err != nil {
			return err
		}
	}
	return nil
}

// Kinds of version (docs/compactacion-historial-v1.md, section 4). documents.move creates no
// version, so there is no kind for it.
const (
	KindCreate  = "create"
	KindUpdate  = "update"
	KindRestore = "restore"
)

type versionInput struct {
	docID   string
	kind    string
	version int
	key     string
	p       *prepared
	author  string
	// authorName is the name of the author when the version is written (P-1).
	authorName *string
	via        string
	sessionID  string
	message    string
	at         time.Time
}

// versionProtected is the permanent protection of a version, decided on insert: creations,
// restorations, versions with a message and versions written by a service are never compacted.
// Agent versions are not protected by their origin: they follow the block rule like human ones.
func versionProtected(kind, via, message string) bool {
	return kind == KindCreate || kind == KindRestore || message != "" || via == "service"
}

func insertVersion(ctx context.Context, tx pgx.Tx, v versionInput) error {
	if v.kind == "" {
		v.kind = KindUpdate
	}
	_, err := tx.Exec(ctx, `INSERT INTO document_versions (document_id, version, storage_key, checksum, size_bytes, title, author, via,
		session_id, message, created_at, kind, protected, author_name) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),NULLIF($10,''),$11,$12,$13,$14)`,
		v.docID, v.version, v.key, v.p.checksum, v.p.size, v.p.title, v.author, v.via, v.sessionID, v.message, v.at,
		v.kind, versionProtected(v.kind, v.via, v.message), v.authorName)
	return err
}

// lockFolderShared protects against a concurrent delete of the target folder.
func lockFolderShared(ctx context.Context, tx store.Querier, folderID string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT TRUE FROM folders WHERE id = $1 AND deleted_at IS NULL FOR SHARE`, folderID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return ok, err
}
