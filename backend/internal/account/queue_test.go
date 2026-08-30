package account

import (
	"encoding/json"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestDeadLetterQueue(t *testing.T) {
	if got := DeadLetterQueue("account.deletion"); got != "account.deletion.dlq" {
		t.Fatalf("DeadLetterQueue = %q, want %q", got, "account.deletion.dlq")
	}
}

// TestJobRoundTrip pins the wire format: the API and the worker are separate
// processes, so a renamed field would strand queued deletions.
func TestJobRoundTrip(t *testing.T) {
	body, err := json.Marshal(Job{UserID: "2f6b1c1e-0f2f-4f7c-9c1b-1d9d1a2f3b44"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(body) != `{"user_id":"2f6b1c1e-0f2f-4f7c-9c1b-1d9d1a2f3b44"}` {
		t.Fatalf("encoded job = %s", body)
	}
	var job Job
	if err := json.Unmarshal(body, &job); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if job.UserID != "2f6b1c1e-0f2f-4f7c-9c1b-1d9d1a2f3b44" {
		t.Fatalf("decoded user id = %q", job.UserID)
	}
}

// TestAttemptsOf covers the retry counter that decides between another attempt
// and the dead-letter queue: brokers and clients hand back AMQP integers in
// several widths, and a missing or foreign header must read as "not tried yet"
// rather than exhausting the retries of a deletion nobody can resubmit.
func TestAttemptsOf(t *testing.T) {
	cases := map[string]struct {
		headers amqp.Table
		want    int64
	}{
		"missing":   {amqp.Table{}, 0},
		"nil table": {nil, 0},
		"int64":     {amqp.Table{attemptsHeader: int64(2)}, 2},
		"int32":     {amqp.Table{attemptsHeader: int32(1)}, 1},
		"int":       {amqp.Table{attemptsHeader: 3}, 3},
		"string":    {amqp.Table{attemptsHeader: "2"}, 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := attemptsOf(tc.headers); got != tc.want {
				t.Fatalf("attemptsOf = %d, want %d", got, tc.want)
			}
		})
	}
}
