// Package testutil runs the integration environment: real PostgreSQL 16 and MinIO in
// containers (names prefixed `jiku-docs-test-`, random host ports: nothing collides with the
// jiku-local stack), an embedded nats-server with JetStream (no auth-callout: the subject is the
// identity, like in production after the callout), and a double of core that answers
// `jiku-queries` / `files.*` with core's strictness.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Shared containers of a test binary.
type Containers struct {
	PostgresDSN   string // admin DSN (database "test")
	PostgresHost  string
	PostgresPort  string
	MinIOEndpoint string
	MinIOUser     string
	MinIOPassword string
	terminate     []func()
}

var (
	once    sync.Once
	shared  *Containers
	initErr error
)

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Shared starts (once) the containers of this test binary.
func Shared() (*Containers, error) {
	once.Do(func() { shared, initErr = start(context.Background()) })
	return shared, initErr
}

// Terminate stops the shared containers (call from TestMain).
func Terminate() {
	if shared != nil {
		for _, f := range shared.terminate {
			f()
		}
	}
}

func start(ctx context.Context) (*Containers, error) {
	c := &Containers{MinIOUser: "jikudocstest", MinIOPassword: "jikudocstest-secret"}
	suffix := randHex(4)
	pg, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Name:         "jiku-docs-test-pg-" + suffix,
			Image:        "postgres:16-alpine",
			Env:          map[string]string{"POSTGRES_USER": "test", "POSTGRES_PASSWORD": "test", "POSTGRES_DB": "test"},
			Cmd:          []string{"postgres", "-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "max_connections=500"},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor:   wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		return nil, fmt.Errorf("postgres container: %w", err)
	}
	c.terminate = append(c.terminate, func() { _ = pg.Terminate(context.Background()) })
	host, err := pg.Host(ctx)
	if err != nil {
		return nil, err
	}
	port, err := pg.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return nil, err
	}
	c.PostgresHost, c.PostgresPort = host, port.Port()
	c.PostgresDSN = fmt.Sprintf("postgres://test:test@%s:%s/test?sslmode=disable", host, port.Port())

	mc, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Name:         "jiku-docs-test-minio-" + suffix,
			Image:        "minio/minio:RELEASE.2024-09-13T20-26-02Z",
			Cmd:          []string{"server", "/data"},
			Env:          map[string]string{"MINIO_ROOT_USER": c.MinIOUser, "MINIO_ROOT_PASSWORD": c.MinIOPassword},
			ExposedPorts: []string{"9000/tcp"},
			WaitingFor:   wait.ForHTTP("/minio/health/live").WithPort("9000/tcp").WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		Terminate()
		return nil, fmt.Errorf("minio container: %w", err)
	}
	c.terminate = append(c.terminate, func() { _ = mc.Terminate(context.Background()) })
	mhost, err := mc.Host(ctx)
	if err != nil {
		return nil, err
	}
	mport, err := mc.MappedPort(ctx, "9000/tcp")
	if err != nil {
		return nil, err
	}
	c.MinIOEndpoint = fmt.Sprintf("http://%s:%s", mhost, mport.Port())
	return c, nil
}

// NewDatabase creates an empty database and returns its DSN.
func (c *Containers) NewDatabase(ctx context.Context) (string, error) {
	conn, err := pgx.Connect(ctx, c.PostgresDSN)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	name := "jd_" + randHex(6)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		return "", err
	}
	return fmt.Sprintf("postgres://test:test@%s:%s/%s?sslmode=disable", c.PostgresHost, c.PostgresPort, name), nil
}

// SkipIntegration reports whether the integration tests were explicitly disabled.
func SkipIntegration() bool { return os.Getenv("JIKU_DOCS_SKIP_INTEGRATION") == "1" }
