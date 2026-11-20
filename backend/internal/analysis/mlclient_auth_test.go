package analysis

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestMLClientWithoutGoogleAuth(t *testing.T) {
	c, err := NewAuthenticatedMLClient(context.Background(), "http://localhost:8000/", time.Second, "none")
	if err != nil {
		t.Fatal(err)
	}
	if c.baseURL != "http://localhost:8000" || c.http.Timeout != time.Second {
		t.Fatalf("unexpected client: %+v", c)
	}
}

func TestMLClientGoogleAudienceTrimsTrailingSlashes(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idToken, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	audiences := make(chan string, 3)
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		claims := jwt.MapClaims{}
		if _, _, err := jwt.NewParser().ParseUnverified(r.Form.Get("assertion"), claims); err != nil {
			t.Error(err)
			http.Error(w, "invalid assertion", http.StatusBadRequest)
			return
		}
		audience, _ := claims["target_audience"].(string)
		audiences <- audience
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": idToken})
	}))
	defer tokens.Close()
	credentials, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"client_email": "ml-test@example.iam.gserviceaccount.com",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
		"token_uri":    tokens.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, credentials, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
	ml := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/analyze" || r.Header.Get("Authorization") != "Bearer "+idToken {
			t.Errorf("unexpected authenticated request: path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ml.Close()
	for _, suffix := range []string{"", "/", "///"} {
		t.Run("suffix="+suffix, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := NewAuthenticatedMLClient(ctx, ml.URL+suffix, time.Second, "google")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Analyze(ctx, MLRequest{}); err != nil {
				t.Fatal(err)
			}
			select {
			case audience := <-audiences:
				if audience != ml.URL {
					t.Fatalf("audience = %q, want %q", audience, ml.URL)
				}
			case <-ctx.Done():
				t.Fatal("no ID-token audience recorded")
			}
		})
	}
}
