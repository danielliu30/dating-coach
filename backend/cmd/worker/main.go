// Command worker consumes conversation-analysis jobs from RabbitMQ, calls the
// ML analyzer and stores the results. It also drains the account deletion queue
// and watches that queue's dead letters.
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
	"github.com/danielliu30/dating-coach/backend/internal/config"
	"github.com/danielliu30/dating-coach/backend/internal/notify"
	"github.com/danielliu30/dating-coach/backend/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if err := run(); err != nil {
		slog.Error("worker exited", "error", err)
		os.Exit(1)
	}
}

// run builds the analysis and account deletion consumers and keeps them
// consuming until a signal arrives.
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

	analysisWorker := analysis.NewWorker(
		pg.Queries,
		analysis.NewMLClient(cfg.MLServiceURL, cfg.MLServiceTimeout),
		notify.New(cfg),
	)
	deletionWorker := account.NewWorker(pg.Queries)

	slog.Info("worker started",
		"analysis_queue", cfg.AnalysisQueue,
		"deletion_queue", cfg.AccountDeletionQueue,
		"ml_service", cfg.MLServiceURL,
	)

	tasks := map[string]func(context.Context) error{
		"analysis consumer": func(ctx context.Context) error {
			queue, err := analysis.OpenQueue(cfg.RabbitMQURL, cfg.AnalysisQueue)
			if err != nil {
				return err
			}
			defer queue.Close()
			return queue.Consume(ctx, analysisWorker.Handle)
		},
		"account deletion consumer": func(ctx context.Context) error {
			queue, err := account.OpenQueue(cfg.RabbitMQURL, cfg.AccountDeletionQueue)
			if err != nil {
				return err
			}
			defer queue.Close()
			return queue.Consume(ctx, deletionWorker.Handle)
		},
		"account deletion dead-letter monitor": func(ctx context.Context) error {
			queue, err := account.OpenQueue(cfg.RabbitMQURL, cfg.AccountDeletionQueue)
			if err != nil {
				return err
			}
			defer queue.Close()
			return watchDeadLetters(ctx, queue, cfg.AccountDeletionQueue, cfg.DeadLetterAlertPeriod)
		},
	}

	var wg sync.WaitGroup
	for name, task := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			supervise(ctx, name, task)
		}()
	}
	wg.Wait()
	return nil
}

const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
)

// supervise runs task for the life of ctx. A broker restart or dropped channel
// is transient, so a returning task is restarted with exponential backoff
// instead of leaving the queue unattended.
func supervise(ctx context.Context, name string, task func(context.Context) error) {
	backoff := minBackoff
	for {
		err := task(ctx)
		if ctx.Err() != nil {
			return
		}
		slog.Error("worker task stopped", "task", name, "error", err, "retry_in", backoff)
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

// watchDeadLetters logs the deletion dead-letter queue depth every period, at
// warn level while it is non-empty. Alerting on that depth is what surfaces
// deletes that never ran, which otherwise become invisible once the deleted
// account's denylist entry expires and its rows are still present. period must
// be positive, which config.Load enforces.
func watchDeadLetters(ctx context.Context, queue *account.Queue, name string, period time.Duration) error {
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
				slog.Warn("account deletions dead-lettered",
					"queue", account.DeadLetterQueue(name),
					"depth", depth,
				)
				continue
			}
			slog.Debug("account deletion dead-letter queue empty", "queue", account.DeadLetterQueue(name))
		}
	}
}
