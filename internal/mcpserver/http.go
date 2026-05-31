package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const defaultHTTPPath = "/mcp"

// HTTPOptions configures the stateless Streamable HTTP MCP endpoint.
type HTTPOptions struct {
	Path        string
	BearerToken string
}

// Handler returns a stateless Streamable HTTP MCP handler. It accepts JSON-RPC
// requests with POST and returns JSON-RPC responses directly as application/json.
func (s *Server) Handler(ctx context.Context, opts HTTPOptions) http.Handler {
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		path = defaultHTTPPath
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	mux := http.NewServeMux()
	mux.Handle(path, s.authenticated(opts.BearerToken, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleHTTPRPC(ctx, w, r)
	})))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	return mux
}

func (s *Server) authenticated(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setHTTPHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if token != "" {
			got := strings.TrimSpace(r.Header.Get("Authorization"))
			if got != "Bearer "+token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHTTPRPC(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	setHTTPHeaders(w)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Client == nil {
		http.Error(w, "mcp server missing Helios client", http.StatusInternalServerError)
		return
	}
	if _, ok := s.Client.(ArtifactClient); !ok && s.Artifacts == nil {
		http.Error(w, "mcp server missing artifact reader", http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024+1))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > 4*1024*1024 {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		writeHTTPRPC(w, http.StatusBadRequest, rpcErrorResponse(nil, -32700, "parse error", "empty body"))
		return
	}

	if body[0] == '[' {
		s.handleHTTPBatch(ctx, w, body)
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeHTTPRPC(w, http.StatusBadRequest, rpcErrorResponse(nil, -32700, "parse error", err.Error()))
		return
	}
	if req.ID == nil {
		s.handleNotification(req)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeHTTPRPC(w, http.StatusOK, s.handleRequest(ctx, req))
}

func (s *Server) handleHTTPBatch(ctx context.Context, w http.ResponseWriter, body []byte) {
	var reqs []rpcRequest
	if err := json.Unmarshal(body, &reqs); err != nil {
		writeHTTPRPC(w, http.StatusBadRequest, rpcErrorResponse(nil, -32700, "parse error", err.Error()))
		return
	}
	if len(reqs) == 0 {
		writeHTTPRPC(w, http.StatusBadRequest, rpcErrorResponse(nil, -32600, "invalid request", "empty batch"))
		return
	}
	responses := make([]rpcResponse, 0, len(reqs))
	for _, req := range reqs {
		if req.ID == nil {
			s.handleNotification(req)
			continue
		}
		responses = append(responses, s.handleRequest(ctx, req))
	}
	if len(responses) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeHTTPRPC(w, http.StatusOK, responses)
}

func writeHTTPRPC(w http.ResponseWriter, status int, v any) {
	setHTTPHeaders(w)
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, fmt.Sprintf("marshal response: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func setHTTPHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("MCP-Protocol-Version", protocolVersion)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Protocol-Version")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
}
