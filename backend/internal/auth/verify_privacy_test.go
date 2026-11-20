package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestVerifyEmailVerifiedAccountRequiresOwningBearer(t *testing.T) {
	svc, issuer, pool := newTestService(t)
	ctx := context.Background()
	user := verifiedUser(t, svc, pool)
	owner, err := svc.SignIn(ctx, user.email, user.password)
	if err != nil {
		t.Fatal(err)
	}
	wrong, _, err := issuer.Issue(uuid.New(), "other@example.com", RoleUser, ScopeVerify, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	expired, _, err := issuer.Issue(uuid.MustParse(owner.User.ID), user.email, RoleUser, ScopeVerify, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	routes := NewHandler(svc, NewRateLimiter(svc.codes.rdb, 100, time.Minute)).Routes(rejectAll)
	request := func(email, code, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]string{"email": email, "code": code})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/verify", strings.NewReader(string(body)))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		return rec
	}
	unknown := request(uuid.NewString()+"@example.com", "arbitrary", "")
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown account = %d %s", unknown.Code, unknown.Body)
	}
	for name, bearer := range map[string]string{"absent": "", "wrong account": wrong, "expired": expired, "malformed": "invalid"} {
		t.Run(name, func(t *testing.T) {
			got, err := svc.VerifyEmail(ctx, user.email, "arbitrary", bearer)
			if !errors.Is(err, ErrInvalidCode) || !reflect.DeepEqual(got, Session{}) {
				t.Fatalf("verification = %+v, %v; want empty session and ErrInvalidCode", got, err)
			}
			rec := request(user.email, "arbitrary", bearer)
			if rec.Code != unknown.Code || rec.Body.String() != unknown.Body.String() {
				t.Fatalf("verification = %d %s; want %d %s", rec.Code, rec.Body, unknown.Code, unknown.Body)
			}
		})
	}
	if got := request(user.email, "arbitrary", owner.Token); got.Code != http.StatusOK {
		t.Fatalf("owning bearer = %d %s", got.Code, got.Body)
	}
}

func TestVerifyEmailWithoutOwningBearerReturnsOnlyAcknowledgement(t *testing.T) {
	svc, issuer, pool := newTestService(t)
	ctx := context.Background()
	for _, transport := range []string{"service", "http"} {
		for _, caller := range []string{"absent", "wrong account", "expired"} {
			t.Run(transport+"/"+caller, func(t *testing.T) {
				email := "verify-privacy-" + uuid.NewString() + "@example.com"
				signup, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: "correct-horse", DisplayName: "Private profile"})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", email) })
				code, _, err := svc.codes.Get(ctx, signup.User.ID)
				if err != nil || code == "" {
					t.Fatalf("verification code = %q, %v", code, err)
				}
				bearer := ""
				if caller != "absent" {
					id, ttl := uuid.New(), time.Minute
					if caller == "expired" {
						id, ttl = uuid.MustParse(signup.User.ID), -time.Minute
					}
					bearer, _, err = issuer.Issue(id, email, RoleUser, ScopeVerify, ttl)
					if err != nil {
						t.Fatal(err)
					}
				}
				if transport == "service" {
					got, err := svc.VerifyEmail(ctx, email, code, bearer)
					if err != nil || !reflect.DeepEqual(got, Session{}) {
						t.Fatalf("verification = %+v, %v; want empty session", got, err)
					}
				} else {
					body, err := json.Marshal(map[string]string{"email": email, "code": code})
					if err != nil {
						t.Fatal(err)
					}
					req := httptest.NewRequest(http.MethodPost, "/verify", strings.NewReader(string(body)))
					if bearer != "" {
						req.Header.Set("Authorization", "Bearer "+bearer)
					}
					rec := httptest.NewRecorder()
					NewHandler(svc, NewRateLimiter(svc.codes.rdb, 100, time.Minute)).Routes(rejectAll).ServeHTTP(rec, req)
					if rec.Code != http.StatusOK || rec.Body.String() != "{\"status\":\"verified\"}\n" {
						t.Fatalf("verification = %d %s; want generic acknowledgement", rec.Code, rec.Body)
					}
				}
				user, err := svc.queries.GetUserByEmail(ctx, email)
				if err != nil || !user.EmailVerified {
					t.Fatalf("email not verified: %v", err)
				}
			})
		}
	}
}
