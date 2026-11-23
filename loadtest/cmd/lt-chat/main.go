// lt-chat load-tests the live-chat surface while asserting delivery and
// persistence correctness:
//
//   - Each VU owns one seeded account, creates a thread with the seeded
//     coach, opens the websocket, and sends -messages frames paced at
//     -pace with unique client_ids.
//   - A reader per socket matches echoes by client_id; missing or
//     mismatched echoes are counted.
//   - After each cycle the REST history is paged through and every sent
//     body must appear exactly once — duplicates or gaps are counted.
//   - -slow sockets join the busiest thread but never read: the hub's
//     bounded buffer should drop their events (visible in api logs)
//     without hurting other subscribers' echoes.
//   - A share of sends also goes over REST POST /messages to cover the
//     non-WS path.
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
	"strconv"
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
	stagesCSV := flag.String("vus", "5", "comma-separated VU counts per ramp stage")
	duration := flag.Duration("duration", 20*time.Second, "duration of each stage")
	messages := flag.Int("messages", 30, "messages per VU per cycle")
	pace := flag.Duration("pace", 200*time.Millisecond, "delay between sends within a cycle")
	slow := flag.Int("slow", 2, "sockets that connect and never read")
	restShare := flag.Float64("rest-share", 0.1, "fraction of messages sent over REST instead of WS")
	out := flag.String("out", "out", "report output directory")
	flag.Parse()

	var vusList []int
	for _, s := range strings.Split(*stagesCSV, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 1 {
			log.Fatalf("bad -vus list %q", *stagesCSV)
		}
		vusList = append(vusList, n)
	}
	maxVUs := 0
	for _, n := range vusList {
		if n > maxVUs {
			maxVUs = n
		}
	}

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

	stages := make([]harness.Stage, len(vusList))
	for i, n := range vusList {
		stages[i] = harness.Stage{Name: fmt.Sprintf("%dvu", n), VUs: n, Duration: *duration}
	}

	trip := func(mm *harness.Metrics) (bool, string) {
		if ok, reason := harness.ErrorRateTripwire(0.01)(mm); ok {
			return true, reason
		}
		if l := mm.Count("echo_missing"); l > 0 {
			return true, fmt.Sprintf("%d echoes never arrived", l)
		}
		return false, ""
	}

	res := harness.Run(ctx, m, stages, func(ctx context.Context, vu int) {
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
				harness.Pace(ctx, *pace)
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
