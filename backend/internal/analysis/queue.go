package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
)

// prefetchCount bounds both unacked deliveries and in-flight handlers.
const prefetchCount = 4

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

// OpenQueue dials the broker, declares the durable queue and configures
// prefetch plus publisher confirms. Callers must Close the result.
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
	if err := channel.Qos(prefetchCount, 0, false); err != nil {
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

// Publish sends a job and waits for the broker to confirm it, so callers only
// see success once the job is durably queued.
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

// JobPublisher owns a Queue for publishing and redials it when the broker drops the
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
	slog.Warn("republishing analysis job on a fresh channel", "error", err, "analysis_id", job.AnalysisID)
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

// JobHandler processes one analysis job. lastAttempt reports that the delivery
// was already redelivered once, so a failure now is terminal.
type JobHandler func(ctx context.Context, job Job, lastAttempt bool) error

// Consume blocks until ctx is cancelled or the channel drops, running up to
// prefetchCount jobs concurrently so one slow model call does not stall the
// deliveries behind it. A failing first attempt is requeued so a transient ML
// or network blip does not lose the analysis.
func (q *Queue) Consume(ctx context.Context, handle JobHandler) error {
	deliveries, err := q.channel.Consume(q.name, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume queue: %w", err)
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, prefetchCount)
	defer wg.Wait()
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
			select {
			case <-ctx.Done():
				return nil
			case slots <- struct{}{}:
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				if err := handle(ctx, job, delivery.Redelivered); err != nil {
					slog.Error("handle analysis job", "error", err, "analysis_id", job.AnalysisID, "requeue", !delivery.Redelivered)
					_ = delivery.Nack(false, !delivery.Redelivered)
					return
				}
				if err := delivery.Ack(false); err != nil {
					slog.Error("ack analysis job", "error", err, "analysis_id", job.AnalysisID)
				}
			}()
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
