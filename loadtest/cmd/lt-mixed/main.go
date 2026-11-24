// lt-mixed is the thin interference test: it hammers several surfaces at
// once inside one process — auth churn, WS chat, analysis submits and
// booking races — and reports the shared blast radius: aggregate error rate,
// per-endpoint latencies under cross-load, pg_stat_statements deltas and
// pg_stat_activity pressure. It deliberately duplicates work the dedicated
// components already verify in isolation; its only job is to show whether
// running them together changes the answers.
//
// For per-surface invariants run lt-auth/lt-chat/lt-analysis/lt-booking;
// this component intentionally stays shallow.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/danielliu30/dating-coach/loadtest/internal/harness"
)

func main() {
	base := flag.String("base", "http://localhost", "gateway base URL")
	vus := flag.Int("vus", 8, "total VUs split across surfaces")
	duration := flag.Duration("duration", 30*time.Second, "load duration")
	out := flag.String("out", "out", "report output directory")
	flag.Parse()

	tag := harness.Tag("mix")
	ctx := context.Background()
	m := harness.NewMetrics()
	m.Progress(ctx, "mixed", harness.ProgressEvery, *out)
	defer harness.CleanupTag(tag)
	dbBefore, _ := harness.DBStats()

	emails, err := harness.SeedUsers(tag, 1, *vus, "user")
	if err != nil {
		log.Fatalf("seed users: %v", err)
	}
	coachID, err := harness.SeedCoach(tag)
	if err != nil {
		log.Fatalf("seed coach: %v", err)
	}
	slotBase := time.Now().UTC().Add(11 * 24 * time.Hour).Truncate(time.Hour)

	ctx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < *vus; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := harness.NewClient(*base, m)
			if res, err := c.SignIn(ctx, emails[i], harness.SeedPassword); err != nil || res.Status != http.StatusOK {
				m.Inc("signin_failed")
				return
			}
			switch i % 4 {
			case 0: // auth churn: me -> refresh -> signout -> signin
				for ctx.Err() == nil {
					if res, err := c.Me(ctx); err != nil || res.Status != http.StatusOK {
						m.Inc("churn_me_failed")
					}
					if res, err := c.Refresh(ctx); err != nil || res.Status != http.StatusOK {
						m.Inc("churn_refresh_failed")
						return
					}
					harness.Pace(ctx, 400*time.Millisecond)
				}
			case 1: // chat: thread + sends
				thread, res, err := c.StartThread(ctx, coachID)
				if err != nil || res.Status != http.StatusCreated && res.Status != http.StatusOK {
					m.Inc("mixed_thread_failed")
					return
				}
				sock, err := c.OpenChat(ctx, thread.ID)
				if err != nil {
					m.Inc("mixed_ws_failed")
					return
				}
				defer sock.Close()
				n := 0
				for ctx.Err() == nil {
					if err := sock.Send(ctx, fmt.Sprintf("lt-mix-%d", n), fmt.Sprintf("mix-%d-%d", i, n)); err != nil {
						m.Inc("mixed_send_failed")
						return
					}
					n++
					harness.Pace(ctx, 300*time.Millisecond)
				}
			case 2: // analysis submits on the same cadence as lt-analysis
				n := 0
				for ctx.Err() == nil {
					msgs := make([]harness.SubmitMessage, 6)
					for j := range msgs {
						sender := "self"
						if j%2 == 1 {
							sender = "match"
						}
						msgs[j] = harness.SubmitMessage{Sender: sender, Body: fmt.Sprintf("lt mix %d msg %d", n, j)}
					}
					if _, res, err := c.SubmitConversation(ctx, fmt.Sprintf("lt mix %d", n), "Match", msgs); err != nil ||
						res.Status != http.StatusAccepted && res.Status != http.StatusCreated {
						m.Inc("mixed_submit_failed")
					} else {
						m.Inc("mixed_submitted")
					}
					n++
					harness.Pace(ctx, 3*time.Second)
				}
			case 3: // booking attempts on distinct slots
				n := 0
				for ctx.Err() == nil {
					slot := slotBase.Add(time.Duration(2*n+i) * time.Hour).Format(time.RFC3339)
					s, res, err := c.BookSession(ctx, coachID, slot, 60, "lt mix")
					switch {
					case err != nil:
						m.Inc("mixed_book_transport")
					case res.Status == http.StatusCreated:
						m.Inc("mixed_booked")
						_, _ = c.CancelSession(ctx, s.ID)
					case res.Status == http.StatusConflict:
						m.Inc("mixed_book_conflict")
					default:
						m.Inc("mixed_book_unexpected")
					}
					n++
					harness.Pace(ctx, 800*time.Millisecond)
				}
			}
		}(i)
	}
	wg.Wait()

	extra := map[string]any{}
	if stats, err := harness.DBStats(); err == nil {
		extra["db_top_deltas"] = harness.TopDeltas(harness.DiffStats(dbBefore, stats), 15)
	}
	if act, err := harness.PgActivity(); err == nil {
		extra["pg_activity"] = act
	}
	path, err := m.WriteReport(*out, "mixed", extra)
	if err != nil {
		log.Fatalf("write report: %v", err)
	}
	fmt.Printf("mixed report: %s\n", path)
}
