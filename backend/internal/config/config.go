// Package config loads service configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config is the fully resolved configuration shared by cmd/api and cmd/worker.
// Each process reads only the fields it needs.
type Config struct {
	Env      string
	HTTPAddr string

	DatabaseURL string
	RedisURL    string
	RabbitMQURL string

	JWTSecret       string
	JWTTTL          time.Duration
	RefreshTokenTTL time.Duration
	VerifyTokenTTL  time.Duration
	BcryptCost      int
	AuthRateLimit   int
	AuthRateWindow  time.Duration

	MLServiceURL     string
	MLServiceTimeout time.Duration

	AnalysisQueue        string
	AccountDeletionQueue string

	// DeadLetterAlertPeriod is how often the worker reports the depth of the
	// account-deletion dead-letter queue. It drives a ticker, so it must be
	// positive.
	DeadLetterAlertPeriod time.Duration

	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	MailFrom     string

	PublicAppURL string
	CORSOrigins  []string
}

// Load reads configuration from the environment, falling back to .env files.
func Load() (*Config, error) {
	// Missing .env files are not an error: containers get real env vars.
	_ = godotenv.Load(".env", "../.env", "../../.env")

	var bad []string
	envInt := func(key string, fallback int) int {
		raw := env(key, "")
		if raw == "" {
			return fallback
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s=%q is not an integer", key, raw))
			return fallback
		}
		return v
	}
	envDuration := func(key string, fallback time.Duration) time.Duration {
		raw := env(key, "")
		if raw == "" {
			return fallback
		}
		v, err := time.ParseDuration(raw)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s=%q is not a duration", key, raw))
			return fallback
		}
		return v
	}

	cfg := &Config{
		Env:                  env("APP_ENV", "development"),
		HTTPAddr:             env("HTTP_ADDR", ":8080"),
		DatabaseURL:          env("DATABASE_URL", ""),
		RedisURL:             env("REDIS_URL", "redis://localhost:6379/0"),
		RabbitMQURL:          env("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		JWTSecret:            env("JWT_SECRET", ""),
		JWTTTL:               envDuration("JWT_TTL", 15*time.Minute),
		RefreshTokenTTL:      envDuration("REFRESH_TOKEN_TTL", 30*24*time.Hour),
		VerifyTokenTTL:       envDuration("VERIFY_TOKEN_TTL", 30*time.Minute),
		BcryptCost:           envInt("BCRYPT_COST", 12),
		AuthRateLimit:        envInt("AUTH_RATE_LIMIT", 20),
		AuthRateWindow:       envDuration("AUTH_RATE_WINDOW", time.Minute),
		MLServiceURL:         env("ML_SERVICE_URL", "http://localhost:8000"),
		MLServiceTimeout:     envDuration("ML_SERVICE_TIMEOUT", 60*time.Second),
		AnalysisQueue:        env("ANALYSIS_QUEUE", "conversation.analysis"),
		AccountDeletionQueue: env("ACCOUNT_DELETION_QUEUE", "account.deletion"),

		DeadLetterAlertPeriod: envDuration("DEAD_LETTER_ALERT_PERIOD", time.Minute),

		SMTPHost:     env("SMTP_HOST", ""),
		SMTPPort:     envInt("SMTP_PORT", 587),
		SMTPUsername: env("SMTP_USERNAME", ""),
		SMTPPassword: env("SMTP_PASSWORD", ""),
		MailFrom:     env("MAIL_FROM", "no-reply@datingcoach.local"),
		PublicAppURL: env("PUBLIC_APP_URL", "http://localhost:19006"),
		CORSOrigins:  envList("CORS_ORIGINS", []string{"http://localhost:19006", "http://localhost:8081"}),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required")
	}
	// A non-positive duration is accepted by ParseDuration but is never a
	// usable timeout, window or ticker interval.
	for _, d := range []struct {
		key   string
		value time.Duration
	}{
		{"JWT_TTL", cfg.JWTTTL},
		{"VERIFY_TOKEN_TTL", cfg.VerifyTokenTTL},
		{"AUTH_RATE_WINDOW", cfg.AuthRateWindow},
		{"ML_SERVICE_TIMEOUT", cfg.MLServiceTimeout},
		{"DEAD_LETTER_ALERT_PERIOD", cfg.DeadLetterAlertPeriod},
	} {
		if d.value <= 0 {
			bad = append(bad, fmt.Sprintf("%s=%s must be positive", d.key, d.value))
		}
	}

	if len(bad) > 0 {
		return nil, fmt.Errorf("invalid configuration: %s", strings.Join(bad, "; "))
	}
	return cfg, nil
}

// env returns a trimmed environment variable, or fallback when unset or blank.
func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// envList parses a comma-separated variable, used for the CORS origins.
func envList(key string, fallback []string) []string {
	raw := env(key, "")
	if raw == "" {
		return fallback
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}
