package config

import (
	"strings"
	"testing"
)

func TestSMTPConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, env, host, setting, wantError string
		wantTLS                             bool
	}{
		{"dev fallback", "development", "", "", "", false},
		{"dev Mailpit", "development", "mailpit", "false", "", false},
		{"dev TLS opt in", "development", "smtp.example.test", "true", "", true},
		{"production default TLS", "production", "smtp.example.test", "", "", true},
		{"staging default TLS", "staging", "smtp.example.test", "", "", true},
		{"missing SMTP", "production", "", "", "SMTP_HOST", false},
		{"blank SMTP", "staging", "  ", "true", "SMTP_HOST", false},
		{"no production downgrade", "production", "smtp.example.test", "false", "SMTP_TLS", false},
		{"no staging downgrade", "staging", "smtp.example.test", "false", "SMTP_TLS", false},
		{"invalid TLS", "development", "mailpit", "optional", "SMTP_TLS", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			setRequired(t)
			t.Setenv("APP_ENV", tc.env)
			t.Setenv("JWT_SECRET", "q8Zr1vN0bT4xKp7LmW2yHs9cJd6Fg3Ae")
			t.Setenv("POSTGRES_PASSWORD", "db-test-password")
			t.Setenv("RABBITMQ_URL", "amqp://app:mq-test-password@localhost:5672/")
			t.Setenv("SMTP_PASSWORD", "")
			t.Setenv("SMTP_HOST", tc.host)
			t.Setenv("SMTP_TLS", tc.setting)
			cfg, err := Load()
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("Load error=%v want %s", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.SMTPTLS != tc.wantTLS {
				t.Fatalf("SMTPTLS=%v want %v", cfg.SMTPTLS, tc.wantTLS)
			}
		})
	}
}
