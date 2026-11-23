// Package harness provides the shared plumbing every loadtest component uses:
// an HTTP/WebSocket client shaped like the app's ApiClient, Postgres seeding,
// metrics collection, a staged-ramp scaffold and a fake Stripe. Scenario
// logic, invariants and assertions live in the individual components under
// loadtest/cmd — nothing here decides what "correct" means.
package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	apiPrefix      = "/api/v1"
	defaultTimeout = 30 * time.Second
)

// Client speaks to the API the way the app does: REST over JSON for
// everything except the chat socket. One Client plays one account; every call
// is recorded into Metrics under a route-style label so reports aggregate by
// endpoint rather than by concrete id.
type Client struct {
	base string
	http *http.Client
	m    *Metrics

	token        string
	refreshToken string
	userID       string
}

// NewClient builds a Client for base (the nginx gateway origin, e.g.
// http://localhost). A nil Metrics gets a private one; components that pool
// VUs pass a shared Metrics so stats aggregate.
func NewClient(base string, m *Metrics) *Client {
	if m == nil {
		m = NewMetrics()
	}
	return &Client{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Timeout: defaultTimeout},
		m:    m,
	}
}

// Metrics returns the collector the client reports into.
func (c *Client) Metrics() *Metrics { return c.m }

// Token is the access token the last auth call left on the client.
func (c *Client) Token() string { return c.token }

// RefreshToken is the refresh token the last auth call left on the client.
func (c *Client) RefreshToken() string { return c.refreshToken }

// UserID is the account id the last auth call left on the client.
func (c *Client) UserID() string { return c.userID }

// SetSession installs credentials directly, e.g. for a client seeded out of
// band that should act as an already signed-in account.
func (c *Client) SetSession(token, refreshToken, userID string) {
	c.token, c.refreshToken, c.userID = token, refreshToken, userID
}

// Resp is one HTTP call's outcome: status, wire latency and raw body.
type Resp struct {
	Status  int
	Latency time.Duration
	Body    []byte
}

// Decode unmarshals the JSON body into v.
func (r *Resp) Decode(v any) error {
	if len(r.Body) == 0 {
		return fmt.Errorf("empty body")
	}
	return json.Unmarshal(r.Body, v)
}

// ErrBody returns the API's {"error": ...} message, or a status fallback.
func (r *Resp) ErrBody() string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(r.Body, &e) == nil && e.Error != "" {
		return e.Error
	}
	return fmt.Sprintf("status %d", r.Status)
}

// call issues one request under /api/v1 and records it in Metrics. The label
// is the route template ("POST /chat/threads/{id}/messages"), never the
// concrete path, so per-endpoint stats aggregate.
func (c *Client) call(ctx context.Context, method, path, label string, body any) (*Resp, error) {
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode %s: %w", label, err)
		}
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+apiPrefix+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	start := time.Now()
	res, err := c.http.Do(req)
	lat := time.Since(start)
	if err != nil {
		// A stage-end cancellation is shutdown noise, not a real failure:
		// record it under status -1 so error-rate tripwires ignore it.
		if ctx.Err() != nil {
			c.m.Observe(label, -1, lat)
		} else {
			c.m.Observe(label, 0, lat)
		}
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	out := &Resp{Status: res.StatusCode, Latency: lat, Body: data}
	c.m.Observe(label, out.Status, lat)
	return out, err
}

// Raw issues a request outside /api/v1 (health checks, the payments webhook).
func (c *Client) Raw(ctx context.Context, method, path, label string, body any, headers map[string]string) (*Resp, error) {
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode %s: %w", label, err)
		}
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	start := time.Now()
	res, err := c.http.Do(req)
	lat := time.Since(start)
	if err != nil {
		if ctx.Err() != nil {
			c.m.Observe(label, -1, lat)
		} else {
			c.m.Observe(label, 0, lat)
		}
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	out := &Resp{Status: res.StatusCode, Latency: lat, Body: data}
	c.m.Observe(label, out.Status, lat)
	return out, err
}

// --- auth -------------------------------------------------------------------

// User is the account profile subset the components need.
type User struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Role          string `json:"role"`
	DisplayName   string `json:"display_name"`
	EmailVerified bool   `json:"email_verified"`
}

// Session is the token pair plus identity an auth call hands back.
type Session struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    string `json:"expires_at"`
	User         User   `json:"user"`
}

func (c *Client) adopt(s *Session) {
	c.token, c.refreshToken, c.userID = s.Token, s.RefreshToken, s.User.ID
}

// SignUp posts /auth/signup and adopts the returned session on 201. The
// session's token is verify-scoped until /auth/verify succeeds.
func (c *Client) SignUp(ctx context.Context, email, password, displayName, role string) (*Resp, error) {
	res, err := c.call(ctx, http.MethodPost, "/auth/signup", "POST /auth/signup", map[string]string{
		"email": email, "password": password, "display_name": displayName, "role": role,
	})
	if err != nil {
		return res, err
	}
	if res.Status == http.StatusCreated {
		var s Session
		if err := res.Decode(&s); err != nil {
			return res, fmt.Errorf("decode signup session: %w", err)
		}
		c.adopt(&s)
	}
	return res, nil
}

// SignIn posts /auth/signin and adopts the returned session on 200.
func (c *Client) SignIn(ctx context.Context, email, password string) (*Resp, error) {
	res, err := c.call(ctx, http.MethodPost, "/auth/signin", "POST /auth/signin", map[string]string{
		"email": email, "password": password,
	})
	if err != nil {
		return res, err
	}
	if res.Status == http.StatusOK {
		var s Session
		if err := res.Decode(&s); err != nil {
			return res, fmt.Errorf("decode signin session: %w", err)
		}
		c.adopt(&s)
	}
	return res, nil
}

// Refresh posts /auth/refresh with the client's refresh token, or refreshToken
// when explicitly passed (the desync probe passes the same raw token to
// several clients at once). Adopts the rotated pair on 200.
func (c *Client) Refresh(ctx context.Context, refreshToken ...string) (*Resp, error) {
	raw := c.refreshToken
	if len(refreshToken) > 0 {
		raw = refreshToken[0]
	}
	res, err := c.call(ctx, http.MethodPost, "/auth/refresh", "POST /auth/refresh", map[string]string{
		"refresh_token": raw,
	})
	if err != nil {
		return res, err
	}
	if res.Status == http.StatusOK {
		var s Session
		if err := res.Decode(&s); err != nil {
			return res, fmt.Errorf("decode refresh session: %w", err)
		}
		c.adopt(&s)
	}
	return res, nil
}

// Verify posts /auth/verify; when the caller is authenticated as the account
// the response carries a full session, which is adopted.
func (c *Client) Verify(ctx context.Context, email, code string) (*Resp, error) {
	res, err := c.call(ctx, http.MethodPost, "/auth/verify", "POST /auth/verify", map[string]string{
		"email": email, "code": code,
	})
	if err != nil {
		return res, err
	}
	if res.Status == http.StatusOK {
		var s Session
		if err := res.Decode(&s); err == nil && s.Token != "" {
			c.adopt(&s)
		}
	}
	return res, nil
}

// ResendVerification posts /auth/resend-verification. It answers 202 even for
// unknown or already-verified addresses, so only the scraped code proves a
// resend actually happened.
func (c *Client) ResendVerification(ctx context.Context, email string) (*Resp, error) {
	return c.call(ctx, http.MethodPost, "/auth/resend-verification", "POST /auth/resend-verification", map[string]string{
		"email": email,
	})
}

// Me fetches /auth/me.
func (c *Client) Me(ctx context.Context) (*Resp, error) {
	return c.call(ctx, http.MethodGet, "/auth/me", "GET /auth/me", nil)
}

// SignOut posts /auth/signout and drops local credentials on success.
func (c *Client) SignOut(ctx context.Context) (*Resp, error) {
	res, err := c.call(ctx, http.MethodPost, "/auth/signout", "POST /auth/signout", map[string]string{
		"refresh_token": c.refreshToken,
	})
	if err == nil && res.Status == http.StatusOK {
		c.token, c.refreshToken, c.userID = "", "", ""
	}
	return res, err
}

// WSTicket mints a single-use websocket ticket.
func (c *Client) WSTicket(ctx context.Context) (*Resp, error) {
	return c.call(ctx, http.MethodPost, "/auth/ws-ticket", "POST /auth/ws-ticket", nil)
}

// --- coaching ---------------------------------------------------------------

// Coach is the directory entry subset the components need.
type Coach struct {
	ID               string `json:"id"`
	DisplayName      string `json:"display_name"`
	AcceptingClients bool   `json:"accepting_clients"`
	HourlyRateCents  int32  `json:"hourly_rate_cents"`
	ApprovalStatus   string `json:"approval_status"`
}

// Slot is one bookable start time.
type Slot struct {
	Start           string `json:"start"`
	DurationMinutes int32  `json:"duration_minutes"`
}

// BookingSession is the session row subset the components assert on.
type BookingSession struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	PaymentStatus string `json:"payment_status"`
	ScheduledTime string `json:"scheduled_time"`
	RespondBy     string `json:"respond_by,omitempty"`
	CheckoutURL   string `json:"checkout_url,omitempty"`
}

// ListCoaches fetches /coaching/coaches.
func (c *Client) ListCoaches(ctx context.Context) ([]Coach, *Resp, error) {
	res, err := c.call(ctx, http.MethodGet, "/coaching/coaches", "GET /coaching/coaches", nil)
	if err != nil || res.Status != http.StatusOK {
		return nil, res, err
	}
	var out struct {
		Coaches []Coach `json:"coaches"`
	}
	if err := res.Decode(&out); err != nil {
		return nil, res, fmt.Errorf("decode coaches: %w", err)
	}
	return out.Coaches, res, nil
}

// CoachSlots fetches open slots for a coach.
func (c *Client) CoachSlots(ctx context.Context, coachID string, durationMinutes int) ([]Slot, *Resp, error) {
	path := fmt.Sprintf("/coaching/coaches/%s/slots?duration_minutes=%d", coachID, durationMinutes)
	res, err := c.call(ctx, http.MethodGet, path, "GET /coaching/coaches/{id}/slots", nil)
	if err != nil || res.Status != http.StatusOK {
		return nil, res, err
	}
	var out struct {
		Slots []Slot `json:"slots"`
	}
	if err := res.Decode(&out); err != nil {
		return nil, res, fmt.Errorf("decode slots: %w", err)
	}
	return out.Slots, res, nil
}

// BookSession posts /coaching/sessions; a paid booking returns the session
// with checkout_url set, an unpaid one is pending.
func (c *Client) BookSession(ctx context.Context, coachID, scheduledTime string, durationMinutes int, topic string) (BookingSession, *Resp, error) {
	res, err := c.call(ctx, http.MethodPost, "/coaching/sessions", "POST /coaching/sessions", map[string]any{
		"coach_id": coachID, "scheduled_time": scheduledTime, "duration_minutes": durationMinutes, "topic": topic,
	})
	var s BookingSession
	if err == nil && res.Status == http.StatusCreated {
		if err := res.Decode(&s); err != nil {
			return s, res, fmt.Errorf("decode booking: %w", err)
		}
	}
	return s, res, err
}

// RespondAsCoach posts /coach/sessions/{id}/respond with "confirm"|"decline".
func (c *Client) RespondAsCoach(ctx context.Context, sessionID, action string) (*Resp, error) {
	return c.call(ctx, http.MethodPost, fmt.Sprintf("/coach/sessions/%s/respond", sessionID), "POST /coach/sessions/{id}/respond", map[string]string{
		"action": action,
	})
}

// CancelSession posts /coaching/sessions/{id}/cancel.
func (c *Client) CancelSession(ctx context.Context, sessionID string) (*Resp, error) {
	return c.call(ctx, http.MethodPost, fmt.Sprintf("/coaching/sessions/%s/cancel", sessionID), "POST /coaching/sessions/{id}/cancel", nil)
}

// --- chat -------------------------------------------------------------------

// Thread is the chat thread subset the components need.
type Thread struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Message is one persisted chat message.
type Message struct {
	ID        string `json:"id"`
	ThreadID  string `json:"thread_id"`
	SenderID  string `json:"sender_id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// StartThread posts /chat/threads, reusing an existing active thread.
func (c *Client) StartThread(ctx context.Context, coachID string) (Thread, *Resp, error) {
	res, err := c.call(ctx, http.MethodPost, "/chat/threads", "POST /chat/threads", map[string]any{
		"coach_id": coachID, "session_id": nil,
	})
	var t Thread
	if err == nil && res.Status == http.StatusCreated {
		if err := res.Decode(&t); err != nil {
			return t, res, fmt.Errorf("decode thread: %w", err)
		}
	}
	return t, res, err
}

// ThreadHistory fetches a page of the transcript.
func (c *Client) ThreadHistory(ctx context.Context, threadID string, limit int) ([]Message, *Resp, error) {
	res, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/chat/threads/%s/messages?limit=%d", threadID, limit),
		"GET /chat/threads/{id}/messages", nil)
	if err != nil || res.Status != http.StatusOK {
		return nil, res, err
	}
	var out struct {
		Messages []Message `json:"messages"`
	}
	if err := res.Decode(&out); err != nil {
		return nil, res, fmt.Errorf("decode history: %w", err)
	}
	return out.Messages, res, nil
}

// --- analysis ---------------------------------------------------------------

// SubmitMessage is one transcript line: "self" or "match".
type SubmitMessage struct {
	Sender string `json:"sender"`
	Body   string `json:"body"`
}

// AnalysisResult is the result row subset the components assert on.
type AnalysisResult struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"conversation_id"`
	Status         string          `json:"status"`
	ModelVersion   string          `json:"model_version"`
	Segments       json.RawMessage `json:"segments"`
	Overall        json.RawMessage `json:"overall"`
	Error          string          `json:"error,omitempty"`
}

// SubmitConversation posts /analysis/conversations and returns the pending
// analysis to poll.
func (c *Client) SubmitConversation(ctx context.Context, title, matchName string, msgs []SubmitMessage) (AnalysisResult, *Resp, error) {
	res, err := c.call(ctx, http.MethodPost, "/analysis/conversations", "POST /analysis/conversations", map[string]any{
		"title": title, "platform": "tinder", "match_name": matchName, "messages": msgs,
	})
	var out AnalysisResult
	if err == nil && res.Status == http.StatusAccepted {
		if err := res.Decode(&out); err != nil {
			return out, res, fmt.Errorf("decode analysis: %w", err)
		}
	}
	return out, res, err
}

// AnalysisResult fetches /analysis/results/{id}.
func (c *Client) AnalysisResult(ctx context.Context, analysisID string) (AnalysisResult, *Resp, error) {
	res, err := c.call(ctx, http.MethodGet, "/analysis/results/"+analysisID, "GET /analysis/results/{id}", nil)
	var out AnalysisResult
	if err == nil && res.Status == http.StatusOK {
		if err := res.Decode(&out); err != nil {
			return out, res, fmt.Errorf("decode result: %w", err)
		}
	}
	return out, res, err
}

// LabelConversation posts /analysis/conversations/{id}/label.
func (c *Client) LabelConversation(ctx context.Context, conversationID, outcome string) (*Resp, error) {
	return c.call(ctx, http.MethodPost, fmt.Sprintf("/analysis/conversations/%s/label", conversationID),
		"POST /analysis/conversations/{id}/label", map[string]any{
			"outcome": outcome, "reply_received": nil, "engagement_score": nil,
			"segment_labels": nil, "notes": "", "consented": false, "label_source": "user",
		})
}

// WSURL builds the websocket URL for a thread, carrying the access token as
// the ?token= query parameter the middleware accepts on handshakes.
func (c *Client) WSURL(threadID string) (string, error) {
	u, err := url.Parse(c.base)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	u.Path = apiPrefix + "/chat/threads/" + threadID + "/ws"
	u.RawQuery = "token=" + url.QueryEscape(c.token)
	return u.String(), nil
}
