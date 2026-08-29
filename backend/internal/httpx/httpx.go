// Package httpx holds small helpers shared by the HTTP handlers.
package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
)

var ErrValidation = errors.New("validation error")

type ErrorBody struct {
	Error string `json:"error"`
}

func JSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("write json response", "error", err)
	}
}

func Error(w http.ResponseWriter, status int, message string) {
	JSON(w, status, ErrorBody{Error: message})
}

// Decode reads a JSON request body into dst.
func Decode(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

// QueryInt reads an integer query parameter, clamped to [1, max].
func QueryInt(r *http.Request, key string, fallback, max int) int32 {
	v, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || v < 1 {
		v = fallback
	}
	if v > max {
		v = max
	}
	return int32(v)
}
