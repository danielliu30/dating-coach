package notify

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Publish announces event on userID's notification channel, reaching every
// socket the user holds on any API replica. Delivery is fire-and-forget: a
// user with no socket open, or one that is reconnecting, never sees it, so
// callers must keep a durable record (the notifications table, the analysis
// row) that clients refetch. It returns an error when the event cannot be
// encoded or Redis refuses the publish.
func Publish(ctx context.Context, rdb *redis.Client, userID uuid.UUID, event Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode notification: %w", err)
	}
	if err := rdb.Publish(ctx, UserChannel(userID), payload).Err(); err != nil {
		return fmt.Errorf("publish notification: %w", err)
	}
	return nil
}
