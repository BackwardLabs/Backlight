package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/store"
)

func (s *Server) handleCreateCase(w http.ResponseWriter, r *http.Request) {
	var req SubmissionRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if errs := validateSubmission(&req); len(errs) > 0 {
		respondValidationErrors(w, errs)
		return
	}
	force := req.ForceRerun != nil && *req.ForceRerun
	c, dedup, err := s.Store.SubmitCase(ctxOrBackground(r), req.Chain, req.TxHash, req.Source, req.DetectedAt, req.Metadata, force)
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

func (s *Server) handleListCases(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit, offset, perr := parsePagination(q)
	if perr != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination", perr.Error(), nil)
		return
	}

	f := store.CaseListFilter{Limit: limit, Offset: offset}
	if v := q.Get("state"); v != "" {
		f.State = &v
	}
	if v := q.Get("outcome"); v != "" {
		f.Outcome = &v
	}
	if v := q.Get("chain"); v != "" {
		f.Chain = &v
	}
	if v := q.Get("tx_hash"); v != "" {
		f.TxHash = &v
	}
	if v := q.Get("created_from"); v != "" {
		if !isISO8601(v) {
			writeError(w, http.StatusBadRequest, "invalid_pagination", "created_from must be ISO8601", nil)
			return
		}
		f.CreatedFromUTC = &v
	}
	if v := q.Get("created_to"); v != "" {
		if !isISO8601(v) {
			writeError(w, http.StatusBadRequest, "invalid_pagination", "created_to must be ISO8601", nil)
			return
		}
		f.CreatedToUTC = &v
	}

	items, total, err := s.Store.ListCases(ctxOrBackground(r), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error(), nil)
		return
	}
	resp := CaseListResponse{
		Items:  make([]CaseSummary, 0, len(items)),
		Total:  total,
		Limit:  limit,
		Offset: offset,
		Order:  "created_at DESC",
	}
	for i := range items {
		resp.Items = append(resp.Items, toSummary(&items[i]))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetCase(w http.ResponseWriter, r *http.Request) {
	caseID := strings.TrimPrefix(r.URL.Path, "/cases/")
	caseID = strings.TrimSuffix(caseID, "/")
	if strings.Contains(caseID, "/") {
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
	events, err := s.Store.CaseEvents(ctx, caseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error(), nil)
		return
	}
	handoffs, err := s.Store.HandoffAttempts(ctx, caseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error(), nil)
		return
	}
	notes, err := s.Store.NotificationAttempts(ctx, caseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, CaseDetailResponse{
		CaseSummary:          toSummary(c),
		CaseEvents:           events,
		HandoffAttempts:      handoffs,
		NotificationAttempts: notes,
	})
}

// handleRetryHandoff re-runs the downstream fan-out for a case that finished
// engine work (state=done) but failed delivery on at least one URL
// (handoff_status=failed). Successful URLs are not re-delivered.
//
// The retry runs in a detached goroutine so the operator gets HTTP 202 quickly;
// progress shows up via GET /cases/{case_id}.handoff_attempts.
func (s *Server) handleRetryHandoff(w http.ResponseWriter, r *http.Request) {
	caseID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/cases/"), "/retry-handoff")
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
	if c.State != "done" || c.HandoffStatus != "failed" {
		writeJSON(w, http.StatusConflict, ErrorBody{
			ErrorCode:     "handoff_not_retryable",
			Message:       "retry-handoff requires state=done AND handoff_status=failed",
			State:         c.State,
			HandoffStatus: c.HandoffStatus,
		})
		return
	}
	if s.Dispatcher == nil || !s.Dispatcher.Configured() {
		writeJSON(w, http.StatusConflict, ErrorBody{
			ErrorCode:     "handoff_not_retryable",
			Message:       "no downstream URLs are configured (HELIOS_DOWNSTREAM_WEBHOOK_URLS is empty)",
			State:         c.State,
			HandoffStatus: c.HandoffStatus,
		})
		return
	}
	pending, err := s.Dispatcher.PendingURLsForRetry(ctx, caseID)
	if err != nil {
		writeError(w, http.StatusConflict, "handoff_not_retryable", err.Error(), nil)
		return
	}
	// Detach: dispatch in background so the request returns immediately and
	// retry sleeps don't tie up the request goroutine.
	go s.Dispatcher.Dispatch(context.Background(), c, pending)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"case_id":      caseID,
		"retry_urls":   pending,
		"max_attempts": s.Config.HandoffRetryMaxAttempts,
	})
}

func parsePagination(q map[string][]string) (limit, offset int, err error) {
	limit = 50
	offset = 0
	if v := firstQuery(q, "limit"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 0 {
			return 0, 0, errPagination("limit must be a non-negative integer")
		}
		if n > 500 {
			return 0, 0, errPagination("limit must be <= 500")
		}
		limit = n
	}
	if v := firstQuery(q, "offset"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 0 {
			return 0, 0, errPagination("offset must be a non-negative integer")
		}
		offset = n
	}
	return limit, offset, nil
}

func firstQuery(q map[string][]string, key string) string {
	v, ok := q[key]
	if !ok || len(v) == 0 {
		return ""
	}
	return v[0]
}

type paginationErr struct{ msg string }

func (e paginationErr) Error() string { return e.msg }

func errPagination(msg string) error { return paginationErr{msg: msg} }

func isISO8601(s string) bool {
	if _, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return true
	}
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return true
	}
	return false
}
