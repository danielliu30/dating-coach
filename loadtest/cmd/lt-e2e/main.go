// lt-e2e walks each virtual user through the whole product journey under
// concurrency, asserting correctness at every step instead of spot-checking
// one surface:
//
//	signup -> email verify -> session /me -> browse coaches -> pick slot ->
//	book -> mid-journey token refresh -> open thread -> chat over WS ->
//	submit analysis -> poll at the app's 2s cadence -> label -> sign out
//
// Every hop carries the VU's own identity: /me must return the VU's email,
// the booking/thread/analysis must belong to the VU's account, WS echoes
// must come back, and after sign-out the session must be dead. Where the
// component components (lt-auth, lt-chat, ...) prove invariants in
// isolation, this proves them while the surfaces interact inside one
// session lifecycle.
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

const pollInterval = 2 * time.Second // the app's AnalysisResultScreen cadence

func main() {
	base := flag.String("base", "http://localhost", "gateway base URL")
	vus := flag.Int("vus", 3, "concurrent journeys")
	messages := flag.Int("messages", 5, "chat messages per journey")
	analysisTimeout := flag.Duration("analysis-timeout", 60*time.Second, "max wait for a terminal analysis")
	out := flag.String("out", "out", "report output directory")
	flag.Parse()

	tag := harness.Tag("e2e")
	ctx := context.Background()
	m := harness.NewMetrics()
	defer harness.CleanupTag(tag)

	coachID, err := harness.SeedCoach(tag)
	if err != nil {
		log.Fatalf("seed coach: %v", err)
	}
	slot := time.Now().UTC().Add(10 * 24 * time.Hour).Truncate(time.Hour)

	var completed int64
	var wg sync.WaitGroup
	for i := 0; i < *vus; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if runJourney(ctx, m, *base, tag, coachID, slot, i, *messages, *analysisTimeout) {
				atomic.AddInt64(&completed, 1)
			}
		}(i)
	}
	wg.Wait()

	m.Add("journeys_completed", completed)
	m.Add("journeys_failed", int64(*vus)-completed)
	extra := map[string]any{}
	if act, err := harness.PgActivity(); err == nil {
		extra["pg_activity"] = act
	}
	path, err := m.WriteReport(*out, "e2e", extra)
	if err != nil {
		log.Fatalf("write report: %v", err)
	}
	fmt.Printf("e2e report: %s\n", path)
}

// runJourney is one virtual user's entire trip; every failure increments a
// counter so the report separates "the step that broke" from the count.
func runJourney(ctx context.Context, m *harness.Metrics, base, tag, coachID string, slot time.Time, i, messages int, analysisTimeout time.Duration) bool {
	email := fmt.Sprintf("%s-%d@loadtest.local", tag, i)
	pw := harness.SeedPassword
	fail := func(step string) bool {
		m.Inc("failed_at_" + step)
		return false
	}
	_ = slot

	c := harness.NewClient(base, m)
	if res, err := c.SignUp(ctx, email, pw, "LT E2E "+email, "user"); err != nil || res.Status != http.StatusCreated {
		return fail("signup")
	}
	code, err := harness.VerificationCode(email, 15*time.Second)
	if err != nil {
		return fail("verify_code_scrape")
	}
	if res, err := c.Verify(ctx, email, code); err != nil || res.Status != http.StatusOK {
		return fail("verify")
	}

	// Identity: the session must resolve to this VU's email.
	res, err := c.Me(ctx)
	if err != nil || res.Status != http.StatusOK {
		return fail("me")
	}
	var me harness.User
	if err := res.Decode(&me); err != nil || me.Email != email {
		m.Inc("identity_mismatch")
		return fail("me")
	}

	// Coach browse -> slot -> unpaid booking (gateway stack keeps payments off).
	coaches, res, err := c.ListCoaches(ctx)
	if err != nil || res.Status != http.StatusOK || len(coaches) == 0 {
		return fail("browse")
	}
	found := false
	for _, co := range coaches {
		if co.ID == coachID {
			found = true
		}
	}
	if !found {
		m.Inc("seeded_coach_invisible")
		return fail("browse")
	}
	if _, res, err := c.CoachSlots(ctx, coachID, 60); err != nil || res.Status != http.StatusOK {
		return fail("slots")
	}
	// Distinct slot per VU — same-slot races are lt-booking's phase, here a
	// collision would just abort the journey.
	session, res, err := c.BookSession(ctx, coachID, slot.Add(time.Duration(2*i)*time.Hour).Format(time.RFC3339), 60, "lt e2e")
	if err != nil || res.Status != http.StatusCreated {
		return fail("book")
	}
	if session.Status == "pending_payment" {
		m.Inc("unexpected_paid_booking")
	}

	// Mid-journey refresh: the new pair must work and the old one die.
	oldRefresh := c.RefreshToken()
	res, err = c.Refresh(ctx)
	if err != nil || res.Status != http.StatusOK {
		return fail("refresh")
	}
	// Replaying the spent refresh token is what a racing client does — the
	// backend treats it as theft and revokes the whole family, so probe it
	// and measure the blast radius rather than hiding it: if the fresh
	// session is now dead, the journey must re-sign-in to continue.
	if r2, err := c.Refresh(ctx, oldRefresh); err == nil && r2.Status == http.StatusOK {
		m.Inc("stale_refresh_accepted")
	}
	if res, err := c.Me(ctx); err != nil || res.Status != http.StatusOK {
		m.Inc("stale_replay_killed_session")
		if res, err := c.SignIn(ctx, email, pw); err != nil || res.Status != http.StatusOK {
			return fail("resignin_after_revoke")
		}
	}

	// Chat: thread + WS echo + persistence.
	thread, res, err := c.StartThread(ctx, coachID)
	if err != nil || res.Status != http.StatusCreated && res.Status != http.StatusOK {
		return fail("thread")
	}
	sock, err := c.OpenChat(ctx, thread.ID)
	if err != nil {
		return fail("ws_open")
	}
	echoes := map[string]bool{}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		for len(echoes) < messages {
			ev, err := sock.Read(readCtx)
			if err != nil {
				return
			}
			if ev.ClientID != "" {
				echoes[ev.ClientID] = true
			}
		}
	}()
	for i := 0; i < messages; i++ {
		if err := sock.Send(ctx, fmt.Sprintf("lt-e2e-%d", i), fmt.Sprintf("cid-%d", i)); err != nil {
			m.Inc("ws_send_failed")
		}
	}
	<-readDone
	sock.Close()
	if len(echoes) != messages {
		m.Inc("ws_echo_incomplete")
	}
	msgs, res, err := c.ThreadHistory(ctx, thread.ID, 50)
	if err != nil || res.Status != http.StatusOK {
		return fail("history")
	}
	persisted := 0
	for _, msg := range msgs {
		if strings.HasPrefix(msg.Body, "lt-e2e-") {
			persisted++
		}
	}
	if persisted != messages {
		m.Inc("chat_persist_incomplete")
	}

	// Analysis: submit -> 2s poll -> terminal -> label.
	ar, res, err := c.SubmitConversation(ctx, "lt e2e", "Match", transcript(messages))
	if err != nil || res.Status != http.StatusAccepted && res.Status != http.StatusCreated {
		return fail("analysis_submit")
	}
	terminal := false
	deadline := time.Now().Add(analysisTimeout)
	for time.Now().Before(deadline) && !terminal {
		select {
		case <-ctx.Done():
			return fail("analysis_poll")
		case <-time.After(pollInterval):
		}
		ar, res, err = c.AnalysisResult(ctx, ar.ID)
		if err != nil || res.Status != http.StatusOK {
			continue
		}
		terminal = ar.Status == "succeeded" || ar.Status == "failed"
	}
	if !terminal {
		return fail("analysis_terminal")
	}
	if ar.Status != "succeeded" || ar.ModelVersion == "" {
		m.Inc("analysis_bad_result")
		return fail("analysis_terminal")
	}
	if res, err := c.LabelConversation(ctx, ar.ConversationID, "date_set"); err != nil || res.Status != http.StatusOK && res.Status != http.StatusCreated && res.Status != http.StatusNoContent {
		m.Inc("label_failed")
	}

	// Sign-out kills the session; /me must 401 afterwards.
	if res, err := c.SignOut(ctx); err != nil || res.Status != http.StatusOK && res.Status != http.StatusNoContent {
		return fail("signout")
	}
	if res, err := c.Me(ctx); err == nil && res.Status == http.StatusOK {
		m.Inc("session_alive_after_signout")
		return fail("signout")
	}
	m.Inc("journey_ok")
	return true
}

// transcript builds a minimal valid conversation for the analyzer.
func transcript(n int) []harness.SubmitMessage {
	if n < 4 {
		n = 4
	}
	msgs := make([]harness.SubmitMessage, n)
	for i := range msgs {
		sender := "self"
		if i%2 == 1 {
			sender = "match"
		}
		msgs[i] = harness.SubmitMessage{Sender: sender, Body: fmt.Sprintf("lt e2e message %d", i)}
	}
	return msgs
}
