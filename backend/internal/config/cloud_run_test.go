package config

import "testing"

func TestCloudRunConfig(t *testing.T) {
	setRequired(t)
	t.Setenv("APP_ENV", "development")
	t.Setenv("QUEUE_BACKEND", "pubsub")
	t.Setenv("GCP_PROJECT_ID", "test-project")
	t.Setenv("PORT", "9090")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("ML_SERVICE_AUTH", "google")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":9090" || cfg.EmailOutboxTopic != "email.outbox" || cfg.PubSubMaxDeliveryAttempts != 5 {
		t.Fatalf("unexpected config: addr=%s topic=%s attempts=%d", cfg.HTTPAddr, cfg.EmailOutboxTopic, cfg.PubSubMaxDeliveryAttempts)
	}
	t.Setenv("HTTP_ADDR", ":7070")
	cfg, err = Load()
	if err != nil || cfg.HTTPAddr != ":7070" {
		t.Fatalf("HTTP_ADDR override: %v", err)
	}
	for _, tt := range []struct{ key, value string }{{"GCP_PROJECT_ID", ""}, {"QUEUE_BACKEND", "bad"}, {"ML_SERVICE_AUTH", "bad"}, {"PUBSUB_MAX_DELIVERY_ATTEMPTS", "0"}} {
		t.Run(tt.key, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			if _, err := Load(); err == nil {
				t.Fatal("expected invalid configuration")
			}
		})
	}
}

func TestPubSubSkipsRabbitSecret(t *testing.T) {
	cfg := &Config{QueueBackend: "pubsub", JWTSecret: "a-strong-secret-with-many-distinct-bytes-123456", DatabaseURL: "postgres://localhost/test", RabbitMQURL: "amqp://guest:guest@localhost/"}
	if err := cfg.validateSecrets(""); err != nil {
		t.Fatal(err)
	}
	cfg.QueueBackend = "rabbitmq"
	if err := cfg.validateSecrets(""); err == nil {
		t.Fatal("expected RabbitMQ default password rejection")
	}
}

func TestTrustedProxyHops(t *testing.T) {
	for _, tt := range []struct {
		value   string
		want    int
		invalid bool
	}{{"", 1, false}, {"0", 0, false}, {"1", 1, false}, {"2", 2, false}, {"-1", 0, true}, {"invalid", 0, true}} {
		t.Run(tt.value, func(t *testing.T) {
			setRequired(t)
			t.Setenv("TRUSTED_PROXY_HOPS", tt.value)
			cfg, err := Load()
			if tt.invalid {
				if err == nil {
					t.Fatal("expected invalid configuration")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.TrustedProxyHops != tt.want {
				t.Fatalf("TrustedProxyHops = %d, want %d", cfg.TrustedProxyHops, tt.want)
			}
		})
	}
}
