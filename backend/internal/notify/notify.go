// Package notify sends user-facing notifications (email today, push later).
//
// The push transport is a stub: it records the intent so the mobile clients can
// be wired to APNs/FCM without touching the callers.
package notify

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/smtp"
	"net/textproto"

	"github.com/danielliu30/dating-coach/backend/internal/config"
)

// Notifier is the delivery interface the services depend on, so tests can
// capture notifications instead of sending them.
type Notifier interface {
	Email(ctx context.Context, to, subject, body string) error
	Send(ctx context.Context, msg Message) error
	Push(ctx context.Context, userID, title, body string) error
}

// Message is one outgoing email. ICS, when non-empty, is attached as a
// text/calendar invite (METHOD taken from the payload) so mail clients offer to
// add the event to the recipient's calendar.
type Message struct {
	To      string
	Subject string
	Body    string
	ICS     string
}

// Service is the real Notifier: it delivers email over the configured SMTP
// server and records push intent.
type Service struct {
	cfg *config.Config
}

// New returns the notifier used by the API and the worker.
func New(cfg *config.Config) *Service {
	return &Service{cfg: cfg}
}

// Email sends a plain-text message over SMTP. When SMTP is not configured
// (local dev) the message is logged instead so flows such as email verification
// stay usable.
func (s *Service) Email(ctx context.Context, to, subject, body string) error {
	return s.Send(ctx, Message{To: to, Subject: subject, Body: body})
}

// Send delivers msg over SMTP, as a plain-text message or, when it carries an
// ICS payload, as a multipart/mixed message with the invite attached. When SMTP
// is not configured the message is logged instead, including any invite, so
// local runs can still read the links it contains.
func (s *Service) Send(_ context.Context, msg Message) error {
	if s.cfg.SMTPHost == "" {
		slog.Info("email (smtp not configured, logging instead)", "to", msg.To, "subject", msg.Subject, "body", msg.Body, "has_ics", msg.ICS != "")
		return nil
	}

	raw, err := encode(s.cfg.MailFrom, msg)
	if err != nil {
		return err
	}
	addr := fmt.Sprintf("%s:%d", s.cfg.SMTPHost, s.cfg.SMTPPort)

	var auth smtp.Auth
	if s.cfg.SMTPUsername != "" {
		auth = smtp.PlainAuth("", s.cfg.SMTPUsername, s.cfg.SMTPPassword, s.cfg.SMTPHost)
	}
	if err := smtp.SendMail(addr, auth, s.cfg.MailFrom, []string{msg.To}, raw); err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	return nil
}

// encode renders msg as an RFC 5322 message from the given sender: text/plain
// when there is no invite, otherwise multipart/mixed with the invite as a
// text/calendar part that carries its METHOD so calendar clients treat it as a
// request or cancellation rather than a bare attachment.
func encode(from string, msg Message) ([]byte, error) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\n", from, msg.To, msg.Subject)
	if msg.ICS == "" {
		fmt.Fprintf(&buf, "Content-Type: text/plain; charset=utf-8\r\n\r\n%s", msg.Body)
		return buf.Bytes(), nil
	}

	mw := multipart.NewWriter(&buf)
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", mw.Boundary())

	text, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=utf-8"}})
	if err != nil {
		return nil, fmt.Errorf("encode mail body: %w", err)
	}
	if _, err := text.Write([]byte(msg.Body)); err != nil {
		return nil, fmt.Errorf("encode mail body: %w", err)
	}

	method := icsMethod(msg.ICS)
	invite, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {fmt.Sprintf("text/calendar; charset=utf-8; method=%s", method)},
		"Content-Disposition": {`attachment; filename="invite.ics"`},
	})
	if err != nil {
		return nil, fmt.Errorf("encode mail invite: %w", err)
	}
	if _, err := invite.Write([]byte(msg.ICS)); err != nil {
		return nil, fmt.Errorf("encode mail invite: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("encode mail: %w", err)
	}
	return buf.Bytes(), nil
}

// icsMethod extracts the METHOD property from an iCalendar payload, defaulting
// to REQUEST when the payload does not declare one.
func icsMethod(ics string) string {
	for _, line := range bytes.Split([]byte(ics), []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if bytes.HasPrefix(line, []byte("METHOD:")) {
			return string(bytes.TrimPrefix(line, []byte("METHOD:")))
		}
	}
	return "REQUEST"
}

// Push is a stub for mobile push delivery.
func (s *Service) Push(_ context.Context, userID, title, body string) error {
	slog.Info("push notification (stub)", "user_id", userID, "title", title, "body", body)
	return nil
}
