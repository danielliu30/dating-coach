package coaching

import (
	"fmt"
	"strings"
	"time"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// icsTimeLayout is the UTC form of an iCalendar DATE-TIME.
const icsTimeLayout = "20060102T150405Z"

// invite renders the session as an iCalendar invite that mail clients can add to
// the recipient's calendar. The UID is stable per session and SEQUENCE is the
// row's calendar_sequence, which every state change increments, so a later
// invite for the same session replaces the earlier one instead of duplicating
// it. A pending request is TENTATIVE, a confirmed session CONFIRMED, and
// anything else is sent as a CANCEL so the event disappears from calendars it
// was added to. organizer is the sender address the invite is issued from.
func invite(s db.GetSessionPartiesRow, organizer string) string {
	method, status := "REQUEST", "TENTATIVE"
	switch s.Status {
	case "scheduled":
		status = "CONFIRMED"
	case "pending":
	default:
		method, status = "CANCEL", "CANCELLED"
	}
	end := s.ScheduledTime.Add(time.Duration(s.DurationMinutes) * time.Minute)
	summary := fmt.Sprintf("Dating Coach session: %s with %s", s.UserName, s.CoachName)
	description := "Coaching session"
	if s.Topic != "" {
		description = "Topic: " + s.Topic
	}

	var b strings.Builder
	line := func(format string, args ...any) {
		b.WriteString(fmt.Sprintf(format, args...))
		b.WriteString("\r\n")
	}
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//Dating Coach//Coaching//EN")
	line("METHOD:%s", method)
	line("BEGIN:VEVENT")
	line("UID:%s@datingcoach", s.ID)
	line("SEQUENCE:%d", s.CalendarSequence)
	line("DTSTAMP:%s", time.Now().UTC().Format(icsTimeLayout))
	line("DTSTART:%s", s.ScheduledTime.UTC().Format(icsTimeLayout))
	line("DTEND:%s", end.UTC().Format(icsTimeLayout))
	line("SUMMARY:%s", escapeICS(summary))
	line("DESCRIPTION:%s", escapeICS(description))
	line("STATUS:%s", status)
	line("ORGANIZER:mailto:%s", organizer)
	line("ATTENDEE;CN=%s;ROLE=REQ-PARTICIPANT:mailto:%s", paramICS(s.CoachName), s.CoachEmail)
	line("ATTENDEE;CN=%s;ROLE=REQ-PARTICIPANT:mailto:%s", paramICS(s.UserName), s.UserEmail)
	line("END:VEVENT")
	line("END:VCALENDAR")
	return b.String()
}

// escapeICS escapes the characters iCalendar TEXT values reserve.
func escapeICS(v string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\n", `\n`)
	return r.Replace(v)
}

// paramICS renders v as an RFC 5545 property parameter value (e.g. CN=...):
// control characters are dropped, double quotes (which cannot be escaped in a
// parameter) are replaced with apostrophes, and the value is quoted when it
// contains a colon, semicolon or comma so it is not mistaken for a delimiter.
func paramICS(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch {
		case r == '"':
			b.WriteRune('\'')
		case r < 0x20 || r == 0x7f:
		default:
			b.WriteRune(r)
		}
	}
	v = b.String()
	if strings.ContainsAny(v, ":;,") {
		return `"` + v + `"`
	}
	return v
}
