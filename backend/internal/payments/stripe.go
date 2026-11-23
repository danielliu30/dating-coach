package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/stripe/stripe-go/v84"
	"github.com/stripe/stripe-go/v84/webhook"
)

// Stripe is the concrete Provider: a Checkout Session in payment mode with a
// manual-capture PaymentIntent underneath. Checkout.Ref is the Checkout
// Session id; the hold it authorises lives on the session's PaymentIntent,
// which Capture, Release and Refund resolve lazily because it does not exist
// until the customer completes the hosted page.
type Stripe struct {
	client        *stripe.Client
	webhookSecret string
}

// NewStripe builds the provider. It refuses to start without both secrets so
// a deployment cannot enable payments while unable to charge or unable to
// verify webhook signatures. Extra ClientOptions exist so tests can point the
// SDK at a stub server via WithBackends.
func NewStripe(secretKey, webhookSecret string, opts ...stripe.ClientOption) (*Stripe, error) {
	if secretKey == "" {
		return nil, errors.New("STRIPE_SECRET_KEY is required when PAYMENTS_ENABLED=true")
	}
	if webhookSecret == "" {
		return nil, errors.New("STRIPE_WEBHOOK_SECRET is required when PAYMENTS_ENABLED=true")
	}
	return &Stripe{client: stripe.NewClient(secretKey, opts...), webhookSecret: webhookSecret}, nil
}

// Enabled reports true; a constructed Stripe is by definition live.
func (Stripe) Enabled() bool { return true }

// CreateCheckout opens a hosted Checkout Session that authorises the amount
// without capturing it.
func (s *Stripe) CreateCheckout(ctx context.Context, in CheckoutInput) (Checkout, error) {
	params := &stripe.CheckoutSessionCreateParams{
		Mode:              stripe.String("payment"),
		ClientReferenceID: stripe.String(in.SessionID),
		CustomerEmail:     stripe.String(in.CustomerEmail),
		SuccessURL:        stripe.String(in.SuccessURL),
		CancelURL:         stripe.String(in.CancelURL),
		ExpiresAt:         stripe.Int64(in.ExpiresAt.Unix()),
		Metadata:          map[string]string{"session_id": in.SessionID},
		PaymentIntentData: &stripe.CheckoutSessionCreatePaymentIntentDataParams{
			CaptureMethod: stripe.String("manual"),
			Metadata:      map[string]string{"session_id": in.SessionID},
		},
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{{
			Quantity: stripe.Int64(1),
			PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{
				Currency:   stripe.String(in.Currency),
				UnitAmount: stripe.Int64(in.AmountCents),
				ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{
					Name: stripe.String(in.Description),
				},
			},
		}},
	}
	session, err := s.client.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return Checkout{}, fmt.Errorf("create checkout session: %w", err)
	}
	return Checkout{Ref: session.ID, URL: session.URL}, nil
}

// ExpireCheckout closes a hosted page the client abandoned. A session that
// has already left the open state is a no-op, matching the contract that
// expiring a completed or expired checkout must not fail.
func (s *Stripe) ExpireCheckout(ctx context.Context, ref string) error {
	if _, err := s.client.V1CheckoutSessions.Expire(ctx, ref, nil); err != nil {
		session, rerr := s.client.V1CheckoutSessions.Retrieve(ctx, ref, nil)
		if rerr != nil {
			return fmt.Errorf("expire checkout session: %w", err)
		}
		if session.Status == stripe.CheckoutSessionStatusOpen {
			return fmt.Errorf("expire checkout session: %w", err)
		}
	}
	return nil
}

// Capture charges the authorised hold on the session's PaymentIntent.
func (s *Stripe) Capture(ctx context.Context, ref, idempotencyKey string) error {
	pi, err := s.paymentIntent(ctx, ref)
	if err != nil {
		return err
	}
	if pi == "" {
		return fmt.Errorf("checkout %s has no payment intent to capture", ref)
	}
	_, err = s.client.V1PaymentIntents.Capture(ctx, pi, &stripe.PaymentIntentCaptureParams{
		Params: stripe.Params{IdempotencyKey: stripe.String(idempotencyKey)},
	})
	if err != nil {
		return fmt.Errorf("capture payment intent: %w", err)
	}
	return nil
}

// Release voids the uncaptured hold. A checkout the customer never completed
// has no PaymentIntent, so there is nothing to void and the call succeeds.
func (s *Stripe) Release(ctx context.Context, ref, idempotencyKey string) error {
	pi, err := s.paymentIntent(ctx, ref)
	if err != nil {
		return err
	}
	if pi == "" {
		return nil
	}
	_, err = s.client.V1PaymentIntents.Cancel(ctx, pi, &stripe.PaymentIntentCancelParams{
		Params: stripe.Params{IdempotencyKey: stripe.String(idempotencyKey)},
	})
	if err != nil {
		return fmt.Errorf("cancel payment intent: %w", err)
	}
	return nil
}

// Refund returns a captured payment in full.
func (s *Stripe) Refund(ctx context.Context, ref, idempotencyKey string) error {
	pi, err := s.paymentIntent(ctx, ref)
	if err != nil {
		return err
	}
	if pi == "" {
		return fmt.Errorf("checkout %s has no payment intent to refund", ref)
	}
	_, err = s.client.V1Refunds.Create(ctx, &stripe.RefundCreateParams{
		Params:        stripe.Params{IdempotencyKey: stripe.String(idempotencyKey)},
		PaymentIntent: stripe.String(pi),
	})
	if err != nil {
		return fmt.Errorf("create refund: %w", err)
	}
	return nil
}

// paymentIntent resolves the PaymentIntent id behind a Checkout Session ref;
// empty means the hosted page was never completed.
func (s *Stripe) paymentIntent(ctx context.Context, ref string) (string, error) {
	session, err := s.client.V1CheckoutSessions.Retrieve(ctx, ref, nil)
	if err != nil {
		return "", fmt.Errorf("retrieve checkout session: %w", err)
	}
	if session.PaymentIntent == nil {
		return "", nil
	}
	return session.PaymentIntent.ID, nil
}

// ParseWebhook verifies the Stripe-Signature header and normalises the event.
// checkout.session.completed means the card is held; checkout.session.expired
// means the hold never happened. Everything else is recorded but ignored.
func (s *Stripe) ParseWebhook(payload []byte, signature string) (Event, error) {
	// The event fields we consume (id, type, data.object.id and
	// client_reference_id) predate every API version a configured webhook
	// endpoint could send, so a version drift between the endpoint and the
	// SDK must not reject otherwise valid, correctly signed events.
	ev, err := webhook.ConstructEventWithOptions(payload, signature, s.webhookSecret, webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true,
	})
	if err != nil {
		return Event{}, fmt.Errorf("verify webhook signature: %w", err)
	}
	out := Event{ID: ev.ID, Kind: EventIgnored}
	var kind EventKind
	switch string(ev.Type) {
	case "checkout.session.completed":
		kind = EventAuthorized
	case "checkout.session.expired":
		kind = EventExpired
	default:
		return out, nil
	}
	var session stripe.CheckoutSession
	if err := json.Unmarshal(ev.Data.Raw, &session); err != nil {
		return Event{}, fmt.Errorf("decode %s payload: %w", ev.Type, err)
	}
	out.Kind = kind
	out.CheckoutRef = session.ID
	out.SessionID = session.ClientReferenceID
	return out, nil
}
