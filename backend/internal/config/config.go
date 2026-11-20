// Package config loads service configuration from the environment.
package config

import (
	"fmt"
	"net/netip"
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

	DatabaseURL               string
	RedisURL                  string
	RabbitMQURL               string
	QueueBackend              string
	GCPProjectID              string
	PubSubMaxDeliveryAttempts int
	MLServiceAuth             string
	EmailOutboxTopic          string

	JWTSecret            string
	JWTTTL               time.Duration
	VerifyTokenTTL       time.Duration
	RefreshTokenTTL      time.Duration
	ResetCodeTTL         time.Duration
	ResetResendCooldown  time.Duration
	VerifyResendCooldown time.Duration
	BcryptCost           int
	AuthRateLimit        int
	AuthRateWindow       time.Duration
	TrustedProxyHops     int
	TrustedProxyCIDRs    []netip.Prefix
	LoginFailureLimit    int
	LoginFailureWindow   time.Duration

	MLServiceURL        string
	MLServiceTimeout    time.Duration
	ProfileScoreEnabled bool

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
	SMTPTimeout  time.Duration
	MailFrom     string

	// GoogleClientID is the OAuth client ID Google ID tokens must be minted
	// for. Empty disables POST /auth/google; password auth is unaffected.
	GoogleClientID string

	PublicAppURL string
	CORSOrigins  []string

	// PaymentsEnabled is the switch for paid reservations. When off, booking
	// confirms a session immediately and the Stripe settings are ignored.
	PaymentsEnabled     bool
	StripeSecretKey     string
	StripeWebhookSecret string
	// PaymentHoldTTL is how long an unpaid booking keeps its slot reserved.
	PaymentHoldTTL time.Duration
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
	envBool := func(key string, fallback bool) bool {
		raw := env(key, "")
		if raw == "" {
			return fallback
		}
		v, err := strconv.ParseBool(raw)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s=%q is not a boolean", key, raw))
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
		Env:                       env("APP_ENV", "production"),
		HTTPAddr:                  env("HTTP_ADDR", ":"+env("PORT", "8080")),
		QueueBackend:              env("QUEUE_BACKEND", "rabbitmq"),
		GCPProjectID:              env("GCP_PROJECT_ID", ""),
		PubSubMaxDeliveryAttempts: envInt("PUBSUB_MAX_DELIVERY_ATTEMPTS", 5),
		MLServiceAuth:             env("ML_SERVICE_AUTH", "none"),
		EmailOutboxTopic:          env("EMAIL_OUTBOX_TOPIC", "email.outbox"),
		DatabaseURL:               env("DATABASE_URL", ""),
		RedisURL:                  env("REDIS_URL", "redis://localhost:6379/0"),
		RabbitMQURL:               env("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		JWTSecret:                 env("JWT_SECRET", ""),
		JWTTTL:                    envDuration("JWT_TTL", 15*time.Minute),
		VerifyTokenTTL:            envDuration("VERIFY_TOKEN_TTL", 30*time.Minute),
		RefreshTokenTTL:           envDuration("REFRESH_TOKEN_TTL", 30*24*time.Hour),
		ResetCodeTTL:              envDuration("RESET_CODE_TTL", 15*time.Minute),
		ResetResendCooldown:       envDuration("RESET_RESEND_COOLDOWN", time.Minute),
		VerifyResendCooldown:      envDuration("VERIFY_RESEND_COOLDOWN", time.Minute),
		BcryptCost:                envInt("BCRYPT_COST", 12),
		AuthRateLimit:             envInt("AUTH_RATE_LIMIT", 20),
		AuthRateWindow:            envDuration("AUTH_RATE_WINDOW", time.Minute),
		TrustedProxyHops:          envInt("TRUSTED_PROXY_HOPS", 0),
		LoginFailureLimit:         envInt("LOGIN_FAILURE_LIMIT", 10),
		LoginFailureWindow:        envDuration("LOGIN_FAILURE_WINDOW", 15*time.Minute),
		MLServiceURL:              env("ML_SERVICE_URL", "http://localhost:8000"),
		MLServiceTimeout:          envDuration("ML_SERVICE_TIMEOUT", 60*time.Second),
		ProfileScoreEnabled:       envBool("PROFILE_SCORE_ENABLED", false),
		AnalysisQueue:             env("ANALYSIS_QUEUE", "conversation.analysis"),
		AccountDeletionQueue:      env("ACCOUNT_DELETION_QUEUE", "account.deletion"),

		DeadLetterAlertPeriod: envDuration("DEAD_LETTER_ALERT_PERIOD", time.Minute),

		SMTPHost:       env("SMTP_HOST", ""),
		SMTPPort:       envInt("SMTP_PORT", 587),
		SMTPUsername:   env("SMTP_USERNAME", ""),
		SMTPPassword:   env("SMTP_PASSWORD", ""),
		SMTPTimeout:    envDuration("SMTP_TIMEOUT", 30*time.Second),
		MailFrom:       env("MAIL_FROM", "no-reply@datingcoach.local"),
		GoogleClientID: env("GOOGLE_CLIENT_ID", ""),
		PublicAppURL:   env("PUBLIC_APP_URL", "http://localhost:19006"),
		CORSOrigins:    envList("CORS_ORIGINS", []string{"http://localhost:19006", "http://localhost:8081"}),

		PaymentsEnabled:     envBool("PAYMENTS_ENABLED", false),
		StripeSecretKey:     env("STRIPE_SECRET_KEY", ""),
		StripeWebhookSecret: env("STRIPE_WEBHOOK_SECRET", ""),
		PaymentHoldTTL:      envDuration("PAYMENT_HOLD_TTL", 15*time.Minute),
	}

	proxyCIDRs := env("TRUSTED_PROXY_CIDRS", "127.0.0.0/8,::1/128,172.16.0.0/12")
	if proxyCIDRs != "none" {
		for _, raw := range strings.Split(proxyCIDRs, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
			if err != nil {
				bad = append(bad, "TRUSTED_PROXY_CIDRS must contain valid CIDRs or be none")
				break
			}
			cfg.TrustedProxyCIDRs = append(cfg.TrustedProxyCIDRs, prefix)
		}
	}
	if cfg.LoginFailureLimit <= 0 {
		bad = append(bad, "LOGIN_FAILURE_LIMIT must be positive")
	}
	if cfg.LoginFailureWindow > 0 && cfg.LoginFailureWindow < time.Millisecond {
		bad = append(bad, "LOGIN_FAILURE_WINDOW must be at least 1ms")
	}
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.QueueBackend != "rabbitmq" && cfg.QueueBackend != "pubsub" {
		bad = append(bad, "QUEUE_BACKEND must be rabbitmq or pubsub")
	}
	if cfg.QueueBackend == "pubsub" && cfg.GCPProjectID == "" {
		bad = append(bad, "GCP_PROJECT_ID is required for pubsub")
	}
	if cfg.PubSubMaxDeliveryAttempts <= 0 {
		bad = append(bad, "PUBSUB_MAX_DELIVERY_ATTEMPTS must be positive")
	}
	if cfg.MLServiceAuth != "none" && cfg.MLServiceAuth != "google" {
		bad = append(bad, "ML_SERVICE_AUTH must be none or google")
	}
	if cfg.TrustedProxyHops < 0 {
		bad = append(bad, "TRUSTED_PROXY_HOPS must be non-negative")
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
		{"REFRESH_TOKEN_TTL", cfg.RefreshTokenTTL},
		{"RESET_CODE_TTL", cfg.ResetCodeTTL},
		{"RESET_RESEND_COOLDOWN", cfg.ResetResendCooldown},
		{"VERIFY_RESEND_COOLDOWN", cfg.VerifyResendCooldown},
		{"AUTH_RATE_WINDOW", cfg.AuthRateWindow},
		{"LOGIN_FAILURE_WINDOW", cfg.LoginFailureWindow},
		{"ML_SERVICE_TIMEOUT", cfg.MLServiceTimeout},
		{"DEAD_LETTER_ALERT_PERIOD", cfg.DeadLetterAlertPeriod},
		{"PAYMENT_HOLD_TTL", cfg.PaymentHoldTTL},
		{"SMTP_TIMEOUT", cfg.SMTPTimeout},
	} {
		if d.value <= 0 {
			bad = append(bad, fmt.Sprintf("%s=%s must be positive", d.key, d.value))
		}
	}

	// The switch alone is not enough to take money: refuse to start half
	// configured rather than fail on the first booking.
	if cfg.PaymentsEnabled {
		if cfg.StripeSecretKey == "" {
			bad = append(bad, "STRIPE_SECRET_KEY is required when PAYMENTS_ENABLED=true")
		}
		if cfg.StripeWebhookSecret == "" {
			bad = append(bad, "STRIPE_WEBHOOK_SECRET is required when PAYMENTS_ENABLED=true")
		}
	}

	if len(bad) > 0 {
		return nil, fmt.Errorf("invalid configuration: %s", strings.Join(bad, "; "))
	}
	if cfg.Env != "development" {
		if err := cfg.validateSecrets(env("POSTGRES_PASSWORD", "")); err != nil {
			return nil, err
		}
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
