package config

import (
	"strings"
	"testing"
	"time"
)

// setRequired fills in the settings Load rejects an empty environment on, so a
// test only has to state the variable it is exercising.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/dating_coach")
	t.Setenv("JWT_SECRET", "test-secret")
}

// TestLoadRejectsNonPositiveDurations covers durations that parse but cannot be
// used: DEAD_LETTER_ALERT_PERIOD drives a time.Ticker, which panics on a
// non-positive interval and would take the worker down at startup.
func TestLoadRejectsNonPositiveDurations(t *testing.T) {
	cases := map[string]struct{ key, value string }{
		"zero alert period":        {"DEAD_LETTER_ALERT_PERIOD", "0s"},
		"negative alert period":    {"DEAD_LETTER_ALERT_PERIOD", "-1m"},
		"zero jwt ttl":             {"JWT_TTL", "0"},
		"negative rate window":     {"AUTH_RATE_WINDOW", "-30s"},
		"zero refresh ttl":         {"REFRESH_TOKEN_TTL", "0"},
		"zero reset ttl":           {"RESET_CODE_TTL", "0"},
		"zero reset cooldown":      {"RESET_RESEND_COOLDOWN", "0"},
		"zero verify cooldown":     {"VERIFY_RESEND_COOLDOWN", "0"},
		"negative verify cooldown": {"VERIFY_RESEND_COOLDOWN", "-1s"},
		"negative refresh ttl":     {"REFRESH_TOKEN_TTL", "-1h"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			t.Setenv(tc.key, tc.value)
			cfg, err := Load()
			if err == nil {
				t.Fatalf("Load() = %+v, want an error for %s=%s", cfg, tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("Load() error = %v, want it to name %s", err, tc.key)
			}
		})
	}
}

func TestResetCodeTTL(t *testing.T) {
	setRequired(t)
	t.Setenv("RESET_CODE_TTL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ResetCodeTTL != 15*time.Minute {
		t.Fatal(cfg.ResetCodeTTL)
	}
	t.Setenv("RESET_CODE_TTL", "2m")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ResetCodeTTL != 2*time.Minute {
		t.Fatal(cfg.ResetCodeTTL)
	}
}

func TestResetResendCooldown(t *testing.T) {
	setRequired(t)
	t.Setenv("RESET_RESEND_COOLDOWN", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ResetResendCooldown != time.Minute {
		t.Fatal(cfg.ResetResendCooldown)
	}
	t.Setenv("RESET_RESEND_COOLDOWN", "90s")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ResetResendCooldown != 90*time.Second {
		t.Fatal(cfg.ResetResendCooldown)
	}
}

func TestAnalysisPrefetch(t *testing.T) {
	setRequired(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnalysisPrefetch != 4 {
		t.Fatal(cfg.AnalysisPrefetch)
	}
	t.Setenv("ANALYSIS_PREFETCH", "16")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnalysisPrefetch != 16 {
		t.Fatal(cfg.AnalysisPrefetch)
	}
	t.Setenv("ANALYSIS_PREFETCH", "0")
	if _, err := Load(); err == nil {
		t.Fatal("want ANALYSIS_PREFETCH=0 rejected")
	}
}

func TestVerifyResendCooldown(t *testing.T) {
	setRequired(t)
	t.Setenv("VERIFY_RESEND_COOLDOWN", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VerifyResendCooldown != time.Minute {
		t.Fatal(cfg.VerifyResendCooldown)
	}
	t.Setenv("VERIFY_RESEND_COOLDOWN", "90s")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VerifyResendCooldown != 90*time.Second {
		t.Fatal(cfg.VerifyResendCooldown)
	}
}

// TestLoadAcceptsPositiveAlertPeriod pins the default and an override, so the
// validation above cannot start rejecting usable values.
func TestLoadAcceptsPositiveAlertPeriod(t *testing.T) {
	setRequired(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DeadLetterAlertPeriod != time.Minute {
		t.Fatalf("DeadLetterAlertPeriod = %s, want 1m", cfg.DeadLetterAlertPeriod)
	}

	t.Setenv("DEAD_LETTER_ALERT_PERIOD", "30s")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DeadLetterAlertPeriod != 30*time.Second {
		t.Fatalf("DeadLetterAlertPeriod = %s, want 30s", cfg.DeadLetterAlertPeriod)
	}
}

// TestLoadPaymentsSwitch pins the payments switch: off by default, and when on
// the Stripe settings become mandatory so the API cannot start half configured.
func TestLoadPaymentsSwitch(t *testing.T) {
	setRequired(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PaymentsEnabled {
		t.Fatal("PaymentsEnabled should default to false")
	}
	if cfg.PaymentHoldTTL != 15*time.Minute {
		t.Fatalf("PaymentHoldTTL = %s, want 15m", cfg.PaymentHoldTTL)
	}

	t.Setenv("PAYMENTS_ENABLED", "true")
	if _, err := Load(); err == nil {
		t.Fatal("Load() should reject PAYMENTS_ENABLED=true without Stripe settings")
	} else if !strings.Contains(err.Error(), "STRIPE_SECRET_KEY") || !strings.Contains(err.Error(), "STRIPE_WEBHOOK_SECRET") {
		t.Fatalf("Load() error = %v, want it to name both Stripe settings", err)
	}

	t.Setenv("STRIPE_SECRET_KEY", "sk_test_x")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_x")
	t.Setenv("PAYMENT_HOLD_TTL", "5m")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.PaymentsEnabled || cfg.PaymentHoldTTL != 5*time.Minute {
		t.Fatalf("cfg = %+v, want payments enabled with a 5m hold", cfg)
	}

	t.Setenv("PAYMENTS_ENABLED", "maybe")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PAYMENTS_ENABLED") {
		t.Fatalf("Load() error = %v, want a PAYMENTS_ENABLED parse error", err)
	}
}
