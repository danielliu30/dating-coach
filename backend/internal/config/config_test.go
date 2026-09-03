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
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/dating_coach")
	t.Setenv("JWT_SECRET", "test-secret")
}

// TestLoadRejectsNonPositiveDurations covers durations that parse but cannot be
// used: DEAD_LETTER_ALERT_PERIOD drives a time.Ticker, which panics on a
// non-positive interval and would take the worker down at startup.
func TestLoadRejectsNonPositiveDurations(t *testing.T) {
	cases := map[string]struct{ key, value string }{
		"zero alert period":     {"DEAD_LETTER_ALERT_PERIOD", "0s"},
		"negative alert period": {"DEAD_LETTER_ALERT_PERIOD", "-1m"},
		"zero jwt ttl":          {"JWT_TTL", "0"},
		"negative rate window":  {"AUTH_RATE_WINDOW", "-30s"},
		"zero refresh ttl":      {"REFRESH_TOKEN_TTL", "0"},
		"negative refresh ttl":  {"REFRESH_TOKEN_TTL", "-1h"},
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
