package coaching

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
)

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
