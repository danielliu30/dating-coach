package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

func TestSelectProfileEvidence(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	svc := NewService(pool, db.New(pool), nil)
	owner, _ := insertConversation(t, pool)
	other, _ := insertConversation(t, pool)
	now := time.Now().UTC().Truncate(time.Second)
	strong := json.RawMessage(`{"profile_evidence":{"rubric_version":"profile-v1","coverage":"eligible","feedback_needed":{"level":"strong","self_positions":[0]}}}`)
	wrongSpeaker := json.RawMessage(`{"profile_evidence":{"rubric_version":"profile-v1","coverage":"eligible","feedback_needed":{"level":"strong","self_positions":[1]}}}`)

	add := func(user uuid.UUID, age time.Duration, first, reply string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		err := pool.QueryRow(ctx, "INSERT INTO conversations (user_id, created_at) VALUES ($1,$2) RETURNING id",
			user, now.Add(-age)).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		mustExecProfileTest(t, pool, "INSERT INTO messages (conversation_id,position,sender,body) VALUES ($1,0,'self',$2),($1,1,'match',$3)", id, first, reply)
		return id
	}
	run := func(conversation uuid.UUID, age time.Duration, status string, overall json.RawMessage) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		err := pool.QueryRow(ctx, "INSERT INTO analysis_results (conversation_id,created_at,status,overall) VALUES ($1,$2,$3,$4) RETURNING id",
			conversation, now.Add(-age), status, overall).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	idsFor := func(user uuid.UUID) []uuid.UUID {
		t.Helper()
		rows, err := svc.selectProfileEvidence(ctx, user, now)
		if err != nil {
			t.Fatal(err)
		}
		var ids []uuid.UUID
		for _, row := range rows {
			ids = append(ids, row.AnalysisID)
		}
		return ids
	}

	otherRun := run(add(other, time.Hour, "How about Saturday?", "That works."), time.Minute, "succeeded", strong)
	run(add(owner, profileEvidenceWindow+time.Second, "Too old", "Okay."), time.Minute, "succeeded", strong)
	run(add(owner, 4*time.Hour, "How about Saturday?", "That works."), time.Minute, "succeeded", strong)
	newest := add(owner, time.Hour, " How  about\tSaturday? ", "That works.")
	want := run(newest, 20*time.Minute, "succeeded", strong)
	run(newest, 19*time.Minute, "succeeded", wrongSpeaker)
	run(newest, 18*time.Minute, "succeeded", json.RawMessage(`{"profile_evidence":{"rubric_version":"profile-v2","coverage":"eligible","feedback_needed":{"level":"strong","self_positions":[0]}}}`))
	run(newest, 17*time.Minute, "succeeded", json.RawMessage(`{"summary":"legacy"}`))
	run(newest, 16*time.Minute, "pending", strong)
	run(newest, 15*time.Minute, "failed", strong)
	boundary := run(add(owner, profileEvidenceWindow, "Exactly 90 days old", "Okay."), time.Minute, "succeeded", strong)
	for i := range profileEvidenceBatch {
		// A full page of invalid runs must not hide qualifying evidence.
		run(newest, time.Duration(i)*time.Second, "succeeded", wrongSpeaker)
	}
	if got := idsFor(owner); !slices.Equal(got, []uuid.UUID{want, boundary}) {
		t.Fatalf("owner selected %v; want latest valid import and inclusive window boundary", got)
	}
	if got := idsFor(other); !slices.Equal(got, []uuid.UUID{otherRun}) {
		t.Fatalf("other owner selected %v; want only own import", got)
	}

	wantIDs := []uuid.UUID{want}
	for i := 2; i <= 7; i++ {
		id := run(add(owner, time.Duration(i)*time.Hour, fmt.Sprintf("Different day %d", i), "Okay."), time.Minute, "succeeded", strong)
		if len(wantIDs) < profileMaxEvidence {
			wantIDs = append(wantIDs, id)
		}
	}
	if got := idsFor(owner); !slices.Equal(got, wantIDs) {
		t.Fatalf("selected %v; want latest five %v", got, wantIDs)
	}
}

func mustExecProfileTest(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}
