package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gravadigital/jiku-docs/internal/acl"
	"github.com/gravadigital/jiku-docs/internal/coreapi"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/store"
	"github.com/gravadigital/jiku-docs/internal/validate"
)

// Attachments are delegated to core's file service (D-3): jiku-docs keeps the mapping
// attachment -> core file and the permissions; core keeps the bytes and the type/size policy.

type attRow struct {
	ID, DocumentID, FileName, MimeType, Checksum, Status, UploadedBy string
	Size                                                             int64
	CoreFileID                                                       int64
	CreatedAt                                                        time.Time
}

func getAttachment(ctx context.Context, q store.Querier, id string, forUpdate bool) (*attRow, error) {
	sql := `SELECT id, document_id, file_name, mime_type, checksum, status, uploaded_by, size_bytes, core_file_id, created_at
		FROM attachments WHERE id = $1`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	a := &attRow{}
	err := q.QueryRow(ctx, sql, id).Scan(&a.ID, &a.DocumentID, &a.FileName, &a.MimeType, &a.Checksum, &a.Status,
		&a.UploadedBy, &a.Size, &a.CoreFileID, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

// AttachmentLink is the stable link pasted in the markdown (D-5).
func (s *Service) AttachmentLink(id string) string { return s.Set.WebURL + "/docs/attachments/" + id }

func (s *Service) attachmentView(a *attRow) map[string]any {
	return map[string]any{"id": a.ID, "documentId": a.DocumentID, "fileName": a.FileName, "mimeType": a.MimeType,
		"sizeBytes": a.Size, "link": s.AttachmentLink(a.ID), "uploadedBy": a.UploadedBy, "createdAt": protocol.FormatTime(a.CreatedAt)}
}

func mapCoreFileError(err error) error {
	switch {
	case coreapi.IsCode(err, "file_type_not_allowed"):
		return protocol.E(protocol.CodeAttachmentTypeNotAllowed)
	case coreapi.IsCode(err, "file_too_large"):
		return protocol.E(protocol.CodeAttachmentTooLarge)
	case coreapi.IsCode(err, "file_not_found"):
		return protocol.E(protocol.CodeAttachmentNotFound)
	}
	return err
}

// AttachmentUpload is the input of attachments.request-upload.
type AttachmentUpload struct {
	ID, DocumentID, FileName, MimeType, Checksum string
	Size                                         int64
}

func (s *Service) attachmentsRequestUpload(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	o := validate.ParseObject(c.Payload, "", "id", "documentId", "fileName", "mimeType", "fileSize", "checksum")
	var in AttachmentUpload
	in.ID, _ = o.ULID("id", true)
	in.DocumentID, _ = o.ULID("documentId", true)
	in.FileName, _ = o.String("fileName", true, validate.MinLen(1), validate.MaxLen(255))
	in.MimeType, _ = o.String("mimeType", true, validate.MinLen(1), validate.MaxLen(127))
	in.Size, _ = o.Int("fileSize", true, 1, 1<<50)
	in.Checksum, _ = o.String("checksum", true, validate.Pattern(checksumRe))
	if err := o.Err(); err != nil {
		return nil, err
	}
	return s.RequestAttachmentUpload(ctx, c, in)
}

// RequestAttachmentUpload asks core for an upload ticket and records the pending attachment.
func (s *Service) RequestAttachmentUpload(ctx context.Context, c *Call, in AttachmentUpload) (map[string]any, error) {
	d, err := getDoc(ctx, s.DB, in.DocumentID, false)
	if err != nil {
		return nil, err
	}
	if d == nil || d.DeletedAt != nil {
		return nil, protocol.E(protocol.CodeDocumentNotFound)
	}
	if _, err := docWritePerm(c.Caller, d, acl.Write); err != nil {
		return nil, err
	}
	existing, err := getAttachment(ctx, s.DB, in.ID, false)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		same := existing.DocumentID == in.DocumentID && existing.FileName == in.FileName && existing.MimeType == in.MimeType &&
			existing.Size == in.Size && existing.Checksum == in.Checksum && existing.UploadedBy == c.Caller.ID
		if !same || existing.Status != "pending" {
			return nil, protocol.Ef(protocol.CodeAlreadyExists, "An attachment with that id already exists")
		}
	}
	ticket, err := s.Core.RequestUpload(ctx, in.FileName, in.MimeType, in.Size, checksumHex(in.Checksum))
	if err != nil {
		return nil, mapCoreFileError(err)
	}
	now := s.now()
	_, err = s.DB.Exec(ctx, `INSERT INTO attachments (id, document_id, file_name, mime_type, size_bytes, checksum, core_file_id,
		status, uploaded_by, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,'pending',$8,$9)
		ON CONFLICT (id) DO UPDATE SET core_file_id = EXCLUDED.core_file_id, created_at = EXCLUDED.created_at
		WHERE attachments.status = 'pending'`,
		in.ID, in.DocumentID, in.FileName, in.MimeType, in.Size, in.Checksum, ticket.ID, c.Caller.ID, now)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": in.ID, "uploadUrl": ticket.UploadURL, "expiresIn": ticket.ExpiresIn, "link": s.AttachmentLink(in.ID)}, nil
}

func (s *Service) attachmentsConfirm(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	o := validate.ParseObject(c.Payload, "")
	if err := o.Err(); err != nil {
		return nil, err
	}
	return s.ConfirmAttachment(ctx, c, c.Params["attachmentId"])
}

// ConfirmAttachment downloads the uploaded bytes from core's storage, verifies the declared
// SHA-256 (core does not) and marks the attachment ready.
func (s *Service) ConfirmAttachment(ctx context.Context, c *Call, id string) (map[string]any, error) {
	a, err := getAttachment(ctx, s.DB, id, false)
	if err != nil {
		return nil, err
	}
	if a == nil || a.Status == "deleted" {
		return nil, protocol.E(protocol.CodeAttachmentNotFound)
	}
	v, err := s.viewer(ctx, c.Caller)
	if err != nil {
		return nil, err
	}
	if _, _, err := s.visibleDoc(ctx, s.DB, v, a.DocumentID, false); err != nil {
		return nil, protocol.E(protocol.CodeAttachmentNotFound)
	}
	if a.UploadedBy != c.Caller.ID {
		return nil, protocol.Ef(protocol.CodePermissionDenied, "Only whoever requested the upload can confirm it")
	}
	ready := map[string]any{"id": id, "status": "ready"}
	if a.Status == "ready" {
		return ready, nil
	}
	ticket, err := s.Core.RequestDownload(ctx, a.CoreFileID, "attachment")
	if err != nil {
		if coreapi.IsCode(err, "file_not_found") {
			return nil, protocol.E(protocol.CodeUploadNotFound)
		}
		return nil, err
	}
	sum, n, err := s.hashURL(ctx, ticket.DownloadURL, a.Size)
	if err != nil {
		return nil, err
	}
	if n != a.Size || "sha256:"+sum != a.Checksum {
		return nil, protocol.E(protocol.CodeChecksumMismatch).With("sizeBytes", n)
	}
	err = store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		cur, err := getAttachment(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur == nil || cur.Status == "deleted" {
			return protocol.E(protocol.CodeAttachmentNotFound)
		}
		if cur.Status == "ready" {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE attachments SET status = 'ready', confirmed_at = $2 WHERE id = $1`, id, s.now()); err != nil {
			return err
		}
		d, err := getDoc(ctx, tx, a.DocumentID, false)
		if err != nil {
			return err
		}
		em := s.emitter(tx, c.Caller, c.CorrelationID)
		return em.emit(ctx, event{Type: "attachment.created",
			Entity:   map[string]any{"type": "attachment", "id": id, "documentId": d.ID, "folderId": d.FolderID},
			Snapshot: map[string]any{"fileName": a.FileName, "mimeType": a.MimeType, "sizeBytes": a.Size, "link": s.AttachmentLink(id)},
			FolderID: d.FolderID, DocumentID: d.ID})
	})
	if err != nil {
		return nil, err
	}
	return ready, nil
}

// hashURL streams an object and returns its SHA-256 and size, reading at most max+1 bytes.
func (s *Service) hashURL(ctx context.Context, u string, max int64) (string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("%w: downloading the attachment: %w", coreapi.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return "", 0, protocol.Ef(protocol.CodeUploadNotFound, "The file has not been uploaded to the URL yet")
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("%w: storage answered %d", coreapi.ErrUnavailable, resp.StatusCode)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(resp.Body, max+1))
	if err != nil {
		return "", 0, fmt.Errorf("%w: reading the attachment: %w", coreapi.ErrUnavailable, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func (s *Service) attachmentsRequestDownload(ctx context.Context, c *Call) (any, error) {
	o := validate.ParseObject(c.Payload, "")
	if err := o.Err(); err != nil {
		return nil, err
	}
	id := c.Params["attachmentId"]
	a, err := getAttachment(ctx, s.DB, id, false)
	if err != nil {
		return nil, err
	}
	if a == nil || a.Status != "ready" {
		return nil, protocol.E(protocol.CodeAttachmentNotFound)
	}
	v, err := s.viewer(ctx, c.Caller)
	if err != nil {
		return nil, err
	}
	if _, _, err := s.visibleDoc(ctx, s.DB, v, a.DocumentID, false); err != nil {
		return nil, protocol.E(protocol.CodeAttachmentNotFound)
	}
	ticket, err := s.Core.RequestDownload(ctx, a.CoreFileID, "inline")
	if err != nil {
		return nil, mapCoreFileError(err)
	}
	return map[string]any{"id": id, "downloadUrl": ticket.DownloadURL, "expiresIn": ticket.ExpiresIn}, nil
}

func (s *Service) attachmentsDelete(ctx context.Context, c *Call) (any, error) {
	if err := requireWriter(c.Caller); err != nil {
		return nil, err
	}
	o := validate.ParseObject(c.Payload, "")
	if err := o.Err(); err != nil {
		return nil, err
	}
	id := c.Params["attachmentId"]
	err := store.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		a, err := getAttachment(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if a == nil {
			return protocol.E(protocol.CodeAttachmentNotFound)
		}
		d, err := getDoc(ctx, tx, a.DocumentID, false)
		if err != nil {
			return err
		}
		if _, err := docWritePerm(c.Caller, d, acl.Write); err != nil {
			if protocol.CodeOf(err) == protocol.CodeDocumentNotFound {
				return protocol.E(protocol.CodeAttachmentNotFound)
			}
			return err
		}
		if a.Status == "deleted" {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE attachments SET status = 'deleted', deleted_at = $2 WHERE id = $1`, id, s.now()); err != nil {
			return err
		}
		if a.Status != "ready" {
			return nil // a pending attachment never announced itself
		}
		em := s.emitter(tx, c.Caller, c.CorrelationID)
		return em.emit(ctx, event{Type: "attachment.deleted",
			Entity:   map[string]any{"type": "attachment", "id": id, "documentId": d.ID, "folderId": d.FolderID},
			Snapshot: map[string]any{"fileName": a.FileName, "mimeType": a.MimeType, "sizeBytes": a.Size, "link": s.AttachmentLink(id)},
			FolderID: d.FolderID, DocumentID: d.ID})
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id}, nil
}
