package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// GoogleJWKSURL is where Google publishes the RSA keys its ID tokens are
// signed with; the key set rotates, so it is fetched on demand and cached.
const GoogleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

// googleIssuers are the two issuer values Google ID tokens legitimately carry.
var googleIssuers = []string{"https://accounts.google.com", "accounts.google.com"}

// ErrInvalidGoogleToken is returned for an ID token that is malformed, signed
// by an unknown key, expired, minted for another client, or whose email Google
// has not verified. The wrapped detail is for logs, not clients.
var ErrInvalidGoogleToken = errors.New("invalid google id token")

// ErrGoogleKeysUnavailable is returned when Google's key set could not be
// fetched or decoded, so the token could not be checked either way. Callers
// should answer with a 5xx rather than treat the token as bad.
var ErrGoogleKeysUnavailable = errors.New("google signing keys unavailable")

// GoogleIdentity is what a verified Google ID token vouches for: the address
// Google confirmed, plus the profile fields used to seed a new account.
type GoogleIdentity struct {
	Subject string
	Email   string
	Name    string
}

// GoogleTokenVerifier turns a Google ID token into a GoogleIdentity or an
// error. Service depends on the interface so tests can substitute a stub and
// cmd/api owns the real verifier.
type GoogleTokenVerifier interface {
	Verify(ctx context.Context, idToken string) (GoogleIdentity, error)
}

// googleClaims is the subset of Google's ID-token payload the API relies on.
type googleClaims struct {
	jwt.RegisteredClaims
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
}

// GoogleVerifier checks Google ID tokens against Google's published RSA keys
// for one OAuth client ID. It caches the key set and refetches it once when a
// token names a key id it has not seen, which is how rotation surfaces.
type GoogleVerifier struct {
	clientID string
	jwksURL  string
	client   *http.Client
	cacheTTL time.Duration

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

// NewGoogleVerifier returns a verifier that accepts tokens minted for clientID.
// jwksURL is GoogleJWKSURL in production; tests point it at a local server.
// client may be nil for http.DefaultClient with a 10s timeout.
func NewGoogleVerifier(clientID, jwksURL string, client *http.Client) *GoogleVerifier {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &GoogleVerifier{clientID: clientID, jwksURL: jwksURL, client: client, cacheTTL: time.Hour}
}

// Verify parses and validates idToken: RS256 signature by a current Google key,
// Google issuer, audience equal to the configured client ID, unexpired, and
// email_verified true. Any token failure, including a kid Google does not
// publish, yields an error wrapping ErrInvalidGoogleToken; a key-set fetch
// failure wraps ErrGoogleKeysUnavailable instead so callers can tell a bad
// token from an unreachable Google.
func (v *GoogleVerifier) Verify(ctx context.Context, idToken string) (GoogleIdentity, error) {
	claims := &googleClaims{}
	var fetchErr error
	token, err := jwt.ParseWithClaims(idToken, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("token has no kid header")
		}
		key, err := v.key(ctx, kid)
		if errors.Is(err, ErrGoogleKeysUnavailable) {
			fetchErr = err
		}
		return key, err
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithAudience(v.clientID),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30*time.Second),
	)
	if fetchErr != nil {
		return GoogleIdentity{}, fetchErr
	}
	if err != nil || !token.Valid {
		return GoogleIdentity{}, fmt.Errorf("%w: %v", ErrInvalidGoogleToken, err)
	}
	if !issuedByGoogle(claims.Issuer) {
		return GoogleIdentity{}, fmt.Errorf("%w: issuer %q", ErrInvalidGoogleToken, claims.Issuer)
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email == "" || !claims.EmailVerified {
		return GoogleIdentity{}, fmt.Errorf("%w: email not verified by google", ErrInvalidGoogleToken)
	}
	return GoogleIdentity{Subject: claims.Subject, Email: email, Name: strings.TrimSpace(claims.Name)}, nil
}

// issuedByGoogle reports whether iss is one of Google's issuer strings.
func issuedByGoogle(iss string) bool {
	for _, want := range googleIssuers {
		if iss == want {
			return true
		}
	}
	return false
}

// key returns the public key for kid, refetching the key set when kid is
// unknown or the cache is older than cacheTTL. A failed fetch wraps
// ErrGoogleKeysUnavailable; a kid still unknown after a fresh fetch is a plain
// error, since the token was signed with a key Google does not publish.
func (v *GoogleVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if key, ok := v.keys[kid]; ok && time.Since(v.fetchedAt) < v.cacheTTL {
		return key, nil
	}
	keys, err := v.fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGoogleKeysUnavailable, err)
	}
	v.keys, v.fetchedAt = keys, time.Now()
	key, ok := keys[kid]
	if !ok {
		return nil, fmt.Errorf("unknown key id %q", kid)
	}
	return key, nil
}

// fetch downloads the JWKS document and decodes every RSA key in it, keyed by kid.
func (v *GoogleVerifier) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks endpoint returned %d", resp.StatusCode)
	}
	var doc struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode jwks: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, fmt.Errorf("decode modulus of %q: %w", k.Kid, err)
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, fmt.Errorf("decode exponent of %q: %w", k.Kid, err)
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if len(keys) == 0 {
		return nil, errors.New("jwks document has no RSA keys")
	}
	return keys, nil
}
