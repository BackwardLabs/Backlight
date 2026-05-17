package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/UPside-Lumos-V2/helios/internal/artifacts"
)

func (s *Server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	caseID := strings.TrimSpace(r.PathValue("case_id"))
	if caseID == "" || strings.Contains(caseID, "/") {
		http.NotFound(w, r)
		return
	}
	ctx := ctxOrBackground(r)
	c, err := s.Store.GetCase(ctx, caseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error(), nil)
		return
	}
	if c == nil {
		writeError(w, http.StatusNotFound, "case_not_found", "no case row for the requested id", map[string]string{"case_id": caseID})
		return
	}
	reader, err := artifacts.NewReader(s.Config.OutputRoot, artifacts.DefaultMaxBytes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "artifact_reader_error", err.Error(), nil)
		return
	}
	files, err := reader.List(artifacts.CaseRef{CaseID: c.CaseID, OutputRoot: c.OutputRoot})
	if err != nil {
		writeError(w, http.StatusConflict, "artifact_unavailable", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, ArtifactListResponse{CaseID: caseID, Allowed: reader.AllowedPaths(), Files: files})
}

func (s *Server) handleReadArtifact(w http.ResponseWriter, r *http.Request) {
	caseID := strings.TrimSpace(r.PathValue("case_id"))
	artifactPath := strings.TrimSpace(r.PathValue("artifact_path"))
	if caseID == "" || strings.Contains(caseID, "/") || artifactPath == "" || strings.Contains(artifactPath, "/") {
		http.NotFound(w, r)
		return
	}
	ctx := ctxOrBackground(r)
	c, err := s.Store.GetCase(ctx, caseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error(), nil)
		return
	}
	if c == nil {
		writeError(w, http.StatusNotFound, "case_not_found", "no case row for the requested id", map[string]string{"case_id": caseID})
		return
	}
	maxBytes, err := parseOptionalMaxBytes(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_max_bytes", err.Error(), nil)
		return
	}
	reader, err := artifacts.NewReader(s.Config.OutputRoot, artifacts.DefaultMaxBytes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "artifact_reader_error", err.Error(), nil)
		return
	}
	result, err := reader.Read(artifacts.CaseRef{CaseID: c.CaseID, OutputRoot: c.OutputRoot}, artifactPath, maxBytes)
	if err != nil {
		writeError(w, http.StatusConflict, "artifact_unavailable", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, ArtifactReadResponse{CaseID: caseID, Artifact: result})
}

func parseOptionalMaxBytes(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("max_bytes"))
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, strconv.ErrSyntax
	}
	return n, nil
}
