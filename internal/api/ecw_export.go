package api

import (
	"net/http"
	"strings"

	"github.com/UPside-Lumos-V2/helios/internal/artifacts"
	"github.com/UPside-Lumos-V2/helios/internal/store"
)

const ecwExportProfile = "ecw-internal-complete"

type ECWCaseSummary struct {
	CaseID       string  `json:"case_id"`
	IncidentSlug string  `json:"incident_slug"`
	Chain        string  `json:"chain"`
	TxHash       string  `json:"tx_hash"`
	State        string  `json:"state"`
	Outcome      *string `json:"outcome"`
	FailureKind  *string `json:"failure_kind"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

type ECWExportResponse struct {
	Profile   string                 `json:"profile"`
	Case      ECWCaseSummary         `json:"case"`
	MaxBytes  int64                  `json:"max_bytes"`
	Allowed   []string               `json:"allowed"`
	Artifacts []artifacts.ReadResult `json:"artifacts"`
	Missing   []string               `json:"missing"`
}

func (s *Server) handleECWExport(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeECWExport(w, r) {
		return
	}

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

	reader, err := artifacts.NewECWReader(s.Config.OutputRoot, s.Config.ECWExportMaxBytes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "artifact_reader_error", err.Error(), nil)
		return
	}
	files, err := reader.List(artifacts.CaseRef{CaseID: c.CaseID, OutputRoot: c.OutputRoot})
	if err != nil {
		writeError(w, http.StatusConflict, "artifact_unavailable", err.Error(), nil)
		return
	}

	resp := ECWExportResponse{
		Profile:   ecwExportProfile,
		Case:      toECWCaseSummary(c),
		MaxBytes:  reader.MaxBytes,
		Allowed:   reader.AllowedPaths(),
		Artifacts: make([]artifacts.ReadResult, 0, len(files)),
		Missing:   make([]string, 0),
	}
	for _, file := range files {
		if !file.Exists {
			resp.Missing = append(resp.Missing, file.Path)
			continue
		}
		artifact, err := reader.Read(artifacts.CaseRef{CaseID: c.CaseID, OutputRoot: c.OutputRoot}, file.Path, 0)
		if err != nil {
			writeError(w, http.StatusConflict, "artifact_unavailable", err.Error(), map[string]string{"path": file.Path})
			return
		}
		resp.Artifacts = append(resp.Artifacts, *artifact)
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) authorizeECWExport(w http.ResponseWriter, r *http.Request) bool {
	if s.Config == nil || strings.TrimSpace(s.Config.ECWExportToken) == "" {
		writeError(w, http.StatusForbidden, "ecw_export_disabled", "BACKLIGHT_ECW_EXPORT_TOKEN is not configured", nil)
		return false
	}
	header := r.Header.Get("Authorization")
	if header == "" {
		writeError(w, http.StatusUnauthorized, "missing_token", "Authorization header is required", nil)
		return false
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) || strings.TrimSpace(header[len(prefix):]) != s.Config.ECWExportToken {
		writeError(w, http.StatusForbidden, "invalid_ecw_token", "Bearer token does not match BACKLIGHT_ECW_EXPORT_TOKEN", nil)
		return false
	}
	return true
}

func toECWCaseSummary(c *store.Case) ECWCaseSummary {
	return ECWCaseSummary{
		CaseID:       c.CaseID,
		IncidentSlug: store.IncidentSlug(c),
		Chain:        c.Chain,
		TxHash:       c.TxHash,
		State:        c.State,
		Outcome:      c.Outcome,
		FailureKind:  c.FailureKind,
		CreatedAt:    c.CreatedAt,
		UpdatedAt:    c.UpdatedAt,
	}
}
