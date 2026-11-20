package analysis

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/google/uuid"
)

func TestPubSubPublisherEmulator(t *testing.T) {
	if os.Getenv("PUBSUB_EMULATOR_HOST") == "" {
		t.Skip("PUBSUB_EMULATOR_HOST unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := pubsub.NewClient(ctx, "publisher-test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	topic, err := c.CreateTopic(ctx, "test-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	defer topic.Delete(context.Background())
	p, err := OpenPubSubPublisher[Job](ctx, "publisher-test", topic.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	sub, err := c.CreateSubscription(ctx, "test-"+uuid.NewString(), pubsub.SubscriptionConfig{Topic: topic})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Delete(context.Background())
	want := Job{}
	if err := p.Publish(ctx, want); err != nil {
		t.Fatal(err)
	}
	received := false
	err = sub.Receive(ctx, func(_ context.Context, m *pubsub.Message) {
		var got Job
		if err := json.Unmarshal(m.Data, &got); err != nil {
			t.Error(err)
		}
		received = true
		m.Ack()
		cancel()
	})
	if err != nil && ctx.Err() == nil {
		t.Fatal(err)
	}
	if !received {
		t.Fatal("no published message received")
	}
}
