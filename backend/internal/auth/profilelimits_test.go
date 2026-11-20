package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSignUpFieldLengthBoundaries(t *testing.T) {
	svc, _, pool := newTestService(t)
	h := NewHandler(svc, nil)
	for _, field := range []string{"display_name", "email"} {
		for _, over := range []bool{false, true} {
			t.Run(field+map[bool]string{false: "/at_limit", true: "/over_limit"}[over], func(t *testing.T) {
				in := SignUpInput{Email: uuid.NewString() + "@example.com", Password: "correct-horse", DisplayName: "Name"}
				extra := 0
				if over {
					extra = 1
				}
				if field == "display_name" {
					in.DisplayName = strings.Repeat("界", MaxDisplayNameLen+extra)
				} else {
					prefix := uuid.NewString()
					in.Email = prefix + strings.Repeat("a", MaxEmailLen+extra-len(prefix)-len("@example.com")) + "@example.com"
				}
				t.Cleanup(func() {
					if _, err := pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", in.Email); err != nil {
						t.Error(err)
					}
				})
				data, _ := json.Marshal(in)
				rec := httptest.NewRecorder()
				h.signUp(rec, httptest.NewRequest(http.MethodPost, "/signup", bytes.NewReader(data)))
				want := http.StatusCreated
				if over {
					want = http.StatusBadRequest
				}
				if rec.Code != want {
					t.Fatalf("status=%d want=%d body=%s", rec.Code, want, rec.Body)
				}
			})
		}
	}
}

func TestDatingPreferencesHTTPBoundary(t *testing.T) {
	svc, _, pool := newTestService(t)
	user := verifiedUser(t, svc, pool)
	row, err := svc.queries.GetUserByEmail(context.Background(), user.email)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, MaxDatingPreferencesLen, MaxDatingPreferencesLen + 1} {
		data, _ := json.Marshal(DatingProfileInput{DatingPreferences: strings.Repeat("界", n)})
		req := httptest.NewRequest(http.MethodPatch, "/me", bytes.NewReader(data))
		req = req.WithContext(WithPrincipal(req.Context(), Principal{UserID: row.ID}))
		rec := httptest.NewRecorder()
		NewHandler(svc, nil).updateMe(rec, req)
		want := http.StatusOK
		if n > MaxDatingPreferencesLen {
			want = http.StatusBadRequest
		}
		if rec.Code != want {
			t.Fatalf("length=%d status=%d want=%d body=%s", n, rec.Code, want, rec.Body)
		}
	}
}

func TestGoogleProfileCannotBypassNameLimit(t *testing.T) {
	svc := &Service{}
	_, err := svc.createGoogleUser(context.Background(), GoogleIdentity{Email: "test@example.com", Name: strings.Repeat("界", MaxDisplayNameLen+1)}, RoleUser)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected length validation before database access: %v", err)
	}
}
