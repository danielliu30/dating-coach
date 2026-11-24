package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/danielliu30/dating-coach/backend/internal/account"
	"github.com/danielliu30/dating-coach/backend/internal/analysis"
	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/coaching"
	"github.com/danielliu30/dating-coach/backend/internal/config"
	"github.com/danielliu30/dating-coach/backend/internal/httpx"
	"github.com/danielliu30/dating-coach/backend/internal/notify"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

type task func(context.Context) (any, error)

func pushHandler[T any](handle func(context.Context, T, bool) error, maxAttempts int, deletion bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Message struct {
				Data       []byte            `json:"data"`
				ID         string            `json:"messageId"`
				Attributes map[string]string `json:"attributes"`
			} `json:"message"`
			Subscription    string `json:"subscription"`
			DeliveryAttempt int    `json:"deliveryAttempt"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 10<<20))
		err := dec.Decode(&envelope)
		if err == nil {
			var extra any
			if e := dec.Decode(&extra); e != io.EOF {
				err = errors.New("trailing envelope content")
			}
		}
		var job T
		if err == nil {
			err = json.Unmarshal(envelope.Message.Data, &job)
		}
		if err != nil {
			slog.Error("undecodable Pub/Sub message acknowledged and discarded", "error", err)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		last := envelope.DeliveryAttempt >= maxAttempts
		if err := handle(r.Context(), job, last); err != nil {
			slog.Error("Pub/Sub job failed", "message_id", envelope.Message.ID, "error", err, "last_attempt", last)
			if deletion && last {
				slog.Error("account deletions need an operator", "message_id", envelope.Message.ID, "error", err)
			}
			http.Error(w, "job failed", 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// Cloud Run IAM (--no-allow-unauthenticated) authenticates push and scheduler calls.
func workerRouter(a analysis.JobHandler, d account.JobHandler, maxAttempts int, tasks map[string]task, stats *httpx.Stats) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /stats", stats.Handler)
	mux.HandleFunc("POST /pubsub/analysis", pushHandler(a, maxAttempts, false))
	mux.HandleFunc("POST /pubsub/account-deletion", pushHandler(d, maxAttempts, true))
	mux.HandleFunc("POST /pubsub/email-outbox", pushHandler(func(ctx context.Context, _ json.RawMessage, _ bool) error {
		_, err := tasks["relay-emails"](ctx)
		return err
	}, maxAttempts, false))
	run := func(w http.ResponseWriter, r *http.Request, names []string) {
		if len(names) == 0 {
			http.Error(w, "task required", 400)
			return
		}
		for _, name := range names {
			if _, ok := tasks[name]; !ok {
				http.NotFound(w, r)
				return
			}
		}
		results := make([]any, 0, len(names))
		status := http.StatusOK
		for _, name := range names {
			result, err := tasks[name](r.Context())
			entry := map[string]any{"task": name, "result": result}
			if err != nil {
				slog.Error("scheduled task failed", "task", name, "error", err)
				entry["error"] = err.Error()
				status = http.StatusInternalServerError
			}
			results = append(results, entry)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(results)
	}
	mux.HandleFunc("POST /tasks/batch", func(w http.ResponseWriter, r *http.Request) { run(w, r, r.URL.Query()["task"]) })
	mux.HandleFunc("POST /tasks/{name}", func(w http.ResponseWriter, r *http.Request) { run(w, r, []string{r.PathValue("name")}) })
	return mux
}

// closeablePublisher is the account deletion publisher plus the Close both
// backends already implement.
type closeablePublisher interface {
	account.Publisher
	Close()
}

// openJobPublisher returns the deletion-queue publisher matching the queue
// backend — Pub/Sub on Cloud Run, RabbitMQ self-hosted — so /tasks/* works
// in both modes without requiring GCP credentials locally.
func openJobPublisher(ctx context.Context, cfg *config.Config) (closeablePublisher, error) {
	if cfg.QueueBackend == "pubsub" {
		return analysis.OpenPubSubPublisher[account.Job](ctx, cfg.GCPProjectID, cfg.AccountDeletionQueue)
	}
	return account.OpenPublisher(cfg.RabbitMQURL, cfg.AccountDeletionQueue)
}

func runHTTP(ctx context.Context, cfg *config.Config, q *db.Queries, a *analysis.Worker, d *account.Worker, deny *auth.Denylist, bookings *coaching.Service, n notify.Notifier, payments bool, stats *httpx.Stats) error {
	publisher, err := openJobPublisher(ctx, cfg)
	if err != nil {
		return err
	}
	defer publisher.Close()
	relay := account.NewRelay(q, publisher)
	emails := notify.NewRelay(q, n)
	tasks := map[string]task{
		"purge-refresh-tokens": func(ctx context.Context) (any, error) { return d.PurgeRefreshTokens(ctx) },
		"relay-deletions":      func(ctx context.Context) (any, error) { return relay.Drain(ctx) },
		"relay-emails":         func(ctx context.Context) (any, error) { return emails.Drain(ctx) },
		"check-email-outbox":   func(ctx context.Context) (any, error) { return emails.CheckExhausted(ctx) },
		"expire-bookings":      func(ctx context.Context) (any, error) { return expireBookingsPass(ctx, bookings) },
		"replay-revocations":   func(ctx context.Context) (any, error) { return nil, deny.ReplayRevocations(ctx, q) },
	}
	if payments {
		tasks["sweep-payments"] = func(ctx context.Context) (any, error) { return sweepPaymentsPass(ctx, bookings) }
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	server := &http.Server{Addr: ":" + port, Handler: stats.Middleware(workerRouter(a.Handle, d.Handle, cfg.PubSubMaxDeliveryAttempts, tasks, stats)), ReadHeaderTimeout: 10 * time.Second, BaseContext: nil}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}
