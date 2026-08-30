package account

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
)

// prefetchCount bounds unacked deliveries; deletes are cheap and rare, so one
// at a time keeps the ordering easy to reason about.
const prefetchCount = 1

// Job is the unit of work handed to the deletion worker: the account whose rows
// must be removed.
type Job struct {
	UserID string `json:"user_id"`
}

// Queue is a RabbitMQ wrapper for the account deletion queue, used by the API
// (publish) and the worker (consume). Deliveries that exhaust their retries are
// dead-lettered instead of dropped, so a failed delete stays visible after the
// denylist entry for the account expires.
type Queue struct {
	conn    *amqp.Connection
	channel *amqp.Channel
	name    string
}

// DeadLetterExchange returns the exchange failed deliveries of queue are routed to.
func DeadLetterExchange(queue string) string { return queue + ".dlx" }

// DeadLetterQueue returns the queue holding deletions that exhausted their retries.
func DeadLetterQueue(queue string) string { return queue + ".dead" }

// OpenQueue dials the broker and declares the deletion queue, its dead-letter
// exchange and the bound dead-letter queue, plus prefetch and publisher
// confirms. Callers must Close the result.
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
	queue := &Queue{conn: conn, channel: channel, name: name}
	if err := queue.declare(name); err != nil {
		queue.Close()
		return nil, err
	}
	if err := channel.Qos(prefetchCount, 0, false); err != nil {
		queue.Close()
		return nil, fmt.Errorf("set qos: %w", err)
	}
	// Publisher confirms: the caller has already revoked the account's tokens,
	// so it must not report success for a delete the broker never stored.
	if err := channel.Confirm(false); err != nil {
		queue.Close()
		return nil, fmt.Errorf("enable publisher confirms: %w", err)
	}
	return queue, nil
}

// declare sets up the dead-letter topology and the work queue pointing at it.
func (q *Queue) declare(name string) error {
	exchange := DeadLetterExchange(name)
	if err := q.channel.ExchangeDeclare(exchange, amqp.ExchangeFanout, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare exchange %q: %w", exchange, err)
	}
	dead := DeadLetterQueue(name)
	if _, err := q.channel.QueueDeclare(dead, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare queue %q: %w", dead, err)
	}
	if err := q.channel.QueueBind(dead, "", exchange, false, nil); err != nil {
		return fmt.Errorf("bind queue %q: %w", dead, err)
	}
	if _, err := q.channel.QueueDeclare(name, true, false, false, false, amqp.Table{
		"x-dead-letter-exchange": exchange,
	}); err != nil {
		return fmt.Errorf("declare queue %q: %w", name, err)
	}
	return nil
}

// Publish sends a job and waits for the broker to confirm it, so callers only
// see success once the deletion is durably queued.
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
		return fmt.Errorf("publish job: broker nacked deletion of account %s", job.UserID)
	}
	return nil
}

// DeadLetterDepth returns the number of deletions parked in the dead-letter
// queue. Alerting on a non-zero depth is what catches deletes that never
// completed, since the denylist entry protecting them expires with the JWT TTL.
func (q *Queue) DeadLetterDepth() (int, error) {
	// Passive declare only inspects the queue, but it closes the channel when
	// the queue is missing, so declare it the same way OpenQueue does.
	state, err := q.channel.QueueDeclare(DeadLetterQueue(q.name), true, false, false, false, nil)
	if err != nil {
		return 0, fmt.Errorf("inspect dead-letter queue: %w", err)
	}
	return state.Messages, nil
}

// JobPublisher owns a Queue for publishing and redials when the broker drops the
// connection, so an API process outlives a RabbitMQ restart.
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

// Publish satisfies Publisher. A failure that is not caused by the caller's
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

// JobHandler performs one account deletion. Returning an error asks for another
// delivery; lastAttempt reports that this delivery was already retried, so a
// failure now dead-letters the job.
type JobHandler func(ctx context.Context, job Job, lastAttempt bool) error

// Consume blocks until ctx is cancelled or the channel drops. A first failure is
// requeued so a transient database blip does not lose the deletion; a second
// failure, and any undecodable delivery, is rejected onto the dead-letter
// exchange for operators to inspect.
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
				slog.Error("decode account deletion job", "error", err)
				_ = delivery.Nack(false, false)
				continue
			}
			if err := handle(ctx, job, delivery.Redelivered); err != nil {
				slog.Error("handle account deletion job", "error", err, "user_id", job.UserID, "requeue", !delivery.Redelivered)
				_ = delivery.Nack(false, !delivery.Redelivered)
				continue
			}
			if err := delivery.Ack(false); err != nil {
				slog.Error("ack account deletion job", "error", err, "user_id", job.UserID)
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
