// lt-auth load-tests the auth surface while asserting correctness:
//
//  1. signup burst — staged rps-paced signups with unique emails must each
//     get a session; a deliberate duplicate-email race must produce exactly
//     one 201 and 409s for the rest.
//  2. signin churn — staged rps-paced VUs loop signin -> me -> signout on
//     their own seeded accounts; identity must never cross accounts.
//  3. refresh race — N clients concurrently rotate the same refresh token.
//     Rotation is transactional single-use: exactly one should win. A loser
//     is "token reuse" and revokes the whole session family, which signs out
//     the legitimate winner too — the desync this suite quantifies.
//  4. refresh chain — each VU rotates serially and the newest session must
//     keep working; a stale-token replay at the end must 401 and kill the
//     family (the designed anti-theft behavior).
//  5. verify-code desync — concurrent signups each scrape their own
//     six-digit code; every code must verify only its own account, a code
//     replayed against a neighbor's email must be rejected, and a resend
//     must invalidate the previous code.
//
// The component never logs tokens, codes, or credentials.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danielliu30/dating-coach/loadtest/internal/harness"
)

var signupSeq int64

func main() {
	base := flag.String("base", "http://localhost", "gateway base URL")
	stagesCSV := flag.String("vus", "10,20,50", "comma-separated VU counts per churn stage")
	rps := flag.Float64("rps", 150, "aggregate signup/signin attempts per second per stage (0 = unpaced)")
	duration := flag.Duration("duration", 30*time.Second, "duration of each churn stage")
	racers := flag.Int("racers", 8, "clients racing one refresh token in the desync probe")
	rotations := flag.Int("rotations", 4, "refresh rotations per user in the chain stage")
	verifyUsers := flag.Int("verify-users", 10, "accounts exercised by the verify-code desync phase")
	out := flag.String("out", "out", "report output directory")
	flag.Parse()

	stages, err := harness.Stages(*stagesCSV, *duration)
	if err != nil {
		log.Fatalf("%v", err)
	}
	maxVUs := harness.MaxVUs(stages)

	tag := harness.Tag("auth")
	ctx := context.Background()
	m := harness.NewMetrics()
	m.Progress(ctx, "auth", harness.ProgressEvery, *out)
	defer harness.CleanupTag(tag)

	emails, err := harness.SeedUsers(tag, 1, maxVUs+*racers+2, "user")
	if err != nil {
		log.Fatalf("seed users: %v", err)
	}
	pool := emails

	extra := map[string]any{}

	// --- Phase 1: signup burst ------------------------------------------------
	// Every iteration signs up a unique account and a fresh client verifies the
	// returned session shape. Concurrent duplicate attempts on one shared email
	// interleave: exactly one may win.
	{
		dupEmail := tag + "-dup@loadtest.local"
		var dupWins, dupConflicts, dupOther int64
		res := harness.RunRate(ctx, m, stages, *rps,
			func(ctx context.Context, vu int, rate *harness.Rate) {
				for rate.Wait(ctx) {
					c := harness.NewClient(*base, m)
					email := fmt.Sprintf("%s-s%d-%d@loadtest.local", tag, vu, atomic.AddInt64(&signupSeq, 1))
					res, err := c.SignUp(ctx, email, harness.SeedPassword, "LT "+email, "user")
					switch {
					case err != nil:
						m.Inc("signup_transport_errors")
					case res.Status == http.StatusCreated:
						m.Inc("signup_ok")
						var s harness.Session
						if err := res.Decode(&s); err != nil || s.Token == "" || s.User.Email != email {
							m.Failf("signup_bad_session", "%s: decode=%v token_set=%v email=%q",
								email, err, s.Token != "", s.User.Email)
						}
					default:
						m.Failf("signup_unexpected_status", "%s signup → %d", email, res.Status)
					}
					// One in four iterations races the shared email.
					if atomic.AddInt64(&signupSeq, 1)%4 == 0 {
						d := harness.NewClient(*base, m)
						res2, err2 := d.SignUp(ctx, dupEmail, harness.SeedPassword, "LT dup", "user")
						switch {
						case err2 != nil:
							m.Inc("dup_transport_errors")
						case res2.Status == http.StatusCreated:
							atomic.AddInt64(&dupWins, 1)
						case res2.Status == http.StatusConflict:
							atomic.AddInt64(&dupConflicts, 1)
						default:
							atomic.AddInt64(&dupOther, 1)
						}
					}
				}
			}, harness.ErrorRateTripwire(0.01))
		extra["signup_stage"] = res
		extra["dup_signup_winners"] = dupWins
		extra["dup_signup_conflicts"] = dupConflicts
		if dupOther > 0 {
			m.Note("dup signup: %d unexpected statuses", dupOther)
		}
		if dupWins > 1 && dupConflicts == 0 {
			m.Note("dup signup: %d concurrent 201s on one email — unique constraint failed", dupWins)
		}
	}
	// The winning dup email is now a real account; remove it so later phases
	// see a clean pool.
	_, _ = harness.Psql(fmt.Sprintf(`DELETE FROM users WHERE email = '%s-dup@loadtest.local'`, tag))

	// --- Phase 2: signin churn -------------------------------------------------
	// Each VU owns one seeded account and loops signin -> me -> signout.
	// Identity must never cross.
	{
		res := harness.RunRate(ctx, m, stages, *rps,
			func(ctx context.Context, vu int, rate *harness.Rate) {
				email := pool[vu%len(pool)]
				for rate.Wait(ctx) {
					c := harness.NewClient(*base, m)
					res, err := c.SignIn(ctx, email, harness.SeedPassword)
					switch {
					case err != nil:
						m.Inc("signin_transport_errors")
						continue
					case res.Status != http.StatusOK:
						m.Inc("signin_rejected")
						m.ErrOnce("signin", "%s signin → %d", email, res.Status)
						continue
					}
					var s harness.Session
					if err := res.Decode(&s); err != nil || s.User.Email != email || s.Token == "" {
						m.Failf("signin_bad_session", "%s: decode=%v email=%q token_set=%v",
							email, err, s.User.Email, s.Token != "")
						continue
					}
					me, err := c.Me(ctx)
					if err != nil || me.Status != http.StatusOK {
						m.Inc("me_failed")
						m.ErrOnce("me", "%s /me: err=%v status=%d", email, err, harness.Status(me))
						continue
					}
					var u harness.User
					if err := me.Decode(&u); err != nil || u.Email != email || u.ID != s.User.ID {
						m.Failf("identity_crossed", "%s session owned by %s/%s", email, u.Email, u.ID)
						continue
					}
					out, err := c.SignOut(ctx)
					if err != nil || out.Status != http.StatusOK {
						m.Inc("signout_failed")
						m.ErrOnce("signout", "%s signout: err=%v status=%d", email, err, harness.Status(out))
					}
					m.Inc("churn_round_trips")
				}
			}, harness.ErrorRateTripwire(0.01))
		extra["signin_stage"] = res
	}

	// --- Phase 3: refresh race (desync probe) ---------------------------------
	m.SetPhase("refresh race")
	// One account, -racers concurrent refreshes of the same token. Rotation is
	// serialized by the account lock, so exactly one should get 200; each loser
	// counts as token reuse and revokes the family — including the winner's
	// brand-new session. That last part is the desync.
	{
		email := pool[len(pool)-1]
		owner := harness.NewClient(*base, m)
		res, err := owner.SignIn(ctx, email, harness.SeedPassword)
		if err != nil || res.Status != http.StatusOK {
			log.Fatalf("seed race account signin: %v", err)
		}
		raw := owner.RefreshToken()
		var winners, losers, other int64
		clients := make([]*harness.Client, *racers)
		statuses := make([]int, *racers)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range clients {
			clients[i] = harness.NewClient(*base, m)
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				r, err := clients[i].Refresh(ctx, raw)
				if err != nil {
					statuses[i] = 0
					return
				}
				statuses[i] = r.Status
			}(i)
		}
		close(start)
		wg.Wait()
		var winner *harness.Client
		for i, st := range statuses {
			switch st {
			case http.StatusOK:
				winners++
				winner = clients[i]
			case http.StatusUnauthorized, http.StatusForbidden:
				losers++
			default:
				other++
			}
		}
		extra["refresh_race"] = map[string]any{
			"racers": *racers, "winners": winners, "losers": losers, "other": other,
		}
		if winners != 1 {
			m.Note("refresh race: %d winners — expected exactly 1", winners)
		}
		// Did the legitimate winner stay signed in?
		if winner != nil {
			me, err := winner.Me(ctx)
			winnerAlive := err == nil && me.Status == http.StatusOK
			extra["winner_session_alive"] = winnerAlive
			if !winnerAlive {
				m.Note("refresh race desync: winning session dead after losers' reuse (family revocation)")
				m.Inc("winner_desynced")
			} else {
				m.Inc("winner_survived")
			}
			// And can the winner rotate again?
			r2, err2 := winner.Refresh(ctx)
			extra["winner_can_rotate_again"] = err2 == nil && r2.Status == http.StatusOK
		}
	}

	// --- Phase 4: sequential refresh chains ------------------------------------
	m.SetPhase("refresh chains")
	// Each VU rotates serially; the newest session must keep working. Then one
	// deliberate stale-token replay must 401 and kill the family — the
	// anti-theft property.
	{
		var chainOK, chainBreaks, staleRejected, familyDead int64
		harness.Workers(ctx, maxVUs, func(ctx context.Context, vu int) {
			email := pool[(vu+*racers+1)%len(pool)]
			c := harness.NewClient(*base, m)
			res, err := c.SignIn(ctx, email, harness.SeedPassword)
			if err != nil || res.Status != http.StatusOK {
				m.Inc("chain_signin_failed")
				return
			}
			firstRefresh := c.RefreshToken()
			ok := true
			for i := 0; i < *rotations; i++ {
				r, err := c.Refresh(ctx)
				if err != nil || r.Status != http.StatusOK {
					ok = false
					break
				}
				me, err := c.Me(ctx)
				if err != nil || me.Status != http.StatusOK {
					ok = false
					break
				}
			}
			if ok {
				atomic.AddInt64(&chainOK, 1)
			} else {
				atomic.AddInt64(&chainBreaks, 1)
			}
			// Replay the original (now-spent) token: must 401, and the family
			// must die.
			stale := harness.NewClient(*base, m)
			r, err := stale.Refresh(ctx, firstRefresh)
			if err == nil && (r.Status == http.StatusUnauthorized || r.Status == http.StatusForbidden) {
				atomic.AddInt64(&staleRejected, 1)
			}
			me, err := c.Me(ctx)
			if err == nil && me.Status == http.StatusUnauthorized {
				atomic.AddInt64(&familyDead, 1)
			}
		})
		extra["refresh_chain"] = map[string]any{
			"ok": chainOK, "breaks": chainBreaks,
			"stale_rejected": staleRejected, "family_revoked": familyDead,
			"rotations": *rotations, "users": maxVUs,
		}
		if chainBreaks > 0 {
			m.Note("refresh chain: %d/%d users lost a session mid-chain", chainBreaks, maxVUs)
		}
		if familyDead < staleRejected {
			m.Note("stale replay accepted in %d cases without family revocation — anti-theft gap", staleRejected-familyDead)
		}
	}

	// --- Phase 5: verification-code desync --------------------------------------
	m.SetPhase("verify-code desync")
	// -verifyUsers accounts sign up at once; each scrapes the six-digit code
	// the api logged for its own address. Every code must verify only the
	// account it was issued to — a session that resolves to a different email
	// is the desync. Two targeted probes sit on top:
	//   - cross-use: A's code submitted for B must not produce a session.
	//   - resend rotation: a resend must kill the old code (the new one wins).
	{
		type acct struct {
			email  string
			code   string
			client *harness.Client
		}
		accts := make([]*acct, *verifyUsers)
		var signupWG sync.WaitGroup
		for i := range accts {
			a := &acct{email: fmt.Sprintf("%s-vfy-%d@loadtest.local", tag, i)}
			accts[i] = a
			signupWG.Add(1)
			go func(a *acct) {
				defer signupWG.Done()
				a.client = harness.NewClient(*base, m)
				res, err := a.client.SignUp(ctx, a.email, harness.SeedPassword, "LT "+a.email, "user")
				if err != nil || res.Status != http.StatusCreated {
					m.Inc("vfy_signup_failed")
					return
				}
				code, err := harness.VerificationCode(a.email, 15*time.Second)
				if err != nil {
					m.Inc("vfy_code_missing")
					return
				}
				a.code = code
			}(a)
		}
		signupWG.Wait()

		// Cross-use: account[0]'s code posted for account[1] while signed in
		// as account[1]. Expected 400 — a session for B would mean the codes
		// are interchangeable.
		var crossAccepted int64
		if *verifyUsers >= 2 && accts[0].code != "" && accts[1].client != nil {
			r, err := accts[1].client.Verify(ctx, accts[1].email, accts[0].code)
			switch {
			case err != nil:
				m.Inc("vfy_cross_transport_errors")
			case r.Status == http.StatusOK:
				// Any 200 means the foreign code was accepted — the desync.
				atomic.AddInt64(&crossAccepted, 1)
				var s harness.Session
				if r.Decode(&s) == nil && s.Token != "" && s.User.Email != accts[1].email {
					m.Failf("verify_wrong_account", "code for %s verified %s",
						accts[0].email, s.User.Email)
				}
				m.Note("verify desync: another account's code was accepted")
			case r.Status == http.StatusBadRequest || r.Status == http.StatusUnauthorized:
				m.Inc("code_cross_rejected")
			default:
				m.Failf("code_cross_unexpected", "cross-code verify → %d", r.Status)
			}
		}

		// Each account verifies with its own code; the returned session and
		// /me must both resolve to that account's email.
		var verifyOK int64
		var verifyWG sync.WaitGroup
		for _, a := range accts {
			if a.code == "" {
				continue
			}
			verifyWG.Add(1)
			go func(a *acct) {
				defer verifyWG.Done()
				res, err := a.client.Verify(ctx, a.email, a.code)
				if err != nil || res.Status != http.StatusOK {
					m.Failf("verify_failed", "%s verify → err=%v status=%d", a.email, err, harness.Status(res))
					return
				}
				var s harness.Session
				if err := res.Decode(&s); err == nil && s.Token != "" && s.User.Email != a.email {
					m.Failf("verify_wrong_account", "own-code verify of %s produced session for %s", a.email, s.User.Email)
					return
				}
				me, err := a.client.Me(ctx)
				var u harness.User
				if err != nil || me.Status != http.StatusOK || me.Decode(&u) != nil || u.Email != a.email {
					m.Failf("verify_desync", "%s: /me → err=%v status=%d email=%q", a.email, err, harness.Status(me), u.Email)
					return
				}
				atomic.AddInt64(&verifyOK, 1)
			}(a)
		}
		verifyWG.Wait()

		// Resend rotation on one fresh account: resend, confirm the old code
		// is dead and the new code verifies the same account.
		var resendNewOK, resendOldAccepted int64
		{
			a := &acct{email: fmt.Sprintf("%s-vfy-r@loadtest.local", tag)}
			a.client = harness.NewClient(*base, m)
			res, err := a.client.SignUp(ctx, a.email, harness.SeedPassword, "LT "+a.email, "user")
			if err == nil && res.Status == http.StatusCreated {
				if oldCode, err := harness.VerificationCode(a.email, 15*time.Second); err == nil {
					time.Sleep(1100 * time.Millisecond) // VERIFY_RESEND_COOLDOWN=1s in loadtest/.env
					if r, err := a.client.ResendVerification(ctx, a.email); err != nil || r.Status != http.StatusAccepted {
						m.Failf("resend_failed", "%s resend → err=%v status=%d", a.email, err, harness.Status(r))
					} else {
						// Wait for the new code email — the scraper returns
						// the latest logged code, which is still oldCode until
						// the resend lands in the log.
						var newCode string
						deadline := time.Now().Add(15 * time.Second)
						for time.Now().Before(deadline) && newCode == "" {
							if c, err := harness.VerificationCode(a.email, 2*time.Second); err == nil && c != oldCode {
								newCode = c
							}
						}
						if newCode == "" {
							m.Inc("resend_code_missing")
						} else {
							// Old code must be dead now — any 200 is a desync.
							stale := harness.NewClient(*base, m)
							if r, err := stale.Verify(ctx, a.email, oldCode); err == nil && r.Status == http.StatusOK {
								atomic.AddInt64(&resendOldAccepted, 1)
								m.Note("resend desync: superseded code still verified the account")
							}
							// New code must work and land on the same account.
							if r2, err := a.client.Verify(ctx, a.email, newCode); err == nil && r2.Status == http.StatusOK {
								var s harness.Session
								if r2.Decode(&s) == nil && (s.Token == "" || s.User.Email == a.email) {
									atomic.AddInt64(&resendNewOK, 1)
								} else {
									m.Failf("resend_verify_desync", "%s new-code session → %s", a.email, s.User.Email)
								}
							} else {
								m.Failf("resend_verify_failed", "%s new code verify → status=%d", a.email, harness.Status(r2))
							}
						}
					}
				}
			}
		}

		extra["verify_desync"] = map[string]any{
			"accounts":              *verifyUsers,
			"verified_own_code":     verifyOK,
			"cross_code_accepted":   crossAccepted,
			"resend_new_code_ok":    resendNewOK,
			"resend_old_code_still": resendOldAccepted,
		}
		if crossAccepted > 0 || resendOldAccepted > 0 {
			m.Note("verify-code desync detected: cross=%d stale_resend=%d", crossAccepted, resendOldAccepted)
		}
	}

	path, err := m.WriteReport(*out, "auth", extra)
	if err != nil {
		log.Fatalf("write report: %v", err)
	}
	fmt.Printf("auth report: %s\n", path)
}
