// lt-auth load-tests the auth surface while asserting correctness:
//
//  1. signup burst — concurrent signups with unique emails must each get a
//     session; a deliberate duplicate-email race must produce exactly one 201
//     and 409s for the rest.
//  2. signin churn — staged VUs loop signin -> me -> signout on their own
//     seeded accounts; identity must never cross accounts.
//  3. refresh race — N clients concurrently rotate the same refresh token.
//     Rotation is transactional single-use: exactly one should win. A loser
//     is "token reuse" and revokes the whole session family, which signs out
//     the legitimate winner too — the desync this suite quantifies.
//  4. refresh chain — each VU rotates serially and the newest session must
//     keep working; a stale-token replay at the end must 401 and kill the
//     family (the designed anti-theft behavior).
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
	vus := flag.Int("vus", 5, "concurrent virtual users for the churn stages")
	duration := flag.Duration("duration", 20*time.Second, "duration of each churn stage")
	racers := flag.Int("racers", 8, "clients racing one refresh token in the desync probe")
	rotations := flag.Int("rotations", 4, "refresh rotations per user in the chain stage")
	out := flag.String("out", "out", "report output directory")
	flag.Parse()

	tag := harness.Tag("auth")
	ctx := context.Background()
	m := harness.NewMetrics()
	defer harness.CleanupTag(tag)

	emails, err := harness.SeedUsers(tag, 1, *vus+*racers+2, "user")
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
		res := harness.Run(ctx, m, []harness.Stage{{Name: "signup", VUs: *vus, Duration: *duration}},
			func(ctx context.Context, vu int) {
				for ctx.Err() == nil {
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
							m.Inc("signup_bad_session")
						}
					default:
						m.Inc("signup_unexpected_status")
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
		res := harness.Run(ctx, m, []harness.Stage{{Name: "signin", VUs: *vus, Duration: *duration}},
			func(ctx context.Context, vu int) {
				email := pool[vu%len(pool)]
				for ctx.Err() == nil {
					c := harness.NewClient(*base, m)
					res, err := c.SignIn(ctx, email, harness.SeedPassword)
					switch {
					case err != nil:
						m.Inc("signin_transport_errors")
						continue
					case res.Status != http.StatusOK:
						m.Inc("signin_rejected")
						continue
					}
					var s harness.Session
					if err := res.Decode(&s); err != nil || s.User.Email != email || s.Token == "" {
						m.Inc("signin_bad_session")
						continue
					}
					me, err := c.Me(ctx)
					if err != nil || me.Status != http.StatusOK {
						m.Inc("me_failed")
						continue
					}
					var u harness.User
					if err := me.Decode(&u); err != nil || u.Email != email || u.ID != s.User.ID {
						m.Inc("identity_crossed")
						continue
					}
					out, err := c.SignOut(ctx)
					if err != nil || out.Status != http.StatusOK {
						m.Inc("signout_failed")
					}
					m.Inc("churn_round_trips")
				}
			}, harness.ErrorRateTripwire(0.01))
		extra["signin_stage"] = res
	}

	// --- Phase 3: refresh race (desync probe) ---------------------------------
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
	// Each VU rotates serially; the newest session must keep working. Then one
	// deliberate stale-token replay must 401 and kill the family — the
	// anti-theft property.
	{
		var chainOK, chainBreaks, staleRejected, familyDead int64
		harness.Workers(ctx, *vus, func(ctx context.Context, vu int) {
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
			"rotations": *rotations, "users": *vus,
		}
		if chainBreaks > 0 {
			m.Note("refresh chain: %d/%d users lost a session mid-chain", chainBreaks, *vus)
		}
		if familyDead < staleRejected {
			m.Note("stale replay accepted in %d cases without family revocation — anti-theft gap", staleRejected-familyDead)
		}
	}

	path, err := m.WriteReport(*out, "auth", extra)
	if err != nil {
		log.Fatalf("write report: %v", err)
	}
	fmt.Printf("auth report: %s\n", path)
}
