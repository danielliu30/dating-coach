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
	// Publisher confirms: without them a broker restart silently swallows jobs
	// that callers were told had been queued.
	if err := channel.Confirm(false); err != nil {
		channel.Close()
		conn.Close()
		return nil, fmt.Errorf("enable publisher confirms: %w", err)
	}
	return &Queue{conn: conn, channel: channel, name: name}, nil
}

func (q *Queue) Publish(ctx context.Context, job Job) error {
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("encode job: %w", err)
	}
	confirm, err := q.channel.PublishWithDeferredConfirmWithContext(ctx, "", q.name, true, false, amqp.Publishing{
		ContentType:  "application/json",
		Body:         body,
		DeliveryMode: amqp.Persistent,
	})
	if err != nil {
		return fmt.Errorf("publish job: %w", err)
	}
	acked, err := confirm.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("await publish confirm: %w", err)
	}
	if !acked {
		return fmt.Errorf("publish job: broker nacked analysis %s", job.AnalysisID)
	}
	return nil
}

// Consume blocks until ctx is cancelled, calling handle for every job. handle
// receives whether this is the last attempt (the delivery was already
// redelivered once); a failing first attempt is requeued so a transient ML or
// network blip does not lose the analysis.
func (q *Queue) Consume(ctx context.Context, handle func(ctx context.Context, job Job, lastAttempt bool) error) error {
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
			if err := handle(ctx, job, delivery.Redelivered); err != nil {
				slog.Error("handle analysis job", "error", err, "analysis_id", job.AnalysisID, "requeue", !delivery.Redelivered)
				_ = delivery.Nack(false, !delivery.Redelivered)
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
