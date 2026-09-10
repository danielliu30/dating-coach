package analysis

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

type fakeUsers struct {
	user db.User
	err  error
}

func (f fakeUsers) GetUserByID(context.Context, uuid.UUID) (db.User, error) { return f.user, f.err }

type fakeImageAnalyzer struct {
	got  MLImageRequest
	resp MLImageResponse
}

func (f *fakeImageAnalyzer) AnalyzeImages(_ context.Context, in MLImageRequest) (MLImageResponse, error) {
	f.got = in
	return f.resp, nil
}

func TestImageServiceAnalyzeTailorsWithStoredPreferences(t *testing.T) {
	ml := &fakeImageAnalyzer{resp: MLImageResponse{ModelVersion: "v", Overall: MLImageOverall{Summary: "ok"}}}
	svc := NewImageService(fakeUsers{user: db.User{DatingPreferences: "someone who hikes"}}, ml)

	out, err := svc.Analyze(context.Background(), uuid.New(), ImageInput{Images: []ImageRef{
		{URL: "https://cdn.example/a.jpg"},
		{Base64: base64.StdEncoding.EncodeToString([]byte("png-bytes")), MediaType: "image/png"},
	}})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if out.Overall.Summary != "ok" {
		t.Fatalf("response %+v", out)
	}
	if ml.got.Preferences != "someone who hikes" {
		t.Fatalf("preferences %q not forwarded", ml.got.Preferences)
	}
	if len(ml.got.Images) != 2 || ml.got.Images[0].MediaType != "image/jpeg" || ml.got.Images[1].MediaType != "image/png" {
		t.Fatalf("images %+v", ml.got.Images)
	}
}

func TestImageServiceAnalyzeRejectsBadInput(t *testing.T) {
	big := strings.Repeat("A", base64.StdEncoding.EncodedLen(maxImageBytes+1))
	cases := map[string]ImageInput{
		"none":          {},
		"too many":      {Images: make([]ImageRef, maxImages+1)},
		"neither":       {Images: []ImageRef{{MediaType: "image/png"}}},
		"both":          {Images: []ImageRef{{URL: "https://x/a.jpg", Base64: "QUJD"}}},
		"bad scheme":    {Images: []ImageRef{{URL: "ftp://x/a.jpg"}}},
		"relative url":  {Images: []ImageRef{{URL: "/a.jpg"}}},
		"bad base64":    {Images: []ImageRef{{Base64: "not base64!"}}},
		"oversize":      {Images: []ImageRef{{Base64: big}}},
		"bad mediatype": {Images: []ImageRef{{URL: "https://x/a.svg", MediaType: "image/svg+xml"}}},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			ml := &fakeImageAnalyzer{}
			_, err := NewImageService(fakeUsers{}, ml).Analyze(context.Background(), uuid.New(), in)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("got %v, want ErrInvalidInput", err)
			}
			if len(ml.got.Images) != 0 {
				t.Fatalf("analyzer was called with %+v", ml.got)
			}
		})
	}
}

func TestImageServiceAnalyzeUnknownUser(t *testing.T) {
	svc := NewImageService(fakeUsers{err: pgx.ErrNoRows}, &fakeImageAnalyzer{})
	_, err := svc.Analyze(context.Background(), uuid.New(), ImageInput{Images: []ImageRef{{URL: "https://x/a.jpg"}}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}
