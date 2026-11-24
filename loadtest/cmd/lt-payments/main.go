// lt-payments load-tests the paid-booking state machine end to end against
// the payments-enabled api-pay service while a fake Stripe on the host stands
// in for api.stripe.com and the driver signs webhook payloads itself:
//
//   - book -> pending_payment with a checkout_url -> signed
//     checkout.session.completed -> pending+authorized -> coach confirm ->
//     capture hit exactly once -> paid+scheduled
//   - the decline path voids the hold via PaymentIntent cancel -> released
//   - checkout.session.expired abandons the hold -> cancelled + failed
//   - forged signatures are rejected (400), unrelated events acked and
//     dropped, and a redelivered event id is a no-op (payment_events row
//     count stays 1)
//   - the concurrent phase races N users through book -> authorize ->
//     confirm/decline and asserts the fake saw exactly one capture per
//     confirmed session and one cancel per declined one
//
// The fake binds :12111 on all interfaces because containers reach it via
// host.docker.internal; it is load-test scaffolding only.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/danielliu30/dating-coach/loadtest/internal/harness"
)

const (
	webhookSecret = "whsec_lt" // matches STRIPE_WEBHOOK_SECRET in .env
	webhookPath   = "/api/v1/payments/webhook"
)

func main() {
	api := flag.String("api", "http://localhost:8082", "payments-enabled api base URL")
	fakeAddr := flag.String("fake-addr", ":12111", "fake Stripe listen address (all interfaces — containers reach it via host.docker.internal)")
	vus := flag.Int("vus", 4, "concurrent paid bookings")
	bringUp := flag.Bool("compose-up", true, "docker compose up api-pay/worker-pay before testing")
	out := flag.String("out", "out", "report output directory")
	flag.Parse()

	tag := harness.Tag("pay")
	ctx := context.Background()
	m := harness.NewMetrics()
	defer harness.CleanupTag(tag)
	dbBefore, _ := harness.DBStats()

	fake := harness.NewFakeStripe()
	fakeBase, err := fake.Start(*fakeAddr)
	if err != nil {
		log.Fatalf("start fake stripe: %v", err)
	}
	defer fake.Close()
	m.Note("fake stripe on %s (containers use host.docker.internal)", fakeBase)

	if *bringUp {
		if err := harness.ComposeUp("api-pay", "worker-pay"); err != nil {
			log.Fatalf("compose up pay services: %v", err)
		}
	}
	waitHealthy(ctx, *api)

	emails, err := harness.SeedUsers(tag, 1, *vus+3, "user")
	if err != nil {
		log.Fatalf("seed users: %v", err)
	}
	coachID, err := harness.SeedCoach(tag)
	if err != nil {
		log.Fatalf("seed coach: %v", err)
	}
	coach := harness.NewClient(*api, m)
	if res, err := coach.SignIn(ctx, tag+"-coach@loadtest.local", harness.SeedPassword); err != nil || res.Status != http.StatusOK {
		log.Fatalf("coach signin: %v", err)
	}
	baseSlot := time.Now().UTC().Add(9 * 24 * time.Hour).Truncate(time.Hour)

	userAt := func(i int) *harness.Client {
		c := harness.NewClient(*api, m)
		if res, err := c.SignIn(ctx, emails[i], harness.SeedPassword); err != nil || res.Status != http.StatusOK {
			log.Fatalf("signin %s: %v", emails[i], err)
		}
		return c
	}

	// bookPaid books a session through the paid flow; it must come back
	// pending_payment with a checkout the fake recorded.
	bookPaid := func(c *harness.Client, slot time.Time) (harness.BookingSession, bool) {
		s, res, err := c.BookSession(ctx, coachID, slot.Format(time.RFC3339), 60, "lt pay")
		if err != nil || res.Status != http.StatusCreated {
			m.Inc("book_failed")
			return s, false
		}
		if s.Status != "pending_payment" || s.CheckoutURL == "" {
			m.Inc("book_wrong_state")
			m.Note("book returned status=%q payment=%q checkout=%q", s.Status, s.PaymentStatus, s.CheckoutURL)
			return s, false
		}
		m.Inc("book_ok")
		return s, true
	}
	checkoutRef := func(url string) string {
		if i := strings.LastIndex(url, "/"); i >= 0 {
			return url[i+1:]
		}
		return ""
	}
	sendWebhook := func(c *harness.Client, payload []byte, sig string) int {
		res, err := c.Raw(ctx, http.MethodPost, webhookPath, "POST /payments/webhook",
			json.RawMessage(payload), map[string]string{"Stripe-Signature": sig})
		if err != nil {
			m.Inc("webhook_transport_error")
			return 0
		}
		return res.Status
	}
	state := func(id string) (string, string) {
		out, err := harness.Psql(fmt.Sprintf(
			`SELECT status || '|' || COALESCE(payment_status,'') FROM coaching_sessions WHERE id = '%s'`, id))
		if err != nil {
			return "query_failed", ""
		}
		parts := strings.SplitN(strings.TrimSpace(strings.Split(out, "\n")[0]), "|", 2)
		if len(parts) == 2 {
			return parts[0], parts[1]
		}
		return parts[0], ""
	}
	paymentEvents := func(id string) int {
		out, err := harness.Psql(fmt.Sprintf(`SELECT count(*) FROM payment_events WHERE session_id = '%s'`, id))
		var n int
		if err == nil {
			fmt.Sscanf(out, "%d", &n)
		}
		return n
	}

	// --- Phase 1: sequential invariants --------------------------------------
	{
		u := userAt(0)
		s, ok := bookPaid(u, baseSlot)
		if ok {
			ref := checkoutRef(s.CheckoutURL)
			if fake.CountCalls("/v1/checkout/sessions") == 0 || ref == "" {
				m.Inc("checkout_not_recorded")
			}

			// A forged signature must never reach the state machine.
			payload, _ := fake.EventAuthorized(ref)
			if st := sendWebhook(u, payload, "t=1,v1=forged"); st != http.StatusBadRequest {
				m.Inc("forged_signature_accepted")
				m.Note("forged webhook returned %d", st)
			}

			// An unrelated event type is acked and dropped.
			ignored, _ := fake.EventIgnored()
			if st := sendWebhook(u, ignored, harness.Sign(webhookSecret, ignored)); st != http.StatusOK {
				m.Inc("ignored_event_rejected")
			}
			st, _ := state(s.ID)
			if st != "pending_payment" {
				m.Inc("ignored_event_mutated_state")
			}

			// Authorization moves the booking to pending + authorized.
			if st := sendWebhook(u, payload, harness.Sign(webhookSecret, payload)); st != http.StatusOK {
				m.Inc("auth_webhook_failed")
			}
			waitState(ctx, state, s.ID, "pending", "authorized", m, 20*time.Second)

			// Redelivery of the same event id is a no-op: still one row.
			sendWebhook(u, payload, harness.Sign(webhookSecret, payload))
			if n := paymentEvents(s.ID); n != 1 {
				m.Inc("duplicate_webhook_applied")
				m.Note("session %s has %d payment_events after redelivery", s.ID, n)
			} else {
				m.Inc("duplicate_webhook_ignored")
			}

			// Coach confirm -> capture hits the PI exactly once -> paid.
			if res, err := coach.RespondAsCoach(ctx, s.ID, "confirm"); err != nil || res.Status != http.StatusOK {
				m.Inc("confirm_failed")
			}
			waitState(ctx, state, s.ID, "scheduled", "paid", m, 20*time.Second)
			if fake.CountCalls("/capture") != 1 {
				m.Inc("capture_count_wrong")
				m.Note("capture calls = %d after one confirm", fake.CountCalls("/capture"))
			}
		}
	}

	// --- Phase 2: decline releases the hold -----------------------------------
	{
		u := userAt(1)
		s, ok := bookPaid(u, baseSlot.Add(2*time.Hour))
		if ok {
			ref := checkoutRef(s.CheckoutURL)
			payload, _ := fake.EventAuthorized(ref)
			sendWebhook(u, payload, harness.Sign(webhookSecret, payload))
			waitState(ctx, state, s.ID, "pending", "authorized", m, 20*time.Second)
			before := fake.CountCalls("/cancel")
			if res, err := coach.RespondAsCoach(ctx, s.ID, "decline"); err != nil || res.Status != http.StatusOK {
				m.Inc("decline_failed")
			}
			st, pay := waitState(ctx, state, s.ID, "declined", "released", m, 90*time.Second)
			if st == "declined" && pay == "released" && fake.CountCalls("/cancel") == before+1 {
				m.Inc("release_ok")
			} else {
				m.Inc("release_wrong")
				m.Note("decline path: status=%s payment=%s cancels=%d", st, pay, fake.CountCalls("/cancel")-before)
			}
		}
	}

	// --- Phase 3: checkout expiry abandons the hold ----------------------------
	{
		u := userAt(2)
		s, ok := bookPaid(u, baseSlot.Add(4*time.Hour))
		if ok {
			ref := checkoutRef(s.CheckoutURL)
			payload, _ := fake.EventExpired(ref)
			if st := sendWebhook(u, payload, harness.Sign(webhookSecret, payload)); st != http.StatusOK {
				m.Inc("expired_webhook_failed")
			}
			st, pay := waitState(ctx, state, s.ID, "", "", m, 20*time.Second)
			if (st == "cancelled" || st == "expired") && (pay == "failed" || pay == "released") {
				m.Inc("expiry_ok")
			} else {
				m.Inc("expiry_wrong")
				m.Note("expired checkout: status=%s payment=%s", st, pay)
			}
		}
	}

	// --- Phase 4: concurrent paid bookings ------------------------------------
	{
		var wg sync.WaitGroup
		var confirmed, declined int64
		for i := 0; i < *vus; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				u := userAt(3 + i)
				s, ok := bookPaid(u, baseSlot.Add(time.Duration(6+2*i)*time.Hour))
				if !ok {
					return
				}
				ref := checkoutRef(s.CheckoutURL)
				payload, _ := fake.EventAuthorized(ref)
				sig := harness.Sign(webhookSecret, payload)
				if st := sendWebhook(u, payload, sig); st != http.StatusOK {
					m.Inc("race_webhook_failed")
					return
				}
				sendWebhook(u, payload, sig) // redelivery races the coach
				action := "confirm"
				if i%2 == 1 {
					action = "decline"
				}
				res, err := coach.RespondAsCoach(ctx, s.ID, action)
				if err != nil || res.Status != http.StatusOK {
					m.Inc("race_respond_failed")
					return
				}
				wantSt, wantPay := "scheduled", "paid"
				if action == "decline" {
					wantSt, wantPay = "declined", "released"
					declined++
				} else {
					confirmed++
				}
				st, pay := waitState(ctx, state, s.ID, wantSt, wantPay, m, 90*time.Second)
				if st == wantSt && pay == wantPay {
					m.Inc("race_consistent")
				} else {
					m.Inc("race_inconsistent")
					m.Note("race session %s: status=%s payment=%s want %s/%s", s.ID, st, pay, wantSt, wantPay)
				}
			}(i)
		}
		wg.Wait()
		caps, cancels := int64(fake.CountCalls("/capture")), int64(fake.CountCalls("/cancel"))
		// Phase 1's confirm adds one more capture.
		if caps != confirmed+1 {
			m.Inc("race_capture_mismatch")
			m.Note("captures=%d confirmed=%d", caps, confirmed)
		}
		if cancels != declined+1 { // phase 2's decline
			m.Inc("race_release_mismatch")
			m.Note("cancels=%d declined=%d", cancels, declined)
		}
	}

	if stats, err := harness.DBStats(); err == nil {
		extra["db_top_deltas"] = harness.TopDeltas(harness.DiffStats(dbBefore, stats), 15)
	}

	fmt.Printf("payments report: %s\n", reportPath(m, *out, tag, fake))
}

var extra = map[string]any{}

func reportPath(m *harness.Metrics, out, tag string, fake *harness.FakeStripe) string {
	extra["stripe_calls"] = fake.Calls()
	path, err := m.WriteReport(out, "payments", extra)
	if err != nil {
		log.Fatalf("write report: %v", err)
	}
	return path
}

// waitHealthy polls api/healthz until the pay services answer.
func waitHealthy(ctx context.Context, api string) {
	c := harness.NewClient(api, harness.NewMetrics())
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		res, err := c.Raw(ctx, http.MethodGet, "/healthz", "GET /healthz", nil, nil)
		if err == nil && res.Status == http.StatusOK {
			return
		}
		time.Sleep(time.Second)
	}
	log.Fatalf("api-pay at %s never became healthy — is the fake stripe reachable?", api)
}

// waitState polls the DB until the session reaches wantSt/wantPay (empty
// means "any change from pending_payment"), then returns what it settled on.
// Swept transitions like releasing->released need a wait past the worker's
// one-minute payment sweep; synchronous transitions get 20s.
func waitState(ctx context.Context, state func(string) (string, string), id, wantSt, wantPay string, m *harness.Metrics, timeout time.Duration) (string, string) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, pay := state(id)
		if wantSt == "" {
			if st != "pending_payment" && st != "pending" {
				return st, pay
			}
		} else if st == wantSt && (wantPay == "" || pay == wantPay) {
			return st, pay
		}
		select {
		case <-ctx.Done():
			return st, pay
		case <-time.After(300 * time.Millisecond):
		}
	}
	st, pay := state(id)
	m.Note("waitState %s timed out at %s/%s want %s/%s", id, st, pay, wantSt, wantPay)
	return st, pay
}
