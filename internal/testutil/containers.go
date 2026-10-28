// Package testutil runs the integration environment: real PostgreSQL 16 and Garage (S3) in
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
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Shared containers of a test binary.
type Containers struct {
	PostgresDSN  string // admin DSN (database "test")
	PostgresHost string
	PostgresPort string
	S3Endpoint   string
	S3AccessKey  string
	S3SecretKey  string
	terminate    []func()
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
	// Garage key format: "GK" + 12 bytes in hex, and a 32-byte secret in hex.
	c := &Containers{S3AccessKey: "GK" + randHex(12), S3SecretKey: randHex(32)}
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

	s3, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Name:  "jiku-docs-test-garage-" + suffix,
			Image: "dxflrs/garage:v2.5.0",
			// --single-node lays out the cluster by itself and --default-access-key creates the key of
			// the environment (with permission to create buckets): no admin commands needed.
			Cmd: []string{"/garage", "server", "--single-node", "--default-access-key"},
			Env: map[string]string{
				"GARAGE_RPC_SECRET":         randHex(32),
				"GARAGE_DEFAULT_ACCESS_KEY": c.S3AccessKey,
				"GARAGE_DEFAULT_SECRET_KEY": c.S3SecretKey,
			},
			Files: []testcontainers.ContainerFile{{
				Reader: strings.NewReader(garageConfig), ContainerFilePath: "/etc/garage.toml", FileMode: 0o644,
			}},
			ExposedPorts: []string{"3900/tcp", "3903/tcp"},
			WaitingFor:   wait.ForHTTP("/health").WithPort("3903/tcp").WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		Terminate()
		return nil, fmt.Errorf("garage container: %w", err)
	}
	c.terminate = append(c.terminate, func() { _ = s3.Terminate(context.Background()) })
	s3host, err := s3.Host(ctx)
	if err != nil {
		return nil, err
	}
	s3port, err := s3.MappedPort(ctx, "3900/tcp")
	if err != nil {
		return nil, err
	}
	c.S3Endpoint = fmt.Sprintf("http://%s:%s", s3host, s3port.Port())
	return c, nil
}

// garageConfig is the single-node configuration of the test Garage. The region is the one the
// tests sign with; the RPC secret comes from GARAGE_RPC_SECRET.
const garageConfig = `metadata_dir = "/var/lib/garage/meta"
data_dir = "/var/lib/garage/data"
db_engine = "sqlite"
replication_factor = 1
rpc_bind_addr = "[::]:3901"
rpc_public_addr = "127.0.0.1:3901"

[s3_api]
s3_region = "us-east-1"
api_bind_addr = "[::]:3900"

[admin]
api_bind_addr = "[::]:3903"
`

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
