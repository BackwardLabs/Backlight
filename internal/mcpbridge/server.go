package mcpbridge

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/UPside-Lumos-V2/helios/internal/handoff"
)

// Server receives Helios downstream handoff payloads and indexes them locally.
type Server struct {
	Store  *Store
	Token  string
	Logger *slog.Logger
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /handoff", s.handleHandoff)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleHandoff(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "bridge store is not configured"})
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or invalid bridge token"})
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var payload handoff.Payload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed_json", "detail": err.Error()})
		return
	}
	if err := s.Store.UpsertPayload(r.Context(), payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if s.Logger != nil {
		s.Logger.Info("handoff indexed", "case_id", payload.CaseID, "outcome", payload.Outcome)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "stored", "case_id": payload.CaseID})
}

func (s *Server) authorized(r *http.Request) bool {
	if s.Token == "" {
		return true
	}
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	return strings.HasPrefix(header, prefix) && strings.TrimSpace(header[len(prefix):]) == s.Token
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
