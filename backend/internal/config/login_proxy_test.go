package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoginAndProxyDefaults(t *testing.T) {
	setRequired(t)
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	t.Setenv("LOGIN_FAILURE_LIMIT", "")
	t.Setenv("LOGIN_FAILURE_WINDOW", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LoginFailureLimit != 10 || cfg.LoginFailureWindow != 15*time.Minute {
		t.Fatalf("limit=%d window=%s", cfg.LoginFailureLimit, cfg.LoginFailureWindow)
	}
	if len(cfg.TrustedProxyCIDRs) != 3 || cfg.TrustedProxyCIDRs[0].String() != "127.0.0.0/8" || cfg.TrustedProxyCIDRs[1].String() != "::1/128" || cfg.TrustedProxyCIDRs[2].String() != "172.16.0.0/12" {
		t.Fatal(cfg.TrustedProxyCIDRs)
	}
}

func TestLoginAndProxyOverrides(t *testing.T) {
	setRequired(t)
	t.Setenv("TRUSTED_PROXY_CIDRS", "192.0.2.0/24, 2001:db8::/32")
	t.Setenv("LOGIN_FAILURE_LIMIT", "5")
	t.Setenv("LOGIN_FAILURE_WINDOW", "30m")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LoginFailureLimit != 5 || cfg.LoginFailureWindow != 30*time.Minute || len(cfg.TrustedProxyCIDRs) != 2 {
		t.Fatal("overrides not applied")
	}
	t.Setenv("TRUSTED_PROXY_CIDRS", "none")
	cfg, err = Load()
	if err != nil || len(cfg.TrustedProxyCIDRs) != 0 {
		t.Fatalf("trust none: cfg=%+v err=%v", cfg, err)
	}
}

func TestLoginAndProxyRejectInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"TRUSTED_PROXY_CIDRS", "invalid"},
		{"TRUSTED_PROXY_CIDRS", "127.0.0.1"},
		{"LOGIN_FAILURE_LIMIT", "0"},
		{"LOGIN_FAILURE_LIMIT", "-1"},
		{"LOGIN_FAILURE_LIMIT", "invalid"},
		{"LOGIN_FAILURE_WINDOW", "0"},
		{"LOGIN_FAILURE_WINDOW", "-1m"},
		{"LOGIN_FAILURE_WINDOW", "1ns"},
		{"LOGIN_FAILURE_WINDOW", "invalid"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			setRequired(t)
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("Load()=%v, want error naming %s", err, tc.key)
			}
		})
	}
}
