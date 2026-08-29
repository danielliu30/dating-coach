// Package notify sends user-facing notifications (email today, push later).
//
// The push transport is a stub: it records the intent so the mobile clients can
// be wired to APNs/FCM without touching the callers.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"net/smtp"

	"github.com/danielliu30/dating-coach/backend/internal/config"
)

type Notifier interface {
	Email(ctx context.Context, to, subject, body string) error
	Push(ctx context.Context, userID, title, body string) error
}

type Service struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Service {
	return &Service{cfg: cfg}
}

// Email sends a message over SMTP. When SMTP is not configured (local dev) the
// message is logged instead so flows such as email verification stay usable.
func (s *Service) Email(_ context.Context, to, subject, body string) error {
	if s.cfg.SMTPHost == "" {
		slog.Info("email (smtp not configured, logging instead)", "to", to, "subject", subject, "body", body)
		return nil
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		s.cfg.MailFrom, to, subject, body)
	addr := fmt.Sprintf("%s:%d", s.cfg.SMTPHost, s.cfg.SMTPPort)

	var auth smtp.Auth
	if s.cfg.SMTPUsername != "" {
		auth = smtp.PlainAuth("", s.cfg.SMTPUsername, s.cfg.SMTPPassword, s.cfg.SMTPHost)
	}
	if err := smtp.SendMail(addr, auth, s.cfg.MailFrom, []string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	return nil
}

// Push is a stub for mobile push delivery.
func (s *Service) Push(_ context.Context, userID, title, body string) error {
	slog.Info("push notification (stub)", "user_id", userID, "title", title, "body", body)
	return nil
}
