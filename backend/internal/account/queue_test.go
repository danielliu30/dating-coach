package account

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestAttemptNumberIgnoresBrokerRedelivery(t *testing.T) {
	// The broker sets Redelivered when a consumer died before acking, which says
	// nothing about the handler having run.
	delivery := amqp.Delivery{Redelivered: true}
	attempt := attemptNumber(delivery.Headers)
	if attempt != 1 {
		t.Fatalf("attemptNumber = %d, want 1", attempt)
	}
	if attempt >= MaxAttempts {
		t.Fatalf("attempt %d of %d would dead-letter the first handler failure", attempt, MaxAttempts)
	}
}

func TestAttemptNumberReadsStampedHeader(t *testing.T) {
	for name, headers := range map[string]amqp.Table{
		"int32":   {attemptHeader: int32(2)},
		"int64":   {attemptHeader: int64(2)},
		"int":     {attemptHeader: 2},
		"float64": {attemptHeader: float64(2)},
	} {
		t.Run(name, func(t *testing.T) {
			if got := attemptNumber(headers); got != 2 {
				t.Fatalf("attemptNumber = %d, want 2", got)
			}
		})
	}
}

func TestAttemptNumberFallsBackOnUnusableHeader(t *testing.T) {
	for name, headers := range map[string]amqp.Table{
		"wrong type": {attemptHeader: "two"},
		"zero":       {attemptHeader: int32(0)},
		"negative":   {attemptHeader: int32(-3)},
	} {
		t.Run(name, func(t *testing.T) {
			if got := attemptNumber(headers); got != 1 {
				t.Fatalf("attemptNumber = %d, want 1", got)
			}
		})
	}
}
