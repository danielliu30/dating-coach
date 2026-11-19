package analysis

import (
	"context"
	"encoding/json"

	"cloud.google.com/go/pubsub"
)

// PubSubPublisher publishes JSON jobs to a Google Cloud Pub/Sub topic.
type PubSubPublisher[T any] struct {
	client *pubsub.Client
	topic  *pubsub.Topic
}

// OpenPubSubPublisher opens a topic publisher using ADC or PUBSUB_EMULATOR_HOST.
func OpenPubSubPublisher[T any](ctx context.Context, project, topic string) (*PubSubPublisher[T], error) {
	c, err := pubsub.NewClient(ctx, project)
	if err != nil {
		return nil, err
	}
	return &PubSubPublisher[T]{client: c, topic: c.Topic(topic)}, nil
}

// Publish waits for the broker to acknowledge the JSON-encoded job.
func (p *PubSubPublisher[T]) Publish(ctx context.Context, job T) error {
	body, err := json.Marshal(job)
	if err != nil {
		return err
	}
	_, err = p.topic.Publish(ctx, &pubsub.Message{Data: body}).Get(ctx)
	return err
}

// Close flushes outstanding messages and releases the client.
func (p *PubSubPublisher[T]) Close() { p.topic.Stop(); _ = p.client.Close() }
