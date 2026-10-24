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

// messages are the default Spanish texts for the interface. Callers decide by code, never by
// message.
var messages = map[string]string{
	CodeInvalidFields:               "El pedido tiene campos inválidos o no declarados",
	CodeInvalidCursor:               "El cursor no es válido para esta consulta: se obtiene de la respuesta anterior y hay que reusarlo con el mismo filtro y el mismo orden",
	CodeDocumentNotFound:            "El documento no existe o no es visible",
	CodeFolderNotFound:              "La carpeta no existe o no es visible",
	CodeAttachmentNotFound:          "El adjunto no existe o no es visible",
	CodePermissionDenied:            "No tenés permiso para esta operación",
	CodeVersionConflict:             "La versión base no coincide con la versión actual del documento",
	CodeDocumentLocked:              "Otra sesión está editando el documento",
	CodeLockNotHeld:                 "La sesión no tiene el lock del documento",
	CodeAlreadyExists:               "Ya existe un recurso con ese identificador o nombre",
	CodeFileNameInvalid:             "El nombre de archivo no es válido: tiene que terminar en .md y no puede contener / ni \\",
	CodeFrontmatterInvalid:          "El frontmatter del documento no es válido",
	CodeInvalidRelation:             "El documento referencia una entidad de Jiku inexistente o no visible",
	CodeContentTooLarge:             "El contenido supera el tamaño permitido",
	CodeChecksumMismatch:            "El checksum declarado no coincide con el contenido",
	CodeUploadNotFound:              "La subida no existe, venció o ya fue usada",
	CodeFolderNotEmpty:              "La carpeta tiene contenido",
	CodeAttachmentTypeNotAllowed:    "El tipo de archivo no está permitido",
	CodeAttachmentTooLarge:          "El archivo supera el tamaño permitido",
	CodeQueryTimeout:                "La consulta tardó demasiado: achicá el filtro",
	CodeUnknownCaller:               "La identidad no está registrada en Jiku: reconectá",
	CodeCallerNotAuthorized:         "La identidad no tiene acceso a esta operación",
	CodeUpstreamUnavailable:         "Jiku no respondió: reintentá en unos segundos",
	CodeInternalError:               "Error interno",
	CodeShareNotFound:               "El link no existe o ya no es válido",
	CodeSharingBlocked:              "La carpeta no permite links compartidos",
	CodeShareExpiryInvalid:          "El vencimiento tiene que ser futuro y como máximo a 180 días",
	CodeSecretWarningNotAcked:       "El documento puede contener secretos: confirmá antes de compartirlo",
	CodeSemanticSearchNotConfigured: "La búsqueda semántica no está disponible: usá mode text",
	CodeAttachmentsNotConfigured:    "Los adjuntos no están configurados",
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

// DefaultMessage returns the Spanish text of a code.
func DefaultMessage(code string) string { return messages[code] }
