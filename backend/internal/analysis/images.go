package analysis

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

const (
	maxImages = 10
	// Decoded size of one inline photo. Generous for phone photos, small enough
	// that ten of them still fit comfortably in one request.
	maxImageBytes     = 5 << 20
	maxImageURLLength = 2000
)

// allowedImageMediaTypes mirrors the analyzer's ImageRef.media_type pattern.
var allowedImageMediaTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
	"image/gif":  true,
}

// ImageAnalyzer is the analyzer call ImageService depends on; *MLClient
// implements it and tests substitute a fake.
type ImageAnalyzer interface {
	AnalyzeImages(ctx context.Context, in MLImageRequest) (MLImageResponse, error)
}

// preferencesReader is the one query ImageService needs; *db.Queries implements it.
type preferencesReader interface {
	GetUserByID(ctx context.Context, id uuid.UUID) (db.User, error)
}

// ImageRef is one photo in an ImageInput. Exactly one of URL or Base64 must be
// set; Base64 is the raw payload without a data: prefix. MediaType defaults to
// image/jpeg.
type ImageRef struct {
	URL       string `json:"url,omitempty"`
	Base64    string `json:"base64,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

// ImageInput is the decoded POST /analysis/images body: the profile photos to
// assess. Preferences are not part of it; they come from the caller's profile.
type ImageInput struct {
	Images []ImageRef `json:"images"`
}

// ImageService runs the photo track: it validates the photos, pairs them with
// the caller's stored dating preferences and asks the analyzer synchronously.
// Unlike conversations, photos are never stored and no job is queued.
type ImageService struct {
	users preferencesReader
	ml    ImageAnalyzer
}

// NewImageService wires the service dependencies; called once from cmd/api.
func NewImageService(users preferencesReader, ml ImageAnalyzer) *ImageService {
	return &ImageService{users: users, ml: ml}
}

// Analyze validates in, tailors the request with userID's dating_preferences
// and returns the analyzer's verdict. Validation failures wrap ErrInvalidInput
// and name the offending image by index; a userID that no longer exists yields
// ErrNotFound. Analyzer failures are returned as-is for respondErr to log.
func (s *ImageService) Analyze(ctx context.Context, userID uuid.UUID, in ImageInput) (MLImageResponse, error) {
	if len(in.Images) == 0 {
		return MLImageResponse{}, fmt.Errorf("%w: at least one image is required", ErrInvalidInput)
	}
	if len(in.Images) > maxImages {
		return MLImageResponse{}, fmt.Errorf("%w: at most %d images are supported", ErrInvalidInput, maxImages)
	}
	refs := make([]MLImageRef, 0, len(in.Images))
	for i, img := range in.Images {
		ref, err := validateImageRef(img)
		if err != nil {
			return MLImageResponse{}, fmt.Errorf("%w: images[%d] %v", ErrInvalidInput, i, err)
		}
		refs = append(refs, ref)
	}

	user, err := s.users.GetUserByID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return MLImageResponse{}, ErrNotFound
	}
	if err != nil {
		return MLImageResponse{}, fmt.Errorf("load preferences: %w", err)
	}

	return s.ml.AnalyzeImages(ctx, MLImageRequest{Images: refs, Preferences: user.DatingPreferences})
}

// validateImageRef checks one photo reference against the analyzer's contract
// (url XOR base64, http(s) URL, decodable base64 under maxImageBytes, known
// media type) and returns it in wire form with the media type defaulted.
func validateImageRef(img ImageRef) (MLImageRef, error) {
	if (img.URL == "") == (img.Base64 == "") {
		return MLImageRef{}, errors.New("must set exactly one of url or base64")
	}
	mediaType := img.MediaType
	if mediaType == "" {
		mediaType = "image/jpeg"
	}
	if !allowedImageMediaTypes[mediaType] {
		return MLImageRef{}, fmt.Errorf("media_type %q is not supported", mediaType)
	}
	if img.URL != "" {
		if utf8.RuneCountInString(img.URL) > maxImageURLLength {
			return MLImageRef{}, errors.New("url is too long")
		}
		u, err := url.Parse(img.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return MLImageRef{}, errors.New("url must be an absolute http(s) URL")
		}
		return MLImageRef{URL: img.URL, MediaType: mediaType}, nil
	}
	// DecodedLen counts padding, so it can overshoot by two; it only guards
	// against decoding something absurdly large before the exact check below.
	if base64.StdEncoding.DecodedLen(len(img.Base64)) > maxImageBytes+2 {
		return MLImageRef{}, fmt.Errorf("exceeds %d bytes", maxImageBytes)
	}
	decoded, err := base64.StdEncoding.DecodeString(img.Base64)
	if err != nil {
		return MLImageRef{}, errors.New("base64 is not valid standard base64")
	}
	if len(decoded) > maxImageBytes {
		return MLImageRef{}, fmt.Errorf("exceeds %d bytes", maxImageBytes)
	}
	return MLImageRef{Base64: img.Base64, MediaType: mediaType}, nil
}
