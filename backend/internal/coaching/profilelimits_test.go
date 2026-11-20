package coaching

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
)

func TestUnchangedLegacyCoachTextAllowsOtherEdits(t *testing.T) {
	svc, pool := testService(t)
	coachID := insertUser(t, pool, "coach")
	ctx := context.Background()
	if _, err := svc.UpsertProfile(ctx, coachID, UpsertProfileInput{}); err != nil {
		t.Fatal(err)
	}
	in := UpsertProfileInput{
		Headline:        strings.Repeat("界", MaxHeadlineLen+1),
		Bio:             strings.Repeat("界", MaxBioLen+1),
		Specialties:     make([]string, MaxSpecialties+1),
		HourlyRateCents: 15000,
	}
	for i := range in.Specialties {
		in.Specialties[i] = strings.Repeat("界", MaxSpecialtyLen+1)
	}
	if _, err := pool.Exec(ctx, "UPDATE coaches SET headline=$2, bio=$3, specialties=$4 WHERE user_id=$1", coachID, in.Headline, in.Bio, in.Specialties); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.UpsertProfile(ctx, coachID, in); err != nil || got.HourlyRateCents != in.HourlyRateCents {
		t.Fatalf("unchanged legacy text blocked update: coach=%+v err=%v", got, err)
	}
	if _, err := svc.SetAvailability(ctx, coachID, []AvailabilityWindow{{Weekday: 1, StartMinute: 600, EndMinute: 660}}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"headline", "bio", "specialties"} {
		changed := in
		switch field {
		case "headline":
			changed.Headline += "x"
		case "bio":
			changed.Bio += "x"
		case "specialties":
			changed.Specialties = append([]string(nil), in.Specialties...)
			changed.Specialties[0] += "x"
		}
		if _, err := svc.UpsertProfile(ctx, coachID, changed); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("changed legacy %s bypassed limit: %v", field, err)
		}
	}
}

func TestCoachProfileFieldBoundaries(t *testing.T) {
	svc, pool := testService(t)
	coachID := insertUser(t, pool, "coach")
	for _, field := range []struct {
		name  string
		limit int
		set   func(*UpsertProfileInput, int)
	}{
		{"headline", MaxHeadlineLen, func(in *UpsertProfileInput, n int) { in.Headline = strings.Repeat("界", n) }},
		{"bio", MaxBioLen, func(in *UpsertProfileInput, n int) { in.Bio = strings.Repeat("界", n) }},
		{"specialty", MaxSpecialtyLen, func(in *UpsertProfileInput, n int) { in.Specialties = []string{strings.Repeat("界", n)} }},
		{"specialties count", MaxSpecialties, func(in *UpsertProfileInput, n int) {
			in.Specialties = make([]string, n)
			for i := range in.Specialties {
				in.Specialties[i] = "skill"
			}
		}},
	} {
		for _, extra := range []int{0, 1} {
			t.Run(field.name+map[int]string{0: "/at_limit", 1: "/over_limit"}[extra], func(t *testing.T) {
				in := UpsertProfileInput{}
				field.set(&in, field.limit+extra)
				data, _ := json.Marshal(in)
				req := httptest.NewRequest(http.MethodPut, "/profile", bytes.NewReader(data))
				req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{UserID: coachID, Role: auth.RoleCoach}))
				rec := httptest.NewRecorder()
				NewHandler(svc, nil).upsertProfile(rec, req)
				want := http.StatusOK
				if extra != 0 {
					want = http.StatusBadRequest
				}
				if rec.Code != want {
					t.Fatalf("status=%d want=%d body=%s", rec.Code, want, rec.Body)
				}
			})
		}
	}
	for _, timezone := range []string{"", "UTC", strings.Repeat("a", MaxTimezoneLen+1)} {
		data, _ := json.Marshal(UpsertProfileInput{Timezone: timezone})
		req := httptest.NewRequest(http.MethodPut, "/profile", bytes.NewReader(data))
		req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{UserID: coachID, Role: auth.RoleCoach}))
		rec := httptest.NewRecorder()
		NewHandler(svc, nil).upsertProfile(rec, req)
		want := http.StatusOK
		if len(timezone) > MaxTimezoneLen {
			want = http.StatusBadRequest
		}
		if rec.Code != want {
			t.Fatalf("timezone status=%d want=%d body=%s", rec.Code, want, rec.Body)
		}
	}
}
