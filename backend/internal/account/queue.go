// Package account owns account lifecycle work that outlives a request:
// deleting an account's rows after its sessions have already been revoked.
package account

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
)

// prefetchCount bounds unacked deletions. Deletions are rare and cascade across
// most tables, so they are processed one at a time.
const prefetchCount = 1

// Job is one queued account deletion.
type Job struct {
	UserID string `json:"user_id"`
}

// DeadLetterQueue returns the name of the queue that holds deletions the worker
// could not apply, derived from the work queue's name.
func DeadLetterQueue(name string) string { return name + ".dlq" }

// Queue is a thin RabbitMQ wrapper used by the API (publish) and the worker
// (consume). Its work queue dead-letters into DeadLetterQueue(name): a deletion
// that keeps failing must be kept for an operator, because the account is
// already revoked and the user cannot retry it themselves.
type Queue struct {
	conn    *amqp.Connection
	channel *amqp.Channel
	name    string
}

// OpenQueue dials the broker, declares the work queue and its dead-letter
// queue, and configures prefetch plus publisher confirms. Callers must Close
// the result. Declaring an existing queue with different arguments fails, so a
// queue created before dead-lettering must be removed once.
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
	closeAll := func(err error) (*Queue, error) {
		channel.Close()
		conn.Close()
		return nil, err
	}
	dlq := DeadLetterQueue(name)
	if _, err := channel.QueueDeclare(dlq, true, false, false, false, nil); err != nil {
		return closeAll(fmt.Errorf("declare queue %q: %w", dlq, err))
	}
	args := amqp.Table{"x-dead-letter-exchange": "", "x-dead-letter-routing-key": dlq}
	if _, err := channel.QueueDeclare(name, true, false, false, false, args); err != nil {
		return closeAll(fmt.Errorf("declare queue %q: %w", name, err))
	}
	if err := channel.Qos(prefetchCount, 0, false); err != nil {
		return closeAll(fmt.Errorf("set qos: %w", err))
	}
	// Publisher confirms: without them a broker restart silently swallows a
	// deletion the user was told had been accepted.
	if err := channel.Confirm(false); err != nil {
		return closeAll(fmt.Errorf("enable publisher confirms: %w", err))
	}
	return &Queue{conn: conn, channel: channel, name: name}, nil
}

// Publish sends a deletion and waits for the broker to confirm it, so callers
// only see success once the job is durably queued.
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
		return fmt.Errorf("publish job: broker nacked deletion of %s", job.UserID)
	}
	return nil
}

// JobPublisher owns a Queue for publishing and redials it when the broker drops
// the connection, so an API process outlives a RabbitMQ restart.
type JobPublisher struct {
	url   string
	name  string
	mu    sync.Mutex
	queue *Queue
}

// OpenPublisher dials the broker and returns the publisher used by the API.
func OpenPublisher(url, name string) (*JobPublisher, error) {
	queue, err := OpenQueue(url, name)
	if err != nil {
		return nil, err
	}
	return &JobPublisher{url: url, name: name, queue: queue}, nil
}

// Publish queues a deletion. A failure that is not caused by the caller's
// context is retried once on a fresh connection, since the usual cause is a
// channel the broker closed while the process was idle.
func (p *JobPublisher) Publish(ctx context.Context, job Job) error {
	queue, err := p.acquire()
	if err != nil {
		return err
	}
	err = queue.Publish(ctx, job)
	if err == nil || ctx.Err() != nil {
		return err
	}
	slog.Warn("republishing account deletion on a fresh channel", "error", err, "user_id", job.UserID)
	p.discard(queue)
	queue, dialErr := p.acquire()
	if dialErr != nil {
		return fmt.Errorf("%w (redial: %v)", err, dialErr)
	}
	return queue.Publish(ctx, job)
}

// acquire returns the live queue, dialing a new one when there is none or the
// previous connection is closed.
func (p *JobPublisher) acquire() (*Queue, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.queue != nil && !p.queue.conn.IsClosed() {
		return p.queue, nil
	}
	p.queue = nil
	queue, err := OpenQueue(p.url, p.name)
	if err != nil {
		return nil, err
	}
	p.queue = queue
	return queue, nil
}

// discard drops stale so the next acquire redials, ignoring a queue another
// caller already replaced.
func (p *JobPublisher) discard(stale *Queue) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.queue != stale {
		return
	}
	p.queue = nil
	stale.Close()
}

// Close releases the broker connection; deferred by cmd/api.
func (p *JobPublisher) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.queue != nil {
		p.queue.Close()
		p.queue = nil
	}
}

// JobHandler applies one deletion. lastAttempt reports that the delivery was
// already redelivered once, so a failure now sends the job to the dead-letter
// queue instead of being retried again.
type JobHandler func(ctx context.Context, job Job, lastAttempt bool) error

// Consume blocks until ctx is cancelled or the channel drops. A first failure
// is requeued so a database blip does not strand a revoked account's rows; a
// second failure, and any job that will not decode, is rejected without requeue
// and therefore dead-lettered rather than looping forever.
func (q *Queue) Consume(ctx context.Context, handle JobHandler) error {
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
				slog.Error("decode account deletion", "error", err)
				_ = delivery.Nack(false, false)
				continue
			}
			if err := handle(ctx, job, delivery.Redelivered); err != nil {
				slog.Error("handle account deletion", "error", err, "user_id", job.UserID, "requeue", !delivery.Redelivered)
				_ = delivery.Nack(false, !delivery.Redelivered)
				continue
			}
			if err := delivery.Ack(false); err != nil {
				slog.Error("ack account deletion", "error", err, "user_id", job.UserID)
			}
		}
	}
}

// Close shuts the channel and connection down.
func (q *Queue) Close() {
	if err := q.channel.Close(); err != nil {
		slog.Debug("close rabbitmq channel", "error", err)
	}
	if err := q.conn.Close(); err != nil {
		slog.Debug("close rabbitmq connection", "error", err)
	}
}
