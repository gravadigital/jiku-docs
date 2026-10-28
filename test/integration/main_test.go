// Package integration runs jiku-docs end to end against real PostgreSQL 16 and Garage (S3)
// containers, an embedded nats-server with JetStream and a strict double of core. Every request,
// reply, event and notification is validated against docs/apis/.
//
// Requires Docker. JIKU_DOCS_SKIP_INTEGRATION=1 skips the package.
package integration

import (
	"os"
	"testing"

	"github.com/gravadigital/jiku-docs/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Terminate()
	os.Exit(code)
}
