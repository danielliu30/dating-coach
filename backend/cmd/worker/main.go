// Command worker consumes the background queues: conversation-analysis jobs,
// which it runs through the ML analyzer and stores, and account deletions,
// whose sessions it revokes before removing their rows.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/danielliu30/dating-coach/backend/internal/account"
	"github.com/danielliu30/dating-coach/backend/internal/analysis"
	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/coaching"
	"github.com/danielliu30/dating-coach/backend/internal/config"
	"github.com/danielliu30/dating-coach/backend/internal/notify"
	"github.com/danielliu30/dating-coach/backend/internal/store"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if err := run(); err != nil {
		slog.Error("worker exited", "error", err)
		os.Exit(1)
	}
}

// run builds the analysis worker and keeps it consuming until a signal arrives.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pg, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pg.Close()

	notifier := notify.New(cfg)
	worker := analysis.NewWorker(
		pg.Queries,
		analysis.NewMLClient(cfg.MLServiceURL, cfg.MLServiceTimeout),
		notifier,
	)
	bookings := coaching.NewService(pg.Pool, pg.Queries, cfg.PublicAppURL, cfg.MailFrom)
	// The worker revokes the sessions of the accounts it deletes, so it needs
	// the same denylist the API writes.
	rdb, err := store.OpenRedis(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()

	deleter := account.NewWorker(pg.Queries, auth.NewDenylist(rdb, cfg.JWTTTL))

	slog.Info("worker started",
		"analysis_queue", cfg.AnalysisQueue,
		"deletion_queue", cfg.AccountDeletionQueue,
		"dead_letter_alert_period", cfg.DeadLetterAlertPeriod,
		"ml_service", cfg.MLServiceURL,
	)

	var wg sync.WaitGroup
	wg.Add(8)
	go func() {
		defer wg.Done()
		deleter.PurgeExpiredRefreshTokens(ctx, refreshTokenPurgeInterval)
	}()
	go func() {
		defer wg.Done()
		consume(ctx, "analysis", func(ctx context.Context) error {
			return runAnalysisConsumer(ctx, cfg.RabbitMQURL, cfg.AnalysisQueue, worker.Handle)
		})
	}()
	go func() {
		defer wg.Done()
		consume(ctx, "account deletion", func(ctx context.Context) error {
			return runDeletionConsumer(ctx, cfg.RabbitMQURL, cfg.AccountDeletionQueue, deleter.Handle)
		})
	}()
	go func() {
		defer wg.Done()
		consume(ctx, "dead letter monitor", func(ctx context.Context) error {
			return watchDeadLetters(ctx, cfg.RabbitMQURL, cfg.AccountDeletionQueue, cfg.DeadLetterAlertPeriod)
		})
	}()
	go func() {
		defer wg.Done()
		consume(ctx, "deletion outbox relay", func(ctx context.Context) error {
			return relayDeletions(ctx, cfg.RabbitMQURL, cfg.AccountDeletionQueue, pg.Queries)
		})
	}()
	go func() {
		defer wg.Done()
		consume(ctx, "email outbox relay", func(ctx context.Context) error {
			return notify.NewRelay(pg.Queries, notifier).Run(ctx)
		})
	}()
	go func() {
		defer wg.Done()
		consume(ctx, "email outbox monitor", func(ctx context.Context) error {
			return notify.NewRelay(pg.Queries, notifier).WatchExhausted(ctx, cfg.DeadLetterAlertPeriod)
		})
	}()
	go func() {
		defer wg.Done()
		consume(ctx, "booking expiry", func(ctx context.Context) error {
			return expireBookings(ctx, bookings)
		})
	}()
	wg.Wait()
	return nil
}

const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
	// Expired refresh tokens are only dead weight, so sweeping them hourly is
	// frequent enough to keep the table bounded.
	refreshTokenPurgeInterval = time.Hour
)

// consume runs one consumer for the life of ctx, restarting it with exponential
// backoff. A broker restart or dropped channel is transient, so the worker
// redials instead of exiting and leaving jobs queued indefinitely. kind names
// the consumer in the log lines; run holds a connection until it breaks.
func consume(ctx context.Context, kind string, run func(context.Context) error) {
	backoff := minBackoff
	for {
		err := run(ctx)
		if ctx.Err() != nil {
			return
		}
		slog.Error("consumer stopped", "consumer", kind, "error", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

// runAnalysisConsumer holds one broker connection for as long as it stays healthy.
func runAnalysisConsumer(ctx context.Context, url, name string, handle analysis.JobHandler) error {
	queue, err := analysis.OpenQueue(url, name)
	if err != nil {
		return err
	}
	defer queue.Close()
	return queue.Consume(ctx, handle)
}

// watchDeadLetters logs the depth of the deletion dead-letter queue every
// period until ctx ends, so a deployment can alert on deletions that failed
// after the API already answered the account holder. It holds its own broker
// connection: a failed inspection closes the channel, which must not take the
// consumer down with it. period must be positive; config.Load rejects anything
// else.
func watchDeadLetters(ctx context.Context, url, name string, period time.Duration) error {
	queue, err := account.OpenQueue(url, name)
	if err != nil {
		return err
	}
	defer queue.Close()

	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			depth, err := queue.DeadLetterDepth()
			if err != nil {
				return err
			}
			if depth > 0 {
				slog.Warn("account deletions need an operator",
					"queue", account.DeadLetterQueue(name),
					"depth", depth,
				)
			}
		}
	}
}

// relayDeletions queues the deletions that were recorded in the outbox but never
// published, until ctx ends. It holds its own publisher, which redials on its
// own, so the broker outage that stranded those deletions does not also keep the
// relay from picking them up once it is over.
func relayDeletions(ctx context.Context, url, name string, queries *db.Queries) error {
	publisher, err := account.OpenPublisher(url, name)
	if err != nil {
		return err
	}
	defer publisher.Close()
	return account.NewRelay(queries, publisher).Run(ctx)
}

// bookingExpiryInterval is how often unanswered session requests are checked
// against their deadline; it bounds how long an expired request keeps its slot.
const bookingExpiryInterval = time.Minute

// expireBookings releases session requests whose coach did not respond in time,
// every bookingExpiryInterval until ctx ends. A failed pass is logged and
// retried on the next tick rather than returned.
func expireBookings(ctx context.Context, svc *coaching.Service) error {
	ticker := time.NewTicker(bookingExpiryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			n, err := svc.ExpirePending(ctx)
			if err != nil {
				slog.Error("expire session requests", "error", err)
				continue
			}
			if n > 0 {
				slog.Info("expired unanswered session requests", "count", n)
			}
		}
	}
}

// runDeletionConsumer holds one broker connection for as long as it stays
// healthy, on the deletion queue and its dead-letter queue.
func runDeletionConsumer(ctx context.Context, url, name string, handle account.JobHandler) error {
	queue, err := account.OpenQueue(url, name)
	if err != nil {
		return err
	}
	defer queue.Close()
	return queue.Consume(ctx, handle)
}
