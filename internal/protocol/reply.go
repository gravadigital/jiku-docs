package protocol

import (
	"encoding/json"
	"errors"
)

// Reply is the envelope every endpoint answers with, identical to core's.
type Reply struct {
	Status       string         `json:"status"`
	Data         any            `json:"data,omitempty"`
	ErrorCode    string         `json:"errorCode,omitempty"`
	ErrorMessage string         `json:"errorMessage,omitempty"`
	ErrorDetails map[string]any `json:"errorDetails,omitempty"`
}

const (
	StatusSuccess = "success"
	StatusFailure = "failure"
)

// Success wraps data in a success envelope.
func Success(data any) Reply { return Reply{Status: StatusSuccess, Data: data} }

// Failure turns any error into a failure envelope. Errors that are not *Error become
// internal_error, without leaking their text.
func Failure(err error) Reply {
	var pe *Error
	if !errors.As(err, &pe) {
		pe = E(CodeInternalError)
	}
	msg := pe.Message
	if msg == "" {
		msg = DefaultMessage(pe.Code)
	}
	return Reply{Status: StatusFailure, ErrorCode: pe.Code, ErrorMessage: msg, ErrorDetails: pe.Details}
}

// Encode marshals the envelope. Marshalling our own types cannot fail in practice; if it does,
// the caller still gets a valid envelope.
func (r Reply) Encode() []byte {
	b, err := json.Marshal(r)
	if err != nil {
		b, _ = json.Marshal(Failure(E(CodeInternalError)))
	}
	return b
}

// CodeOf returns the error code of err, or "" for nil, or internal_error for foreign errors.
func CodeOf(err error) string {
	if err == nil {
		return ""
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return CodeInternalError
}
