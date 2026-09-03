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

	"github.com/danielliu30/dating-coach/backend/internal/account"
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

// chain composes middlewares into one that applies them left to right, so a
// route group taking a single middleware still gets both authentication and the
// revocation check.
func chain(middlewares ...func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			next = middlewares[i](next)
		}
		return next
	}
}

// run loads configuration, opens the backing services, assembles each feature's
// service and handler, and serves until a signal arrives.
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

	queue, err := analysis.OpenPublisher(cfg.RabbitMQURL, cfg.AnalysisQueue)
	if err != nil {
		return err
	}
	defer queue.Close()

	deletions, err := account.OpenPublisher(cfg.RabbitMQURL, cfg.AccountDeletionQueue)
	if err != nil {
		return err
	}
	defer deletions.Close()

	notifier := notify.New(cfg)
	issuer := auth.NewTokenIssuer(cfg.JWTSecret)
	limiter := auth.NewRateLimiter(rdb, cfg.AuthRateLimit, cfg.AuthRateWindow)
	// A revocation only has to outlive the tokens that existed when it was made.
	denylist := auth.NewDenylist(rdb, cfg.JWTTTL)
	// Every authenticated route also consults the denylist, because a deleted
	// account's token stays validly signed until it expires on its own.
	active := auth.RequireActive(denylist)

	authHandler := auth.NewHandler(
		auth.NewService(pg.Queries, issuer, notifier, cfg.BcryptCost, cfg.PublicAppURL, denylist, deletions, cfg.JWTTTL, cfg.VerifyTokenTTL),
		limiter,
	)
	coachingHandler := coaching.NewHandler(coaching.NewService(pg.Pool, pg.Queries))
	hub := chat.NewHub(rdb)
	// Sockets authenticated before a deletion would otherwise keep running
	// until their next scheduled re-check; this closes them as it happens.
	go auth.WatchRevocations(ctx, rdb, hub.EndSessions)
	chatHandler := chat.NewHandler(chat.NewService(pg.Queries, hub), hub, denylist, cfg.CORSOrigins)
	analysisHandler := analysis.NewHandler(analysis.NewService(pg.Pool, pg.Queries, queue))

	router := newRouter(cfg, auth.Middleware(issuer), active, handlers{
		auth:     authHandler,
		coaching: coachingHandler,
		chat:     chatHandler,
		analysis: analysisHandler,
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

// handlers groups the feature handlers newRouter mounts.
type handlers struct {
	auth     *auth.Handler
	coaching *coaching.Handler
	chat     *chat.Handler
	analysis *analysis.Handler
}

// newRouter builds the API routing tree: an unauthenticated health check, the
// auth endpoints (reachable by verify-scoped tokens so a fresh sign-up can
// confirm its address) and a private group every other feature is mounted
// under, which authenticates the caller and then demands a session-scoped
// token. verifyToken and active are taken separately rather than pre-chained
// because the auth endpoints apply the revocation check to only some of their
// routes.
func newRouter(cfg *config.Config, verifyToken, active func(http.Handler) http.Handler, h handlers) http.Handler {
	authenticate := chain(verifyToken, active)

	router := chi.NewRouter()
	router.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, middleware.Logger)
	router.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.CORSOrigins,
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok", "env": cfg.Env})
	})

	router.Route("/api/v1", func(v1 chi.Router) {
		v1.Mount("/auth", h.auth.Routes(verifyToken, active))
		v1.Group(func(private chi.Router) {
			private.Use(authenticate, auth.RequireScope(auth.ScopeSession))
			private.Mount("/coaching", h.coaching.Routes())
			private.Mount("/chat", h.chat.Routes())
			private.Mount("/analysis", h.analysis.Routes())
			private.Route("/coach", func(coach chi.Router) {
				coach.Use(auth.RequireCoach)
				coach.Mount("/", h.coaching.CoachRoutes())
			})
		})
	})
	return router
}
