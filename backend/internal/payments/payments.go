// Package payments abstracts the third-party payment provider that clients pay
// through to reserve a coaching session. The coaching service only sees the
// Provider interface, so the provider can be swapped or, when payments are
// switched off, replaced by Disabled without the booking code branching.
package payments

import (
	"context"
	"errors"
	"time"
)

// ErrDisabled is returned by Disabled for operations that need a real
// provider; callers treat it as "payments are switched off".
var ErrDisabled = errors.New("payments are disabled")

// CheckoutInput describes the charge to collect for one session.
type CheckoutInput struct {
	// SessionID is echoed back by the provider so a webhook can be matched to
	// the booking it pays for.
	SessionID   string
	AmountCents int64
	Currency    string
	Description string
	// CustomerEmail prefills the hosted payment page.
	CustomerEmail string
	// ExpiresAt bounds how long the hosted page accepts payment; it should
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
	Ref string
	// URL is the hosted payment page the client is sent to.
	URL string
}

// EventKind classifies webhook events the booking flow reacts to.
type EventKind string

const (
	// EventPaid means the checkout was completed and the charge captured.
	EventPaid EventKind = "paid"
	// EventExpired means the hosted page expired without a payment.
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
type Provider interface {
	// Enabled reports whether bookings must be paid for. The service uses it
	// to choose between confirming a session immediately and holding it.
	Enabled() bool
	// CreateCheckout starts a hosted payment for in and returns where to send
	// the client. It must not be called when Enabled is false.
	CreateCheckout(ctx context.Context, in CheckoutInput) (Checkout, error)
	// ExpireCheckout invalidates a hosted page whose booking hold has lapsed,
	// so a late payment cannot land on a slot that has been released.
	ExpireCheckout(ctx context.Context, ref string) error
	// Refund returns the full amount of a completed checkout.
	Refund(ctx context.Context, ref string) error
	// ParseWebhook verifies payload against signature and returns the
	// normalised event. It must reject anything not signed by the provider.
	ParseWebhook(payload []byte, signature string) (Event, error)
}

// ErrNoProvider is returned by New when payments are switched on but no
// provider implementation is available in this build.
var ErrNoProvider = errors.New("PAYMENTS_ENABLED=true but no payment provider is available in this build")

// New selects the Provider for the running configuration: Disabled when the
// switch is off, otherwise ErrNoProvider until a real provider is wired in.
// Callers should treat the error as fatal at startup.
func New(enabled bool) (Provider, error) {
	if !enabled {
		return Disabled{}, nil
	}
	return nil, ErrNoProvider
}

// Disabled is the Provider used when PAYMENTS_ENABLED is off: Enabled reports
// false and every other method fails with ErrDisabled, so accidentally
// reaching a payment path is loud rather than silent.
type Disabled struct{}

// Enabled always reports false.
func (Disabled) Enabled() bool { return false }

// CreateCheckout always fails with ErrDisabled.
func (Disabled) CreateCheckout(context.Context, CheckoutInput) (Checkout, error) {
	return Checkout{}, ErrDisabled
}

// ExpireCheckout always fails with ErrDisabled.
func (Disabled) ExpireCheckout(context.Context, string) error { return ErrDisabled }

// Refund always fails with ErrDisabled.
func (Disabled) Refund(context.Context, string) error { return ErrDisabled }

// ParseWebhook always fails with ErrDisabled.
func (Disabled) ParseWebhook([]byte, string) (Event, error) { return Event{}, ErrDisabled }
