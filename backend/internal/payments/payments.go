// Package payments abstracts the third-party payment provider that clients pay
// through to reserve a coaching session. The coaching service only sees the
// Provider interface, so the provider can be swapped or, when payments are
// switched off, replaced by Disabled without the booking code branching.
//
// The model is authorise-then-capture: the hosted checkout only places a hold
// on the client's card, the platform captures it once the coach confirms the
// booking and releases it if the coach declines or the request expires.
package payments

import (
	"context"
	"errors"
	"time"
)

// ErrDisabled is returned by Disabled for operations that need a real
// provider; callers treat it as "payments are switched off".
var ErrDisabled = errors.New("payments are disabled")

// Session payment_status values, shared with the SQL in store/queries.
const (
	// StatusNotRequired is the only status when payments are switched off.
	StatusNotRequired = "not_required"
	// StatusPending means the hosted checkout is open and the card not yet held.
	StatusPending = "pending"
	// StatusAuthorized means the card is held; the coach's answer decides
	// whether it is captured or released.
	StatusAuthorized = "authorized"
	// StatusPaid means the hold was captured; money has moved.
	StatusPaid = "paid"
	// StatusReleasing means a hold that is owed a release the sweeper has not
	// completed yet.
	StatusReleasing = "releasing"
	// StatusReleased means the hold was voided without a charge.
	StatusReleased = "released"
	// StatusExpiring means an open checkout the sweeper still has to close.
	StatusExpiring = "expiring"
	// StatusFailed means no card was ever held: the checkout expired or was
	// abandoned.
	StatusFailed = "failed"
	// StatusRefundDue and StatusRefunded track a captured payment being
	// returned.
	StatusRefundDue = "refund_due"
	StatusRefunded  = "refunded"
)

// CheckoutInput describes the hold to place for one session.
type CheckoutInput struct {
	// SessionID is echoed back by the provider so a webhook can be matched to
	// the booking it pays for.
	SessionID   string
	AmountCents int64
	Currency    string
	Description string
	// CustomerEmail prefills the hosted payment page.
	CustomerEmail string
	// ExpiresAt bounds how long the hosted page accepts a card; it should
	// match the booking's slot hold.
	ExpiresAt time.Time
	// SuccessURL and CancelURL are where the provider sends the customer
	// back to after the hosted page.
	SuccessURL string
	CancelURL  string
}

// Checkout is the provider-side record of a started payment.
type Checkout struct {
	// Ref is the provider's identifier, stored on the session as payment_ref.
	// Capture, Release and Refund all take it.
	Ref string
	// URL is the hosted payment page the client is sent to.
	URL string
}

// EventKind classifies webhook events the booking flow reacts to.
type EventKind string

const (
	// EventAuthorized means the checkout was completed and the card is held,
	// uncaptured.
	EventAuthorized EventKind = "authorized"
	// EventExpired means the hosted page expired without a card being held.
	EventExpired EventKind = "expired"
	// EventIgnored is any other provider event; it is recorded but not acted on.
	EventIgnored EventKind = "ignored"
)

// Event is a provider webhook normalised to what the booking flow needs.
type Event struct {
	// ID is the provider's unique event identifier, used for idempotency.
	ID   string
	Kind EventKind
	// CheckoutRef matches Checkout.Ref for the payment the event concerns.
	CheckoutRef string
	// SessionID is the value passed as CheckoutInput.SessionID, if present.
	SessionID string
}

// Provider is the operations the booking flow needs from a payment service.
// Capture, Release and Refund must be idempotent on the provider side for a
// given idempotencyKey, because the service retries them after a failed
// commit and the sweeper retries them after a provider outage.
type Provider interface {
	// Enabled reports whether bookings must be paid for. The service uses it
	// to choose between requesting the coach's confirmation immediately and
	// first holding the slot for a card authorisation.
	Enabled() bool
	// CreateCheckout starts a hosted checkout that authorises, but does not
	// capture, in.AmountCents.
	CreateCheckout(ctx context.Context, in CheckoutInput) (Checkout, error)
	// ExpireCheckout closes a hosted page the client has not completed, so an
	// abandoned booking cannot be paid for later. Expiring an already
	// completed or expired checkout must not fail.
	ExpireCheckout(ctx context.Context, ref string) error
	// Capture charges the held amount after the coach confirmed.
	Capture(ctx context.Context, ref, idempotencyKey string) error
	// Release voids the hold without charging, after a decline, expiry or
	// cancellation.
	Release(ctx context.Context, ref, idempotencyKey string) error
	// Refund returns a captured payment in full.
	Refund(ctx context.Context, ref, idempotencyKey string) error
	// ParseWebhook verifies signature over payload and normalises the event.
	ParseWebhook(payload []byte, signature string) (Event, error)
}

// Disabled is the Provider used when PAYMENTS_ENABLED is off: bookings go
// straight to the coach and every payment operation reports ErrDisabled.
type Disabled struct{}

// Enabled always reports false.
func (Disabled) Enabled() bool { return false }

// CreateCheckout always fails with ErrDisabled.
func (Disabled) CreateCheckout(context.Context, CheckoutInput) (Checkout, error) {
	return Checkout{}, ErrDisabled
}

// ExpireCheckout always fails with ErrDisabled.
func (Disabled) ExpireCheckout(context.Context, string) error { return ErrDisabled }

// Capture always fails with ErrDisabled.
func (Disabled) Capture(context.Context, string, string) error { return ErrDisabled }

// Release always fails with ErrDisabled.
func (Disabled) Release(context.Context, string, string) error { return ErrDisabled }

// Refund always fails with ErrDisabled.
func (Disabled) Refund(context.Context, string, string) error { return ErrDisabled }

// ParseWebhook always fails with ErrDisabled; the webhook route is not
// mounted when payments are off.
func (Disabled) ParseWebhook([]byte, string) (Event, error) { return Event{}, ErrDisabled }

// ErrNoProvider is returned by New when payments are enabled but no concrete
// provider is compiled in yet.
var ErrNoProvider = errors.New("payments enabled but no payment provider is configured")

// New returns the Provider for the configured mode: Disabled when enabled is
// false. Enabling payments fails with ErrNoProvider until a concrete provider
// exists, so a misconfigured deployment refuses to start rather than
// silently giving sessions away.
func New(enabled bool) (Provider, error) {
	if !enabled {
		return Disabled{}, nil
	}
	return nil, ErrNoProvider
}
