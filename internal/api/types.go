package api

import (
	"encoding/json"

	"github.com/UPside-Lumos-V2/helios/internal/artifacts"
	"github.com/UPside-Lumos-V2/helios/internal/store"
)

// SubmissionRequest is the shared body for POST /signals and POST /cases.
type SubmissionRequest struct {
	Chain      string          `json:"chain"`
	TxHash     string          `json:"tx_hash"`
	Source     *string         `json:"source,omitempty"`
	DetectedAt *string         `json:"detected_at,omitempty"`
	ForceRerun *bool           `json:"force_rerun,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

// SubmissionResponse is the 202 body for POST /signals and POST /cases.
type SubmissionResponse struct {
	CaseID       string  `json:"case_id"`
	Status       string  `json:"status"`
	ParentCaseID *string `json:"parent_case_id"`
}

// CaseSummary is the GET /cases item shape (excludes sub-arrays).
type CaseSummary struct {
	CaseID             string          `json:"case_id"`
	Chain              string          `json:"chain"`
	TxHash             string          `json:"tx_hash"`
	Source             *string         `json:"source"`
	DetectedAt         *string         `json:"detected_at"`
	Metadata           json.RawMessage `json:"metadata"`
	State              string          `json:"state"`
	Outcome            *string         `json:"outcome"`
	FailureKind        *string         `json:"failure_kind"`
	OutputRoot         *string         `json:"output_root"`
	SummaryJSONPath    *string         `json:"summary_json_path"`
	AttemptNumber      int             `json:"attempt_number"`
	ParentCaseID       *string         `json:"parent_case_id"`
	ForceRerun         bool            `json:"force_rerun"`
	HandoffStatus      string          `json:"handoff_status"`
	NotificationStatus string          `json:"notification_status"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
}

// CaseListResponse is the GET /cases body.
type CaseListResponse struct {
	Items  []CaseSummary `json:"items"`
	Total  int           `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
	Order  string        `json:"order"`
}

// CaseDetailResponse is the GET /cases/{case_id} body — case-detail + sub-arrays.
type CaseDetailResponse struct {
	CaseSummary
	CaseEvents           []store.CaseEvent           `json:"case_events"`
	HandoffAttempts      []store.HandoffAttempt      `json:"handoff_attempts"`
	NotificationAttempts []store.NotificationAttempt `json:"notification_attempts"`
}

// ArtifactListResponse is GET /cases/{case_id}/artifacts.
type ArtifactListResponse struct {
	CaseID  string               `json:"case_id"`
	Allowed []string             `json:"allowed"`
	Files   []artifacts.FileInfo `json:"files"`
}

// ArtifactReadResponse is GET /cases/{case_id}/artifacts/{path}.
type ArtifactReadResponse struct {
	CaseID   string                `json:"case_id"`
	Artifact *artifacts.ReadResult `json:"artifact"`
}

func toSummary(c *store.Case) CaseSummary {
	return CaseSummary{
		CaseID:             c.CaseID,
		Chain:              c.Chain,
		TxHash:             c.TxHash,
		Source:             c.Source,
		DetectedAt:         c.DetectedAt,
		Metadata:           c.Metadata,
		State:              c.State,
		Outcome:            c.Outcome,
		FailureKind:        c.FailureKind,
		OutputRoot:         c.OutputRoot,
		SummaryJSONPath:    c.SummaryJSONPath,
		AttemptNumber:      c.AttemptNumber,
		ParentCaseID:       c.ParentCaseID,
		ForceRerun:         c.ForceRerun,
		HandoffStatus:      c.HandoffStatus,
		NotificationStatus: c.NotificationStatus,
		CreatedAt:          c.CreatedAt,
		UpdatedAt:          c.UpdatedAt,
	}
}
