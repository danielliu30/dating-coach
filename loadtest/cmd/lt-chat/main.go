// lt-chat load-tests the live-chat surface while asserting delivery and
// persistence correctness:
//
//   - Each VU owns one seeded account, creates a thread with the seeded
//     coach, opens the websocket, and sends -messages frames paced at
//     -rps aggregate (or -pace when -rps is 0) with unique client_ids.
//   - A reader per socket matches echoes by client_id; missing or
//     mismatched echoes are counted.
//   - After each cycle the REST history is paged through and every sent
//     body must appear exactly once — duplicates or gaps are counted.
//   - -slow sockets join the busiest thread but never read: the hub's
//     bounded buffer should drop their events (visible in api logs)
//     without hurting other subscribers' echoes.
//   - A share of sends also goes over REST POST /messages to cover the
//     non-WS path.
//   - Fan-out phase: the coach replies into every thread at once. Each
//     client's socket must see only its own thread's tagged replies —
//     a foreign tag on the wrong socket is a fan-out routing bug.
//
// The component never logs tokens or message contents beyond the
// synthetic lt-<vu>-<seq> bodies it generated.
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

const (
	echoWait    = 10 * time.Second
	pageLimit   = 100
	historyMaxI = 20 // page cap per verification pass
)

func main() {
	base := flag.String("base", "http://localhost", "gateway base URL")
	stagesCSV := flag.String("vus", "10,20,50", "comma-separated VU counts per ramp stage")
	rps := flag.Float64("rps", 150, "aggregate message sends per second per stage (0 = paced by -pace)")
	duration := flag.Duration("duration", 30*time.Second, "duration of each stage")
	messages := flag.Int("messages", 30, "messages per VU per cycle")
	pace := flag.Duration("pace", 200*time.Millisecond, "delay between sends within a cycle when -rps is 0")
	slow := flag.Int("slow", 2, "sockets that connect and never read")
	restShare := flag.Float64("rest-share", 0.1, "fraction of messages sent over REST instead of WS")
	fanMsgs := flag.Int("fan-msgs", 10, "coach replies per thread in the fan-out phase")
	out := flag.String("out", "out", "report output directory")
	flag.Parse()

	stages, err := harness.Stages(*stagesCSV, *duration)
	if err != nil {
		log.Fatalf("%v", err)
	}
	maxVUs := harness.MaxVUs(stages)

	tag := harness.Tag("chat")
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

	// Pre-sign-in every VU and pre-create its thread so sockets are the only
	// per-stage setup. Signin through the real API keeps this honest.
	clients := make([]*harness.Client, maxVUs)
	threads := make([]string, maxVUs)
	for i := 0; i < maxVUs; i++ {
		c := harness.NewClient(*base, m)
		res, err := c.SignIn(ctx, emails[i], harness.SeedPassword)
		if err != nil || res.Status != http.StatusOK {
			log.Fatalf("signin %s: %v", emails[i], err)
		}
		t, res2, err := c.StartThread(ctx, coachID)
		if err != nil || res2.Status != http.StatusCreated {
			log.Fatalf("start thread for %s: %v (status %d)", emails[i], err, res2.Status)
		}
		clients[i], threads[i] = c, t.ID
	}
	m.Note("seeded %d users, coach, %d threads", maxVUs, maxVUs)

	// Slow sockets on the first threads: subscribed, buffer fills, never read.
	var slowSocks []*harness.Socket
	for i := 0; i < *slow && i < maxVUs; i++ {
		s, err := clients[i].OpenChat(ctx, threads[i])
		if err != nil {
			m.Note("slow socket %d failed to open: %v", i, err)
			continue
		}
		slowSocks = append(slowSocks, s)
	}
	extra := map[string]any{"threads": maxVUs, "coach": coachID != ""}

	trip := func(mm *harness.Metrics) (bool, string) {
		if ok, reason := harness.ErrorRateTripwire(0.01)(mm); ok {
			return true, reason
		}
		if l := mm.Count("echo_missing"); l > 0 {
			return true, fmt.Sprintf("%d echoes never arrived", l)
		}
		return false, ""
	}

	res := harness.RunRate(ctx, m, stages, *rps, func(ctx context.Context, vu int, rate *harness.Rate) {
		c, threadID := clients[vu], threads[vu]
		sock, err := c.OpenChat(ctx, threadID)
		if err != nil {
			m.Inc("ws_open_failed")
			return
		}
		defer sock.Close()
		m.Inc("ws_open_ok")

		pending := sync.Map{} // clientID -> body
		var rd sync.WaitGroup
		rd.Add(1)
		// The reader outlives the stage context so echoes in flight at
		// stage end still resolve before the socket closes — otherwise a
		// canceled read is miscounted as a missing echo.
		readCtx := context.WithoutCancel(ctx)
		go func() {
			defer rd.Done()
			for {
				ev, err := sock.Read(readCtx)
				if err != nil {
					return
				}
				switch ev.Type {
				case "message":
					if body, ok := pending.Load(ev.ClientID); ok {
						if body.(string) == ev.Body {
							m.Inc("echo_ok")
						} else {
							m.Inc("echo_body_mismatch")
						}
						pending.Delete(ev.ClientID)
					} else if ev.ClientID != "" {
						m.Inc("echo_unknown_client_id")
					}
				case "error":
					m.Inc("send_rejected")
					pending.Delete(ev.ClientID)
				case "history", "typing", "presence":
					m.Inc("event_" + ev.Type)
				}
			}
		}()

		var seq int64
		for ctx.Err() == nil {
			// One cycle: -messages sends, drain echoes, verify history.
			sent := map[string]string{}
			for i := 0; i < *messages; i++ {
				n := atomic.AddInt64(&seq, 1)
				cid := fmt.Sprintf("%d-%d", vu, n)
				body := fmt.Sprintf("lt-%d-%d", vu, n)
				sent[body] = cid
				useREST := float64(i)/float64(*messages) < *restShare
				if useREST {
					r, err := c.Raw(ctx, http.MethodPost,
						fmt.Sprintf("/api/v1/chat/threads/%s/messages", threadID),
						"POST /chat/threads/{id}/messages", map[string]string{"body": body},
						map[string]string{"Authorization": "Bearer " + c.Token()})
					if err != nil || (r.Status != http.StatusCreated && r.Status != http.StatusOK) {
						m.Inc("rest_send_failed")
					} else {
						m.Inc("rest_send_ok")
						// REST sends carry no client_id, so their WS echo
						// can't be correlated; persistence check covers them.
					}
				} else {
					pending.Store(cid, body)
					if err := sock.Send(ctx, body, cid); err != nil {
						pending.Delete(cid)
						m.Inc("ws_send_failed")
					}
				}
				if i%5 == 0 {
					_ = sock.Typing(ctx, true)
					m.Inc("typing_sent")
				}
				if rate != nil {
					rate.Wait(ctx)
				} else {
					harness.Pace(ctx, *pace)
				}
			}
			m.Add("messages_sent", int64(len(sent)))

			// Drain echoes: unresolved client_ids are missing echoes.
			drain(m, &pending)

			// Persistence: page history, every sent body exactly once. A
			// failed page means unverifiable, not missing.
			seen := map[string]int{}
			verified := true
			for page := 0; page < historyMaxI; page++ {
				msgs, r, err := historyPage(ctx, c, threadID, page)
				if err != nil || r.Status != http.StatusOK {
					m.Inc("history_failed")
					verified = false
					break
				}
				for _, msg := range msgs {
					if strings.HasPrefix(msg.Body, "lt-") {
						seen[msg.Body]++
					}
				}
				if len(msgs) < pageLimit {
					break
				}
			}
			for body := range sent {
				if !verified {
					m.Inc("persist_unverified")
					continue
				}
				switch seen[body] {
				case 1:
					m.Inc("persist_ok")
				case 0:
					m.Inc("persist_missing")
				default:
					m.Inc("persist_dup")
				}
			}
		}
		// Stage ended: give in-flight sends one last echo window on the
		// still-open socket before it goes away.
		drain(m, &pending)
		sock.Close()
		rd.Wait()
	}, trip)

	extra["ramp"] = res

	// --- Fan-out phase --------------------------------------------------------
	// One coach, many client threads: the coach answers every thread at once
	// over REST while every client holds an open socket. A socket may only
	// ever see its own thread's tagged replies — a lt-fan-t<j> body arriving
	// on thread i's socket (j != i) is a cross-thread routing bug.
	{
		coach := harness.NewClient(*base, m)
		if res, err := coach.SignIn(ctx, tag+"-coach@loadtest.local", harness.SeedPassword); err != nil || res.Status != http.StatusOK {
			m.Note("fan-out: coach signin failed — phase skipped")
		} else {
			got := make([]int64, maxVUs)
			readCtx, stopReads := context.WithCancel(ctx)
			var fanWG sync.WaitGroup
			var fanSocks []*harness.Socket
			for i := 0; i < maxVUs; i++ {
				sock, err := clients[i].OpenChat(ctx, threads[i])
				if err != nil {
					m.Inc("fan_ws_open_failed")
					continue
				}
				fanSocks = append(fanSocks, sock)
				fanWG.Add(1)
				go func(vu int, s *harness.Socket) {
					defer fanWG.Done()
					own := fmt.Sprintf("lt-fan-t%d-", vu)
					for {
						ev, err := s.Read(readCtx)
						if err != nil {
							return
						}
						if ev.Type != "message" {
							continue
						}
						switch {
						case strings.HasPrefix(ev.Body, own):
							atomic.AddInt64(&got[vu], 1)
						case strings.HasPrefix(ev.Body, "lt-fan-t"):
							m.Inc("fanout_cross_thread")
						default:
							m.Inc("fanout_unexpected_body")
						}
					}
				}(i, sock)
			}

			// Coach replies into every thread at the stage rate.
			rate := harness.NewRate(*rps)
			sent := make([]int, maxVUs)
			for i := 0; i < maxVUs && rate.Wait(ctx); i++ {
				for seq := 0; seq < *fanMsgs; seq++ {
					if !rate.Wait(ctx) {
						break
					}
					body := fmt.Sprintf("lt-fan-t%d-%d", i, seq)
					r, err := coach.Raw(ctx, http.MethodPost,
						fmt.Sprintf("/api/v1/chat/threads/%s/messages", threads[i]),
						"POST /chat/threads/{id}/messages", map[string]string{"body": body},
						map[string]string{"Authorization": "Bearer " + coach.Token()})
					if err != nil || (r.Status != http.StatusCreated && r.Status != http.StatusOK) {
						m.Inc("fan_send_failed")
						continue
					}
					sent[i]++
				}
			}
			rate.Close()

			// Let in-flight fan events land, then stop the readers.
			time.Sleep(echoWait)
			stopReads()
			fanWG.Wait()
			for _, s := range fanSocks {
				s.Close()
			}

			var delivered, missing int64
			for i := 0; i < maxVUs; i++ {
				delivered += got[i]
				if d := int64(sent[i]) - got[i]; d > 0 {
					missing += d
					m.Note("fan-out: thread %d got %d/%d coach replies on its socket", i, got[i], sent[i])
				}
			}
			extra["fanout"] = map[string]any{
				"threads": maxVUs, "sent_per_thread": *fanMsgs,
				"delivered": delivered, "missing": missing,
				"cross_thread": m.Count("fanout_cross_thread"),
			}

			// Persistence: each fan reply must live in exactly its own thread.
			for i := 0; i < maxVUs && sent[i] > 0; i++ {
				seen := 0
				for page := 0; page < historyMaxI; page++ {
					msgs, r, err := historyPage(ctx, clients[i], threads[i], page)
					if err != nil || r.Status != http.StatusOK {
						break
					}
					for _, msg := range msgs {
						if strings.HasPrefix(msg.Body, fmt.Sprintf("lt-fan-t%d-", i)) {
							seen++
						}
					}
					if len(msgs) < pageLimit {
						break
					}
				}
				if seen == sent[i] {
					m.Inc("fanout_persist_ok")
				} else {
					m.Inc("fanout_persist_missing")
				}
			}
		}
	}

	if len(slowSocks) > 0 {
		if n, err := harness.CountLogLines("api", "30m", "dropping chat event for slow client"); err == nil {
			extra["slow_client_drops"] = n
			m.Note("api logged %d slow-client event drops across %d unread sockets", n, len(slowSocks))
		}
	}
	for _, s := range slowSocks {
		s.Close()
	}

	path, err := m.WriteReport(*out, "chat", extra)
	if err != nil {
		log.Fatalf("write report: %v", err)
	}
	fmt.Printf("chat report: %s\n", path)
}

// drain waits up to echoWait for pending client_ids to resolve, then counts
// whatever remains as missing echoes.
func drain(m *harness.Metrics, pending *sync.Map) {
	deadline := time.Now().Add(echoWait)
	for time.Now().Before(deadline) {
		empty := true
		pending.Range(func(_, _ any) bool { empty = false; return false })
		if empty {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	var lost int64
	pending.Range(func(k, _ any) bool {
		lost++
		pending.Delete(k)
		return true
	})
	m.Add("echo_missing", lost)
}

// historyPage fetches one 1-based page of thread history.
func historyPage(ctx context.Context, c *harness.Client, threadID string, page int) ([]harness.Message, *harness.Resp, error) {
	res, err := c.Raw(ctx, http.MethodGet,
		fmt.Sprintf("/api/v1/chat/threads/%s/messages?limit=%d&offset=%d", threadID, pageLimit, page*pageLimit),
		"GET /chat/threads/{id}/messages", nil,
		map[string]string{"Authorization": "Bearer " + c.Token()})
	if err != nil || res.Status != http.StatusOK {
		return nil, res, err
	}
	var out struct {
		Messages []harness.Message `json:"messages"`
	}
	if err := res.Decode(&out); err != nil {
		return nil, res, err
	}
	return out.Messages, res, nil
}
