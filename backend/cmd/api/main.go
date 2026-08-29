// Command api serves the REST + WebSocket API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/danielliu30/dating-coach/backend/internal/analysis"
	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/chat"
	"github.com/danielliu30/dating-coach/backend/internal/coaching"
	"github.com/danielliu30/dating-coach/backend/internal/config"
	"github.com/danielliu30/dating-coach/backend/internal/httpx"
	"github.com/danielliu30/dating-coach/backend/internal/notify"
	"github.com/danielliu30/dating-coach/backend/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if err := run(); err != nil {
		slog.Error("api exited", "error", err)
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

	rdb, err := store.OpenRedis(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()

	queue, err := analysis.OpenQueue(cfg.RabbitMQURL, cfg.AnalysisQueue)
	if err != nil {
		return err
	}
	defer queue.Close()

	notifier := notify.New(cfg)
	issuer := auth.NewTokenIssuer(cfg.JWTSecret, cfg.JWTTTL)
	limiter := auth.NewRateLimiter(rdb, cfg.AuthRateLimit, cfg.AuthRateWindow)
	authenticate := auth.Middleware(issuer)

	authHandler := auth.NewHandler(
		auth.NewService(pg.Queries, issuer, notifier, cfg.BcryptCost, cfg.PublicAppURL),
		limiter,
	)
	coachingHandler := coaching.NewHandler(coaching.NewService(pg.Pool, pg.Queries))
	hub := chat.NewHub(rdb)
	chatHandler := chat.NewHandler(chat.NewService(pg.Queries, hub), hub, cfg.CORSOrigins)
	analysisHandler := analysis.NewHandler(analysis.NewService(pg.Pool, pg.Queries, queue))

	router := chi.NewRouter()
	router.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, middleware.Logger)
	router.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.CORSOrigins,
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok", "env": cfg.Env})
	})

	router.Route("/api/v1", func(v1 chi.Router) {
		v1.Mount("/auth", authHandler.Routes(authenticate))
		v1.Group(func(private chi.Router) {
			private.Use(authenticate)
			private.Mount("/coaching", coachingHandler.Routes())
			private.Mount("/chat", chatHandler.Routes())
			private.Mount("/analysis", analysisHandler.Routes())
			private.Route("/coach", func(coach chi.Router) {
				coach.Use(auth.RequireCoach)
				coach.Mount("/", coachingHandler.CoachRoutes())
			})
		})
	})

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("api listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		slog.Info("shutting down api")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}
