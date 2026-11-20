package httpx

import (
	"bytes"
	"errors"
	"io"
	"net/http"
)

const (
	MaxJSONBodyBytes       = 1 << 20
	MaxTranscriptBodyBytes = 16 << 20
)

func LimitRequestBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				Error(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			if r.Body != nil {
				body := http.MaxBytesReader(w, r.Body, maxBytes)
				defer body.Close()
				data, err := io.ReadAll(body)
				if err != nil {
					var tooLarge *http.MaxBytesError
					if errors.As(err, &tooLarge) {
						Error(w, http.StatusRequestEntityTooLarge, "request body too large")
					} else {
						Error(w, http.StatusBadRequest, "invalid request body")
					}
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(data))
			}
			next.ServeHTTP(w, r)
		})
	}
}
