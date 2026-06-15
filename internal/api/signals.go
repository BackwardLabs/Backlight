package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/store"
)

const signalMetadataSchemaVersion = "lumos_signal_to_helios.v1"

type signalMetadataContract struct {
	SchemaVersion   string          `json:"schema_version"`
	LumosSignalID   string          `json:"lumos_signal_id"`
	IncidentGroupID string          `json:"incident_group_id"`
	ProtocolName    string          `json:"protocol_name"`
	SourceRef       signalSourceRef `json:"source_ref"`
}

type signalSourceRef struct {
	Source      string `json:"source"`
	SourceID    string `json:"source_id"`
	SourceURL   string `json:"source_url"`
	PublishedAt string `json:"published_at"`
}

func (s *Server) handleSignal(w http.ResponseWriter, r *http.Request) {
	var req SubmissionRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	signalMeta, errs := validateSignalSubmission(&req)
	if len(errs) > 0 {
		respondValidationErrors(w, errs)
		return
	}
	ctx := ctxOrBackground(r)
	metadata := normalizeSignalMetadata(req.Metadata)
	metadata = s.enrichIncidentMetadata(ctx, req.Chain, req.TxHash, metadata)
	// /signals does not honour force_rerun (hack-detector cannot opt into reruns).
	c, dedup, err := s.Store.SubmitCase(ctx, req.Chain, req.TxHash, req.Source, req.DetectedAt, metadata, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error(), nil)
		return
	}
	if dedup == store.DedupExisting || dedup == store.DedupExistingNonRetryable {
		if mergeErr := s.Store.MergeCaseMetadata(ctx, c.CaseID, signalMetadataMergeFields(signalMeta)); mergeErr != nil {
			writeError(w, http.StatusInternalServerError, "store_error", mergeErr.Error(), nil)
			return
		}
	}
	if err := s.Store.RecordIncomingSignal(ctx, store.IncomingSignal{
		LumosSignalID:   strings.TrimSpace(signalMeta.LumosSignalID),
		IncidentGroupID: strings.TrimSpace(signalMeta.IncidentGroupID),
		CaseID:          c.CaseID,
		Chain:           req.Chain,
		TxHash:          req.TxHash,
		ProtocolName:    strings.TrimSpace(signalMeta.ProtocolName),
		Source:          strings.TrimSpace(*req.Source),
		SourceURL:       strings.TrimSpace(signalMeta.SourceRef.SourceURL),
		DetectedAt:      strings.TrimSpace(*req.DetectedAt),
		Metadata:        metadata,
		DedupStatus:     dedup,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusAccepted, SubmissionResponse{
		CaseID:       c.CaseID,
		Status:       dedup,
		ParentCaseID: c.ParentCaseID,
	})
}

func validateSignalSubmission(req *SubmissionRequest) (signalMetadataContract, []validationError) {
	errs := validateSubmission(req)
	if req.Source == nil || strings.TrimSpace(*req.Source) == "" {
		errs = append(errs, validationError{Field: "source", Message: "source is required for /signals"})
	}
	if req.DetectedAt == nil || strings.TrimSpace(*req.DetectedAt) == "" {
		errs = append(errs, validationError{Field: "detected_at", Message: "detected_at is required for /signals"})
	}

	meta, metaErrs := parseSignalMetadataContract(req.Metadata)
	errs = append(errs, metaErrs...)
	return meta, errs
}

func parseSignalMetadataContract(raw json.RawMessage) (signalMetadataContract, []validationError) {
	var meta signalMetadataContract
	if len(raw) == 0 {
		return meta, []validationError{{Field: "metadata", Message: "metadata is required for /signals"}}
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return meta, []validationError{{Field: "metadata", Message: "metadata must be a JSON object"}}
	}

	var errs []validationError
	if strings.TrimSpace(meta.SchemaVersion) != signalMetadataSchemaVersion {
		errs = append(errs, validationError{Field: "metadata.schema_version", Message: "metadata.schema_version must be " + signalMetadataSchemaVersion})
	}
	if strings.TrimSpace(meta.LumosSignalID) == "" {
		errs = append(errs, validationError{Field: "metadata.lumos_signal_id", Message: "metadata.lumos_signal_id is required"})
	}
	if strings.TrimSpace(meta.IncidentGroupID) == "" {
		errs = append(errs, validationError{Field: "metadata.incident_group_id", Message: "metadata.incident_group_id is required"})
	}
	if strings.TrimSpace(meta.ProtocolName) == "" {
		errs = append(errs, validationError{Field: "metadata.protocol_name", Message: "metadata.protocol_name is required"})
	}
	if strings.TrimSpace(meta.SourceRef.Source) == "" {
		errs = append(errs, validationError{Field: "metadata.source_ref.source", Message: "metadata.source_ref.source is required"})
	}
	if strings.TrimSpace(meta.SourceRef.SourceID) == "" {
		errs = append(errs, validationError{Field: "metadata.source_ref.source_id", Message: "metadata.source_ref.source_id is required"})
	}
	if strings.TrimSpace(meta.SourceRef.SourceURL) == "" {
		errs = append(errs, validationError{Field: "metadata.source_ref.source_url", Message: "metadata.source_ref.source_url is required"})
	} else if u, err := url.Parse(strings.TrimSpace(meta.SourceRef.SourceURL)); err != nil || u.Scheme == "" || u.Host == "" {
		errs = append(errs, validationError{Field: "metadata.source_ref.source_url", Message: "metadata.source_ref.source_url must be an absolute URL"})
	}
	if strings.TrimSpace(meta.SourceRef.PublishedAt) == "" {
		errs = append(errs, validationError{Field: "metadata.source_ref.published_at", Message: "metadata.source_ref.published_at is required"})
	} else if _, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(meta.SourceRef.PublishedAt)); err != nil {
		if _, err2 := time.Parse(time.RFC3339, strings.TrimSpace(meta.SourceRef.PublishedAt)); err2 != nil {
			errs = append(errs, validationError{Field: "metadata.source_ref.published_at", Message: "metadata.source_ref.published_at must be ISO8601"})
		}
	}
	return meta, errs
}

func normalizeSignalMetadata(raw json.RawMessage) json.RawMessage {
	normalized := map[string]any{}
	_ = json.Unmarshal(raw, &normalized)
	if protocol, ok := normalized["protocol_name"].(string); ok && strings.TrimSpace(protocol) != "" {
		if existing, ok := normalized["protocol"].(string); !ok || strings.TrimSpace(existing) == "" {
			normalized["protocol"] = strings.TrimSpace(protocol)
		}
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return raw
	}
	return data
}

func signalMetadataMergeFields(meta signalMetadataContract) map[string]any {
	return map[string]any{
		"schema_version":     signalMetadataSchemaVersion,
		"lumos_signal_id":    strings.TrimSpace(meta.LumosSignalID),
		"incident_group_id":  strings.TrimSpace(meta.IncidentGroupID),
		"protocol_name":      strings.TrimSpace(meta.ProtocolName),
		"protocol":           strings.TrimSpace(meta.ProtocolName),
		"source_ref":         meta.SourceRef,
		"protocol_source":    "hack_detector_signal",
		"signal_schema_name": "lumos_signal_to_helios",
	}
}
