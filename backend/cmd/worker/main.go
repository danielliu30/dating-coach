// Command worker consumes conversation-analysis jobs from RabbitMQ, calls the
// ML analyzer and stores the results.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	worker := analysis.NewWorker(
		pg.Queries,
		analysis.NewMLClient(cfg.MLServiceURL, cfg.MLServiceTimeout),
		notify.New(cfg),
	)

	slog.Info("analysis worker started", "queue", cfg.AnalysisQueue, "ml_service", cfg.MLServiceURL)
	return consume(ctx, cfg.RabbitMQURL, cfg.AnalysisQueue, worker.Handle)
}

const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
)

// consume keeps a consumer attached to the queue for the life of ctx. A broker
// restart or dropped channel is transient, so the worker redials with
// exponential backoff instead of exiting and leaving jobs queued indefinitely.
func consume(ctx context.Context, url, name string, handle analysis.JobHandler) error {
	backoff := minBackoff
	for {
		err := runConsumer(ctx, url, name, handle)
		if ctx.Err() != nil {
			return nil
		}
		slog.Error("analysis consumer stopped", "error", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

func runConsumer(ctx context.Context, url, name string, handle analysis.JobHandler) error {
	queue, err := analysis.OpenQueue(url, name)
	if err != nil {
		return err
	}
	defer queue.Close()
	return queue.Consume(ctx, handle)
}
