package account

import (
	"encoding/json"
	"testing"
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
