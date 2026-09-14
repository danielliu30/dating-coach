package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fakeGoogle is a stand-in for Google's token endpoint: it holds signing keys,
// serves them as a JWKS document and mints ID tokens with them.
type fakeGoogle struct {
	keys    map[string]*rsa.PrivateKey
	server  *httptest.Server
	fetches atomic.Int32
}

// newFakeGoogle starts a JWKS server publishing one RSA key under each kid.
func newFakeGoogle(t *testing.T, kids ...string) *fakeGoogle {
	t.Helper()
	g := &fakeGoogle{keys: map[string]*rsa.PrivateKey{}}
	for _, kid := range kids {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		g.keys[kid] = key
	}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.fetches.Add(1)
		var doc struct {
			Keys []map[string]string `json:"keys"`
		}
		for kid, key := range g.keys {
			doc.Keys = append(doc.Keys, map[string]string{
				"kid": kid, "kty": "RSA", "alg": "RS256", "use": "sig",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			})
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(g.server.Close)
	return g
}

// token signs claims with the key registered under kid.
func (g *fakeGoogle) token(t *testing.T, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(g.keys[kid])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

// goodClaims is a valid Google payload for the given client id.
func goodClaims(clientID string) jwt.MapClaims {
	return jwt.MapClaims{
		"iss": "https://accounts.google.com", "aud": clientID, "sub": "1234567890",
		"email": "Person@Example.com", "email_verified": true, "name": "Pat Example",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	}
}

func TestGoogleVerifierAcceptsValidToken(t *testing.T) {
	g := newFakeGoogle(t, "k1")
	v := NewGoogleVerifier("client-1", g.server.URL, nil)

	id, err := v.Verify(context.Background(), g.token(t, "k1", goodClaims("client-1")))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Email != "person@example.com" || id.Name != "Pat Example" || id.Subject != "1234567890" {
		t.Fatalf("identity = %+v", id)
	}
	if _, err := v.Verify(context.Background(), g.token(t, "k1", goodClaims("client-1"))); err != nil {
		t.Fatalf("second Verify: %v", err)
	}
	if got := g.fetches.Load(); got != 1 {
		t.Fatalf("jwks fetched %d times, want 1 (cached)", got)
	}
}

func TestGoogleVerifierRejectsBadTokens(t *testing.T) {
	g := newFakeGoogle(t, "k1")
	other := newFakeGoogle(t, "k1")
	unpublished := newFakeGoogle(t, "k9")
	v := NewGoogleVerifier("client-1", g.server.URL, nil)

	cases := map[string]string{
		"wrong audience": g.token(t, "k1", func() jwt.MapClaims { c := goodClaims("client-1"); c["aud"] = "client-2"; return c }()),
		"wrong issuer":   g.token(t, "k1", func() jwt.MapClaims { c := goodClaims("client-1"); c["iss"] = "https://evil.example"; return c }()),
		"expired": g.token(t, "k1", func() jwt.MapClaims {
			c := goodClaims("client-1")
			c["exp"] = time.Now().Add(-2 * time.Minute).Unix()
			return c
		}()),
		"unverified":  g.token(t, "k1", func() jwt.MapClaims { c := goodClaims("client-1"); c["email_verified"] = false; return c }()),
		"no email":    g.token(t, "k1", func() jwt.MapClaims { c := goodClaims("client-1"); delete(c, "email"); return c }()),
		"foreign key": other.token(t, "k1", goodClaims("client-1")),
		"unknown kid": unpublished.token(t, "k9", goodClaims("client-1")),
		"garbage":     "not.a.jwt",
	}
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, goodClaims("client-1"))
	hs.Header["kid"] = "k1"
	if signed, err := hs.SignedString([]byte("secret")); err == nil {
		cases["hs256"] = signed
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrInvalidGoogleToken) {
				t.Fatalf("err = %v, want ErrInvalidGoogleToken", err)
			}
		})
	}
}

func TestGoogleVerifierRefetchesOnKeyRotation(t *testing.T) {
	g := newFakeGoogle(t, "old")
	v := NewGoogleVerifier("client-1", g.server.URL, nil)
	if _, err := v.Verify(context.Background(), g.token(t, "old", goodClaims("client-1"))); err != nil {
		t.Fatalf("Verify with old key: %v", err)
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	g.keys["new"] = key
	if _, err := v.Verify(context.Background(), g.token(t, "new", goodClaims("client-1"))); err != nil {
		t.Fatalf("Verify with rotated key: %v", err)
	}
	if got := g.fetches.Load(); got != 2 {
		t.Fatalf("jwks fetched %d times, want 2", got)
	}
	if _, err := v.Verify(context.Background(), g.token(t, "old", func() jwt.MapClaims {
		c := goodClaims("client-1")
		return c
	}())); err != nil {
		t.Fatalf("old key still published, so still valid: %v", err)
	}
}

func TestGoogleVerifierReportsUnreachableJWKS(t *testing.T) {
	g := newFakeGoogle(t, "k1")
	tok := g.token(t, "k1", goodClaims("client-1"))
	g.server.Close()
	v := NewGoogleVerifier("client-1", g.server.URL, nil)
	_, err := v.Verify(context.Background(), tok)
	if !errors.Is(err, ErrGoogleKeysUnavailable) || errors.Is(err, ErrInvalidGoogleToken) {
		t.Fatalf("err = %v, want ErrGoogleKeysUnavailable and not ErrInvalidGoogleToken", err)
	}

	live := newFakeGoogle(t, "k1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewGoogleVerifier("client-1", live.server.URL, nil).Verify(ctx, live.token(t, "k1", goodClaims("client-1")))
	if !errors.Is(err, ErrGoogleKeysUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want both ErrGoogleKeysUnavailable and context.Canceled", err)
	}
}
