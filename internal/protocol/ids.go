package protocol

import (
	"crypto/rand"
	"regexp"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

var ulidRe = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// IsULID reports whether s is an uppercase Crockford ULID, valid as a subject token.
func IsULID(s string) bool { return ulidRe.MatchString(s) }

var (
	ulidMu      sync.Mutex
	ulidEntropy = ulid.Monotonic(rand.Reader, 0)
)

// NewULID returns a new monotonic ULID.
func NewULID() string {
	ulidMu.Lock()
	defer ulidMu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now()), ulidEntropy).String()
}

// RootFolderID is the well-known id of the root folder `/`, created by the first migration.
const RootFolderID = "00000000000000000000000000"

// FormatTime renders a timestamp the way every reply and event does: ISO 8601, UTC, milliseconds.
func FormatTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// Now is the current time truncated to milliseconds, so what is stored is exactly what is shown
// (and what a keyset cursor compares against).
func Now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }
