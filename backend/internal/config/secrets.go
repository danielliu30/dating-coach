package config

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	amqp "github.com/rabbitmq/amqp091-go"
)

// DevelopmentEnv is the only APP_ENV in which the shipped example secrets are
// accepted.
const DevelopmentEnv = "development"

// MinJWTSecretBytes is the shortest JWT_SECRET accepted outside development:
// HS256 keys should be at least as long as the 256-bit hash.
const MinJWTSecretBytes = 32

// minJWTSecretDistinctBytes rejects long but trivially guessable secrets such
// as a repeated character.
const minJWTSecretDistinctBytes = 10

// exampleJWTSecrets are the JWT_SECRET defaults shipped in .env.example and
// docker-compose.yml.
var exampleJWTSecrets = []string{"change-me-to-a-long-random-string", "dev-secret-change-me"}

// examplePostgresPassword is the POSTGRES_PASSWORD default from .env.example
// and docker-compose.yml.
const examplePostgresPassword = "datingcoach"

// ValidateSecrets refuses the .env.example defaults whenever APP_ENV is not
// development, so a copied example file cannot reach production. Errors name
// the offending variable, never its value.
func (c *Config) ValidateSecrets() error {
	if c.Env == DevelopmentEnv {
		return nil
	}

	var bad []string
	switch {
	case isPlaceholder(c.JWTSecret) || containsFold(exampleJWTSecrets, c.JWTSecret):
		bad = append(bad, "JWT_SECRET is the example value")
	case len(c.JWTSecret) < MinJWTSecretBytes:
		bad = append(bad, fmt.Sprintf("JWT_SECRET is shorter than %d bytes", MinJWTSecretBytes))
	case distinctBytes(c.JWTSecret) < minJWTSecretDistinctBytes:
		bad = append(bad, "JWT_SECRET has too little entropy")
	}

	if pg, err := pgconn.ParseConfig(c.DatabaseURL); err == nil && pg.Password == examplePostgresPassword {
		bad = append(bad, "DATABASE_URL uses the example Postgres password")
	}
	if c.PostgresPassword == examplePostgresPassword {
		bad = append(bad, "POSTGRES_PASSWORD is the example value")
	}

	// amqp091 falls back to guest:guest when the URL carries no credentials.
	if uri, err := amqp.ParseURI(c.RabbitMQURL); err == nil && uri.Username == "guest" && uri.Password == "guest" {
		bad = append(bad, "RABBITMQ_URL uses the RabbitMQ default user")
	}

	for _, s := range []struct{ key, value string }{
		{"SMTP_PASSWORD", c.SMTPPassword},
		{"STRIPE_SECRET_KEY", c.StripeSecretKey},
		{"STRIPE_WEBHOOK_SECRET", c.StripeWebhookSecret},
	} {
		if isPlaceholder(s.value) {
			bad = append(bad, s.key+" is a placeholder value")
		}
	}

	if len(bad) > 0 {
		return fmt.Errorf("refusing to start with APP_ENV=%s and example secrets (set real values, or APP_ENV=%s for local use): %s",
			c.Env, DevelopmentEnv, strings.Join(bad, "; "))
	}
	return nil
}

// isPlaceholder spots the shapes .env.example uses for secrets it cannot ship:
// "change-me..." strings and the "re_xxxx…" / "SG.xxxx…" provider examples.
func isPlaceholder(v string) bool {
	v = strings.ToLower(v)
	return strings.Contains(v, "change-me") || strings.Contains(v, "xxxxxxxx")
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

func distinctBytes(s string) int {
	var seen [256]bool
	n := 0
	for i := 0; i < len(s); i++ {
		if !seen[s[i]] {
			seen[s[i]] = true
			n++
		}
	}
	return n
}
