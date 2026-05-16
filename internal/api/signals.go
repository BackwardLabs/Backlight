package api

import "net/http"

func (s *Server) handleSignal(w http.ResponseWriter, r *http.Request) {
	var req SubmissionRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if errs := validateSubmission(&req); len(errs) > 0 {
		respondValidationErrors(w, errs)
		return
	}
	// /signals does not honour force_rerun (hack-detector cannot opt into reruns).
	c, dedup, err := s.Store.SubmitCase(ctxOrBackground(r), req.Chain, req.TxHash, req.Source, req.DetectedAt, req.Metadata, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusAccepted, SubmissionResponse{
		CaseID:       c.CaseID,
		Status:       dedup,
		ParentCaseID: c.ParentCaseID,
	})
}
