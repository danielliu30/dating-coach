package payments

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v84/webhook"
)

const testWebhookSecret = "whsec_test_secret"

// newTestStripe returns a provider whose API calls land on the stub server,
// wired the same way STRIPE_API_BASE_URL wires it in production.
func newTestStripe(t *testing.T, server *httptest.Server) *Stripe {
	t.Helper()
	p, err := NewStripe("sk_test_x", testWebhookSecret, server.URL)
	if err != nil {
		t.Fatalf("NewStripe: %v", err)
	}
	return p
}

type apiCall struct {
	path           string
	idempotencyKey string
	form           url.Values
}

// stubStripe records every API call and answers with canned objects.
func stubStripe(t *testing.T, handler func(call apiCall) any) (*httptest.Server, *[]apiCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []apiCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		call := apiCall{path: r.Method + " " + r.URL.Path, idempotencyKey: r.Header.Get("Idempotency-Key"), form: nil}
		if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
			call.form, _ = url.ParseQuery(string(body))
		}
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()
		out := handler(call)
		w.Header().Set("Content-Type", "application/json")
		if s, ok := out.(string); ok {
			fmt.Fprint(w, s)
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestCreateCheckoutSendsManualCapture(t *testing.T) {
	srv, calls := stubStripe(t, func(apiCall) any {
		return `{"id":"cs_test_1","url":"https://checkout.stripe.test/pay/cs_test_1"}`
	})
	p := newTestStripe(t, srv)

	got, err := p.CreateCheckout(context.Background(), CheckoutInput{
		SessionID: "sess-123", AmountCents: 5000, Currency: "usd",
		Description: "coaching", CustomerEmail: "c@example.com",
		ExpiresAt:  time.Now().Add(time.Hour),
		SuccessURL: "https://app.test/ok", CancelURL: "https://app.test/no",
	})
	if err != nil {
		t.Fatalf("CreateCheckout: %v", err)
	}
	if got.Ref != "cs_test_1" || got.URL == "" {
		t.Fatalf("Checkout = %+v", got)
	}
	if len(*calls) != 1 || (*calls)[0].path != "POST /v1/checkout/sessions" {
		t.Fatalf("calls = %+v", *calls)
	}
	f := (*calls)[0].form
	if f.Get("payment_intent_data[capture_method]") != "manual" {
		t.Fatalf("capture_method = %q, want manual", f.Get("payment_intent_data[capture_method]"))
	}
	if f.Get("client_reference_id") != "sess-123" || f.Get("mode") != "payment" {
		t.Fatalf("form missing session linkage: %v", f)
	}
}

// sessionWithPI answers a session retrieve carrying a payment intent id.
func sessionWithPI(pi string) string {
	if pi == "" {
		return `{"id":"cs_test_1","status":"complete","payment_intent":null}`
	}
	return fmt.Sprintf(`{"id":"cs_test_1","status":"complete","payment_intent":{"id":%q}}`, pi)
}

func TestCaptureResolvesPaymentIntentWithIdempotencyKey(t *testing.T) {
	srv, calls := stubStripe(t, func(c apiCall) any {
		if strings.HasPrefix(c.path, "GET /v1/checkout/sessions/") {
			return sessionWithPI("pi_123")
		}
		return `{"id":"pi_123","status":"succeeded"}`
	})
	p := newTestStripe(t, srv)
	if err := p.Capture(context.Background(), "cs_test_1", "capture-sess-123"); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if len(*calls) != 2 || (*calls)[1].path != "POST /v1/payment_intents/pi_123/capture" {
		t.Fatalf("calls = %+v", *calls)
	}
	if (*calls)[1].idempotencyKey != "capture-sess-123" {
		t.Fatalf("idempotency key = %q", (*calls)[1].idempotencyKey)
	}
}

func TestReleaseOnIncompleteCheckoutIsNoop(t *testing.T) {
	srv, calls := stubStripe(t, func(c apiCall) any {
		return sessionWithPI("")
	})
	p := newTestStripe(t, srv)
	if err := p.Release(context.Background(), "cs_test_1", "release-sess-123"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected only the session retrieve, calls = %+v", *calls)
	}
}

func TestRefundTargetsPaymentIntent(t *testing.T) {
	srv, calls := stubStripe(t, func(c apiCall) any {
		if strings.HasPrefix(c.path, "GET /v1/checkout/sessions/") {
			return sessionWithPI("pi_123")
		}
		return `{"id":"re_123","status":"succeeded"}`
	})
	p := newTestStripe(t, srv)
	if err := p.Refund(context.Background(), "cs_test_1", "refund-sess-123"); err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if len(*calls) != 2 || (*calls)[1].path != "POST /v1/refunds" {
		t.Fatalf("calls = %+v", *calls)
	}
	if (*calls)[1].form.Get("payment_intent") != "pi_123" {
		t.Fatalf("refund form = %v", (*calls)[1].form)
	}
}

func TestExpireOnClosedCheckoutDoesNotFail(t *testing.T) {
	srv, _ := stubStripe(t, func(c apiCall) any {
		if strings.Contains(c.path, "/expire") {
			return `{"id":"cs_test_1","status":"expired"}`
		}
		return sessionWithPI("")
	})
	p := newTestStripe(t, srv)
	if err := p.ExpireCheckout(context.Background(), "cs_test_1"); err != nil {
		t.Fatalf("ExpireCheckout: %v", err)
	}
}

func signedEvent(t *testing.T, payload []byte) string {
	t.Helper()
	return webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload,
		Secret:  testWebhookSecret,
	}).Header
}

func eventPayload(typ, sessionID, clientRef string) []byte {
	return []byte(fmt.Sprintf(`{
		"id": "evt_test_1",
		"object": "event",
		"type": %q,
		"data": {"object": {"id": %q, "object": "checkout.session", "client_reference_id": %q}}
	}`, typ, sessionID, clientRef))
}

func TestParseWebhookMapsCompletedToAuthorized(t *testing.T) {
	p := &Stripe{webhookSecret: testWebhookSecret}
	payload := eventPayload("checkout.session.completed", "cs_test_1", "sess-123")
	ev, err := p.ParseWebhook(payload, signedEvent(t, payload))
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if ev.Kind != EventAuthorized || ev.CheckoutRef != "cs_test_1" || ev.SessionID != "sess-123" || ev.ID != "evt_test_1" {
		t.Fatalf("Event = %+v", ev)
	}
}

func TestParseWebhookMapsExpired(t *testing.T) {
	p := &Stripe{webhookSecret: testWebhookSecret}
	payload := eventPayload("checkout.session.expired", "cs_test_1", "sess-123")
	ev, err := p.ParseWebhook(payload, signedEvent(t, payload))
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if ev.Kind != EventExpired || ev.CheckoutRef != "cs_test_1" {
		t.Fatalf("Event = %+v", ev)
	}
}

func TestParseWebhookRejectsBadSignature(t *testing.T) {
	p := &Stripe{webhookSecret: testWebhookSecret}
	payload := eventPayload("checkout.session.completed", "cs_test_1", "sess-123")
	if _, err := p.ParseWebhook(payload, "t=1,v1=forged"); err == nil {
		t.Fatal("forged signature accepted")
	}
	// A signature for a different secret must fail too.
	other := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload,
		Secret:  "whsec_wrong",
	}).Header
	if _, err := p.ParseWebhook(payload, other); err == nil {
		t.Fatal("signature under wrong secret accepted")
	}
}

func TestParseWebhookIgnoresUnrelatedEvents(t *testing.T) {
	p := &Stripe{webhookSecret: testWebhookSecret}
	payload := eventPayload("payment_intent.created", "pi_x", "")
	ev, err := p.ParseWebhook(payload, signedEvent(t, payload))
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if ev.Kind != EventIgnored || ev.ID != "evt_test_1" {
		t.Fatalf("Event = %+v", ev)
	}
}
