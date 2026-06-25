package api

import (
	"fmt"
	"net/http"
	"regexp"
	"time"
)

var (
	// tx_hash is treated as a hex string with optional 0x prefix, 64 hex chars
	// (32 bytes) — the standard Ethereum-style tx hash shape. Other chains
	// with longer hashes will need this loosened in a future revision.
	txHashRegex = regexp.MustCompile(`^(0x)?[0-9a-fA-F]{64}$`)
)

// maxCandidateTxHashes bounds how many multi-tx exploit candidates a single
// submission may carry (mirrors hack-detector's extractor cap).
const maxCandidateTxHashes = 3

type validationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func validateSubmission(req *SubmissionRequest) []validationError {
	var errs []validationError
	if req.Chain == "" {
		errs = append(errs, validationError{Field: "chain", Message: "chain is required"})
	}
	if req.TxHash == "" {
		errs = append(errs, validationError{Field: "tx_hash", Message: "tx_hash is required"})
	} else if !txHashRegex.MatchString(req.TxHash) {
		errs = append(errs, validationError{Field: "tx_hash", Message: "tx_hash must be a 64-character hex string with optional 0x prefix"})
	}
	if len(req.CandidateTxHashes) > maxCandidateTxHashes {
		errs = append(errs, validationError{Field: "candidate_tx_hashes", Message: fmt.Sprintf("at most %d candidate_tx_hashes are allowed", maxCandidateTxHashes)})
	}
	for i, h := range req.CandidateTxHashes {
		if !txHashRegex.MatchString(h) {
			errs = append(errs, validationError{Field: fmt.Sprintf("candidate_tx_hashes[%d]", i), Message: "candidate_tx_hashes must be a 64-character hex string with optional 0x prefix"})
		}
	}
	if req.DetectedAt != nil && *req.DetectedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, *req.DetectedAt); err != nil {
			if _, err2 := time.Parse(time.RFC3339, *req.DetectedAt); err2 != nil {
				errs = append(errs, validationError{Field: "detected_at", Message: "detected_at must be ISO8601"})
			}
		}
	}
	return errs
}

func respondValidationErrors(w http.ResponseWriter, errs []validationError) {
	writeError(w, http.StatusBadRequest, "validation_error", "submission failed validation", errs)
}
