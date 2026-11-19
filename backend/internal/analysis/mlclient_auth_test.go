package analysis

import (
	"context"
	"testing"
	"time"
)

func TestMLClientWithoutGoogleAuth(t *testing.T) {
	c, err := NewAuthenticatedMLClient(context.Background(), "http://localhost:8000/", time.Second, "none")
	if err != nil {
		t.Fatal(err)
	}
	if c.baseURL != "http://localhost:8000" || c.http.Timeout != time.Second {
		t.Fatalf("unexpected client: %+v", c)
	}
}
