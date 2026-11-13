package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

func sprintf(format string, a ...any) string {
	if len(a) == 0 {
		return format
	}
	return fmt.Sprintf(format, a...)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) { writeJSON(w, status, v) }

// DecodeJSON decodes the request body into v. Unknown fields and trailing
// data are 400 invalid_argument; a body over the cap is 413
// payload_too_large (the cap is applied by the body-limit middleware).
func DecodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return bodyError(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errInvalid("unexpected data after the JSON body")
		}
		return bodyError(err)
	}
	return nil
}

func errTooLarge(limit int64) *Error {
	return &Error{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large",
		Message: fmt.Sprintf("request body exceeds %d bytes", limit)}
}

func bodyError(err error) *Error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return errTooLarge(mbe.Limit)
	}
	if errors.Is(err, io.EOF) {
		return errInvalid("empty request body")
	}
	return errInvalid("invalid JSON body: %v", err)
}
