package harness

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FakeStripe is an HTTP stub of the small Stripe API surface the backend's
// payment provider touches: checkout sessions, payment intents and refunds.
// It records every call so lt-payments can assert exactly-once capture /
// release / refund, and it mints signed webhook payloads with the shared
// webhook secret. It is only a stub — it enforces no real payment logic.
type FakeStripe struct {
	mu        sync.Mutex
	checkouts map[string]*fakeCheckout
	intents   map[string]*fakeIntent
	idem      map[string][]byte // idempotency key -> recorded response
	calls     []StripeCall
	n         int
	base      string
	srv       *http.Server
}

// StripeCall is one request the provider made to the fake.
type StripeCall struct {
	At             time.Time `json:"at"`
	Method         string    `json:"method"`
	Path           string    `json:"path"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
}

type fakeCheckout struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Status        string `json:"status"`
	URL           string `json:"url"`
	PaymentIntent any    `json:"payment_intent"`
	ExpiresAt     int64  `json:"expires_at"`
}

type fakeIntent struct {
	ID           string `json:"id"`
	Object       string `json:"object"`
	Status       string `json:"status"`
	Amount       int64  `json:"amount"`
	Currency     string `json:"currency"`
	LatestCharge any    `json:"latest_charge,omitempty"`
}

// NewFakeStripe builds an unstarted stub. The id counter is seeded from the
// clock so checkout refs (stored as the unique coaching_sessions.payment_ref)
// cannot collide with a previous run's rows, including ones a crashed run
// left behind.
func NewFakeStripe() *FakeStripe {
	return &FakeStripe{
		checkouts: map[string]*fakeCheckout{},
		intents:   map[string]*fakeIntent{},
		idem:      map[string][]byte{},
		n:         int(time.Now().UnixNano() % 1e9),
	}
}

// Start binds on listenAddr (e.g. ":0" to pick a port) and serves until Close.
// It returns the reachable base URL.
func (f *FakeStripe) Start(listenAddr string) (string, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkout/sessions", f.createCheckout)
	mux.HandleFunc("GET /v1/checkout/sessions/{id}", f.getCheckout)
	mux.HandleFunc("POST /v1/checkout/sessions/{id}/expire", f.expireCheckout)
	mux.HandleFunc("POST /v1/payment_intents/{id}/capture", f.captureIntent)
	mux.HandleFunc("POST /v1/payment_intents/{id}/cancel", f.cancelIntent)
	mux.HandleFunc("POST /v1/refunds", f.createRefund)
	mux.HandleFunc("GET /v1/payment_intents/{id}", f.getIntent)

	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return "", err
	}
	f.srv = &http.Server{Handler: mux}
	go func() { _ = f.srv.Serve(lis) }()
	f.base = "http://" + lis.Addr().String()
	return f.base, nil
}

// Base is the URL the provider should be pointed at via STRIPE_BASE_URL. The
// API container needs host.docker.internal:<port> instead of 127.0.0.1.
func (f *FakeStripe) Base() string { return f.base }

// Close stops serving.
func (f *FakeStripe) Close() {
	if f.srv != nil {
		_ = f.srv.Close()
	}
}

// Calls returns a copy of the recorded API calls.
func (f *FakeStripe) Calls() []StripeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]StripeCall(nil), f.calls...)
}

// CountCalls reports how many calls hit path, e.g. "/capture".
func (f *FakeStripe) CountCalls(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int
	for _, c := range f.calls {
		if strings.Contains(c.Path, substr) {
			n++
		}
	}
	return n
}

// IntentStatus returns the fake's recorded status for a payment intent.
func (f *FakeStripe) IntentStatus(intentID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i, ok := f.intents[intentID]; ok {
		return i.Status
	}
	return ""
}

func (f *FakeStripe) nextID(prefix string) string {
	f.n++
	return fmt.Sprintf("%s_lt_%d", prefix, f.n)
}

// record logs the call and returns the idempotency key, a prior replayed
// response, and the parsed form. The body is drained here, so handlers must
// use the returned form rather than r.ParseForm, which would see an empty
// body.
func (f *FakeStripe) record(r *http.Request) (key string, prior []byte, form url.Values, replayed bool) {
	body, _ := io.ReadAll(r.Body)
	form = r.URL.Query()
	if strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		form, _ = url.ParseQuery(string(body))
	}
	key = r.Header.Get("Idempotency-Key")
	f.mu.Lock()
	f.calls = append(f.calls, StripeCall{
		At: time.Now(), Method: r.Method, Path: r.URL.Path, IdempotencyKey: key,
	})
	if key != "" {
		if p, ok := f.idem[key]; ok {
			f.mu.Unlock()
			return "", p, form, true
		}
	}
	f.mu.Unlock()
	return key, nil, form, false
}

func (f *FakeStripe) writeJSON(w http.ResponseWriter, key string, status int, v any) {
	data, _ := json.Marshal(v)
	if key != "" {
		f.mu.Lock()
		f.idem[key] = data
		f.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func formAmount(form url.Values) int64 {
	n, _ := strconv.ParseInt(form.Get("line_items[0][price_data][unit_amount]"), 10, 64)
	return n
}

func (f *FakeStripe) createCheckout(w http.ResponseWriter, r *http.Request) {
	key, prior, form, replayed := f.record(r)
	if replayed {
		w.Header().Set("Content-Type", "application/json")
		w.Write(prior)
		return
	}
	f.mu.Lock()
	id := f.nextID("cs")
	piID := f.nextID("pi")
	amount := formAmount(r.Form)
	if amount == 0 {
		amount = 12000
	}
	currency := form.Get("line_items[0][price_data][currency]")
	if currency == "" {
		currency = "usd"
	}
	f.intents[piID] = &fakeIntent{ID: piID, Object: "payment_intent", Status: "requires_capture", Amount: amount, Currency: currency}
	expires := time.Now().Add(time.Hour).Unix()
	if raw := form.Get("expires_at"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			expires = v
		}
	}
	cs := &fakeCheckout{
		ID: id, Object: "checkout.session", Status: "open",
		URL: f.base + "/checkout/" + id, PaymentIntent: piID, ExpiresAt: expires,
	}
	f.checkouts[id] = cs
	f.mu.Unlock()
	f.writeJSON(w, key, http.StatusOK, cs)
}

func (f *FakeStripe) getCheckout(w http.ResponseWriter, r *http.Request) {
	key, prior, _, replayed := f.record(r)
	if replayed {
		w.Header().Set("Content-Type", "application/json")
		w.Write(prior)
		return
	}
	f.mu.Lock()
	cs, ok := f.checkouts[r.PathValue("id")]
	if ok {
		if piID, isStr := cs.PaymentIntent.(string); isStr {
			if pi, ok2 := f.intents[piID]; ok2 {
				cp := *cs
				cp.PaymentIntent = pi
				cs = &cp
			}
		}
	}
	f.mu.Unlock()
	if !ok {
		f.writeJSON(w, key, http.StatusNotFound, map[string]any{"error": map[string]string{"message": "no such checkout session"}})
		return
	}
	f.writeJSON(w, key, http.StatusOK, cs)
}

func (f *FakeStripe) expireCheckout(w http.ResponseWriter, r *http.Request) {
	key, prior, _, replayed := f.record(r)
	if replayed {
		w.Header().Set("Content-Type", "application/json")
		w.Write(prior)
		return
	}
	f.mu.Lock()
	cs, ok := f.checkouts[r.PathValue("id")]
	if ok {
		cs.Status = "expired"
		if piID, isStr := cs.PaymentIntent.(string); isStr {
			if pi, ok2 := f.intents[piID]; ok2 {
				pi.Status = "canceled"
			}
		}
	}
	f.mu.Unlock()
	if !ok {
		f.writeJSON(w, key, http.StatusNotFound, map[string]any{"error": map[string]string{"message": "no such checkout session"}})
		return
	}
	f.writeJSON(w, key, http.StatusOK, cs)
}

func (f *FakeStripe) getIntent(w http.ResponseWriter, r *http.Request) {
	key, prior, _, replayed := f.record(r)
	if replayed {
		w.Header().Set("Content-Type", "application/json")
		w.Write(prior)
		return
	}
	f.mu.Lock()
	pi, ok := f.intents[r.PathValue("id")]
	f.mu.Unlock()
	if !ok {
		f.writeJSON(w, key, http.StatusNotFound, map[string]any{"error": map[string]string{"message": "no such payment intent"}})
		return
	}
	f.writeJSON(w, key, http.StatusOK, pi)
}

func (f *FakeStripe) captureIntent(w http.ResponseWriter, r *http.Request) {
	key, prior, _, replayed := f.record(r)
	if replayed {
		w.Header().Set("Content-Type", "application/json")
		w.Write(prior)
		return
	}
	f.mu.Lock()
	pi, ok := f.intents[r.PathValue("id")]
	if ok {
		pi.Status = "succeeded"
		pi.LatestCharge = "ch_" + pi.ID
	}
	f.mu.Unlock()
	if !ok {
		f.writeJSON(w, key, http.StatusNotFound, map[string]any{"error": map[string]string{"message": "no such payment intent"}})
		return
	}
	f.writeJSON(w, key, http.StatusOK, pi)
}

func (f *FakeStripe) cancelIntent(w http.ResponseWriter, r *http.Request) {
	key, prior, _, replayed := f.record(r)
	if replayed {
		w.Header().Set("Content-Type", "application/json")
		w.Write(prior)
		return
	}
	f.mu.Lock()
	pi, ok := f.intents[r.PathValue("id")]
	if ok {
		pi.Status = "canceled"
	}
	f.mu.Unlock()
	if !ok {
		f.writeJSON(w, key, http.StatusNotFound, map[string]any{"error": map[string]string{"message": "no such payment intent"}})
		return
	}
	f.writeJSON(w, key, http.StatusOK, pi)
}

func (f *FakeStripe) createRefund(w http.ResponseWriter, r *http.Request) {
	key, prior, form, replayed := f.record(r)
	if replayed {
		w.Header().Set("Content-Type", "application/json")
		w.Write(prior)
		return
	}
	f.mu.Lock()
	id := f.nextID("re")
	f.mu.Unlock()
	f.writeJSON(w, key, http.StatusOK, map[string]any{
		"id": id, "object": "refund", "status": "succeeded",
		"charge": form.Get("charge"), "payment_intent": form.Get("payment_intent"),
	})
}

// Sign mints a Stripe-Signature header for payload under the scheme
// t=<unix>,v1=HMAC-SHA256(secret, "<t>.<payload>").
func Sign(secret string, payload []byte) string {
	t := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(t + "." + string(payload)))
	return "t=" + t + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// EventAuthorized builds a signed-ready checkout.session.completed event body
// whose session carries checkoutRef and the payment intent the fake created
// for it.
func (f *FakeStripe) EventAuthorized(checkoutRef string) ([]byte, error) {
	f.mu.Lock()
	cs, ok := f.checkouts[checkoutRef]
	var piID string
	if ok {
		if s, isStr := cs.PaymentIntent.(string); isStr {
			piID = s
		}
	}
	id := f.nextID("evt")
	f.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown checkout session %s", checkoutRef)
	}
	return json.Marshal(map[string]any{
		"id": id, "object": "event", "type": "checkout.session.completed",
		"data": map[string]any{"object": map[string]any{
			"id": checkoutRef, "object": "checkout.session", "payment_intent": piID,
		}},
	})
}

// EventExpired builds a checkout.session.expired event body for checkoutRef.
func (f *FakeStripe) EventExpired(checkoutRef string) ([]byte, error) {
	f.mu.Lock()
	cs, ok := f.checkouts[checkoutRef]
	var piID string
	if ok {
		if s, isStr := cs.PaymentIntent.(string); isStr {
			piID = s
		}
	}
	id := f.nextID("evt")
	f.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown checkout session %s", checkoutRef)
	}
	return json.Marshal(map[string]any{
		"id": id, "object": "event", "type": "checkout.session.expired",
		"data": map[string]any{"object": map[string]any{
			"id": checkoutRef, "object": "checkout.session", "payment_intent": piID,
		}},
	})
}

// EventIgnored builds an event type the webhook should acknowledge and drop.
func (f *FakeStripe) EventIgnored() ([]byte, error) {
	f.mu.Lock()
	id := f.nextID("evt")
	f.mu.Unlock()
	return json.Marshal(map[string]any{
		"id": id, "object": "event", "type": "customer.created",
		"data": map[string]any{"object": map[string]any{"id": "cus_lt"}},
	})
}
