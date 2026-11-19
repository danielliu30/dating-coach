package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func TestSignInRejectsPasswordChangedBeforeLock(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	email := "signin-reset-" + uuid.NewString() + "@example.com"
	signup, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: "old-password", DisplayName: "Reset"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE email=$1", email) })
	id := uuid.MustParse(signup.User.ID)
	if _, err := svc.queries.VerifyUserEmail(ctx, id); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("new-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := svc.queries.WithTx(tx).LockUserForRefresh(ctx, id); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := svc.SignIn(ctx, email, "old-password")
		result <- err
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE $1::integer = ANY(pg_blocking_pids(pid))
				  AND query LIKE '-- name: LockUserForRefresh%'
			)`, tx.Conn().PgConn().PID()).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("sign in returned before acquiring the account lock: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if _, err := tx.Exec(ctx, "UPDATE users SET password_hash=$2 WHERE id=$1", id, string(hash)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("sign in with stale password = %v, want ErrInvalidCredentials", err)
	}
}
