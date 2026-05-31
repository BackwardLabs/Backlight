package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// IncomingSignal is the durable provenance record received from hack-detector.
type IncomingSignal struct {
	LumosSignalID   string          `json:"lumos_signal_id"`
	IncidentGroupID string          `json:"incident_group_id"`
	CaseID          string          `json:"case_id"`
	Chain           string          `json:"chain"`
	TxHash          string          `json:"tx_hash"`
	ProtocolName    string          `json:"protocol_name"`
	Source          string          `json:"source"`
	SourceURL       string          `json:"source_url"`
	DetectedAt      string          `json:"detected_at"`
	Metadata        json.RawMessage `json:"metadata"`
	DedupStatus     string          `json:"dedup_status,omitempty"`
	ReceivedAt      string          `json:"received_at,omitempty"`
	UpdatedAt       string          `json:"updated_at,omitempty"`
}

// RecordIncomingSignal upserts the hack-detector signal and appends a case event.
// lumos_signal_id is the idempotency key from the upstream signal row.
func (s *Store) RecordIncomingSignal(ctx context.Context, sig IncomingSignal) error {
	if len(sig.Metadata) == 0 {
		sig.Metadata = json.RawMessage("{}")
	}
	now := nowUTC()
	eventPayload, err := json.Marshal(map[string]any{
		"lumos_signal_id":   sig.LumosSignalID,
		"incident_group_id": sig.IncidentGroupID,
		"protocol_name":     sig.ProtocolName,
		"source":            sig.Source,
		"source_url":        sig.SourceURL,
		"detected_at":       sig.DetectedAt,
		"dedup_status":      sig.DedupStatus,
		"schema":            "helios-signal-received-event-v1",
	})
	if err != nil {
		return fmt.Errorf("marshal signal event payload: %w", err)
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO incoming_signals (
				lumos_signal_id, incident_group_id, case_id, chain, tx_hash,
				protocol_name, source, source_url, detected_at, metadata,
				received_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(lumos_signal_id) DO UPDATE SET
				incident_group_id = excluded.incident_group_id,
				case_id = excluded.case_id,
				chain = excluded.chain,
				tx_hash = excluded.tx_hash,
				protocol_name = excluded.protocol_name,
				source = excluded.source,
				source_url = excluded.source_url,
				detected_at = excluded.detected_at,
				metadata = excluded.metadata,
				updated_at = excluded.updated_at`,
			sig.LumosSignalID, sig.IncidentGroupID, sig.CaseID, sig.Chain, sig.TxHash,
			sig.ProtocolName, sig.Source, sig.SourceURL, sig.DetectedAt, string(sig.Metadata),
			now, now,
		)
		if err != nil {
			return fmt.Errorf("record incoming signal: %w", err)
		}
		return appendEventTx(ctx, tx, sig.CaseID, nil, nil, "signal_received", eventPayload, now)
	})
}
