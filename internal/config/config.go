// Package config reads the service configuration from the environment. Every variable is
// documented in `.env.dist`.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the whole service configuration.
type Config struct {
	// Bus
	NATSURL       string
	NATSCreds     string
	Instance      string
	ServiceName   string
	ServiceKeyB64 string // DOCS_SERVICE_USER_KEY_B64: Zitadel JSON key of the machine user, base64
	ServiceKeyFile string
	ZitadelIssuer  string
	ZitadelProject string
	CoreTimeout    time.Duration

	// Jiku integration
	APIUserID    string
	WebURL       string
	PublicURL    string

	// Storage
	DatabaseURL     string
	S3Endpoint      string
	S3PublicEndpoint string
	S3Region        string
	S3Bucket        string
	S3AccessKey     string
	S3SecretKey     string
	S3PathStyle     bool

	// Secrets
	SecretKey []byte

	// Operation
	HTTPAddr         string
	LogLevel         string
	IndexerEnabled   bool
	JanitorEnabled   bool
	OutboxEnabled    bool
	RelationReviewHour int
}

// FromEnv reads the configuration. It does not validate: each subcommand validates what it needs.
func FromEnv() Config {
	return Config{
		NATSURL:        env("NATS_URL", "nats://localhost:4222"),
		NATSCreds:      env("NATS_CREDS", ""),
		Instance:       env("NATS_INSTANCE", "dev"),
		ServiceName:    env("DOCS_SERVICE_NAME", "jiku-docs"),
		ServiceKeyB64:  env("DOCS_SERVICE_USER_KEY_B64", ""),
		ServiceKeyFile: env("DOCS_SERVICE_USER_KEY_FILE", ""),
		ZitadelIssuer:  env("ZITADEL_ISSUER_URL", "https://id.grava.io"),
		ZitadelProject: env("ZITADEL_PROJECT_ID", ""),
		CoreTimeout:    envDuration("CORE_TIMEOUT", 10*time.Second),

		APIUserID: env("JIKU_API_USER_ID", ""),
		WebURL:    strings.TrimRight(env("JIKU_WEB_URL", ""), "/"),
		PublicURL: strings.TrimRight(env("JIKU_PUBLIC_URL", ""), "/"),

		DatabaseURL:      env("DATABASE_URL", ""),
		S3Endpoint:       env("DOCS_S3_ENDPOINT", ""),
		S3PublicEndpoint: env("DOCS_S3_PUBLIC_ENDPOINT", ""),
		S3Region:         env("DOCS_S3_REGION", "us-east-1"),
		S3Bucket:         env("DOCS_S3_BUCKET", "jiku-docs-content"),
		S3AccessKey:      env("DOCS_S3_ACCESS_KEY", ""),
		S3SecretKey:      env("DOCS_S3_SECRET_KEY", ""),
		S3PathStyle:      envBool("DOCS_S3_FORCE_PATH_STYLE", true),

		SecretKey: decodeKey(env("DOCS_SECRET_KEY", "")),

		HTTPAddr:           env("DOCS_HTTP_ADDR", ":8080"),
		LogLevel:           env("LOG_LEVEL", "info"),
		IndexerEnabled:     envBool("DOCS_INDEXER_ENABLED", true),
		JanitorEnabled:     envBool("DOCS_JANITOR_ENABLED", true),
		OutboxEnabled:      envBool("DOCS_OUTBOX_ENABLED", true),
		RelationReviewHour: envInt("DOCS_RELATION_REVIEW_HOUR_UTC", 6),
	}
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envBool(key string, def bool) bool {
	v := env(key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(key string, def int) int {
	v := env(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	v := env(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func decodeKey(s string) []byte {
	if s == "" {
		return nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b
		}
	}
	return nil
}

// ServiceKey returns the bytes of the Zitadel JSON key, from base64 or from a file.
func (c Config) ServiceKey() ([]byte, error) {
	if c.ServiceKeyB64 != "" {
		b, err := base64.StdEncoding.DecodeString(c.ServiceKeyB64)
		if err != nil {
			return nil, fmt.Errorf("DOCS_SERVICE_USER_KEY_B64 is not valid base64: %w", err)
		}
		return b, nil
	}
	if c.ServiceKeyFile != "" {
		return os.ReadFile(c.ServiceKeyFile)
	}
	return nil, errors.New("set DOCS_SERVICE_USER_KEY_B64 (or DOCS_SERVICE_USER_KEY_FILE) to the Zitadel JSON key of the jiku_docs machine user")
}

// ValidateDatabase checks what any database-touching subcommand needs.
func (c Config) ValidateDatabase() error {
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	return nil
}

// ValidateStorage checks the content bucket settings.
func (c Config) ValidateStorage() error {
	var missing []string
	if c.S3Endpoint == "" {
		missing = append(missing, "DOCS_S3_ENDPOINT")
	}
	if c.S3Bucket == "" {
		missing = append(missing, "DOCS_S3_BUCKET")
	}
	if c.S3AccessKey == "" {
		missing = append(missing, "DOCS_S3_ACCESS_KEY")
	}
	if c.S3SecretKey == "" {
		missing = append(missing, "DOCS_S3_SECRET_KEY")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing storage settings: %s", strings.Join(missing, ", "))
	}
	if _, err := url.Parse(c.S3Endpoint); err != nil {
		return fmt.Errorf("DOCS_S3_ENDPOINT: %w", err)
	}
	return nil
}

// ValidateServe checks everything `serve` needs.
func (c Config) ValidateServe() error {
	if err := c.ValidateDatabase(); err != nil {
		return err
	}
	if err := c.ValidateStorage(); err != nil {
		return err
	}
	var missing []string
	if c.APIUserID == "" {
		missing = append(missing, "JIKU_API_USER_ID")
	}
	if c.WebURL == "" {
		missing = append(missing, "JIKU_WEB_URL")
	}
	if c.PublicURL == "" {
		missing = append(missing, "JIKU_PUBLIC_URL")
	}
	if c.NATSCreds == "" {
		missing = append(missing, "NATS_CREDS")
	}
	if c.ZitadelProject == "" {
		missing = append(missing, "ZITADEL_PROJECT_ID")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing settings: %s", strings.Join(missing, ", "))
	}
	if len(c.SecretKey) < 32 {
		return errors.New("DOCS_SECRET_KEY must be at least 32 random bytes, base64 (openssl rand -base64 32)")
	}
	if c.Instance != "dev" && c.Instance != "prod" {
		return fmt.Errorf("NATS_INSTANCE must be dev or prod, got %q", c.Instance)
	}
	return nil
}
