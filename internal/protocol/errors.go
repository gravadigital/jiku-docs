// Package protocol holds the wire conventions of jiku-docs: the reply envelope, the error
// catalog, the subject grammar and the identifier formats. It copies the conventions of
// Jiku's core (`@jiku/nats-protocol`) so a caller that already integrated core has nothing
// new to learn.
package protocol

import "fmt"

// Error codes. The catalog is open inside v1: callers treat an unknown code as a generic error.
const (
	CodeInvalidFields               = "invalid_fields"
	CodeInvalidCursor               = "invalid_cursor"
	CodeDocumentNotFound            = "document_not_found"
	CodeFolderNotFound              = "folder_not_found"
	CodeAttachmentNotFound          = "attachment_not_found"
	CodePermissionDenied            = "permission_denied"
	CodeVersionConflict             = "version_conflict"
	CodeVersionPruned               = "version_pruned"
	CodeDocumentLocked              = "document_locked"
	CodeLockNotHeld                 = "lock_not_held"
	CodeAlreadyExists               = "already_exists"
	CodeFileNameInvalid             = "file_name_invalid"
	CodeFrontmatterInvalid          = "frontmatter_invalid"
	CodeInvalidRelation             = "invalid_relation"
	CodeContentTooLarge             = "content_too_large"
	CodeChecksumMismatch            = "checksum_mismatch"
	CodeUploadNotFound              = "upload_not_found"
	CodeFolderNotEmpty              = "folder_not_empty"
	CodeAttachmentTypeNotAllowed    = "attachment_type_not_allowed"
	CodeAttachmentTooLarge          = "attachment_too_large"
	CodeQueryTimeout                = "query_timeout"
	CodeUnknownCaller               = "unknown_caller"
	CodeCallerNotAuthorized         = "caller_not_authorized"
	CodeUpstreamUnavailable         = "upstream_unavailable"
	CodeInternalError               = "internal_error"
	CodeShareNotFound               = "share_not_found"
	CodeSharingBlocked              = "sharing_blocked"
	CodeShareExpiryInvalid          = "share_expiry_invalid"
	CodeSecretWarningNotAcked       = "secret_warning_not_acknowledged"
	CodeSemanticSearchNotConfigured = "semantic_search_not_configured"
	CodeAttachmentsNotConfigured    = "attachments_not_configured"
)

// WarningPossibleSecret is the non-blocking warning a write returns when the secret detector
// flags the content.
const WarningPossibleSecret = "possible_secret_detected"

// messages are the default texts for the interface. Callers decide by code, never by
// message.
var messages = map[string]string{
	CodeInvalidFields:               "The request has invalid or undeclared fields",
	CodeInvalidCursor:               "The cursor is not valid for this query: take it from the previous reply and reuse it with the same filter and the same sort",
	CodeDocumentNotFound:            "The document does not exist or is not visible",
	CodeFolderNotFound:              "The folder does not exist or is not visible",
	CodeAttachmentNotFound:          "The attachment does not exist or is not visible",
	CodePermissionDenied:            "You do not have permission for this operation",
	CodeVersionConflict:             "The base version does not match the current version of the document",
	CodeVersionPruned:               "The requested version was pruned: use one of the nearest kept versions",
	CodeDocumentLocked:              "Another session is editing the document",
	CodeLockNotHeld:                 "The session does not hold the lock of the document",
	CodeAlreadyExists:               "A resource with that identifier or name already exists",
	CodeFileNameInvalid:             "The file name is not valid: it must end in .md and cannot contain / or \\",
	CodeFrontmatterInvalid:          "The frontmatter of the document is not valid",
	CodeInvalidRelation:             "The document references a Jiku entity that does not exist or is not visible",
	CodeContentTooLarge:             "The content exceeds the allowed size",
	CodeChecksumMismatch:            "The declared checksum does not match the content",
	CodeUploadNotFound:              "The upload does not exist, expired or was already used",
	CodeFolderNotEmpty:              "The folder is not empty",
	CodeAttachmentTypeNotAllowed:    "The file type is not allowed",
	CodeAttachmentTooLarge:          "The file exceeds the allowed size",
	CodeQueryTimeout:                "The query took too long: narrow the filter",
	CodeUnknownCaller:               "The identity is not registered in Jiku: reconnect",
	CodeCallerNotAuthorized:         "The identity has no access to this operation",
	CodeUpstreamUnavailable:         "Jiku did not respond: retry in a few seconds",
	CodeInternalError:               "Internal error",
	CodeShareNotFound:               "The share link does not exist or is no longer valid",
	CodeSharingBlocked:              "A restricted document does not allow share links",
	CodeShareExpiryInvalid:          "The expiry must be in the future and at most 180 days ahead",
	CodeSecretWarningNotAcked:       "The document may contain secrets: confirm before sharing it",
	CodeSemanticSearchNotConfigured: "Semantic search is not available: use mode text",
	CodeAttachmentsNotConfigured:    "Attachments are not configured",
}

// Error is a failure that travels back to the caller as a `failure` envelope.
type Error struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return e.Code
}

// E builds an error with the default message of its code.
func E(code string) *Error {
	return &Error{Code: code, Message: messages[code]}
}

// Ef builds an error with a specific message.
func Ef(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// With adds a detail and returns the same error, for chaining.
func (e *Error) With(key string, value any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[key] = value
	return e
}

// InvalidField is the canonical `invalid_fields` failure: `{ field, value, allowed }`.
func InvalidField(field string, value any, allowed []string, format string, args ...any) *Error {
	e := Ef(CodeInvalidFields, format, args...)
	e.With("field", field)
	if value != nil {
		e.With("value", value)
	}
	if allowed != nil {
		e.With("allowed", allowed)
	}
	return e
}

// DefaultMessage returns the default text of a code.
func DefaultMessage(code string) string { return messages[code] }
