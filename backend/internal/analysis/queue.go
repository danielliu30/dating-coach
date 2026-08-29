package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Job is the unit of work handed to the analysis worker.
type Job struct {
	AnalysisID     string `json:"analysis_id"`
	ConversationID string `json:"conversation_id"`
	UserID         string `json:"user_id"`
}

// Queue is a thin RabbitMQ wrapper used by both the API (publish) and the worker
// (consume).
type Queue struct {
	conn    *amqp.Connection
	channel *amqp.Channel
	name    string
}

func OpenQueue(url, name string) (*Queue, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}
	channel, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("open channel: %w", err)
	}
	if _, err := channel.QueueDeclare(name, true, false, false, false, nil); err != nil {
		channel.Close()
		conn.Close()
		return nil, fmt.Errorf("declare queue %q: %w", name, err)
	}
	if err := channel.Qos(4, 0, false); err != nil {
		channel.Close()
		conn.Close()
		return nil, fmt.Errorf("set qos: %w", err)
	}
	return &Queue{conn: conn, channel: channel, name: name}, nil
}

func (q *Queue) Publish(ctx context.Context, job Job) error {
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("encode job: %w", err)
	}
	if err := q.channel.PublishWithContext(ctx, "", q.name, false, false, amqp.Publishing{
		ContentType:  "application/json",
		Body:         body,
		DeliveryMode: amqp.Persistent,
	}); err != nil {
		return fmt.Errorf("publish job: %w", err)
	}
	return nil
}

// Consume blocks until ctx is cancelled, calling handle for every job. Jobs that
// fail are nacked without requeue; the analysis row records the failure.
func (q *Queue) Consume(ctx context.Context, handle func(context.Context, Job) error) error {
	deliveries, err := q.channel.Consume(q.name, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume queue: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case delivery, open := <-deliveries:
			if !open {
				return fmt.Errorf("rabbitmq channel closed")
			}
			var job Job
			if err := json.Unmarshal(delivery.Body, &job); err != nil {
				slog.Error("decode analysis job", "error", err)
				_ = delivery.Nack(false, false)
				continue
			}
			if err := handle(ctx, job); err != nil {
				slog.Error("handle analysis job", "error", err, "analysis_id", job.AnalysisID)
				_ = delivery.Nack(false, false)
				continue
			}
			if err := delivery.Ack(false); err != nil {
				slog.Error("ack analysis job", "error", err, "analysis_id", job.AnalysisID)
			}
		}
	}
}

func (q *Queue) Close() {
	if err := q.channel.Close(); err != nil {
		slog.Debug("close rabbitmq channel", "error", err)
	}
	if err := q.conn.Close(); err != nil {
		slog.Debug("close rabbitmq connection", "error", err)
	}
}
