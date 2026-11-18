package config

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	amqp "github.com/rabbitmq/amqp091-go"
)

func (c *Config) validateSecrets(postgresPassword string) error {
	var bad []string
	switch c.JWTSecret {
	case "change-me-to-a-long-random-string", "dev-secret-change-me":
		bad = append(bad, "JWT_SECRET is a shipped default")
	default:
		if len(c.JWTSecret) < 32 || !hasEnoughDistinctBytes(c.JWTSecret) {
			bad = append(bad, "JWT_SECRET must be at least 32 bytes with at least 10 distinct bytes")
		}
	}
	if pg, err := pgconn.ParseConfig(c.DatabaseURL); err == nil && pg.Password == "datingcoach" {
		bad = append(bad, "DATABASE_URL uses the example Postgres password")
	}
	if postgresPassword == "datingcoach" {
		bad = append(bad, "POSTGRES_PASSWORD is a shipped default")
	}
	if uri, err := amqp.ParseURI(c.RabbitMQURL); err == nil && uri.Password == "guest" {
		bad = append(bad, "RABBITMQ_URL uses the shipped default password")
	}
	switch c.SMTPPassword {
	case "re_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", "SG.xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx":
		bad = append(bad, "SMTP_PASSWORD is an example placeholder")
	}
	if len(bad) != 0 {
		return fmt.Errorf("refusing to start with example or weak secrets outside development: %s", strings.Join(bad, "; "))
	}
	return nil
}

func hasEnoughDistinctBytes(s string) bool {
	var seen [256]bool
	n := 0
	for i := 0; i < len(s); i++ {
		if !seen[s[i]] {
			seen[s[i]] = true
			n++
			if n == 10 {
				return true
			}
		}
	}
	return false
}
