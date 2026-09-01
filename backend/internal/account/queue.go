// Package account owns account lifecycle work that outlives a request:
// revoking a deleted account's sessions and removing its rows.
package account

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// prefetchCount bounds unacked deletions. Deletions are rare and cascade across
// most tables, so they are processed one at a time.
const prefetchCount = 1

// maxAttempts is how many times a deletion is handed to the handler before it
// is dead-lettered. With retryDelay between attempts it bounds how long a
// dependency outage can hold up an accepted deletion: the account keeps its rows
// for at most maxAttempts*retryDelay before an operator is alerted.
const maxAttempts = 20

// attemptsHeader carries how many times a deletion has already been handled.
// RabbitMQ's Redelivered flag cannot serve as the counter: it is also set when
// a delivery is recovered after a worker or channel dies, which is not a failed
// attempt.
const attemptsHeader = "x-attempts"

// retryDelay is how long a failed deletion waits before the next attempt. The
// usual cause of a failure is a database that is down or overloaded, and an
// immediate republish would spend every attempt inside the same outage.
const retryDelay = 30 * time.Second

// Job is one queued account deletion.
type Job struct {
	UserID string `json:"user_id"`
}

// DeadLetterQueue returns the name of the queue that holds deletions the worker
// could not apply, derived from the work queue's name.
func DeadLetterQueue(name string) string { return name + ".dlq" }

// RetryQueue returns the name of the queue that holds deletions waiting for
// their next attempt, derived from the work queue's name. Nothing consumes it:
// its messages expire back onto the work queue.
func RetryQueue(name string) string { return name + ".retry" }

// Queue is a thin RabbitMQ wrapper used by the API (publish) and the worker
// (consume). Its work queue dead-letters into DeadLetterQueue(name): a deletion
// that keeps failing must be kept for an operator, because its owner has been
// told the account is gone and cannot retry it themselves.
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
	// The retry queue has no consumer: a message sits there until its TTL runs
	// out and the broker dead-letters it back onto the work queue.
	retry := RetryQueue(name)
	retryArgs := amqp.Table{
		"x-message-ttl":             retryDelay.Milliseconds(),
		"x-dead-letter-exchange":    "",
		"x-dead-letter-routing-key": name,
	}
	if _, err := channel.QueueDeclare(retry, true, false, false, false, retryArgs); err != nil {
		return closeAll(fmt.Errorf("declare queue %q: %w", retry, err))
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
	return q.publish(ctx, q.name, job, 0)
}

// publish queues job on routingKey with its attempt count, which Consume uses
// to decide between another attempt and the dead-letter queue. Retries go to
// RetryQueue rather than the work queue, so they are only redelivered once the
// retry queue's TTL has elapsed.
func (q *Queue) publish(ctx context.Context, routingKey string, job Job, attempts int64) error {
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("encode job: %w", err)
	}
	confirm, err := q.channel.PublishWithDeferredConfirmWithContext(ctx, "", routingKey, true, false, amqp.Publishing{
		ContentType:  "application/json",
		Body:         body,
		DeliveryMode: amqp.Persistent,
		Headers:      amqp.Table{attemptsHeader: attempts},
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

// JobHandler applies one deletion. lastAttempt reports that no further attempt
// follows, so a failure now sends the job to the dead-letter queue.
type JobHandler func(ctx context.Context, job Job, lastAttempt bool) error

// attemptsOf reads the attempt count a previous pass stamped on the delivery.
// AMQP field types are broker- and client-dependent, so the integer widths are
// all accepted; anything else counts as a first attempt.
func attemptsOf(headers amqp.Table) int64 {
	switch v := headers[attemptsHeader].(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	case int16:
		return int64(v)
	case int:
		return int64(v)
	default:
		return 0
	}
}

// Consume blocks until ctx is cancelled or the channel drops. A failed deletion
// is republished with an incremented attempt count so a database blip does not
// strand a revoked account's rows; once maxAttempts is reached, and for any job
// that will not decode, the delivery is rejected without requeue and therefore
// dead-lettered rather than looping forever.
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
			attempts := attemptsOf(delivery.Headers) + 1
			lastAttempt := attempts >= maxAttempts
			if err := handle(ctx, job, lastAttempt); err != nil {
				slog.Error("handle account deletion", "error", err, "user_id", job.UserID, "attempts", attempts)
				if lastAttempt {
					_ = delivery.Nack(false, false)
					continue
				}
				// Republish rather than requeue: a requeued delivery keeps the
				// original headers, so the count would never advance. A failure
				// here falls back to a plain requeue, which repeats an attempt
				// but never drops the deletion.
				if err := q.publish(ctx, RetryQueue(q.name), job, attempts); err != nil {
					slog.Error("requeue account deletion", "error", err, "user_id", job.UserID)
					_ = delivery.Nack(false, true)
					continue
				}
				if err := delivery.Ack(false); err != nil {
					slog.Error("ack retried account deletion", "error", err, "user_id", job.UserID)
				}
				continue
			}
			if err := delivery.Ack(false); err != nil {
				slog.Error("ack account deletion", "error", err, "user_id", job.UserID)
			}
		}
	}
}

// DeadLetterDepth reports how many deletions are sitting in the dead-letter
// queue. Those accounts still hold rows, and the revocation that outlives them
// expires with the JWT TTL, so a non-zero depth needs an operator before then.
// The passive declare closes the channel if the queue is missing, which makes
// the owning Queue unusable; callers should hold a connection of their own
// rather than share the consumer's.
func (q *Queue) DeadLetterDepth() (int, error) {
	dlq := DeadLetterQueue(q.name)
	state, err := q.channel.QueueDeclarePassive(dlq, true, false, false, false, nil)
	if err != nil {
		return 0, fmt.Errorf("inspect queue %q: %w", dlq, err)
	}
	return state.Messages, nil
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
