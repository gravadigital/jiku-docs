package config

import "testing"

func TestFromEnvSDKAliases(t *testing.T) {
	t.Setenv("JIKU_SERVERS", "nats://nats:4222")
	t.Setenv("JIKU_CREDS", "/run/secrets/nats.creds")
	t.Setenv("JIKU_INSTANCE", "prod")
	t.Setenv("JIKU_KEY_FILE", "/run/secrets/key.json")
	t.Setenv("JIKU_ISSUER", "https://id.example.com")
	t.Setenv("JIKU_PROJECT_ID", "123")

	c := FromEnv()
	if c.NATSURL != "nats://nats:4222" || c.NATSCreds != "/run/secrets/nats.creds" || c.Instance != "prod" ||
		c.ServiceKeyFile != "/run/secrets/key.json" || c.ZitadelIssuer != "https://id.example.com" ||
		c.ZitadelProject != "123" {
		t.Fatalf("aliases not applied: %+v", c)
	}
}

func TestFromEnvOwnNameWins(t *testing.T) {
	t.Setenv("NATS_INSTANCE", "dev")
	t.Setenv("JIKU_INSTANCE", "prod")
	t.Setenv("ZITADEL_PROJECT_ID", "own")
	t.Setenv("JIKU_PROJECT_ID", "alias")

	c := FromEnv()
	if c.Instance != "dev" || c.ZitadelProject != "own" {
		t.Fatalf("own name must win: instance=%q project=%q", c.Instance, c.ZitadelProject)
	}
}
