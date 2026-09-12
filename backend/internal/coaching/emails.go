package coaching

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// mailer records the emails the booking flow sends in email_outbox, through
// whichever Queries it is given so callers can enqueue inside the transaction
// that changes the session. Delivery is the worker's job (notify.Relay).
type mailer struct {
	appURL   string
	mailFrom string
}

// enqueue records one email; ics may be empty.
func (m mailer) enqueue(ctx context.Context, q *db.Queries, to, subject, body, ics string) error {
	var attachment *string
	if ics != "" {
		attachment = &ics
	}
	if _, err := q.EnqueueEmail(ctx, db.EnqueueEmailParams{ToEmail: to, Subject: subject, Body: body, Ics: attachment}); err != nil {
		return fmt.Errorf("enqueue email to %s: %w", to, err)
	}
	return nil
}

// zone resolves the coach's timezone when forCoach is set and UTC otherwise
// (clients' timezones are unknown).
func zone(s db.GetSessionPartiesRow, forCoach bool) *time.Location {
	if forCoach {
		if l, err := time.LoadLocation(s.CoachTimezone); err == nil {
			return l
		}
	}
	return time.UTC
}

// clock formats an instant for email in the given zone.
func clock(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("Mon 2 Jan 2006 at 15:04 MST")
}

// when formats the session start and length for the recipient.
func when(s db.GetSessionPartiesRow, forCoach bool) string {
	return fmt.Sprintf("%s (%d minutes)", clock(s.ScheduledTime, zone(s, forCoach)), s.DurationMinutes)
}

// respondLink builds the tokenised link the coach follows from their inbox to
// answer a request without signing in; action is "confirm" or "decline".
func (m mailer) respondLink(s db.GetSessionPartiesRow, token, action string) string {
	return fmt.Sprintf("%s/booking/respond?session=%s&token=%s&action=%s", strings.TrimRight(m.appURL, "/"), s.ID, token, action)
}

// coachRequest asks the coach to confirm or decline a pending request. It is
// sent for new bookings and for client-initiated reschedules, which return the
// session to pending with a fresh token.
func (m mailer) coachRequest(ctx context.Context, q *db.Queries, s db.GetSessionPartiesRow, token string, rescheduled bool) error {
	subject := fmt.Sprintf("Session request from %s", s.UserName)
	intro := fmt.Sprintf("%s has requested a coaching session with you.", s.UserName)
	if rescheduled {
		subject = fmt.Sprintf("%s moved their session — please reconfirm", s.UserName)
		intro = fmt.Sprintf("%s has moved their session with you to a new time and needs you to reconfirm it.", s.UserName)
	}
	deadline := ""
	if s.RespondBy != nil {
		deadline = fmt.Sprintf("\n\nPlease respond by %s; after that the request expires and the slot is released.", clock(*s.RespondBy, zone(s, true)))
	}
	body := fmt.Sprintf("Hi %s,\n\n%s\n\nWhen: %s\nTopic: %s\n\nConfirm: %s\nDecline: %s%s\n\nYou can also respond from your coach dashboard.",
		s.CoachName, intro, when(s, true), orNone(s.Topic), m.respondLink(s, token, "confirm"), m.respondLink(s, token, "decline"), deadline)
	return m.enqueue(ctx, q, s.CoachEmail, subject, body, invite(s, m.mailFrom))
}

// clientRequested tells the client their request reached the coach and is
// waiting on a confirmation.
func (m mailer) clientRequested(ctx context.Context, q *db.Queries, s db.GetSessionPartiesRow) error {
	body := fmt.Sprintf("Hi %s,\n\nYour session request has been sent to %s.\n\nWhen: %s\nTopic: %s\n\nWe will email you as soon as %s confirms. The time is held for you until then.",
		s.UserName, s.CoachName, when(s, false), orNone(s.Topic), s.CoachName)
	return m.enqueue(ctx, q, s.UserEmail, fmt.Sprintf("Request sent to %s", s.CoachName), body, "")
}

// clientConfirmed tells the client the coach accepted, with a confirmed invite.
func (m mailer) clientConfirmed(ctx context.Context, q *db.Queries, s db.GetSessionPartiesRow) error {
	body := fmt.Sprintf("Hi %s,\n\n%s has confirmed your session.\n\nWhen: %s\nTopic: %s\n",
		s.UserName, s.CoachName, when(s, false), orNone(s.Topic))
	if s.MeetingUrl != "" {
		body += "Join: " + s.MeetingUrl + "\n"
	}
	body += "\nA calendar invite is attached."
	return m.enqueue(ctx, q, s.UserEmail, fmt.Sprintf("%s confirmed your session", s.CoachName), body, invite(s, m.mailFrom))
}

// clientDeclined tells the client the coach declined the request.
func (m mailer) clientDeclined(ctx context.Context, q *db.Queries, s db.GetSessionPartiesRow) error {
	body := fmt.Sprintf("Hi %s,\n\n%s is unable to take your session on %s.\n\nThe time has been released; you can request another slot from the app.",
		s.UserName, s.CoachName, when(s, false))
	return m.enqueue(ctx, q, s.UserEmail, fmt.Sprintf("%s declined your session request", s.CoachName), body, invite(s, m.mailFrom))
}

// clientExpired tells the client the coach did not answer in time.
func (m mailer) clientExpired(ctx context.Context, q *db.Queries, s db.GetSessionPartiesRow) error {
	body := fmt.Sprintf("Hi %s,\n\n%s did not respond to your request for %s in time, so it has expired and the time has been released.\n\nYou can request another slot from the app.",
		s.UserName, s.CoachName, when(s, false))
	return m.enqueue(ctx, q, s.UserEmail, fmt.Sprintf("Your session request to %s expired", s.CoachName), body, invite(s, m.mailFrom))
}

// coachCalendarUpdate sends the coach the invite matching the session's new
// state once a request leaves pending: a CONFIRMED invite on confirmation, a
// CANCEL on decline or expiry, so the tentative event from the request email
// is updated or removed in their calendar.
func (m mailer) coachCalendarUpdate(ctx context.Context, q *db.Queries, s db.GetSessionPartiesRow) error {
	var subject, body string
	switch s.Status {
	case StatusScheduled:
		subject = fmt.Sprintf("Confirmed: session with %s", s.UserName)
		body = fmt.Sprintf("Hi %s,\n\nYour session with %s is confirmed.\n\nWhen: %s\nTopic: %s\n\nThe attached invite updates the tentative event in your calendar.",
			s.CoachName, s.UserName, when(s, true), orNone(s.Topic))
	case StatusDeclined:
		subject = fmt.Sprintf("Declined: session request from %s", s.UserName)
		body = fmt.Sprintf("Hi %s,\n\nYou declined %s's request for %s. The slot is open again and the attached update removes the tentative event from your calendar.",
			s.CoachName, s.UserName, when(s, true))
	case StatusExpired:
		subject = fmt.Sprintf("Expired: session request from %s", s.UserName)
		body = fmt.Sprintf("Hi %s,\n\n%s's request for %s was not answered in time and has expired. The slot is open again and the attached update removes the tentative event from your calendar.",
			s.CoachName, s.UserName, when(s, true))
	default:
		return nil
	}
	return m.enqueue(ctx, q, s.CoachEmail, subject, body, invite(s, m.mailFrom))
}

// cancelled tells the party who did not cancel that the session is off.
func (m mailer) cancelled(ctx context.Context, q *db.Queries, s db.GetSessionPartiesRow, byCoach bool) error {
	if byCoach {
		body := fmt.Sprintf("Hi %s,\n\n%s has cancelled your session on %s.\n\nYou can request another slot from the app.",
			s.UserName, s.CoachName, when(s, false))
		return m.enqueue(ctx, q, s.UserEmail, fmt.Sprintf("%s cancelled your session", s.CoachName), body, invite(s, m.mailFrom))
	}
	body := fmt.Sprintf("Hi %s,\n\n%s has cancelled their session on %s. The slot is open again.",
		s.CoachName, s.UserName, when(s, true))
	return m.enqueue(ctx, q, s.CoachEmail, fmt.Sprintf("%s cancelled their session", s.UserName), body, invite(s, m.mailFrom))
}

// selfCalendarUpdate sends the party who made a change (toCoach picks which)
// the invite reflecting it, so the event they already hold in their own
// calendar is moved or removed too: a CANCEL after they cancel, the new time
// after a coach reschedules. It is only meant for a party who received an
// invite before; callers skip it otherwise.
func (m mailer) selfCalendarUpdate(ctx context.Context, q *db.Queries, s db.GetSessionPartiesRow, toCoach bool) error {
	to, name, other, forCoach := s.UserEmail, s.UserName, s.CoachName, false
	if toCoach {
		to, name, other, forCoach = s.CoachEmail, s.CoachName, s.UserName, true
	}
	var subject, body string
	if s.Status == StatusCancelled {
		subject = fmt.Sprintf("Cancelled: session with %s", other)
		body = fmt.Sprintf("Hi %s,\n\nYou cancelled your session with %s on %s. The attached update removes it from your calendar.",
			name, other, when(s, forCoach))
	} else {
		subject = fmt.Sprintf("Moved: session with %s", other)
		body = fmt.Sprintf("Hi %s,\n\nYou moved your session with %s.\n\nNew time: %s\nTopic: %s\n\nThe attached update moves the event in your calendar.",
			name, other, when(s, forCoach), orNone(s.Topic))
	}
	return m.enqueue(ctx, q, to, subject, body, invite(s, m.mailFrom))
}

// coachRescheduled tells the client the coach moved the session, with an
// updated invite. The session keeps its status, so no reconfirmation is needed.
func (m mailer) coachRescheduled(ctx context.Context, q *db.Queries, s db.GetSessionPartiesRow) error {
	body := fmt.Sprintf("Hi %s,\n\n%s has moved your session.\n\nNew time: %s\nTopic: %s\n\nAn updated calendar invite is attached.",
		s.UserName, s.CoachName, when(s, false), orNone(s.Topic))
	return m.enqueue(ctx, q, s.UserEmail, fmt.Sprintf("%s moved your session", s.CoachName), body, invite(s, m.mailFrom))
}

// orNone substitutes a placeholder for an empty topic.
func orNone(v string) string {
	if v == "" {
		return "(none)"
	}
	return v
}
