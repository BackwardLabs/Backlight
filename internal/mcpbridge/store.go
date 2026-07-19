// Package mcpbridge stores downstream Backlight handoff payloads for read-only MCP access.
package mcpbridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/api"
	"github.com/UPside-Lumos-V2/helios/internal/handoff"
	"github.com/UPside-Lumos-V2/helios/internal/heliosclient"

	_ "modernc.org/sqlite"
)

const schemaDDL = `
CREATE TABLE IF NOT EXISTS mcp_handoffs (
	case_id           TEXT PRIMARY KEY,
	chain             TEXT NOT NULL,
	tx_hash           TEXT NOT NULL,
	state             TEXT NOT NULL,
	outcome           TEXT NOT NULL,
	failure_kind      TEXT,
	output_root       TEXT,
	summary_json_path TEXT,
	attempt_number    INTEGER NOT NULL,
	payload_json      TEXT NOT NULL,
	received_at       TEXT NOT NULL,
	updated_at        TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_mcp_handoffs_received_at ON mcp_handoffs(received_at DESC);
CREATE INDEX IF NOT EXISTS idx_mcp_handoffs_chain_tx_hash ON mcp_handoffs(chain, tx_hash);
CREATE INDEX IF NOT EXISTS idx_mcp_handoffs_state_outcome ON mcp_handoffs(state, outcome);
`

// Store is the bridge-local index populated by POST /handoff.
type Store struct {
	db *sql.DB
}

// Open creates or opens the bridge SQLite database.
func Open(ctx context.Context, path string) (*Store, error) {
	return open(ctx, path, false)
}

// OpenReadOnly opens an existing bridge SQLite database without creating schema
// or requiring write permissions. This is the mode used by the stdio MCP server.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	return open(ctx, path, true)
}

func open(ctx context.Context, path string, readOnly bool) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("BACKLIGHT_MCP_BRIDGE_DB_PATH is required")
	}
	if dir := filepath.Dir(path); !readOnly && dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create bridge db parent: %w", err)
		}
	}

	q := url.Values{}
	if readOnly {
		q.Set("mode", "ro")
	} else {
		q.Set("_pragma", "journal_mode(WAL)")
	}
	q.Set("_pragma", "busy_timeout(5000)")
	dsn := fmt.Sprintf("file:%s?%s", path, q.Encode())

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open bridge sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping bridge sqlite: %w", err)
	}
	if !readOnly {
		if _, err := db.ExecContext(ctx, schemaDDL); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("apply bridge schema: %w", err)
		}
	}
	if readOnly {
		return &Store{db: db}, nil
	}
	if err := tightenSQLiteFiles(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// UpsertPayload stores the latest handoff payload for a case id.
func (s *Store) UpsertPayload(ctx context.Context, p handoff.Payload) error {
	if err := validatePayload(p); err != nil {
		return err
	}
	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO mcp_handoffs (
			case_id, chain, tx_hash, state, outcome, failure_kind,
			output_root, summary_json_path, attempt_number, payload_json,
			received_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(case_id) DO UPDATE SET
			chain = excluded.chain,
			tx_hash = excluded.tx_hash,
			state = excluded.state,
			outcome = excluded.outcome,
			failure_kind = excluded.failure_kind,
			output_root = excluded.output_root,
			summary_json_path = excluded.summary_json_path,
			attempt_number = excluded.attempt_number,
			payload_json = excluded.payload_json,
			updated_at = excluded.updated_at`,
		p.CaseID, p.Chain, p.TxHash, p.State, p.Outcome, p.FailureKind,
		p.OutputRoot, p.SummaryJSONPath, p.AttemptNumber, string(body),
		now, now,
	)
	if err != nil {
		return fmt.Errorf("upsert bridge payload: %w", err)
	}
	return nil
}

// ListCases implements mcpserver.BacklightClient using the bridge index.
func (s *Store) ListCases(ctx context.Context, opts heliosclient.ListOptions) (*api.CaseListResponse, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}

	where, args := buildFilter(opts)
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM mcp_handoffs "+where, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count bridge cases: %w", err)
	}

	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.QueryContext(ctx, `
		SELECT case_id, chain, tx_hash, state, outcome, failure_kind,
		       output_root, summary_json_path, attempt_number, payload_json,
		       received_at, updated_at
		FROM mcp_handoffs `+where+`
		ORDER BY received_at DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("list bridge cases: %w", err)
	}
	defer rows.Close()

	resp := &api.CaseListResponse{Items: []api.CaseSummary{}, Total: total, Limit: limit, Offset: offset, Order: "received_at DESC"}
	for rows.Next() {
		c, err := scanSummary(rows)
		if err != nil {
			return nil, err
		}
		resp.Items = append(resp.Items, c)
	}
	return resp, rows.Err()
}

// GetCase implements mcpserver.BacklightClient using the bridge index.
func (s *Store) GetCase(ctx context.Context, caseID string) (*api.CaseDetailResponse, error) {
	caseID = strings.TrimSpace(caseID)
	if caseID == "" {
		return nil, fmt.Errorf("case_id is required")
	}
	if strings.Contains(caseID, "/") {
		return nil, fmt.Errorf("case_id must not contain slash")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT case_id, chain, tx_hash, state, outcome, failure_kind,
		       output_root, summary_json_path, attempt_number, payload_json,
		       received_at, updated_at
		FROM mcp_handoffs WHERE case_id = ?`, caseID)
	c, err := scanSummary(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("case not found: %s", caseID)
	}
	if err != nil {
		return nil, err
	}
	return &api.CaseDetailResponse{CaseSummary: c}, nil
}

func validatePayload(p handoff.Payload) error {
	switch {
	case strings.TrimSpace(p.CaseID) == "":
		return fmt.Errorf("case_id is required")
	case strings.TrimSpace(p.Chain) == "":
		return fmt.Errorf("chain is required")
	case strings.TrimSpace(p.TxHash) == "":
		return fmt.Errorf("tx_hash is required")
	case strings.TrimSpace(p.State) == "":
		return fmt.Errorf("state is required")
	case strings.TrimSpace(p.Outcome) == "":
		return fmt.Errorf("outcome is required")
	case p.State != "done" && p.State != "handed-off":
		return fmt.Errorf("state %q is not accepted for MCP bridge indexing", p.State)
	case p.Outcome != "verified" && p.Outcome != "partial" && p.Outcome != "unverified":
		return fmt.Errorf("outcome %q is not accepted for MCP bridge indexing", p.Outcome)
	default:
		return nil
	}
}

func tightenSQLiteFiles(path string) error {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o640); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("chmod bridge sqlite file %s: %w", p, err)
		}
	}
	return nil
}

func buildFilter(opts heliosclient.ListOptions) (string, []any) {
	var clauses []string
	var args []any
	add := func(clause string, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		clauses = append(clauses, clause)
		args = append(args, value)
	}
	add("state = ?", opts.State)
	add("outcome = ?", opts.Outcome)
	add("chain = ?", opts.Chain)
	add("tx_hash = ?", opts.TxHash)
	add("received_at >= ?", opts.CreatedFrom)
	add("received_at <= ?", opts.CreatedTo)
	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSummary(r rowScanner) (api.CaseSummary, error) {
	var (
		c             api.CaseSummary
		outcome       string
		failureKind   sql.NullString
		outputRoot    sql.NullString
		summaryJSON   sql.NullString
		payloadJSON   string
		receivedAt    string
		updatedAt     string
		bridgeSource  = "mcp-bridge"
		notification  = "unknown"
		handoffStatus = "indexed"
	)
	err := r.Scan(
		&c.CaseID, &c.Chain, &c.TxHash, &c.State, &outcome, &failureKind,
		&outputRoot, &summaryJSON, &c.AttemptNumber, &payloadJSON,
		&receivedAt, &updatedAt,
	)
	if err != nil {
		return api.CaseSummary{}, err
	}
	c.Source = &bridgeSource
	c.Metadata = json.RawMessage(payloadJSON)
	c.Outcome = &outcome
	if failureKind.Valid {
		c.FailureKind = &failureKind.String
	}
	if outputRoot.Valid {
		c.OutputRoot = &outputRoot.String
	}
	if summaryJSON.Valid {
		c.SummaryJSONPath = &summaryJSON.String
	}
	c.HandoffStatus = handoffStatus
	c.NotificationStatus = notification
	c.CreatedAt = receivedAt
	c.UpdatedAt = updatedAt
	return c, nil
}
