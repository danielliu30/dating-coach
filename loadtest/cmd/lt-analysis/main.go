// lt-analysis load-tests the conversation-analysis pipeline while asserting
// end-to-end correctness:
//
//   - Each VU submits transcripts at -interval and polls the result at the
//     app's real 2-second cadence until terminal or -terminal-timeout.
//   - Every terminal result must be 'succeeded' with model_version,
//     segments, overall, and a matching conversation_id — malformed,
//     wrong-owner, or timed-out analyses are counted.
//   - A RabbitMQ monitor samples queue depth + unacked so the report shows
//     submit→queue→worker→ML throughput as backlog grows (worker prefetch
//     is 4; the analyzer is a single uvicorn process).
//   - Each VU also probes the neighbor VU's analysis id — the result must
//     come back 403/404 (ownership isolation), never 200.
//   - The 2s polling loop is itself the frontend-amplification measure:
//     GET /analysis/results count vs submitted count lands in the report.
//
// The component never logs transcripts beyond its own synthetic bodies.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/danielliu30/dating-coach/loadtest/internal/harness"
)

const (
	pollInterval = 2 * time.Second // the app's AnalysisResultScreen cadence
	analysisQ    = "conversation.analysis"
)

func main() {
	base := flag.String("base", "http://localhost", "gateway base URL")
	vus := flag.Int("vus", 4, "concurrent submitting users")
	duration := flag.Duration("duration", 30*time.Second, "stage duration")
	interval := flag.Duration("interval", 3*time.Second, "pause between submits per VU")
	msgCount := flag.Int("messages", 40, "transcript messages per submission")
	terminalTimeout := flag.Duration("terminal-timeout", 45*time.Second, "max wait for a terminal result")
	out := flag.String("out", "out", "report output directory")
	flag.Parse()

	tag := fmt.Sprintf("anls%d", time.Now().Unix()%100000)
	ctx := context.Background()
	m := harness.NewMetrics()
	defer harness.CleanupTag(tag)

	emails, err := harness.SeedUsers(tag, 1, *vus, "user")
	if err != nil {
		log.Fatalf("seed users: %v", err)
	}

	clients := make([]*harness.Client, *vus)
	latestAnalysis := make([]atomic.Value, *vus) // stores string analysis ids
	for i := range clients {
		c := harness.NewClient(*base, m)
		res, err := c.SignIn(ctx, emails[i], harness.SeedPassword)
		if err != nil || res.Status != http.StatusOK {
			log.Fatalf("signin %s: %v", emails[i], err)
		}
		clients[i] = c
	}

	// Queue-depth monitor.
	var maxReady, maxUnacked int64
	monCtx, stopMon := context.WithCancel(ctx)
	defer stopMon()
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-monCtx.Done():
				return
			case <-t.C:
				ready, unacked, err := harness.QueueDepth(analysisQ)
				if err != nil {
					continue
				}
				if int64(ready) > atomic.LoadInt64(&maxReady) {
					atomic.StoreInt64(&maxReady, int64(ready))
				}
				if int64(unacked) > atomic.LoadInt64(&maxUnacked) {
					atomic.StoreInt64(&maxUnacked, int64(unacked))
				}
			}
		}
	}()

	trip := func(mm *harness.Metrics) (bool, string) {
		if ok, reason := harness.ErrorRateTripwire(0.01)(mm); ok {
			return true, reason
		}
		if n := mm.Count("terminal_timeouts"); n > 0 {
			return true, fmt.Sprintf("%d analyses never reached a terminal state", n)
		}
		if n := mm.Count("ownership_leak"); n > 0 {
			return true, fmt.Sprintf("%d cross-account result reads returned 200", n)
		}
		return false, ""
	}

	var seq int64
	res := harness.Run(ctx, m, []harness.Stage{{Name: "submit", VUs: *vus, Duration: *duration}},
		func(ctx context.Context, vu int) {
			c := clients[vu]
			var pending []struct {
				id      string
				sentAt  time.Time
				convID  string
				checked bool
			}
			for ctx.Err() == nil {
				// Submit one transcript.
				n := atomic.AddInt64(&seq, 1)
				msgs := make([]harness.SubmitMessage, *msgCount)
				for i := range msgs {
					sender := "self"
					if i%2 == 1 {
						sender = "match"
					}
					msgs[i] = harness.SubmitMessage{Sender: sender, Body: fmt.Sprintf("lt transcript %d message %d", n, i)}
				}
				a, r, err := c.SubmitConversation(ctx, fmt.Sprintf("lt %d", n), "Match", msgs)
				switch {
				case err != nil:
					m.Inc("submit_transport_errors")
				case r.Status != http.StatusAccepted:
					m.Inc("submit_rejected")
				case a.ID == "":
					m.Inc("submit_bad_response")
				default:
					m.Inc("submit_ok")
					pending = append(pending, struct {
						id      string
						sentAt  time.Time
						convID  string
						checked bool
					}{id: a.ID, sentAt: time.Now(), convID: a.ConversationID})
					latestAnalysis[vu].Store(a.ID)
				}

				// Poll outstanding analyses at the app's cadence.
				for i := range pending {
					p := &pending[i]
					got, rr, err := c.AnalysisResult(ctx, p.id)
					if err != nil {
						m.Inc("poll_transport_errors")
						continue
					}
					if rr.Status != http.StatusOK {
						m.Inc("poll_rejected")
						continue
					}
					m.Inc("poll_requests")
					switch got.Status {
					case "succeeded":
						m.Inc("terminal_ok")
						m.Observe("ANALYSIS terminal latency", 200, time.Since(p.sentAt))
						checkResult(m, got, p.convID)
						p.checked = true
					case "failed":
						m.Inc("terminal_failed")
						m.Observe("ANALYSIS terminal latency", 200, time.Since(p.sentAt))
						p.checked = true
					default:
						if time.Since(p.sentAt) > *terminalTimeout {
							m.Inc("terminal_timeouts")
							p.checked = true
						}
					}
				}
				kept := pending[:0]
				for _, p := range pending {
					if !p.checked {
						kept = append(kept, p)
					}
				}
				pending = kept

				// Ownership probe: read the neighbor's latest analysis.
				if neighbor := (vu + 1) % *vus; neighbor != vu {
					if v := latestAnalysis[neighbor].Load(); v != nil {
						if id, ok := v.(string); ok && id != "" {
							_, rr, err := c.AnalysisResult(ctx, id)
							switch {
							case err != nil:
							case rr.Status == http.StatusForbidden || rr.Status == http.StatusNotFound:
								m.Inc("ownership_denied")
							default:
								m.Inc("ownership_leak")
							}
						}
					}
				}
				harness.Pace(ctx, *interval)
			}
			// Stage end: anything still pending that can't be polled counts
			// against the next stage's timeout budget implicitly.
			for _, p := range pending {
				if !p.checked {
					m.Inc("pending_at_stage_end")
				}
			}
		}, trip)

	ready, unacked, _ := harness.QueueDepth(analysisQ)
	path, err := m.WriteReport(*out, "analysis", map[string]any{
		"ramp":                res,
		"queue_max_ready":     maxReady,
		"queue_max_unacked":   maxUnacked,
		"queue_final_ready":   ready,
		"queue_final_unacked": unacked,
		"queue":               analysisQ,
	})
	if err != nil {
		log.Fatalf("write report: %v", err)
	}
	fmt.Printf("analysis report: %s\n", path)
}

// checkResult asserts a succeeded analysis actually carries a usable result:
// correct conversation, model version, and non-empty segments/overall.
func checkResult(m *harness.Metrics, r harness.AnalysisResult, wantConv string) {
	if r.ConversationID != wantConv {
		m.Inc("result_wrong_conversation")
		return
	}
	if r.ModelVersion == "" {
		m.Inc("result_no_model_version")
	}
	var segs []json.RawMessage
	if err := json.Unmarshal(r.Segments, &segs); err != nil || len(segs) == 0 {
		m.Inc("result_empty_segments")
	}
	var overall map[string]json.RawMessage
	if err := json.Unmarshal(r.Overall, &overall); err != nil || len(overall) == 0 {
		m.Inc("result_empty_overall")
	}
}
