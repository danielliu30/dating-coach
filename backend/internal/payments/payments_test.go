package payments

import (
	"context"
	"errors"
	"testing"
)

func TestNewReturnsDisabledWhenOff(t *testing.T) {
	p, err := New(false, "", "", "")
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

func TestNewFailsWhenOnWithoutSecrets(t *testing.T) {
	if _, err := New(true, "", "", ""); err == nil {
		t.Fatal("New(true) with no secrets succeeded")
	}
	if _, err := New(true, "sk_test_x", "", ""); err == nil {
		t.Fatal("New(true) with no webhook secret succeeded")
	}
}
