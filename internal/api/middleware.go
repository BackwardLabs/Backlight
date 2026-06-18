package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// ErrorBody is the standard error response envelope.
type ErrorBody struct {
	ErrorCode     string         `json:"error_code"`
	Message       string         `json:"message,omitempty"`
	Details       any            `json:"details,omitempty"`
	CaseID        string         `json:"case_id,omitempty"`
	State         string         `json:"state,omitempty"`
	HandoffStatus string         `json:"handoff_status,omitempty"`
	Extra         map[string]any `json:"-"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string, details any) {
	writeJSON(w, status, ErrorBody{ErrorCode: code, Message: message, Details: details})
}

// authMiddleware enforces the core API Bearer token on the protected mux. Routes
// with distinct auth requirements must be mounted outside this middleware.
func authMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" {
			writeError(w, http.StatusUnauthorized, "missing_token", "Authorization header is required", nil)
			return
		}
		const prefix = "Bearer "
		if !strings.HasPrefix(header, prefix) || strings.TrimSpace(header[len(prefix):]) != token {
			writeError(w, http.StatusForbidden, "invalid_token", "Bearer token does not match HELIOS_API_TOKEN", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// decodeJSONBody reads the request body into v and distinguishes parse errors
// (400 malformed_json) from validation errors handled by callers.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "malformed_json", err.Error(), nil)
		return false
	}
	return true
}

func ctxOrBackground(r *http.Request) context.Context {
	if r.Context() != nil {
		return r.Context()
	}
	return context.Background()
}
