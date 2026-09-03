package coaching

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/payments"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

func TestPriceCentsRoundsToNearestCent(t *testing.T) {
	cases := []struct {
		rate, minutes int32
		want          int64
	}{
		{6000, 60, 6000},
		{6000, 45, 4500},
		{6000, 30, 3000},
		{9999, 45, 7499}, // 7499.25 rounds down
		{9999, 50, 8333}, // 8332.5 rounds up
		{0, 60, 0},
	}
	for _, tc := range cases {
		if got := priceCents(tc.rate, tc.minutes); got != tc.want {
			t.Errorf("priceCents(%d, %d) = %d, want %d", tc.rate, tc.minutes, got, tc.want)
		}
	}
}

// fakeProvider records provider calls and lets a test fail the next one.
type fakeProvider struct {
	checkouts int
	captured  []string
	released  []string
	expired   []string
	refunded  []string
	// fail, when set, is returned by every operation.
	fail error
}

func (f *fakeProvider) Enabled() bool { return true }

func (f *fakeProvider) CreateCheckout(_ context.Context, in payments.CheckoutInput) (payments.Checkout, error) {
	if f.fail != nil {
		return payments.Checkout{}, f.fail
	}
	f.checkouts++
	ref := fmt.Sprintf("cs_%s", in.SessionID)
	return payments.Checkout{Ref: ref, URL: "https://pay.test/" + ref}, nil
}

func (f *fakeProvider) ExpireCheckout(_ context.Context, ref string) error {
	if f.fail != nil {
		return f.fail
	}
	f.expired = append(f.expired, ref)
	return nil
}

func (f *fakeProvider) Capture(_ context.Context, ref, key string) error {
	if f.fail != nil {
		return f.fail
	}
	f.captured = append(f.captured, ref+"/"+key)
	return nil
}

func (f *fakeProvider) Release(_ context.Context, ref, key string) error {
	if f.fail != nil {
		return f.fail
	}
	f.released = append(f.released, ref+"/"+key)
	return nil
}

func (f *fakeProvider) Refund(_ context.Context, ref, key string) error {
	if f.fail != nil {
		return f.fail
	}
	f.refunded = append(f.refunded, ref+"/"+key)
	return nil
}

func (f *fakeProvider) ParseWebhook([]byte, string) (payments.Event, error) {
	return payments.Event{}, errors.New("not used")
}

// paidService is testService with payments switched on through a fakeProvider
// and a coach who charges 6000 cents an hour.
func paidService(t *testing.T) (*Service, *fakeProvider, *pgxpool.Pool, uuid.UUID) {
	t.Helper()
	svc, pool := testService(t)
	provider := &fakeProvider{}
	svc.payments = provider
	coach := insertCoach(t, pool)
	if _, err := pool.Exec(context.Background(), "UPDATE coaches SET hourly_rate_cents = 6000 WHERE user_id = $1", coach); err != nil {
		t.Fatal(err)
	}
	return svc, provider, pool, coach
}

// rowOf reads a session row back.
func rowOf(t *testing.T, svc *Service, id string) db.CoachingSession {
	t.Helper()
	row, err := svc.queries.GetCoachingSession(context.Background(), uuid.MustParse(id))
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// authorize delivers the provider's "card held" webhook for a booking.
func authorize(t *testing.T, svc *Service, s Session, eventID string) {
	t.Helper()
	row := rowOf(t, svc, s.ID)
	err := svc.ApplyPaymentEvent(context.Background(), payments.Event{ID: eventID, Kind: payments.EventAuthorized, CheckoutRef: *row.PaymentRef})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
}

func TestPaidBookingHoldsSlotAndOnlyAsksCoachOnceAuthorised(t *testing.T) {
	svc, provider, pool, coach := paidService(t)
	ctx := context.Background()
	client, other := insertUser(t, pool, "user"), insertUser(t, pool, "user")

	start := nextSlot()
	s, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: start, DurationMinutes: 30})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if s.Status != StatusPendingPayment || s.PaymentStatus != payments.StatusPending || s.AmountCents != 3000 || s.CheckoutURL == "" || s.HoldExpiresAt == "" {
		t.Fatalf("paid booking = %+v, want pending_payment hold with checkout url", s)
	}
	if _, err := svc.BookSession(ctx, other, BookInput{CoachID: coach.String(), ScheduledTime: start}); !errors.Is(err, ErrSlotTaken) {
		t.Fatalf("booking a held slot: err = %v, want ErrSlotTaken", err)
	}
	if got := outboxFor(t, pool, emailOf(t, pool, coach)); len(got) != 0 {
		t.Fatalf("coach emails before authorisation = %v, want none", got)
	}

	authorize(t, svc, s, "evt_"+s.ID)
	authorize(t, svc, s, "evt_"+s.ID) // redelivery is a no-op
	row := rowOf(t, svc, s.ID)
	if row.Status != StatusPending || row.PaymentStatus != payments.StatusAuthorized || row.RespondBy == nil || row.ConfirmationToken == nil || row.HoldExpiresAt != nil {
		t.Fatalf("after authorisation = %+v, want pending/authorized with deadline and token", row)
	}
	if got := outboxFor(t, pool, emailOf(t, pool, coach)); len(got) != 1 || !strings.Contains(got[0], "request") {
		t.Fatalf("coach emails = %v, want one request", got)
	}
	outboxFor(t, pool, emailOf(t, pool, client))
	if len(provider.captured) != 0 || len(provider.released) != 0 {
		t.Fatalf("nothing may be captured or released before the coach answers: %+v", provider)
	}
}

func TestCoachConfirmCapturesAndDeclineReleases(t *testing.T) {
	svc, provider, pool, coach := paidService(t)
	ctx := context.Background()
	client := insertUser(t, pool, "user")

	book := func(start string) Session {
		s, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: start})
		if err != nil {
			t.Fatalf("book: %v", err)
		}
		authorize(t, svc, s, "evt_"+s.ID)
		return s
	}
	confirmed := book(nextSlot())
	declined := book(time.Now().Add(96 * time.Hour).Truncate(time.Hour).UTC().Format(time.RFC3339))

	// A provider outage must leave the request pending for a retry.
	provider.fail = errors.New("provider down")
	if _, err := svc.RespondAsCoach(ctx, uuid.MustParse(confirmed.ID), coach, "confirm"); !errors.Is(err, ErrPayment) {
		t.Fatalf("confirm with provider down: err = %v, want ErrPayment", err)
	}
	if row := rowOf(t, svc, confirmed.ID); row.Status != StatusPending || row.PaymentStatus != payments.StatusAuthorized {
		t.Fatalf("after failed capture = %s/%s, want pending/authorized", row.Status, row.PaymentStatus)
	}
	provider.fail = nil

	got, err := svc.RespondAsCoach(ctx, uuid.MustParse(confirmed.ID), coach, "confirm")
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if got.Status != StatusScheduled || got.PaymentStatus != payments.StatusPaid {
		t.Fatalf("confirmed = %s/%s, want scheduled/paid", got.Status, got.PaymentStatus)
	}
	if len(provider.captured) != 1 || !strings.HasSuffix(provider.captured[0], "/"+confirmed.ID) {
		t.Fatalf("captured = %v, want one capture keyed by the session", provider.captured)
	}

	got, err = svc.RespondAsCoach(ctx, uuid.MustParse(declined.ID), coach, "decline")
	if err != nil {
		t.Fatalf("decline: %v", err)
	}
	if got.Status != StatusDeclined || got.PaymentStatus != payments.StatusReleasing {
		t.Fatalf("declined = %s/%s, want declined/releasing", got.Status, got.PaymentStatus)
	}
	res, err := svc.SweepPayments(ctx, time.Now())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.AuthorizationsFreed != 1 || len(provider.released) != 1 || len(provider.captured) != 1 {
		t.Fatalf("sweep = %+v, provider = %+v; want exactly the declined hold released", res, provider)
	}
	if row := rowOf(t, svc, declined.ID); row.PaymentStatus != payments.StatusReleased {
		t.Fatalf("declined payment_status = %s, want released", row.PaymentStatus)
	}
	outboxFor(t, pool, emailOf(t, pool, coach))
	outboxFor(t, pool, emailOf(t, pool, client))
}

func TestUnansweredAuthorisedRequestExpiresAndReleases(t *testing.T) {
	svc, provider, pool, coach := paidService(t)
	ctx := context.Background()
	client := insertUser(t, pool, "user")

	s, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: nextSlot()})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	authorize(t, svc, s, "evt_"+s.ID)
	if _, err := pool.Exec(ctx, "UPDATE coaching_sessions SET respond_by = now() - interval '1 minute' WHERE id = $1", s.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.ExpirePending(ctx); err != nil || n != 1 {
		t.Fatalf("expire = %d, %v; want 1", n, err)
	}
	if row := rowOf(t, svc, s.ID); row.Status != StatusExpired || row.PaymentStatus != payments.StatusReleasing {
		t.Fatalf("expired = %s/%s, want expired/releasing", row.Status, row.PaymentStatus)
	}
	if _, err := svc.SweepPayments(ctx, time.Now()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(provider.released) != 1 || len(provider.captured) != 0 {
		t.Fatalf("provider = %+v, want one release and no capture", provider)
	}
	outboxFor(t, pool, emailOf(t, pool, coach))
	outboxFor(t, pool, emailOf(t, pool, client))
}

func TestLapsedHoldRefusesLateAuthorisationUnlessSlotStillFree(t *testing.T) {
	svc, provider, pool, coach := paidService(t)
	ctx := context.Background()
	client, other := insertUser(t, pool, "user"), insertUser(t, pool, "user")
	start := nextSlot()

	s, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: start})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	res, err := svc.SweepPayments(ctx, time.Now().Add(time.Hour))
	if err != nil || res.HoldsReleased != 1 || res.CheckoutsExpired != 1 {
		t.Fatalf("sweep = %+v, %v; want the hold released and its checkout expired", res, err)
	}
	if row := rowOf(t, svc, s.ID); row.Status != StatusExpired || row.PaymentStatus != payments.StatusFailed {
		t.Fatalf("lapsed hold = %s/%s, want expired/failed", row.Status, row.PaymentStatus)
	}

	// Slot still free: the late authorisation revives the request.
	authorize(t, svc, s, "late_"+s.ID)
	if row := rowOf(t, svc, s.ID); row.Status != StatusPending || row.PaymentStatus != payments.StatusAuthorized {
		t.Fatalf("revived = %s/%s, want pending/authorized", row.Status, row.PaymentStatus)
	}

	// Slot taken meanwhile: the authorisation is released instead.
	start2 := time.Now().Add(96 * time.Hour).Truncate(time.Hour).UTC().Format(time.RFC3339)
	s2, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: start2})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if _, err := svc.SweepPayments(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BookSession(ctx, other, BookInput{CoachID: coach.String(), ScheduledTime: start2}); err != nil {
		t.Fatalf("other should get the lapsed slot: %v", err)
	}
	authorize(t, svc, s2, "late_"+s2.ID)
	if row := rowOf(t, svc, s2.ID); row.Status != StatusExpired || row.PaymentStatus != payments.StatusReleasing {
		t.Fatalf("late authorisation for a taken slot = %s/%s, want expired/releasing", row.Status, row.PaymentStatus)
	}
	if _, err := svc.SweepPayments(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(provider.released) != 1 || len(provider.captured) != 0 {
		t.Fatalf("provider = %+v, want one release and no capture", provider)
	}
	outboxFor(t, pool, emailOf(t, pool, coach))
	outboxFor(t, pool, emailOf(t, pool, client))
	outboxFor(t, pool, emailOf(t, pool, other))
}

func TestClientCancelReleasesHoldOrAuthorisation(t *testing.T) {
	svc, provider, pool, coach := paidService(t)
	ctx := context.Background()
	client := insertUser(t, pool, "user")

	// Cancel while still at the payment page: checkout gets closed, and a
	// late authorisation is released rather than reviving the booking.
	s, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: nextSlot()})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	got, err := svc.SetStatus(ctx, uuid.MustParse(s.ID), client, StatusCancelled)
	if err != nil {
		t.Fatalf("cancel hold: %v", err)
	}
	if got.Status != StatusCancelled || got.PaymentStatus != payments.StatusExpiring {
		t.Fatalf("cancelled hold = %s/%s, want cancelled/expiring", got.Status, got.PaymentStatus)
	}
	authorize(t, svc, s, "late_"+s.ID)
	if row := rowOf(t, svc, s.ID); row.Status != StatusCancelled || row.PaymentStatus != payments.StatusReleasing {
		t.Fatalf("authorised after cancel = %s/%s, want cancelled/releasing", row.Status, row.PaymentStatus)
	}

	// Cancel an authorised request awaiting the coach: release, no charge.
	s2, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: time.Now().Add(96 * time.Hour).Truncate(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	authorize(t, svc, s2, "evt_"+s2.ID)
	got, err = svc.SetStatus(ctx, uuid.MustParse(s2.ID), client, StatusCancelled)
	if err != nil {
		t.Fatalf("cancel authorised: %v", err)
	}
	if got.Status != StatusCancelled || got.PaymentStatus != payments.StatusReleasing {
		t.Fatalf("cancelled authorised = %s/%s, want cancelled/releasing", got.Status, got.PaymentStatus)
	}
	res, err := svc.SweepPayments(ctx, time.Now())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.AuthorizationsFreed != 2 || len(provider.released) != 2 || len(provider.captured) != 0 {
		t.Fatalf("sweep = %+v, provider = %+v; want both holds released", res, provider)
	}
	outboxFor(t, pool, emailOf(t, pool, coach))
	outboxFor(t, pool, emailOf(t, pool, client))
}
