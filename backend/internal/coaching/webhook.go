package coaching

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/danielliu30/dating-coach/backend/internal/httpx"
	"github.com/danielliu30/dating-coach/backend/internal/payments"
)

// WebhookRoutes mounts POST /payments/webhook. The provider calls it without a
// bearer token — the signed payload is the credential, verified inside
// ParseWebhook. It is only mounted when payments are enabled.
func (h *Handler) WebhookRoutes(provider payments.Provider) http.Handler {
	r := chi.NewRouter()
	r.Post("/webhook", h.paymentWebhook(provider))
	return r
}

// paymentWebhook verifies the provider signature, normalises the event and
// applies it to the booking state machine. Bad signatures get 400; an
// unknown checkout ref is acked so the provider stops redelivering an event
// that can never match a session; everything else gets 500 so the provider
// retries while the failure is transient.
func (h *Handler) paymentWebhook(provider payments.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "unreadable payload")
			return
		}
		ev, err := provider.ParseWebhook(payload, r.Header.Get("Stripe-Signature"))
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid webhook signature")
			return
		}
		if err := h.svc.ApplyPaymentEvent(r.Context(), ev); err != nil {
			if errors.Is(err, ErrNotFound) {
				w.WriteHeader(http.StatusOK)
				return
			}
			slog.Error("payment webhook failed", "event_id", ev.ID, "kind", ev.Kind, "err", err)
			httpx.Error(w, http.StatusInternalServerError, "webhook processing failed")
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}
