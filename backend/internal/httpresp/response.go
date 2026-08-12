// Package httpresp provides the API's shared JSON response envelope.
package httpresp

import (
	"encoding/json"
	"net/http"
)

// APIErrorCode is a machine-readable error identifier sent to the client.
// The frontend localizes it via its own translation tables; the backend never
// sends localized text.
type APIErrorCode string

const (
	ErrUnauthorized APIErrorCode = "UNAUTHORIZED"
)

// WriteJSON writes a JSON response with the API's standard content type.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// WriteError emits the canonical machine-readable API error envelope.
func WriteError(w http.ResponseWriter, status int, code APIErrorCode) {
	WriteJSON(w, status, map[string]any{"success": false, "error_code": string(code)})
}
