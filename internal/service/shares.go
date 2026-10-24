package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gravadigital/jiku-docs/internal/acl"
	"github.com/gravadigital/jiku-docs/internal/markdown"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/store"
	"github.com/gravadigital/jiku-docs/internal/validate"
)

// Share rules (section 4).
const (
	ShareDefaultTTL = 30 * 24 * time.Hour
	ShareMaxTTL     = 180 * 24 * time.Hour
	ShareRetryTTL   = 5 * time.Minute
)

func (s *Service) deriveKey(purpose string) []byte {
	m := hmac.New(sha256.New, s.Set.SecretKey)
	m.Write([]byte("jiku-docs/" + purpose))
	return m.Sum(nil)
}

func (s *Service) sealToken(token string) ([]byte, error) {
	block, err := aes.NewCipher(s.deriveKey("share-retry"))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(token), nil), nil
}

func (s *Service) openToken(sealed []byte) (string, error) {
	block, err := aes.NewCipher(s.deriveKey("share-retry"))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", errors.New("sealed token too short")
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	return string(plain), err
}

// HashIP is the HMAC of a client IP with a service key: accesses are auditable without storing IPs.
func (s *Service) HashIP(ip string) []byte {
	m := hmac.New(sha256.New, s.deriveKey("ip-hash"))
	m.Write([]byte(ip))
	return m.Sum(nil)
}

func tokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func newShareToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "jds_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// ShareURL is the public link of a token (D-5).
func (s *Service) ShareURL(token string) string { return s.Set.PublicURL + "/p/docs/" + token }

type shareRow struct {
	ID, DocumentID, Mode, CreatedBy string
	Version                         *int
	IncludeAttachments              bool
	Note                            *string
	CreatedAt, ExpiresAt            time.Time
	RevokedAt                       *time.Time
	RevokedBy, RevokeReason         *string
	AccessCount                     int64
	LastAccessAt                    *time.Time
	RetryCipher, RequestHash        []byte
}

const shareCols = `id, document_id, mode, created_by, version, include_attachments, note, created_at, expires_at, revoked_at,
	revoked_by, revoke_reason, access_count, last_access_at, token_retry_cipher, request_hash`

func scanShare(row pgx.Row) (*shareRow, error) {
	r := &shareRow{}
	err := row.Scan(&r.ID, &r.DocumentID, &r.Mode, &r.CreatedBy, &r.Version, &r.IncludeAttachments, &r.Note, &r.CreatedAt,
		&r.ExpiresAt, &r.RevokedAt, &r.RevokedBy, &r.RevokeReason, &r.AccessCount, &r.LastAccessAt, &r.RetryCipher, &r.RequestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func (r *shareRow) status(now time.Time) string {
	switch {
	case r.RevokedAt != nil:
		return "revoked"
	case !r.ExpiresAt.After(now):
		return "expired"
	}
	return "active"
}

func timePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return protocol.FormatTime(*t)
}

func (r *shareRow) view(now time.Time) map[string]any {
	var version, note, revokedBy, reason any
	if r.Version != nil {
		version = *r.Version
	}
	if r.Note != nil {
		note = *r.Note
	}
	if r.RevokedBy != nil {
		revokedBy = *r.RevokedBy
	}
	if r.RevokeReason != nil {
		reason = *r.RevokeReason
	}
	return map[string]any{
		"id": r.ID, "documentId": r.DocumentID, "mode": r.Mode, "version": version, "includeAttachments": r.IncludeAttachments,
		"note": note, "status": r.status(now), "createdBy": r.CreatedBy, "createdAt": protocol.FormatTime(r.CreatedAt),
		"expiresAt": protocol.FormatTime(r.ExpiresAt), "revokedAt": timePtr(r.RevokedAt), "revokedBy": revokedBy,
		"revokeReason": reason, "accessCount": r.AccessCount, "lastAccessAt": timePtr(r.LastAccessAt),
	}
}

func (r *shareRow) eventSnapshot() map[string]any {
	var version any
	if r.Version != nil {
		version = *r.Version
	}
	return map[string]any{"mode": r.Mode, "version": version, "includeAttachments": r.IncludeAttachments,
		"expiresAt": protocol.FormatTime(r.ExpiresAt), "createdBy": r.CreatedBy, "revokedAt": timePtr(r.RevokedAt)}
}

func shareEntity(r *shareRow, folderID string) map[string]any {
	return map[string]any{"type": "share", "id": r.ID, "documentId": r.DocumentID, "folderId": folderID}
}

func (s *Service) sharesNew(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	o := validate.ParseObject(c.Payload, "", "id", "documentId", "mode", "version", "expiresAt", "includeAttachments", "note", "acknowledgeSecretWarning")
	id, _ := o.ULID("id", true)
	docID, _ := o.ULID("documentId", true)
	mode, _ := o.Enum("mode", true, "latest", "pinned")
	version, hasVersion := o.Int("version", false, 1, 1<<31-1)
	expiresAt, _ := o.Time("expiresAt", true)
	include := o.Bool("includeAttachments", true)
	note, hasNote := o.String("note", false, validate.MaxLen(300))
	ack := o.Bool("acknowledgeSecretWarning", false)
	if err := o.Err(); err != nil {
		return nil, err
	}
	if mode == "pinned" && !hasVersion {
		return nil, protocol.InvalidField("version", nil, nil, "version es obligatorio con mode pinned")
	}
	if mode == "latest" && hasVersion {
		return nil, protocol.InvalidField("version", version, nil, "version solo se acepta con mode pinned")
	}
	expiresAt = expiresAt.Truncate(time.Millisecond)
	reqHash := func() []byte {
		b, _ := json.Marshal([]any{docID, mode, version, protocol.FormatTime(expiresAt), include, note, c.Caller.ID})
		sum := sha256.Sum256(b)
		return sum[:]
	}()
	var out map[string]any
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		d, err := getDoc(ctx, tx, docID, true)
		if err != nil {
			return err
		}
		if d == nil || d.DeletedAt != nil {
			return protocol.E(protocol.CodeDocumentNotFound)
		}
		tree, err := treeTx(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := docWritePerm(tree, tree.For(c.Caller.Principal()), d, acl.Manage); err != nil {
			return err
		}
		now := s.now()
		existing, err := scanShare(tx.QueryRow(ctx, `SELECT `+shareCols+` FROM shares WHERE id = $1`, id))
		if err != nil {
			return err
		}
		if existing != nil {
			if bytes.Equal(existing.RequestHash, reqHash) && existing.RetryCipher != nil && now.Sub(existing.CreatedAt) < ShareRetryTTL {
				token, err := s.openToken(existing.RetryCipher)
				if err == nil {
					out = map[string]any{"id": id, "token": token, "url": s.ShareURL(token), "expiresAt": protocol.FormatTime(existing.ExpiresAt)}
					return nil
				}
			}
			return protocol.Ef(protocol.CodeAlreadyExists, "Ya existe un link con ese id: si se perdió, revocalo y creá otro")
		}
		if tree.SharingBlocked(d.FolderID) {
			return protocol.E(protocol.CodeSharingBlocked)
		}
		if !expiresAt.After(now) || expiresAt.After(now.Add(ShareMaxTTL+time.Minute)) {
			return protocol.E(protocol.CodeShareExpiryInvalid).With("maxDays", 180)
		}
		if mode == "pinned" && int(version) > d.Version {
			return protocol.Ef(protocol.CodeDocumentNotFound, "La versión %d no existe", version).With("version", version)
		}
		if d.SecretWarning && !ack {
			return protocol.E(protocol.CodeSecretWarningNotAcked)
		}
		token, err := newShareToken()
		if err != nil {
			return err
		}
		sealed, err := s.sealToken(token)
		if err != nil {
			return err
		}
		var vptr *int
		if mode == "pinned" {
			v := int(version)
			vptr = &v
		}
		var noteVal *string
		if hasNote {
			noteVal = &note
		}
		if _, err := tx.Exec(ctx, `INSERT INTO shares (id, document_id, token_hash, token_retry_cipher, request_hash, mode, version,
			include_attachments, note, created_by, created_at, expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			id, docID, tokenHash(token), sealed, reqHash, mode, vptr, include, noteVal, c.Caller.ID, now, expiresAt); err != nil {
			return err
		}
		row := &shareRow{ID: id, DocumentID: docID, Mode: mode, Version: vptr, IncludeAttachments: include, CreatedBy: c.Caller.ID,
			CreatedAt: now, ExpiresAt: expiresAt}
		em := s.emitter(tx, c.Caller, c.CorrelationID)
		if err := em.emit(ctx, event{Type: "share.created", Entity: shareEntity(row, d.FolderID), Snapshot: row.eventSnapshot(),
			FolderID: d.FolderID, DocumentID: docID}); err != nil {
			return err
		}
		out = map[string]any{"id": id, "token": token, "url": s.ShareURL(token), "expiresAt": protocol.FormatTime(expiresAt)}
		return nil
	})
	return out, err
}

func (s *Service) sharesRevoke(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	o := validate.ParseObject(c.Payload, "")
	if err := o.Err(); err != nil {
		return nil, err
	}
	id := c.Params["shareId"]
	var out map[string]any
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		r, err := scanShare(tx.QueryRow(ctx, `SELECT `+shareCols+` FROM shares WHERE id = $1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if r == nil {
			return protocol.E(protocol.CodeShareNotFound)
		}
		d, err := getDoc(ctx, tx, r.DocumentID, false)
		if err != nil {
			return err
		}
		tree, err := treeTx(ctx, tx)
		if err != nil {
			return err
		}
		perm := tree.For(c.Caller.Principal()).Permission(d.FolderID)
		if perm < acl.Read {
			return protocol.E(protocol.CodeShareNotFound)
		}
		if perm < acl.Manage && r.CreatedBy != c.Caller.ID {
			return protocol.E(protocol.CodePermissionDenied)
		}
		if r.RevokedAt != nil {
			out = map[string]any{"id": id, "revokedAt": protocol.FormatTime(*r.RevokedAt)}
			return nil
		}
		now := s.now()
		if _, err := tx.Exec(ctx, `UPDATE shares SET revoked_at = $2, revoked_by = $3, revoke_reason = 'manual',
			token_retry_cipher = NULL WHERE id = $1`, id, now, c.Caller.ID); err != nil {
			return err
		}
		r.RevokedAt = &now
		em := s.emitter(tx, c.Caller, c.CorrelationID)
		if err := em.emit(ctx, event{Type: "share.revoked", Entity: shareEntity(r, d.FolderID), Snapshot: r.eventSnapshot(),
			Changes: map[string]any{"reason": "manual"}, FolderID: d.FolderID, DocumentID: d.ID}); err != nil {
			return err
		}
		out = map[string]any{"id": id, "revokedAt": protocol.FormatTime(now)}
		return nil
	})
	return out, err
}

// revokeDocumentShares revokes the live links of a deleted document. Restoring it does not
// bring them back.
func (s *Service) revokeDocumentShares(ctx context.Context, tx pgx.Tx, em *emitter, d *docRow, by string, now time.Time) error {
	rows, err := tx.Query(ctx, `SELECT `+shareCols+` FROM shares WHERE document_id = $1 AND revoked_at IS NULL FOR UPDATE`, d.ID)
	if err != nil {
		return err
	}
	var list []*shareRow
	for rows.Next() {
		r, err := scanShare(rows)
		if err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	rows.Close()
	for _, r := range list {
		if _, err := tx.Exec(ctx, `UPDATE shares SET revoked_at = $2, revoked_by = $3, revoke_reason = 'document_deleted',
			token_retry_cipher = NULL WHERE id = $1`, r.ID, now, by); err != nil {
			return err
		}
		r.RevokedAt = &now
		if err := em.emit(ctx, event{Type: "share.revoked", Entity: shareEntity(r, d.FolderID), Snapshot: r.eventSnapshot(),
			Changes: map[string]any{"reason": "document_deleted"}, FolderID: d.FolderID, DocumentID: d.ID}); err != nil {
			return err
		}
	}
	return nil
}

// ---- public resolution (api only) -----------------------------------------------------------

var tokenRe = regexp.MustCompile(`^jds_[A-Za-z0-9_-]{43}$`)

type shareContext struct {
	share *shareRow
	doc   *docRow
}

// resolveShare validates a token. Any invalid link answers the same share_not_found: it does
// not reveal whether it existed, expired or was revoked.
func (s *Service) resolveShare(ctx context.Context, token string) (*shareContext, error) {
	r, err := scanShare(s.DB.QueryRow(ctx, `SELECT `+shareCols+` FROM shares WHERE token_hash = $1`, tokenHash(token)))
	if err != nil {
		return nil, err
	}
	now := s.now()
	if r == nil || r.status(now) != "active" {
		return nil, protocol.E(protocol.CodeShareNotFound)
	}
	d, err := getDoc(ctx, s.DB, r.DocumentID, false)
	if err != nil {
		return nil, err
	}
	if d == nil || d.DeletedAt != nil {
		return nil, protocol.E(protocol.CodeShareNotFound)
	}
	tree, err := s.Tree(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := tree.Get(d.FolderID); !ok || tree.SharingBlocked(d.FolderID) {
		return nil, protocol.E(protocol.CodeShareNotFound)
	}
	return &shareContext{share: r, doc: d}, nil
}

type clientInfo struct {
	ip, userAgent string
}

func readClient(o *validate.Object) clientInfo {
	var ci clientInfo
	if co, ok := o.Object("client", false, "ip", "userAgent"); ok {
		ci.ip, _ = co.String("ip", false, validate.MaxLen(100))
		ci.userAgent, _ = co.String("userAgent", false, validate.MaxLen(2000))
		if err := co.Err(); err != nil {
			var pe *protocol.Error
			if errors.As(err, &pe) {
				o.Fail(pe)
			}
		}
	}
	if r := []rune(ci.userAgent); len(r) > 200 {
		ci.userAgent = string(r[:200])
	}
	return ci
}

func (s *Service) recordAccess(ctx context.Context, shareID, kind, attachmentID string, ci clientInfo) error {
	now := s.now()
	return store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO share_accesses (share_id, accessed_at, kind, attachment_id, ip_hash, user_agent)
			VALUES ($1,$2,$3,NULLIF($4,''),$5,NULLIF($6,''))`, shareID, now, kind, attachmentID, s.HashIP(ci.ip), ci.userAgent); err != nil {
			return err
		}
		if kind == "document" {
			_, err := tx.Exec(ctx, `UPDATE shares SET access_count = access_count + 1, last_access_at = $2 WHERE id = $1`, shareID, now)
			return err
		}
		return nil
	})
}

func (s *Service) sharesResolve(ctx context.Context, c *Call) (any, error) {
	if !c.Caller.ViaAPI || c.Caller.ID != s.Set.APIUserID {
		return nil, protocol.E(protocol.CodeCallerNotAuthorized)
	}
	o := validate.ParseObject(c.Payload, "", "token", "client")
	token, _ := o.String("token", true, validate.MaxLen(200))
	ci := readClient(o)
	if err := o.Err(); err != nil {
		return nil, err
	}
	if !tokenRe.MatchString(token) {
		return nil, protocol.E(protocol.CodeShareNotFound) // a malformed link is just an invalid link
	}
	sc, err := s.resolveShare(ctx, token)
	if err != nil {
		return nil, err
	}
	version := sc.doc.Version
	if sc.share.Mode == "pinned" && sc.share.Version != nil {
		version = *sc.share.Version
	}
	var key, title string
	if err := s.DB.QueryRow(ctx, `SELECT storage_key, title FROM document_versions WHERE document_id = $1 AND version = $2`,
		sc.doc.ID, version).Scan(&key, &title); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, protocol.E(protocol.CodeShareNotFound)
		}
		return nil, err
	}
	raw, err := s.Blob.Get(ctx, key, MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	atts := map[string]*attRow{}
	attList := []map[string]any{}
	if sc.share.IncludeAttachments {
		rows, err := s.DB.Query(ctx, `SELECT id, file_name, mime_type FROM attachments WHERE document_id = $1 AND status = 'ready' ORDER BY created_at, id`, sc.doc.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			a := &attRow{}
			if err := rows.Scan(&a.ID, &a.FileName, &a.MimeType); err != nil {
				rows.Close()
				return nil, err
			}
			atts[a.ID] = a
			attList = append(attList, map[string]any{"id": a.ID, "fileName": a.FileName, "mimeType": a.MimeType})
		}
		rows.Close()
	}
	html, err := markdown.RenderShare(string(raw), markdown.ShareRender{AttachmentURL: func(id string) string {
		if _, ok := atts[id]; ok {
			return "/p/docs/" + token + "/attachments/" + id
		}
		return ""
	}})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"shareId": sc.share.ID, "documentId": sc.doc.ID, "title": title, "version": version, "attachments": attList}
	if len(html) > MaxInlineBytes {
		sum := sha256.Sum256([]byte(html))
		hkey := "renders/" + sc.share.ID + "/" + itoa(version) + "-" + base64.RawURLEncoding.EncodeToString(sum[:8]) + ".html"
		if err := s.Blob.Put(ctx, hkey, []byte(html), "text/html; charset=utf-8"); err != nil {
			return nil, err
		}
		u, err := s.Blob.PresignGet(ctx, hkey, PresignTTL, "text/html; charset=utf-8")
		if err != nil {
			return nil, err
		}
		out["htmlUrl"] = u
	} else {
		out["html"] = html
	}
	if err := s.recordAccess(ctx, sc.share.ID, "document", "", ci); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) sharesAttachment(ctx context.Context, c *Call) (any, error) {
	if !c.Caller.ViaAPI || c.Caller.ID != s.Set.APIUserID {
		return nil, protocol.E(protocol.CodeCallerNotAuthorized)
	}
	o := validate.ParseObject(c.Payload, "", "token", "attachmentId", "client")
	token, _ := o.String("token", true, validate.MaxLen(200))
	attID, _ := o.ULID("attachmentId", true)
	ci := readClient(o)
	if err := o.Err(); err != nil {
		return nil, err
	}
	if !tokenRe.MatchString(token) {
		return nil, protocol.E(protocol.CodeShareNotFound)
	}
	sc, err := s.resolveShare(ctx, token)
	if err != nil {
		return nil, err
	}
	if !sc.share.IncludeAttachments {
		return nil, protocol.E(protocol.CodeShareNotFound)
	}
	a, err := getAttachment(ctx, s.DB, attID, false)
	if err != nil {
		return nil, err
	}
	if a == nil || a.Status != "ready" || a.DocumentID != sc.doc.ID {
		return nil, protocol.E(protocol.CodeShareNotFound)
	}
	ticket, err := s.Core.RequestDownload(ctx, a.CoreFileID, "inline")
	if err != nil {
		if protocol.CodeOf(mapCoreFileError(err)) == protocol.CodeAttachmentNotFound {
			return nil, protocol.E(protocol.CodeShareNotFound)
		}
		return nil, err
	}
	if err := s.recordAccess(ctx, sc.share.ID, "attachment", a.ID, ci); err != nil {
		return nil, err
	}
	return map[string]any{"downloadUrl": ticket.DownloadURL, "expiresIn": ticket.ExpiresIn}, nil
}
