// lt-booking load-tests the coaching-booking surface while asserting slot
// and lifecycle correctness:
//
//  0. churn — staged VUs book+cancel distinct far-future slots at -rps
//     aggregate; every -race-every iterations each VU targets the shared
//     rotating slot instead, so same-slot contention fires continuously.
//     Distinct-slot conflicts are impossible by construction and counted.
//  1. slot race — every VU books the same coach+time concurrently; exactly
//     one 201 may win, losers must get a clean 409, and the DB must hold
//     exactly one session for that slot (exclusion constraint is the last
//     line of defense).
//  2. respond race — each VU books a distinct pending slot, then confirm
//     and decline are fired concurrently as the coach: exactly one action
//     may succeed and the final status must match the winner.
//  3. cancel race — a user's cancel races the coach's confirm on one
//     pending request; final status must be consistent.
//  4. expiry — a pending request's respond_by is forced into the past; the
//     worker's minute sweep must flip it to 'expired' and free the slot,
//     which is then rebooked successfully.
//  5. overlap invariant — the DB must contain zero overlapping active
//     sessions for the seeded coach when the run ends.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danielliu30/dating-coach/loadtest/internal/harness"
)

func main() {
	base := flag.String("base", "http://localhost", "gateway base URL")
	stagesCSV := flag.String("vus", "10,20,50", "comma-separated VU counts per churn stage")
	rps := flag.Float64("rps", 150, "aggregate booking attempts per second per stage (0 = unpaced)")
	duration := flag.Duration("duration", 30*time.Second, "duration of each churn stage")
	raceEvery := flag.Int64("race-every", 100, "each VU's Nth booking targets the shared rotating slot")
	out := flag.String("out", "out", "report output directory")
	expireWait := flag.Duration("expire-wait", 90*time.Second, "max wait for the expiry sweep")
	flag.Parse()

	stages, err := harness.Stages(*stagesCSV, *duration)
	if err != nil {
		log.Fatalf("%v", err)
	}
	maxVUs := harness.MaxVUs(stages)

	tag := harness.Tag("book")
	ctx := context.Background()
	m := harness.NewMetrics()
	defer harness.CleanupTag(tag)

	emails, err := harness.SeedUsers(tag, 1, maxVUs, "user")
	if err != nil {
		log.Fatalf("seed users: %v", err)
	}
	coachID, err := harness.SeedCoach(tag)
	if err != nil {
		log.Fatalf("seed coach: %v", err)
	}
	coachEmail := tag + "-coach@loadtest.local"

	users := make([]*harness.Client, maxVUs)
	for i := range users {
		c := harness.NewClient(*base, m)
		res, err := c.SignIn(ctx, emails[i], harness.SeedPassword)
		if err != nil || res.Status != http.StatusOK {
			log.Fatalf("signin %s: %v", emails[i], err)
		}
		users[i] = c
	}
	coach := harness.NewClient(*base, m)
	if res, err := coach.SignIn(ctx, coachEmail, harness.SeedPassword); err != nil || res.Status != http.StatusOK {
		log.Fatalf("coach signin: %v", err)
	}
	coach2 := harness.NewClient(*base, m)
	if res, err := coach2.SignIn(ctx, coachEmail, harness.SeedPassword); err != nil || res.Status != http.StatusOK {
		log.Fatalf("coach2 signin: %v", err)
	}

	extra := map[string]any{}
	baseSlot := time.Now().UTC().Add(7 * 24 * time.Hour).Truncate(time.Hour)

	// --- Phase 0: sustained booking churn -------------------------------------
	// Distinct-slot book+cancel at the aggregate rate; every -race-every
	// iterations a VU targets the shared rotating slot so same-slot
	// contention keeps firing through the stage. Distinct-slot conflicts are
	// impossible by construction — any that appear are a real bug.
	{
		soloBase := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Hour)
		hotBase := time.Now().UTC().Add(90 * 24 * time.Hour).Truncate(time.Hour)
		res := harness.RunRate(ctx, m, stages, *rps,
			func(ctx context.Context, vu int, rate *harness.Rate) {
				c := users[vu]
				var seq int64
				for rate.Wait(ctx) {
					seq++
					var slot time.Time
					shared := seq%*raceEvery == 0
					if shared {
						slot = hotBase.Add(time.Duration(seq/(*raceEvery)) * 2 * time.Hour)
					} else {
						slot = soloBase.Add(time.Duration(vu)*20000*time.Hour + time.Duration(seq)*2*time.Hour)
					}
					s, res, err := c.BookSession(ctx, coachID, slot.Format(time.RFC3339), 60, "lt churn")
					switch {
					case err != nil:
						m.Inc("churn_transport_errors")
					case res.Status == http.StatusCreated:
						if shared {
							m.Inc("shared_booked")
						} else {
							m.Inc("solo_booked")
						}
						if _, err := c.CancelSession(ctx, s.ID); err != nil {
							m.Inc("churn_cancel_failed")
						}
					case res.Status == http.StatusConflict:
						if shared {
							m.Inc("shared_conflict")
						} else {
							m.Inc("solo_conflict")
						}
					default:
						m.Inc("churn_unexpected")
					}
				}
			}, harness.ErrorRateTripwire(0.01))
		extra["churn_stage"] = res
		if n := m.Count("solo_conflict"); n > 0 {
			m.Note("churn: %d conflicts on distinct slots — slot allocation overlapped", n)
		}
	}

	// --- Phase 1: same-slot race ---------------------------------------------
	{
		slot := baseSlot.Format(time.RFC3339)
		var winners, conflicts, other int64
		var winSession harness.BookingSession
		var mu sync.Mutex
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, u := range users {
			wg.Add(1)
			go func(u *harness.Client) {
				defer wg.Done()
				<-start
				s, res, err := u.BookSession(ctx, coachID, slot, 60, "lt race")
				switch {
				case err != nil:
					m.Inc("race_transport_errors")
				case res.Status == http.StatusCreated:
					atomic.AddInt64(&winners, 1)
					mu.Lock()
					winSession = s
					mu.Unlock()
				case res.Status == http.StatusConflict:
					atomic.AddInt64(&conflicts, 1)
				default:
					atomic.AddInt64(&other, 1)
					m.Note("slot race loser got unexpected status %d: %s", res.Status, res.ErrBody())
				}
			}(u)
		}
		close(start)
		wg.Wait()
		n := countSessions(coachID, slot)
		extra["slot_race"] = map[string]any{
			"bookers": maxVUs, "winners": winners, "conflicts": conflicts,
			"unexpected": other, "db_sessions": n,
		}
		switch {
		case winners != 1:
			m.Note("slot race: %d winners — expected exactly 1", winners)
		case n != 1:
			m.Note("slot race: db holds %d sessions for the slot — expected 1", n)
		}
		if winSession.ID != "" {
			cancelSessionQuiet(ctx, coach, winSession.ID)
		}
	}

	// --- Phase 2: respond race -------------------------------------------------
	// Each VU books a distinct pending slot; coach confirm and decline are
	// fired concurrently. Exactly one may succeed; the stored status must
	// match the winning action.
	{
		var won, finalConsistent, finalInconsistent int64
		var wg sync.WaitGroup
		for i, u := range users {
			slot := baseSlot.Add(time.Duration(2+i) * 2 * time.Hour).Format(time.RFC3339)
			s, res, err := u.BookSession(ctx, coachID, slot, 60, "lt respond")
			if err != nil || res.Status != http.StatusCreated {
				m.Inc("respond_setup_book_failed")
				continue
			}
			wg.Add(1)
			go func(u *harness.Client, s harness.BookingSession) {
				defer wg.Done()
				type outcome struct {
					action string
					status int
				}
				results := make(chan outcome, 2)
				respond := func(c *harness.Client, action string) {
					r, err := c.RespondAsCoach(ctx, s.ID, action)
					if err != nil {
						results <- outcome{action, 0}
						return
					}
					results <- outcome{action, r.Status}
				}
				go respond(coach, "confirm")
				go respond(coach2, "decline")
				o1, o2 := <-results, <-results
				st := map[string]int{o1.action: o1.status, o2.action: o2.status}
				var winningAction string
				switch {
				case st["confirm"] == http.StatusOK && st["decline"] == http.StatusOK:
					m.Note("respond race: both confirm and decline returned 200 for one session")
					m.Inc("respond_double_win")
					return
				case st["confirm"] == http.StatusOK:
					winningAction = "confirm"
				case st["decline"] == http.StatusOK:
					winningAction = "decline"
				default:
					m.Inc("respond_no_winner")
					return
				}
				atomic.AddInt64(&won, 1)
				want := map[string]string{"confirm": "scheduled", "decline": "declined"}[winningAction]
				final := sessionStatus(s.ID)
				if final == want {
					atomic.AddInt64(&finalConsistent, 1)
				} else {
					atomic.AddInt64(&finalInconsistent, 1)
					m.Note("respond race: %s won but final status is %q", winningAction, final)
				}
			}(u, s)
		}
		wg.Wait()
		extra["respond_race"] = map[string]any{
			"sessions": maxVUs, "decided": won,
			"final_consistent": finalConsistent, "final_inconsistent": finalInconsistent,
		}
	}

	// --- Phase 3: cancel-vs-confirm race ---------------------------------------
	{
		u := users[0]
		slot := baseSlot.Add(48 * time.Hour).Format(time.RFC3339)
		s, res, err := u.BookSession(ctx, coachID, slot, 60, "lt cancel")
		if err == nil && res.Status == http.StatusCreated {
			type outcome struct {
				who    string
				status int
			}
			results := make(chan outcome, 2)
			go func() {
				r, err := u.CancelSession(ctx, s.ID)
				if err != nil {
					results <- outcome{"cancel", 0}
					return
				}
				results <- outcome{"cancel", r.Status}
			}()
			go func() {
				r, err := coach.RespondAsCoach(ctx, s.ID, "confirm")
				if err != nil {
					results <- outcome{"confirm", 0}
					return
				}
				results <- outcome{"confirm", r.Status}
			}()
			o1, o2 := <-results, <-results
			st := map[string]int{o1.who: o1.status, o2.who: o2.status}
			final := sessionStatus(s.ID)
			extra["cancel_race"] = map[string]any{
				"cancel_status": st["cancel"], "confirm_status": st["confirm"], "final": final,
			}
			// Exactly one side may win, and the stored status must match the
			// side that got the 200.
			want := map[int]string{}
			if st["cancel"] == http.StatusOK {
				want[http.StatusOK] = "cancelled"
			}
			if st["confirm"] == http.StatusOK {
				want[http.StatusOK] = "scheduled"
			}
			ok := len(want) == 1 && final == want[http.StatusOK]
			if !ok {
				m.Note("cancel race: cancel=%d confirm=%d left final %q", st["cancel"], st["confirm"], final)
			}
		} else {
			m.Note("cancel race setup booking failed: %v status %d", err, statusOf(res))
		}
	}

	// --- Phase 4: expiry sweep + slot release ----------------------------------
	{
		u := users[1]
		slot := baseSlot.Add(56 * time.Hour).Format(time.RFC3339)
		s, res, err := u.BookSession(ctx, coachID, slot, 60, "lt expiry")
		if err == nil && res.Status == http.StatusCreated {
			if _, err := harness.Psql(fmt.Sprintf(
				`UPDATE coaching_sessions SET respond_by = now() - interval '1 minute' WHERE id = '%s'`, s.ID)); err != nil {
				m.Note("expiry setup failed: %v", err)
			} else {
				deadline := time.Now().Add(*expireWait)
				var final string
				for time.Now().Before(deadline) {
					final = sessionStatus(s.ID)
					if final == "expired" {
						break
					}
					time.Sleep(2 * time.Second)
				}
				if final != "expired" {
					m.Note("expiry sweep: status stayed %q past respond_by within %s", final, *expireWait)
				} else {
					// Slot must be free: rebook it.
					s2, res2, err2 := u.BookSession(ctx, coachID, slot, 60, "lt expiry rebook")
					switch {
					case err2 != nil:
						m.Inc("rebook_transport_errors")
					case res2.Status == http.StatusCreated:
						m.Inc("rebook_after_expiry_ok")
						cancelSessionQuiet(ctx, u, s2.ID)
					default:
						m.Inc("rebook_after_expiry_failed")
						m.Note("rebook after expiry got %d: %s", res2.Status, res2.ErrBody())
					}
				}
				extra["expiry"] = map[string]any{"final": final}
			}
		}
	}

	// --- Phase 5: overlap invariant --------------------------------------------
	if n := countOverlaps(coachID); n != 0 {
		m.Note("overlap invariant: %d overlapping active sessions remain", n)
		extra["overlap_violations"] = n
	} else {
		extra["overlap_violations"] = 0
	}

	path, err := m.WriteReport(*out, "booking", extra)
	if err != nil {
		log.Fatalf("write report: %v", err)
	}
	fmt.Printf("booking report: %s\n", path)
}

// sessionStatus reads the stored status straight from the DB — the
// authoritative answer, not what any handler claimed.
func sessionStatus(id string) string {
	out, err := harness.Psql(fmt.Sprintf(`SELECT status FROM coaching_sessions WHERE id = '%s'`, id))
	if err != nil {
		return "query_failed"
	}
	return strings.TrimSpace(strings.Split(out, "\n")[0])
}

// countSessions counts all sessions for a coach at an exact scheduled_time.
func countSessions(coachID, slot string) int {
	out, err := harness.Psql(fmt.Sprintf(
		`SELECT count(*) FROM coaching_sessions WHERE coach_id = '%s' AND scheduled_time = '%s'`, coachID, slot))
	if err != nil {
		return -1
	}
	var n int
	fmt.Sscanf(out, "%d", &n)
	return n
}

// countOverlaps counts pairs of non-terminal sessions whose windows overlap.
func countOverlaps(coachID string) int {
	out, err := harness.Psql(fmt.Sprintf(`SELECT count(*) FROM coaching_sessions a
JOIN coaching_sessions b ON a.coach_id = b.coach_id AND a.id < b.id
WHERE a.coach_id = '%s'
  AND a.status NOT IN ('declined','expired','cancelled','no_show')
  AND b.status NOT IN ('declined','expired','cancelled','no_show')
  AND a.scheduled_time < b.scheduled_time + b.duration_minutes * interval '1 minute'
  AND b.scheduled_time < a.scheduled_time + a.duration_minutes * interval '1 minute'`, coachID))
	if err != nil {
		return -1
	}
	var n int
	fmt.Sscanf(out, "%d", &n)
	return n
}

func cancelSessionQuiet(ctx context.Context, c *harness.Client, id string) {
	_, _ = c.CancelSession(ctx, id)
}

func statusOf(r *harness.Resp) int {
	if r == nil {
		return 0
	}
	return r.Status
}
