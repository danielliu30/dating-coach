// Command worker consumes conversation-analysis jobs from RabbitMQ, calls the
// ML analyzer and stores the results.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

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

	queue, err := analysis.OpenQueue(cfg.RabbitMQURL, cfg.AnalysisQueue)
	if err != nil {
		return err
	}
	defer queue.Close()

	worker := analysis.NewWorker(
		pg.Queries,
		analysis.NewMLClient(cfg.MLServiceURL, cfg.MLServiceTimeout),
		notify.New(cfg),
	)

	slog.Info("analysis worker started", "queue", cfg.AnalysisQueue, "ml_service", cfg.MLServiceURL)
	return queue.Consume(ctx, worker.Handle)
}
