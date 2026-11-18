package coaching

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestConcurrentBookings(t *testing.T) {
	for _, paid := range []bool{false, true} {
		name := "unpaid"
		if paid {
			name = "paid"
		}
		t.Run(name, func(t *testing.T) {
			svc, pool := testService(t)
			if paid {
				svc.payments = &fakeProvider{}
			}
			coach := insertCoach(t, pool)
			client := insertUser(t, pool, "user")
			t.Cleanup(func() {
				_, err := pool.Exec(context.Background(), `DELETE FROM email_outbox
					WHERE to_email IN (SELECT email FROM users WHERE id IN ($1, $2))`, coach, client)
				if err != nil {
					t.Errorf("clean up booking emails: %v", err)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			const iterations, contenders = 50, 16
			booked, conflicts, unexpected := 0, 0, 0
			for i := 0; i < iterations; i++ {
				start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, i+2).Add(12 * time.Hour)
				ready := make(chan struct{})
				results := make(chan error, contenders)
				var wg sync.WaitGroup
				for j := 0; j < contenders; j++ {
					wg.Add(1)
					go func(j int) {
						defer wg.Done()
						<-ready
						_, err := svc.BookSession(ctx, client, BookInput{
							CoachID: coach.String(), ScheduledTime: start.Add(time.Duration(j%2) * 15 * time.Minute).Format(time.RFC3339), DurationMinutes: 45,
						})
						results <- err
					}(j)
				}
				close(ready)
				wg.Wait()
				close(results)
				winners := 0
				for err := range results {
					switch {
					case err == nil:
						booked++
						winners++
					case errors.Is(err, ErrSlotTaken):
						conflicts++
					default:
						unexpected++
						t.Logf("iteration %d: %v", i, err)
					}
				}
				if winners != 1 {
					t.Errorf("iteration %d: booked %d sessions, want 1", i, winners)
				}
			}
			t.Logf("iterations=%d attempts=%d booked=%d conflicts=%d unexpected/500=%d", iterations, iterations*contenders, booked, conflicts, unexpected)
			if unexpected != 0 {
				t.Errorf("unexpected booking errors: %d", unexpected)
			}
		})
	}
}
