package payments

import (
	"context"
	"errors"
	"testing"
)

func TestNewReturnsDisabledWhenOff(t *testing.T) {
	p, err := New(false)
	if err != nil {
		t.Fatalf("New(false) error = %v", err)
	}
	if p.Enabled() {
		t.Fatal("Disabled provider reports Enabled")
	}
	if _, err := p.CreateCheckout(context.Background(), CheckoutInput{}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("CreateCheckout error = %v, want ErrDisabled", err)
	}
}

func TestNewFailsWhenOnWithoutProvider(t *testing.T) {
	if _, err := New(true); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("New(true) error = %v, want ErrNoProvider", err)
	}
}
