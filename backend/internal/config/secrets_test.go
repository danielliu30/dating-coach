package config

import (
	"strings"
	"testing"
)

func TestLoadProductionSecrets(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_SECRET", "q8Zr1vN0bT4xKp7LmW2yHs9cJd6Fg3Ae")
	t.Setenv("DATABASE_URL", "postgres://app:db-test-password@postgres/db")
	t.Setenv("POSTGRES_PASSWORD", "db-test-password")
	t.Setenv("RABBITMQ_URL", "amqp://app:mq-test-password@rabbitmq:5672/")
	t.Setenv("SMTP_PASSWORD", "smtp-test-password")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() = %v, want valid production config", err)
	}

	for _, tc := range []struct{ name, key, value string }{
		{"example JWT", "JWT_SECRET", "change-me-to-a-long-random-string"},
		{"Compose JWT", "JWT_SECRET", "dev-secret-change-me"},
		{"short JWT", "JWT_SECRET", "q8Zr1vN0bT4xKp7LmW2yHs9cJd6Fg3A"},
		{"repeated JWT", "JWT_SECRET", strings.Repeat("ab", 20)},
		{"Postgres URL", "DATABASE_URL", "postgres://app:datingcoach@postgres/db"},
		{"Postgres DSN", "DATABASE_URL", "host=postgres user=app password=datingcoach"},
		{"Postgres env", "POSTGRES_PASSWORD", "datingcoach"},
		{"RabbitMQ guests", "RABBITMQ_URL", "amqp://guest:guest@rabbitmq:5672/"},
		{"RabbitMQ implicit guests", "RABBITMQ_URL", "amqp://rabbitmq:5672/"},
		{"RabbitMQ custom user with default password", "RABBITMQ_URL", "amqp://app:guest@rabbitmq:5672/"},
		{"Resend example", "SMTP_PASSWORD", "re_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},
		{"SendGrid example", "SMTP_PASSWORD", "SG.xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("Load() must return an error naming %s", tc.key)
			}
			if strings.Contains(err.Error(), tc.value) {
				t.Fatalf("Load() disclosed the value of %s", tc.key)
			}
		})
	}
}

func TestLoadDevelopmentAllowsExampleSecrets(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("JWT_SECRET", "change-me-to-a-long-random-string")
	t.Setenv("DATABASE_URL", "postgres://datingcoach:datingcoach@localhost:5432/datingcoach")
	t.Setenv("POSTGRES_PASSWORD", "datingcoach")
	t.Setenv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	t.Setenv("SMTP_PASSWORD", "re_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() = %v, want valid development config", err)
	}
}
