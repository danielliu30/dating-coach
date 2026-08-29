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

type Config struct {
	Env      string
	HTTPAddr string

	DatabaseURL string
	RedisURL    string
	RabbitMQURL string

	JWTSecret      string
	JWTTTL         time.Duration
	BcryptCost     int
	AuthRateLimit  int
	AuthRateWindow time.Duration

	MLServiceURL     string
	MLServiceTimeout time.Duration

	AnalysisQueue string

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

	cfg := &Config{
		Env:              env("APP_ENV", "development"),
		HTTPAddr:         env("HTTP_ADDR", ":8080"),
		DatabaseURL:      env("DATABASE_URL", ""),
		RedisURL:         env("REDIS_URL", "redis://localhost:6379/0"),
		RabbitMQURL:      env("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		JWTSecret:        env("JWT_SECRET", ""),
		JWTTTL:           envDuration("JWT_TTL", 24*time.Hour),
		BcryptCost:       envInt("BCRYPT_COST", 12),
		AuthRateLimit:    envInt("AUTH_RATE_LIMIT", 20),
		AuthRateWindow:   envDuration("AUTH_RATE_WINDOW", time.Minute),
		MLServiceURL:     env("ML_SERVICE_URL", "http://localhost:8000"),
		MLServiceTimeout: envDuration("ML_SERVICE_TIMEOUT", 60*time.Second),
		AnalysisQueue:    env("ANALYSIS_QUEUE", "conversation.analysis"),
		SMTPHost:         env("SMTP_HOST", ""),
		SMTPPort:         envInt("SMTP_PORT", 587),
		SMTPUsername:     env("SMTP_USERNAME", ""),
		SMTPPassword:     env("SMTP_PASSWORD", ""),
		MailFrom:         env("MAIL_FROM", "no-reply@datingcoach.local"),
		PublicAppURL:     env("PUBLIC_APP_URL", "http://localhost:19006"),
		CORSOrigins:      envList("CORS_ORIGINS", []string{"http://localhost:19006", "http://localhost:8081"}),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(env(key, "")); err == nil {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v, err := time.ParseDuration(env(key, "")); err == nil {
		return v
	}
	return fallback
}

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
