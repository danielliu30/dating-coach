package config

import (
	"strings"
	"testing"
)

func TestLoadRejectsNonPositiveDurations(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"zero", "0s"},
		{"negative", "-1m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://localhost:5432/test")
			t.Setenv("JWT_SECRET", "secret")
			t.Setenv("DEAD_LETTER_ALERT_PERIOD", tc.value)

			cfg, err := Load()
			if err == nil {
				t.Fatalf("Load() = %+v, want an error for DEAD_LETTER_ALERT_PERIOD=%s", cfg, tc.value)
			}
			if !strings.Contains(err.Error(), "DEAD_LETTER_ALERT_PERIOD") {
				t.Fatalf("error = %v, want it to name DEAD_LETTER_ALERT_PERIOD", err)
			}
		})
	}
}

func TestLoadAcceptsPositiveAlertPeriod(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/test")
	t.Setenv("JWT_SECRET", "secret")
	t.Setenv("DEAD_LETTER_ALERT_PERIOD", "30s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DeadLetterAlertPeriod.String() != "30s" {
		t.Fatalf("DeadLetterAlertPeriod = %s, want 30s", cfg.DeadLetterAlertPeriod)
	}
}
