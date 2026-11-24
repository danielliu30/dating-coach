// lt-e2e walks virtual users through product journeys under concurrency,
// asserting correctness at every step instead of spot-checking one surface.
// Four profiles:
//
//	all     — the full lifecycle: signup -> email verify -> /me -> browse ->
//	          book -> mid-journey refresh -> thread -> chat -> analysis ->
//	          label -> sign out (the original lt-e2e journey)
//	book    — sign in -> refresh probe -> loop(browse -> slots -> book
//	          distinct slot -> cancel) -> sign out
//	chat    — sign in -> refresh probe -> thread -> WS -> loop(send, assert
//	          own echo, flag foreign bodies) -> sign out
//	analyze — sign in -> refresh probe -> loop(submit -> 2s poll -> terminal
//	          + succeeded -> label) -> sign out
//
// Every hop carries the VU's own identity: /me must return the VU's email,
// the booking/thread/analysis must belong to the VU's account, WS echoes
// must come back, and after sign-out the session must be dead. Where the
// component suites (lt-auth, lt-chat, ...) prove invariants in isolation,
// this proves them while the surfaces interact inside one session
// lifecycle.
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

// env bundles the run-wide parameters every journey needs.
type env struct {
	m               *harness.Metrics
	base            string
	tag             string
	coachID         string
	slotBase        time.Time
	messages        int
	analysisTimeout time.Duration
	seq             int64
}

func main() {
	base := flag.String("base", "http://localhost", "gateway base URL")
	journey := flag.String("journey", "all", "journey profile: all|book|chat|analyze")
	stagesCSV := flag.String("vus", "10,20,50", "comma-separated VU counts per ramp stage")
	rps := flag.Float64("rps", 150, "aggregate journey actions per second per stage (0 = unpaced)")
	duration := flag.Duration("duration", 30*time.Second, "duration of each stage")
	messages := flag.Int("messages", 5, "chat messages per journey / transcript lines per analysis")
	analysisTimeout := flag.Duration("analysis-timeout", 60*time.Second, "max wait for a terminal analysis")
	out := flag.String("out", "out", "report output directory")
	flag.Parse()

	switch *journey {
	case "all", "book", "chat", "analyze":
	default:
		log.Fatalf("unknown -journey %q (all|book|chat|analyze)", *journey)
	}

	stages, err := harness.Stages(*stagesCSV, *duration)
	if err != nil {
		log.Fatalf("%v", err)
	}
	maxVUs := harness.MaxVUs(stages)

	tag := harness.Tag("e2e")
	ctx := context.Background()
	m := harness.NewMetrics()
	m.Progress(ctx, "e2e-"+*journey, harness.ProgressEvery, *out)
	defer harness.CleanupTag(tag)

	coachID, err := harness.SeedCoach(tag)
	if err != nil {
		log.Fatalf("seed coach: %v", err)
	}
	emails, err := harness.SeedUsers(tag, 1, maxVUs, "user")
	if err != nil {
		log.Fatalf("seed users: %v", err)
	}

	e := &env{
		m: m, base: *base, tag: tag, coachID: coachID,
		slotBase:        time.Now().UTC().Add(10 * 24 * time.Hour).Truncate(time.Hour),
		messages:        *messages,
		analysisTimeout: *analysisTimeout,
	}

	res := harness.RunRate(ctx, m, stages, *rps, func(ctx context.Context, vu int, rate *harness.Rate) {
		email := emails[vu%len(emails)]
		switch *journey {
		case "all":
			for rate.Wait(ctx) {
				e.runAll(ctx, vu)
			}
		case "book":
			e.journeyBook(ctx, vu, rate, email)
		case "chat":
			e.journeyChat(ctx, vu, rate, email)
		case "analyze":
			e.journeyAnalyze(ctx, vu, rate, email)
		}
	}, harness.ErrorRateTripwire(0.01))

	extra := map[string]any{"ramp": res, "journey": *journey}
	if act, err := harness.PgActivity(); err == nil {
		extra["pg_activity"] = act
	}
	path, err := m.WriteReport(*out, "e2e", extra)
	if err != nil {
		log.Fatalf("write report: %v", err)
	}
	fmt.Printf("e2e report: %s\n", path)
}

// signIn logs a seeded account in and asserts the session resolves to that
// account's email — the identity check every profile journey starts from.
func (e *env) signIn(ctx context.Context, email string) (*harness.Client, bool) {
	c := harness.NewClient(e.base, e.m)
	res, err := c.SignIn(ctx, email, harness.SeedPassword)
	if err != nil || res.Status != http.StatusOK {
		e.m.Failf("failed_at_signin", "%s: err=%v status=%d", email, err, harness.Status(res))
		return nil, false
	}
	me, err := c.Me(ctx)
	var u harness.User
	if err != nil || me.Status != http.StatusOK || me.Decode(&u) != nil || u.Email != email {
		e.m.Failf("identity_mismatch", "want %s got %q (err=%v status=%d)", email, u.Email, err, harness.Status(me))
		return nil, false
	}
	return c, true
}

// refreshProbe runs once per journey: the rotated pair must work and the
// spent token must die. Replaying the spent token is what a racing client
// does — the backend treats it as theft and revokes the whole family, so
// the probe measures the blast radius rather than hiding it: if the fresh
// session is dead the journey re-signs-in to continue.
func (e *env) refreshProbe(ctx context.Context, c *harness.Client, email string) bool {
	oldRefresh := c.RefreshToken()
	res, err := c.Refresh(ctx)
	if err != nil || res.Status != http.StatusOK {
		e.m.Failf("failed_at_refresh", "%s: err=%v status=%d", email, err, harness.Status(res))
		return false
	}
	if r2, err := c.Refresh(ctx, oldRefresh); err == nil && r2.Status == http.StatusOK {
		e.m.Failf("stale_refresh_accepted", "%s: replayed refresh got 200", email)
	}
	if res, err := c.Me(ctx); err != nil || res.Status != http.StatusOK {
		e.m.Inc("stale_replay_killed_session")
		if res, err := c.SignIn(ctx, email, harness.SeedPassword); err != nil || res.Status != http.StatusOK {
			e.m.Failf("failed_at_resignin", "%s: err=%v status=%d", email, err, harness.Status(res))
			return false
		}
	}
	return true
}

// signOutDead ends the journey: sign-out must succeed and the session must
// be dead immediately after — a 200 /me post-signout is a session leak.
func (e *env) signOutDead(ctx context.Context, c *harness.Client) bool {
	res, err := c.SignOut(ctx)
	if err != nil || res.Status != http.StatusOK && res.Status != http.StatusNoContent {
		e.m.Inc("failed_at_signout")
		e.m.ErrOnce("signout", "signout: err=%v status=%d", err, harness.Status(res))
		return false
	}
	if res, err := c.Me(ctx); err == nil && res.Status == http.StatusOK {
		e.m.Failf("session_alive_after_signout", "/me still 200 after signout")
		return false
	}
	return true
}

// journeyBook is the "sign in and make a booking" profile: the VU repeats
// the app's real browse -> slots -> book -> cancel flow at the stage rate,
// each on a distinct far-future slot.
func (e *env) journeyBook(ctx context.Context, vu int, rate *harness.Rate, email string) {
	c, ok := e.signIn(ctx, email)
	if !ok {
		return
	}
	if !e.refreshProbe(ctx, c, email) {
		return
	}
	var seq int64
	for rate.Wait(ctx) {
		seq++
		coaches, res, err := c.ListCoaches(ctx)
		if err != nil || res.Status != http.StatusOK {
			e.m.Failf("journey_browse_failed", "vu=%d: err=%v status=%d", vu, err, harness.Status(res))
			continue
		}
		found := false
		for _, co := range coaches {
			if co.ID == e.coachID {
				found = true
			}
		}
		if !found {
			e.m.Failf("seeded_coach_invisible", "vu=%d: coach absent from %d listed", vu, len(coaches))
		}
		if _, res, err := c.CoachSlots(ctx, e.coachID, 60); err != nil || res.Status != http.StatusOK {
			e.m.Failf("journey_slots_failed", "vu=%d: err=%v status=%d", vu, err, harness.Status(res))
			continue
		}
		slot := e.slotBase.Add(time.Duration(vu)*2000*time.Hour + time.Duration(seq)*2*time.Hour)
		s, res, err := c.BookSession(ctx, e.coachID, slot.Format(time.RFC3339), 60, "lt e2e book")
		if err != nil || res.Status != http.StatusCreated {
			e.m.Failf("journey_book_failed", "vu=%d slot=%s: err=%v status=%d", vu, slot.Format(time.RFC3339), err, harness.Status(res))
			continue
		}
		e.m.Inc("journey_booked")
		if r, err := c.CancelSession(ctx, s.ID); err != nil || r.Status != http.StatusOK {
			e.m.Failf("journey_cancel_failed", "vu=%d session=%s: err=%v status=%d", vu, s.ID, err, harness.Status(r))
		}
	}
	if e.signOutDead(ctx, c) {
		e.m.Inc("profile_ok")
	}
}

// journeyChat is the "sign in and live-chat with the coach" profile: the VU
// holds one thread's socket and sends at the stage rate; every echo must
// match its own client_id and no foreign VU traffic may appear on the
// socket.
func (e *env) journeyChat(ctx context.Context, vu int, rate *harness.Rate, email string) {
	c, ok := e.signIn(ctx, email)
	if !ok {
		return
	}
	if !e.refreshProbe(ctx, c, email) {
		return
	}
	thread, res, err := c.StartThread(ctx, e.coachID)
	if err != nil || res.Status != http.StatusCreated && res.Status != http.StatusOK {
		e.m.Failf("failed_at_thread", "vu=%d: err=%v status=%d", vu, err, harness.Status(res))
		return
	}
	sock, err := c.OpenChat(ctx, thread.ID)
	if err != nil {
		e.m.Failf("failed_at_ws_open", "vu=%d thread=%s: %v", vu, thread.ID, err)
		return
	}
	defer sock.Close()

	pending := sync.Map{} // client_id -> body
	readCtx, stopReads := context.WithCancel(context.WithoutCancel(ctx))
	defer stopReads()
	var rd sync.WaitGroup
	rd.Add(1)
	go func() {
		defer rd.Done()
		for {
			ev, err := sock.Read(readCtx)
			if err != nil {
				return
			}
			if ev.Type != "message" {
				continue
			}
			if body, ok := pending.Load(ev.ClientID); ok {
				if body.(string) == ev.Body {
					e.m.Inc("journey_echo_ok")
				} else {
					e.m.Failf("journey_echo_mismatch", "vu=%d client_id=%s got %q", vu, ev.ClientID, ev.Body)
				}
				pending.Delete(ev.ClientID)
			} else if strings.HasPrefix(ev.Body, "lt-e2e-") {
				e.m.Failf("journey_foreign_body", "vu=%d saw %q", vu, ev.Body)
			}
		}
	}()

	var seq int64
	for rate.Wait(ctx) {
		seq++
		cid := fmt.Sprintf("cid-%d-%d", vu, seq)
		body := fmt.Sprintf("lt-e2e-%d-%d", vu, seq)
		pending.Store(cid, body)
		if err := sock.Send(ctx, body, cid); err != nil {
			pending.Delete(cid)
			e.m.Inc("ws_send_failed")
			e.m.ErrOnce("ws_send", "vu=%d ws send: %v", vu, err)
		}
	}

	// Stage over: give in-flight sends one echo window, then count losses.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		empty := true
		pending.Range(func(_, _ any) bool { empty = false; return false })
		if empty {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	var lost int64
	pending.Range(func(k, _ any) bool {
		lost++
		pending.Delete(k)
		return true
	})
	e.m.Add("journey_echo_missing", lost)
	stopReads()
	rd.Wait()
	if e.signOutDead(ctx, c) {
		e.m.Inc("profile_ok")
	}
}

// journeyAnalyze is the "sign in and analyze a dating conversation"
// profile: submit -> poll at the app's 2s cadence -> terminal+succeeded
// with the VU's own conversation_id -> label, looping at the stage rate.
func (e *env) journeyAnalyze(ctx context.Context, vu int, rate *harness.Rate, email string) {
	c, ok := e.signIn(ctx, email)
	if !ok {
		return
	}
	if !e.refreshProbe(ctx, c, email) {
		return
	}
	var seq int64
	for rate.Wait(ctx) {
		seq++
		ar, res, err := c.SubmitConversation(ctx, fmt.Sprintf("lt e2e %d-%d", vu, seq), "Match", transcript(e.messages))
		if err != nil || res.Status != http.StatusAccepted && res.Status != http.StatusCreated {
			e.m.Failf("journey_analysis_submit_failed", "vu=%d: err=%v status=%d", vu, err, harness.Status(res))
			continue
		}
		wantConv := ar.ConversationID
		terminal := false
		deadline := time.Now().Add(e.analysisTimeout)
		for time.Now().Before(deadline) && !terminal && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case <-time.After(pollInterval):
			}
			got, res, err := c.AnalysisResult(ctx, ar.ID)
			if err != nil || res.Status != http.StatusOK {
				continue
			}
			ar = got
			terminal = ar.Status == "succeeded" || ar.Status == "failed"
		}
		if !terminal {
			e.m.Inc("journey_analysis_terminal_fail")
			e.m.ErrOnce("an_terminal", "vu=%d analysis %s not terminal after %s", vu, ar.ID, e.analysisTimeout)
			continue
		}
		if ar.Status != "succeeded" || ar.ModelVersion == "" || ar.ConversationID != wantConv {
			e.m.Failf("journey_analysis_bad_result", "vu=%d analysis=%s status=%q model=%q conv=%s want %s",
				vu, ar.ID, ar.Status, ar.ModelVersion, ar.ConversationID, wantConv)
			continue
		}
		e.m.Inc("journey_analysis_ok")
		if res, err := c.LabelConversation(ctx, wantConv, "date_set"); err != nil ||
			res.Status != http.StatusOK && res.Status != http.StatusCreated && res.Status != http.StatusNoContent {
			e.m.Failf("label_failed", "vu=%d conv=%s: err=%v status=%d", vu, wantConv, err, harness.Status(res))
		}
	}
	if e.signOutDead(ctx, c) {
		e.m.Inc("profile_ok")
	}
}

// runAll is the original full-lifecycle journey; every failure increments a
// counter so the report separates "the step that broke" from the count.
func (e *env) runAll(ctx context.Context, i int) bool {
	m := e.m
	n := atomic.AddInt64(&e.seq, 1)
	email := fmt.Sprintf("%s-%d-%d@loadtest.local", e.tag, i, n)
	pw := harness.SeedPassword
	fail := func(step string) bool {
		m.Failf("failed_at_"+step, "journey-all vu=%d failed at %s", i, step)
		return false
	}

	c := harness.NewClient(e.base, m)
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
		if co.ID == e.coachID {
			found = true
		}
	}
	if !found {
		m.Inc("seeded_coach_invisible")
		return fail("browse")
	}
	if _, res, err := c.CoachSlots(ctx, e.coachID, 60); err != nil || res.Status != http.StatusOK {
		return fail("slots")
	}
	// Distinct slot per journey — same-slot races are lt-booking's phase,
	// here a collision would just abort the journey.
	slot := e.slotBase.Add(time.Duration(2*i)*2000*time.Hour + time.Duration(2*n)*time.Hour)
	session, res, err := c.BookSession(ctx, e.coachID, slot.Format(time.RFC3339), 60, "lt e2e")
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
	thread, res, err := c.StartThread(ctx, e.coachID)
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
		for len(echoes) < e.messages {
			ev, err := sock.Read(readCtx)
			if err != nil {
				return
			}
			if ev.ClientID != "" {
				echoes[ev.ClientID] = true
			}
		}
	}()
	for i := 0; i < e.messages; i++ {
		if err := sock.Send(ctx, fmt.Sprintf("lt-e2e-%d-%d", i, n), fmt.Sprintf("cid-%d-%d", i, n)); err != nil {
			m.Inc("ws_send_failed")
			m.ErrOnce("ws_send", "journey-all vu=%d ws send: %v", i, err)
		}
	}
	<-readDone
	sock.Close()
	if len(echoes) != e.messages {
		m.Failf("ws_echo_incomplete", "journey-all vu=%d: %d/%d echoes", i, len(echoes), e.messages)
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
	if persisted != e.messages {
		m.Failf("chat_persist_incomplete", "journey-all vu=%d: %d/%d persisted", i, persisted, e.messages)
	}

	// Analysis: submit -> 2s poll -> terminal -> label.
	ar, res, err := c.SubmitConversation(ctx, "lt e2e", "Match", transcript(e.messages))
	if err != nil || res.Status != http.StatusAccepted && res.Status != http.StatusCreated {
		return fail("analysis_submit")
	}
	terminal := false
	deadline := time.Now().Add(e.analysisTimeout)
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
		m.Failf("analysis_bad_result", "journey-all vu=%d analysis=%s status=%q model=%q", i, ar.ID, ar.Status, ar.ModelVersion)
		return fail("analysis_terminal")
	}
	if res, err := c.LabelConversation(ctx, ar.ConversationID, "date_set"); err != nil || res.Status != http.StatusOK && res.Status != http.StatusCreated && res.Status != http.StatusNoContent {
		m.Failf("label_failed", "journey-all vu=%d conv=%s: err=%v status=%d", i, ar.ConversationID, err, harness.Status(res))
	}

	// Sign-out kills the session; /me must 401 afterwards.
	if res, err := c.SignOut(ctx); err != nil || res.Status != http.StatusOK && res.Status != http.StatusNoContent {
		return fail("signout")
	}
	if res, err := c.Me(ctx); err == nil && res.Status == http.StatusOK {
		m.Failf("session_alive_after_signout", "journey-all vu=%d: /me still 200 after signout", i)
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
