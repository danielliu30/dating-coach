package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrForbidden    = errors.New("not allowed")
	ErrInvalidInput = errors.New("invalid input")
)

const maxMessageLength = 4000

type Service struct {
	queries *db.Queries
	hub     *Hub
}

func NewService(queries *db.Queries, hub *Hub) *Service {
	return &Service{queries: queries, hub: hub}
}

type Thread struct {
	ID              string `json:"id"`
	UserID          string `json:"user_id"`
	CoachID         string `json:"coach_id"`
	SessionID       string `json:"session_id,omitempty"`
	CounterpartName string `json:"counterpart_name,omitempty"`
	Status          string `json:"status"`
	LastMessageAt   string `json:"last_message_at"`
	CounterpartOnline bool  `json:"counterpart_online"`
}

func threadOf(t db.ChatThread) Thread {
	out := Thread{
		ID:            t.ID.String(),
		UserID:        t.UserID.String(),
		CoachID:       t.CoachID.String(),
		Status:        t.Status,
		LastMessageAt: t.LastMessageAt.UTC().Format(time.RFC3339),
	}
	if t.SessionID != nil {
		out.SessionID = t.SessionID.String()
	}
	return out
}

// StartThread returns the caller's active thread with a coach, creating one when
// none exists.
func (s *Service) StartThread(ctx context.Context, userID, coachID uuid.UUID, sessionID *uuid.UUID) (Thread, error) {
	existing, err := s.queries.GetActiveThreadForPair(ctx, db.GetActiveThreadForPairParams{UserID: userID, CoachID: coachID})
	if err == nil {
		return threadOf(existing), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Thread{}, fmt.Errorf("lookup thread: %w", err)
	}

	created, err := s.queries.CreateChatThread(ctx, db.CreateChatThreadParams{
		UserID:    userID,
		CoachID:   coachID,
		SessionID: sessionID,
	})
	if err != nil {
		return Thread{}, fmt.Errorf("create thread: %w", err)
	}
	return threadOf(created), nil
}

func (s *Service) ListForUser(ctx context.Context, userID uuid.UUID) ([]Thread, error) {
	rows, err := s.queries.ListThreadsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}
	out := make([]Thread, 0, len(rows))
	for _, row := range rows {
		thread := threadOf(db.ChatThread{
			ID: row.ID, UserID: row.UserID, CoachID: row.CoachID, SessionID: row.SessionID,
			Status: row.Status, CreatedAt: row.CreatedAt, LastMessageAt: row.LastMessageAt,
		})
		thread.CounterpartName = row.CounterpartName
		thread.CounterpartOnline = s.online(ctx, row.CoachID)
		out = append(out, thread)
	}
	return out, nil
}

func (s *Service) ListForCoach(ctx context.Context, coachID uuid.UUID, status *string) ([]Thread, error) {
	rows, err := s.queries.ListThreadsForCoach(ctx, db.ListThreadsForCoachParams{CoachID: coachID, Status: status})
	if err != nil {
		return nil, fmt.Errorf("list coach threads: %w", err)
	}
	out := make([]Thread, 0, len(rows))
	for _, row := range rows {
		thread := threadOf(db.ChatThread{
			ID: row.ID, UserID: row.UserID, CoachID: row.CoachID, SessionID: row.SessionID,
			Status: row.Status, CreatedAt: row.CreatedAt, LastMessageAt: row.LastMessageAt,
		})
		thread.CounterpartName = row.CounterpartName
		thread.CounterpartOnline = s.online(ctx, row.UserID)
		out = append(out, thread)
	}
	return out, nil
}

func (s *Service) online(ctx context.Context, userID uuid.UUID) bool {
	online, err := s.hub.IsOnline(ctx, userID)
	return err == nil && online
}

// Thread loads a thread the caller participates in.
func (s *Service) Thread(ctx context.Context, threadID, actorID uuid.UUID) (db.ChatThread, error) {
	thread, err := s.queries.GetChatThread(ctx, threadID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.ChatThread{}, ErrNotFound
		}
		return db.ChatThread{}, fmt.Errorf("get thread: %w", err)
	}
	if thread.UserID != actorID && thread.CoachID != actorID {
		return db.ChatThread{}, ErrForbidden
	}
	return thread, nil
}

func (s *Service) History(ctx context.Context, threadID, actorID uuid.UUID, limit, offset int32) ([]Message, error) {
	if _, err := s.Thread(ctx, threadID, actorID); err != nil {
		return nil, err
	}
	rows, err := s.queries.ListChatMessages(ctx, db.ListChatMessagesParams{ThreadID: threadID, Limit: limit, Offset: offset})
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	out := make([]Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, Message{
			ID:        row.ID.String(),
			ThreadID:  row.ThreadID.String(),
			SenderID:  row.SenderID.String(),
			Body:      row.Body,
			CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out, nil
}

// Send persists a message and broadcasts it to the thread.
func (s *Service) Send(ctx context.Context, threadID, senderID uuid.UUID, body string) (Message, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Message{}, fmt.Errorf("%w: message body is empty", ErrInvalidInput)
	}
	if len(body) > maxMessageLength {
		return Message{}, fmt.Errorf("%w: message body is too long", ErrInvalidInput)
	}
	if _, err := s.Thread(ctx, threadID, senderID); err != nil {
		return Message{}, err
	}

	row, err := s.queries.CreateChatMessage(ctx, db.CreateChatMessageParams{ThreadID: threadID, SenderID: senderID, Body: body})
	if err != nil {
		return Message{}, fmt.Errorf("create message: %w", err)
	}
	if err := s.queries.TouchChatThread(ctx, threadID); err != nil {
		return Message{}, fmt.Errorf("touch thread: %w", err)
	}

	msg := Message{
		ID:        row.ID.String(),
		ThreadID:  row.ThreadID.String(),
		SenderID:  row.SenderID.String(),
		Body:      row.Body,
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
	}
	if err := s.hub.Publish(ctx, threadID, Event{
		Type:      EventMessage,
		ThreadID:  msg.ThreadID,
		MessageID: msg.ID,
		SenderID:  msg.SenderID,
		Body:      msg.Body,
		CreatedAt: msg.CreatedAt,
	}); err != nil {
		return msg, err
	}
	return msg, nil
}

func (s *Service) Typing(ctx context.Context, threadID, senderID uuid.UUID, typing bool) error {
	if _, err := s.Thread(ctx, threadID, senderID); err != nil {
		return err
	}
	return s.hub.Publish(ctx, threadID, Event{
		Type:     EventTyping,
		ThreadID: threadID.String(),
		SenderID: senderID.String(),
		Typing:   typing,
	})
}

func (s *Service) Close(ctx context.Context, threadID, actorID uuid.UUID) (Thread, error) {
	if _, err := s.Thread(ctx, threadID, actorID); err != nil {
		return Thread{}, err
	}
	closed, err := s.queries.CloseChatThread(ctx, threadID)
	if err != nil {
		return Thread{}, fmt.Errorf("close thread: %w", err)
	}
	return threadOf(closed), nil
}
