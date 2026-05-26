package api

import (
	"net/http"

	"github.com/UPside-Lumos-V2/helios/internal/config"
	"github.com/UPside-Lumos-V2/helios/internal/handoff"
	"github.com/UPside-Lumos-V2/helios/internal/store"
)

// Server is the helios HTTP surface. Worker/lumoskit/notify modules will be
// wired through dependencies on this struct in later iterations.
type Server struct {
	Config     *config.Config
	Store      *store.Store
	Dispatcher *handoff.Dispatcher // nil when no downstream URLs are configured
}

func NewServer(cfg *config.Config, st *store.Store, dispatcher *handoff.Dispatcher) *Server {
	return &Server{Config: cfg, Store: st, Dispatcher: dispatcher}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", uiHandler)
	mux.HandleFunc("GET /ui", uiHandler)
	mux.HandleFunc("GET /healthz", healthHandler)

	protected := http.NewServeMux()
	protected.Handle("GET /metrics", metricsHandler)
	protected.HandleFunc("POST /signals", s.handleSignal)
	protected.HandleFunc("POST /cases", s.handleCreateCase)
	protected.HandleFunc("GET /cases", s.handleListCases)
	protected.HandleFunc("GET /cases/{case_id}/artifacts", s.handleListArtifacts)
	protected.HandleFunc("GET /cases/{case_id}/artifacts/{artifact_path...}", s.handleReadArtifact)
	protected.HandleFunc("GET /cases/{case_id}", s.handleGetCase)
	protected.HandleFunc("POST /cases/{case_id}/retry-handoff", s.handleRetryHandoff)

	mux.Handle("/", authMiddleware(s.Config.APIToken, protected))
	return mux
}
