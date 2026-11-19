package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRefreshRejectionReasons(t *testing.T) {
	for _, reason := range []string{"expired", "reset", "signout"} {
		t.Run(reason, func(t *testing.T) {
			svc, _, pool := newTestService(t)
			ctx := context.Background()
			email := "refresh-reason-" + uuid.NewString() + "@example.com"
			signup, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: "old-password", DisplayName: "Reset"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE email=$1", email) })
			id := uuid.MustParse(signup.User.ID)
			if _, err := svc.queries.VerifyUserEmail(ctx, id); err != nil {
				t.Fatal(err)
			}
			session, err := svc.SignIn(ctx, email, "old-password")
			if err != nil {
				t.Fatal(err)
			}
			want := "session revoked"
			switch reason {
			case "expired":
				want = ErrInvalidRefreshToken.Error()
				_, err = pool.Exec(ctx, "UPDATE refresh_tokens SET expires_at=now()-interval '1 second' WHERE user_id=$1", id)
			case "reset":
				if err = svc.ForgotPassword(ctx, email); err != nil {
					t.Fatal(err)
				}
				code, _, _ := svc.resetCodes.Get(ctx, id.String())
				err = svc.ResetPassword(ctx, email, code, "new-password")
			case "signout":
				err = svc.SignOut(ctx, id, SignOutRequest{})
			}
			if err != nil {
				t.Fatal(err)
			}
			routes := NewHandler(svc, NewRateLimiter(svc.codes.rdb, 100, time.Minute)).Routes(rejectAll)
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/refresh", strings.NewReader(`{"refresh_token":"`+session.RefreshToken+`"}`)))
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 401 || body.Error != want {
				t.Fatalf("status=%d body=%s want %q", rec.Code, rec.Body, want)
			}
			if reason != "expired" {
				if _, err := pool.Exec(ctx, "UPDATE revoked_refresh_tokens SET expires_at=now()-interval '1 second' WHERE user_id=$1", id); err != nil {
					t.Fatal(err)
				}
				if revoked, err := svc.queries.RefreshTokenRevoked(ctx, hashRefreshToken(session.RefreshToken)); err != nil || revoked {
					t.Fatalf("expired tombstone: revoked=%v err=%v", revoked, err)
				}
				if _, err := svc.queries.DeleteExpiredRefreshTokens(ctx); err != nil {
					t.Fatal(err)
				}
				var count int
				if err := pool.QueryRow(ctx, "SELECT count(*) FROM revoked_refresh_tokens WHERE user_id=$1", id).Scan(&count); err != nil || count != 0 {
					t.Fatalf("cleanup: count=%d err=%v", count, err)
				}
			}
		})
	}
}
