package config

import (
	"strings"
	"testing"
)

// exampleConfig mirrors the secrets .env.example and docker-compose.yml ship.
func exampleConfig(env string) *Config {
	return &Config{
		Env:              env,
		DatabaseURL:      "postgres://datingcoach:datingcoach@localhost:5432/datingcoach?sslmode=disable",
		PostgresPassword: "datingcoach",
		RabbitMQURL:      "amqp://guest:guest@localhost:5672/",
		JWTSecret:        "change-me-to-a-long-random-string",
	}
}

// secureConfig is a production config that ValidateSecrets must accept.
func secureConfig() *Config {
	return &Config{
		Env:                 "production",
		DatabaseURL:         "postgres://datingcoach:s3cure-db-pass@postgres:5432/datingcoach?sslmode=disable",
		PostgresPassword:    "s3cure-db-pass",
		RabbitMQURL:         "amqp://app:s3cure-mq-pass@rabbitmq:5672/",
		JWTSecret:           "q8Zr1vN0bT4xKp7LmW2yHs9cJd6Fg3Ae",
		SMTPPassword:        "re_live_9f8e7d6c5b4a",
		StripeSecretKey:     "sk_live_51HabcDEF",
		StripeWebhookSecret: "whsec_8YtR2pQ",
	}
}

func TestValidateSecretsDevelopmentAcceptsExampleDefaults(t *testing.T) {
	cfg := exampleConfig("development")
	cfg.SMTPPassword = "re_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	if err := cfg.ValidateSecrets(); err != nil {
		t.Fatalf("ValidateSecrets() in development = %v, want nil", err)
	}
}

func TestValidateSecretsAcceptsRealSecrets(t *testing.T) {
	if err := secureConfig().ValidateSecrets(); err != nil {
		t.Fatalf("ValidateSecrets() = %v, want nil", err)
	}
}

func TestValidateSecretsRejectsExampleDefaults(t *testing.T) {
	cases := map[string]struct {
		key    string
		secret string
		mutate func(*Config)
	}{
		"env example jwt secret": {"JWT_SECRET", "change-me-to-a-long-random-string", func(c *Config) { c.JWTSecret = "change-me-to-a-long-random-string" }},
		"compose jwt secret":     {"JWT_SECRET", "dev-secret-change-me", func(c *Config) { c.JWTSecret = "dev-secret-change-me" }},
		"short jwt secret":       {"JWT_SECRET", "Zr1vN0bT4xKp7LmW2yHs9cJd6Fg3Ae", func(c *Config) { c.JWTSecret = "Zr1vN0bT4xKp7LmW2yHs9cJd6Fg3Ae" }},
		"low entropy jwt secret": {"JWT_SECRET", strings.Repeat("ab", 20), func(c *Config) { c.JWTSecret = strings.Repeat("ab", 20) }},
		"example database password": {"DATABASE_URL", "datingcoach@", func(c *Config) {
			c.DatabaseURL = "postgres://datingcoach:datingcoach@postgres:5432/datingcoach?sslmode=disable"
		}},
		"example database password in dsn": {"DATABASE_URL", "password=datingcoach", func(c *Config) {
			c.DatabaseURL = "host=postgres user=app password=datingcoach dbname=datingcoach"
		}},
		"example postgres password": {"POSTGRES_PASSWORD", "datingcoach", func(c *Config) { c.PostgresPassword = "datingcoach" }},
		"guest rabbitmq":            {"RABBITMQ_URL", "guest:guest", func(c *Config) { c.RabbitMQURL = "amqp://guest:guest@rabbitmq:5672/" }},
		"rabbitmq without credentials": {"RABBITMQ_URL", "guest", func(c *Config) {
			c.RabbitMQURL = "amqp://rabbitmq:5672/"
		}},
		"resend placeholder":   {"SMTP_PASSWORD", "re_xxxx", func(c *Config) { c.SMTPPassword = "re_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" }},
		"sendgrid placeholder": {"SMTP_PASSWORD", "SG.xxxx", func(c *Config) { c.SMTPPassword = "SG.xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" }},
		"stripe key placeholder": {"STRIPE_SECRET_KEY", "change-me", func(c *Config) {
			c.StripeSecretKey = "sk_live_change-me"
		}},
		"stripe webhook placeholder": {"STRIPE_WEBHOOK_SECRET", "whsec_xxxx", func(c *Config) {
			c.StripeWebhookSecret = "whsec_xxxxxxxxxxxxxxxx"
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := secureConfig()
			tc.mutate(cfg)
			err := cfg.ValidateSecrets()
			if err == nil {
				t.Fatalf("ValidateSecrets() = nil, want an error naming %s", tc.key)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("ValidateSecrets() error = %v, want it to name %s", err, tc.key)
			}
			if strings.Contains(err.Error(), tc.secret) {
				t.Fatalf("ValidateSecrets() error = %v leaks the secret value", err)
			}
		})
	}
}

// TestValidateSecretsRejectsEveryExampleDefault checks a straight copy of
// .env.example with APP_ENV changed reports every offending variable at once.
func TestValidateSecretsRejectsEveryExampleDefault(t *testing.T) {
	for _, env := range []string{"production", "staging", "Development", ""} {
		err := exampleConfig(env).ValidateSecrets()
		if err == nil {
			t.Fatalf("APP_ENV=%q: ValidateSecrets() = nil, want an error", env)
		}
		for _, key := range []string{"JWT_SECRET", "DATABASE_URL", "POSTGRES_PASSWORD", "RABBITMQ_URL"} {
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("APP_ENV=%q: error = %v, want it to name %s", env, err, key)
			}
		}
		if strings.Contains(err.Error(), "change-me-to-a-long-random-string") || strings.Contains(err.Error(), "guest:guest@") {
			t.Fatalf("APP_ENV=%q: error = %v leaks a secret value", env, err)
		}
	}
}

func TestLoadReadsPostgresPassword(t *testing.T) {
	setRequired(t)
	t.Setenv("POSTGRES_PASSWORD", "datingcoach")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PostgresPassword != "datingcoach" {
		t.Fatalf("PostgresPassword = %q, want datingcoach", cfg.PostgresPassword)
	}
}
