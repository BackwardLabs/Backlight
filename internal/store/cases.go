package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/UPside-Lumos-V2/helios/internal/metrics"
)

type Case struct {
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

type CaseEvent struct {
	EventID    string          `json:"event_id"`
	CaseID     string          `json:"case_id"`
	FromState  *string         `json:"from_state"`
	ToState    *string         `json:"to_state"`
	EventType  string          `json:"event_type"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt string          `json:"occurred_at"`
}

type HandoffAttempt struct {
	AttemptID             string  `json:"attempt_id"`
	CaseID                string  `json:"case_id"`
	TargetURL             string  `json:"target_url"`
	AttemptedAt           string  `json:"attempted_at"`
	HTTPStatus            *int    `json:"http_status"`
	Result                string  `json:"result"`
	Error                 *string `json:"error"`
	AttemptIndexForTarget int     `json:"attempt_index_for_target"`
}

type NotificationAttempt struct {
	AttemptID    string  `json:"attempt_id"`
	CaseID       string  `json:"case_id"`
	Channel      string  `json:"channel"`
	Event        string  `json:"event"`
	AttemptedAt  string  `json:"attempted_at"`
	Result       string  `json:"result"`
	Error        *string `json:"error"`
	AttemptIndex int     `json:"attempt_index"`
}

const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateDone      = "done"
	StateHandedOff = "handed-off"
	StateFailed    = "failed"
)

const (
	DedupNew          = "new"
	DedupExisting     = "existing"
	DedupRerunCreated = "rerun_created"
)

func NewID(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

// SubmitCase performs dedup-aware insert. Returns (case, dedupResult).
// Caller passes parsed/validated chain, tx_hash, optional source/detected_at/metadata and force_rerun.
// dedup contract (per seed): look up latest lineage leaf for (chain, tx_hash);
//   - no leaf            -> insert root case in state=queued, dedup=new
//   - leaf in active/done/handed-off and force_rerun=false -> return existing case, dedup=existing
//   - leaf in active/done/handed-off and force_rerun=true  -> insert linked child, dedup=rerun_created
//   - leaf in failed                                       -> insert linked child, dedup=rerun_created
func (s *Store) SubmitCase(ctx context.Context, chain, txHash string, source, detectedAt *string, metadata json.RawMessage, forceRerun bool) (*Case, string, error) {
	if len(metadata) == 0 {
		metadata = json.RawMessage("{}")
	}

	var (
		result   string
		outCase  *Case
		insertID = NewID("case")
	)

	err := s.Tx(ctx, func(tx *sql.Tx) error {
		leaf, err := getLatestLeafTx(ctx, tx, chain, txHash)
		if err != nil {
			return err
		}

		now := nowUTC()
		switch {
		case leaf == nil:
			// new root
			c, err := insertCaseTx(ctx, tx, &Case{
				CaseID:             insertID,
				Chain:              chain,
				TxHash:             txHash,
				Source:             source,
				DetectedAt:         detectedAt,
				Metadata:           metadata,
				State:              StateQueued,
				AttemptNumber:      1,
				ForceRerun:         forceRerun,
				HandoffStatus:      "pending",
				NotificationStatus: "pending",
				CreatedAt:          now,
				UpdatedAt:          now,
			})
			if err != nil {
				return err
			}
			if err := appendEventTx(ctx, tx, c.CaseID, nil, ptr(StateQueued), "case_inserted", nil, now); err != nil {
				return err
			}
			metrics.ObserveCaseStateTransition(StateQueued)
			outCase = c
			result = DedupNew
			return nil

		case leaf.State == StateFailed:
			child, err := insertChildCaseTx(ctx, tx, insertID, leaf, source, detectedAt, metadata, forceRerun, now)
			if err != nil {
				return err
			}
			if err := appendEventTx(ctx, tx, child.CaseID, nil, ptr(StateQueued), "case_inserted", nil, now); err != nil {
				return err
			}
			metrics.ObserveCaseStateTransition(StateQueued)
			outCase = child
			result = DedupRerunCreated
			return nil

		default: // active or completed leaf
			if !forceRerun {
				outCase = leaf
				result = DedupExisting
				return nil
			}
			child, err := insertChildCaseTx(ctx, tx, insertID, leaf, source, detectedAt, metadata, true, now)
			if err != nil {
				return err
			}
			if err := appendEventTx(ctx, tx, child.CaseID, nil, ptr(StateQueued), "case_inserted", nil, now); err != nil {
				return err
			}
			metrics.ObserveCaseStateTransition(StateQueued)
			outCase = child
			result = DedupRerunCreated
			return nil
		}
	})
	if err != nil {
		return nil, "", err
	}
	return outCase, result, nil
}

func insertChildCaseTx(ctx context.Context, tx *sql.Tx, caseID string, parent *Case, source, detectedAt *string, metadata json.RawMessage, forceRerun bool, now string) (*Case, error) {
	c := &Case{
		CaseID:             caseID,
		Chain:              parent.Chain,
		TxHash:             parent.TxHash,
		Source:             source,
		DetectedAt:         detectedAt,
		Metadata:           metadata,
		State:              StateQueued,
		AttemptNumber:      parent.AttemptNumber + 1,
		ParentCaseID:       ptr(parent.CaseID),
		ForceRerun:         forceRerun,
		HandoffStatus:      "pending",
		NotificationStatus: "pending",
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	return insertCaseTx(ctx, tx, c)
}

func insertCaseTx(ctx context.Context, tx *sql.Tx, c *Case) (*Case, error) {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO cases (
			case_id, chain, tx_hash, source, detected_at, metadata,
			state, outcome, failure_kind, output_root, summary_json_path,
			attempt_number, parent_case_id, force_rerun,
			handoff_status, notification_status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.CaseID, c.Chain, c.TxHash, c.Source, c.DetectedAt, string(c.Metadata),
		c.State, c.Outcome, c.FailureKind, c.OutputRoot, c.SummaryJSONPath,
		c.AttemptNumber, c.ParentCaseID, boolToInt(c.ForceRerun),
		c.HandoffStatus, c.NotificationStatus, c.CreatedAt, c.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert case: %w", err)
	}
	return c, nil
}

func getLatestLeafTx(ctx context.Context, tx *sql.Tx, chain, txHash string) (*Case, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT case_id, chain, tx_hash, source, detected_at, metadata,
		       state, outcome, failure_kind, output_root, summary_json_path,
		       attempt_number, parent_case_id, force_rerun,
		       handoff_status, notification_status, created_at, updated_at
		FROM cases
		WHERE chain = ? AND tx_hash = ?
		ORDER BY attempt_number DESC
		LIMIT 1`, chain, txHash)
	c, err := scanCase(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func appendEventTx(ctx context.Context, tx *sql.Tx, caseID string, fromState, toState *string, eventType string, payload json.RawMessage, occurredAt string) error {
	var payloadStr *string
	if len(payload) > 0 {
		s := string(payload)
		payloadStr = &s
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO case_events (event_id, case_id, from_state, to_state, event_type, payload, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		NewID("evt"), caseID, fromState, toState, eventType, payloadStr, occurredAt,
	)
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

func (s *Store) GetCase(ctx context.Context, caseID string) (*Case, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT case_id, chain, tx_hash, source, detected_at, metadata,
		       state, outcome, failure_kind, output_root, summary_json_path,
		       attempt_number, parent_case_id, force_rerun,
		       handoff_status, notification_status, created_at, updated_at
		FROM cases WHERE case_id = ?`, caseID)
	c, err := scanCase(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func (s *Store) CaseEvents(ctx context.Context, caseID string) ([]CaseEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT event_id, case_id, from_state, to_state, event_type, payload, occurred_at
		FROM case_events WHERE case_id = ? ORDER BY occurred_at`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CaseEvent
	for rows.Next() {
		var e CaseEvent
		var payload sql.NullString
		if err := rows.Scan(&e.EventID, &e.CaseID, &e.FromState, &e.ToState, &e.EventType, &payload, &e.OccurredAt); err != nil {
			return nil, err
		}
		if payload.Valid {
			e.Payload = json.RawMessage(payload.String)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) HandoffAttempts(ctx context.Context, caseID string) ([]HandoffAttempt, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT attempt_id, case_id, target_url, attempted_at, http_status, result, error, attempt_index_for_target
		FROM handoff_attempts WHERE case_id = ? ORDER BY attempted_at`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HandoffAttempt
	for rows.Next() {
		var a HandoffAttempt
		var status sql.NullInt64
		if err := rows.Scan(&a.AttemptID, &a.CaseID, &a.TargetURL, &a.AttemptedAt, &status, &a.Result, &a.Error, &a.AttemptIndexForTarget); err != nil {
			return nil, err
		}
		if status.Valid {
			n := int(status.Int64)
			a.HTTPStatus = &n
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) NotificationAttempts(ctx context.Context, caseID string) ([]NotificationAttempt, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT attempt_id, case_id, channel, event, attempted_at, result, error, attempt_index
		FROM notification_attempts WHERE case_id = ? ORDER BY attempted_at`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotificationAttempt
	for rows.Next() {
		var a NotificationAttempt
		if err := rows.Scan(&a.AttemptID, &a.CaseID, &a.Channel, &a.Event, &a.AttemptedAt, &a.Result, &a.Error, &a.AttemptIndex); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CaseListFilter is the GET /cases query.
type CaseListFilter struct {
	State          *string
	Outcome        *string
	Chain          *string
	TxHash         *string
	CreatedFromUTC *string
	CreatedToUTC   *string
	Limit          int
	Offset         int
}

func (s *Store) ListCases(ctx context.Context, f CaseListFilter) (items []Case, total int, err error) {
	where, args := buildCaseFilter(f)

	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM cases "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.QueryContext(ctx, `
		SELECT case_id, chain, tx_hash, source, detected_at, metadata,
		       state, outcome, failure_kind, output_root, summary_json_path,
		       attempt_number, parent_case_id, force_rerun,
		       handoff_status, notification_status, created_at, updated_at
		FROM cases `+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanCase(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *c)
	}
	return items, total, rows.Err()
}

func buildCaseFilter(f CaseListFilter) (string, []any) {
	clauses := []string{}
	args := []any{}
	if f.State != nil {
		clauses = append(clauses, "state = ?")
		args = append(args, *f.State)
	}
	if f.Outcome != nil {
		clauses = append(clauses, "outcome = ?")
		args = append(args, *f.Outcome)
	}
	if f.Chain != nil {
		clauses = append(clauses, "chain = ?")
		args = append(args, *f.Chain)
	}
	if f.TxHash != nil {
		clauses = append(clauses, "tx_hash = ?")
		args = append(args, *f.TxHash)
	}
	if f.CreatedFromUTC != nil {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, *f.CreatedFromUTC)
	}
	if f.CreatedToUTC != nil {
		clauses = append(clauses, "created_at <= ?")
		args = append(args, *f.CreatedToUTC)
	}
	if len(clauses) == 0 {
		return "", args
	}
	where := "WHERE "
	for i, c := range clauses {
		if i > 0 {
			where += " AND "
		}
		where += c
	}
	return where, args
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCase(r rowScanner) (*Case, error) {
	var c Case
	var metadata string
	var forceRerun int
	err := r.Scan(
		&c.CaseID, &c.Chain, &c.TxHash, &c.Source, &c.DetectedAt, &metadata,
		&c.State, &c.Outcome, &c.FailureKind, &c.OutputRoot, &c.SummaryJSONPath,
		&c.AttemptNumber, &c.ParentCaseID, &forceRerun,
		&c.HandoffStatus, &c.NotificationStatus, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	c.Metadata = json.RawMessage(metadata)
	c.ForceRerun = forceRerun != 0
	return &c, nil
}

func ptr[T any](v T) *T { return &v }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ClaimNextQueued atomically pulls the oldest queued case, transitions it to
// state=running, assigns its per-attempt output_root + summary_json_path,
// and appends a state_transition case_events row — all in one SQLite tx.
//
// outputRootParent is the directory under which per-case roots are created
// (HELIOS_OUTPUT_ROOT). The returned *Case carries the updated paths and
// state; nil/nil means "no queued work right now".
func (s *Store) ClaimNextQueued(ctx context.Context, outputRootParent string) (*Case, error) {
	var claimed *Case
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `
			SELECT case_id, chain, tx_hash, source, detected_at, metadata,
			       state, outcome, failure_kind, output_root, summary_json_path,
			       attempt_number, parent_case_id, force_rerun,
			       handoff_status, notification_status, created_at, updated_at
			FROM cases WHERE state = 'queued' ORDER BY created_at ASC LIMIT 1`)
		c, err := scanCase(row)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}

		outputRoot := filepath.Join(outputRootParent, c.CaseID)
		summaryPath := filepath.Join(outputRoot, "summary.json")
		now := nowUTC()

		res, err := tx.ExecContext(ctx, `
			UPDATE cases
			   SET state             = 'running',
			       output_root       = ?,
			       summary_json_path = ?,
			       updated_at        = ?
			 WHERE case_id = ? AND state = 'queued'`,
			outputRoot, summaryPath, now, c.CaseID,
		)
		if err != nil {
			return fmt.Errorf("claim update: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			// raced with another claimant; nothing to return
			return nil
		}

		c.State = StateRunning
		c.OutputRoot = &outputRoot
		c.SummaryJSONPath = &summaryPath
		c.UpdatedAt = now

		if err := appendEventTx(ctx, tx, c.CaseID, ptr(StateQueued), ptr(StateRunning), "state_transition", nil, now); err != nil {
			return err
		}
		claimed = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	if claimed != nil {
		metrics.ObserveCaseStateTransition(StateRunning)
	}
	return claimed, nil
}

// MarkDone transitions a running case to state=done with the given outcome.
// When downstreamConfigured=false the seed requires the case to advance
// directly to handed-off (handoff_status=skipped) inside the same tx.
func (s *Store) MarkDone(ctx context.Context, caseID, outcome string, downstreamConfigured bool) error {
	advanced := false
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		now := nowUTC()
		if _, err := tx.ExecContext(ctx, `
			UPDATE cases
			   SET state      = 'done',
			       outcome    = ?,
			       updated_at = ?
			 WHERE case_id = ? AND state = 'running'`,
			outcome, now, caseID,
		); err != nil {
			return fmt.Errorf("mark done update: %w", err)
		}
		payload, _ := json.Marshal(map[string]string{"outcome": outcome})
		if err := appendEventTx(ctx, tx, caseID, ptr(StateRunning), ptr(StateDone), "state_transition", payload, now); err != nil {
			return err
		}
		if downstreamConfigured {
			return nil
		}
		// no downstream URLs configured → skip handoff entirely
		if _, err := tx.ExecContext(ctx, `
			UPDATE cases
			   SET state          = 'handed-off',
			       handoff_status = 'skipped',
			       updated_at     = ?
			 WHERE case_id = ?`,
			now, caseID,
		); err != nil {
			return fmt.Errorf("skip handoff update: %w", err)
		}
		skipPayload, _ := json.Marshal(map[string]string{"reason": "no_downstream_urls_configured"})
		if err := appendEventTx(ctx, tx, caseID, ptr(StateDone), ptr(StateHandedOff), "state_transition", skipPayload, now); err != nil {
			return err
		}
		advanced = true
		return nil
	})
	if err != nil {
		return err
	}
	metrics.ObserveCaseStateTransition(StateDone)
	metrics.ObserveCaseOutcome(outcome)
	if advanced {
		metrics.ObserveCaseStateTransition(StateHandedOff)
	}
	return nil
}

// RecordHandoffAttempt inserts a handoff_attempts row and appends a
// case_events row (event_type=handoff_attempted) in the same SQLite tx so
// the unified audit timeline stays consistent with the per-attempt detail.
func (s *Store) RecordHandoffAttempt(ctx context.Context, a HandoffAttempt) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_attempts (
				attempt_id, case_id, target_url, attempted_at,
				http_status, result, error, attempt_index_for_target
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			a.AttemptID, a.CaseID, a.TargetURL, a.AttemptedAt,
			a.HTTPStatus, a.Result, a.Error, a.AttemptIndexForTarget,
		); err != nil {
			return fmt.Errorf("insert handoff_attempt: %w", err)
		}
		payload, _ := json.Marshal(map[string]any{
			"target_url":    a.TargetURL,
			"http_status":   a.HTTPStatus,
			"result":        a.Result,
			"attempt_index": a.AttemptIndexForTarget,
		})
		return appendEventTx(ctx, tx, a.CaseID, nil, nil, "handoff_attempted", payload, a.AttemptedAt)
	})
}

// SetHandoffStatus updates a case's handoff_status. When advanceToHandedOff
// is true the case ALSO transitions state from done to handed-off in the same
// SQLite transaction (with a state_transition case_events row); the seed
// requires this for the "all URLs succeeded" path and for the "no downstream
// URLs configured" short-circuit.
func (s *Store) SetHandoffStatus(ctx context.Context, caseID, status string, advanceToHandedOff bool) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		now := nowUTC()
		if !advanceToHandedOff {
			if _, err := tx.ExecContext(ctx, `
				UPDATE cases SET handoff_status = ?, updated_at = ? WHERE case_id = ?`,
				status, now, caseID,
			); err != nil {
				return fmt.Errorf("update handoff_status: %w", err)
			}
			return nil
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE cases
			   SET state          = 'handed-off',
			       handoff_status = ?,
			       updated_at     = ?
			 WHERE case_id = ? AND state = 'done'`,
			status, now, caseID,
		); err != nil {
			return fmt.Errorf("advance to handed-off: %w", err)
		}
		payload, _ := json.Marshal(map[string]string{"handoff_status": status})
		if err := appendEventTx(ctx, tx, caseID, ptr(StateDone), ptr(StateHandedOff), "state_transition", payload, now); err != nil {
			return err
		}
		metrics.ObserveCaseStateTransition(StateHandedOff)
		return nil
	})
}

// RecoverRunningCases applies the single startup rule defined by the seed:
// every case found in state=running is updated to state=failed with
// outcome=engine_error, failure_kind=host_restart, handoff_status=skipped,
// and a new linked child case is inserted as state=queued with
// attempt_number+1, parent_case_id set to the predecessor, and a fresh
// output_root (the worker will assign output paths when it later claims it
// via ClaimNextQueued).
//
// Returns the number of orphans recovered. Idempotent — running rows from
// previous startups are processed regardless of how long they sat.
func (s *Store) RecoverRunningCases(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT case_id, chain, tx_hash, source, detected_at, metadata,
		       state, outcome, failure_kind, output_root, summary_json_path,
		       attempt_number, parent_case_id, force_rerun,
		       handoff_status, notification_status, created_at, updated_at
		FROM cases WHERE state = 'running'`)
	if err != nil {
		return 0, fmt.Errorf("query running cases: %w", err)
	}
	defer rows.Close()

	var orphans []*Case
	for rows.Next() {
		c, err := scanCase(rows)
		if err != nil {
			return 0, err
		}
		orphans = append(orphans, c)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	recovered := 0
	for _, orphan := range orphans {
		err := s.Tx(ctx, func(tx *sql.Tx) error {
			now := nowUTC()

			if _, err := tx.ExecContext(ctx, `
				UPDATE cases
				   SET state          = 'failed',
				       outcome        = 'engine_error',
				       failure_kind   = 'host_restart',
				       handoff_status = 'skipped',
				       updated_at     = ?
				 WHERE case_id = ? AND state = 'running'`,
				now, orphan.CaseID,
			); err != nil {
				return fmt.Errorf("mark host_restart failed: %w", err)
			}
			restartPayload, _ := json.Marshal(map[string]string{
				"outcome":      "engine_error",
				"failure_kind": "host_restart",
			})
			if err := appendEventTx(ctx, tx, orphan.CaseID, ptr(StateRunning), ptr(StateFailed), "restart_recovery", restartPayload, now); err != nil {
				return err
			}

			metadata := orphan.Metadata
			if len(metadata) == 0 {
				metadata = json.RawMessage("{}")
			}
			childID := NewID("case")
			child := &Case{
				CaseID:             childID,
				Chain:              orphan.Chain,
				TxHash:             orphan.TxHash,
				Source:             orphan.Source,
				DetectedAt:         orphan.DetectedAt,
				Metadata:           metadata,
				State:              StateQueued,
				AttemptNumber:      orphan.AttemptNumber + 1,
				ParentCaseID:       ptr(orphan.CaseID),
				ForceRerun:         false,
				HandoffStatus:      "pending",
				NotificationStatus: "pending",
				CreatedAt:          now,
				UpdatedAt:          now,
			}
			if _, err := insertCaseTx(ctx, tx, child); err != nil {
				return fmt.Errorf("insert recovery child: %w", err)
			}
			return appendEventTx(ctx, tx, childID, nil, ptr(StateQueued), "restart_recovery", restartPayload, now)
		})
		if err != nil {
			return recovered, err
		}
		metrics.ObserveCaseStateTransition(StateFailed)
		metrics.ObserveCaseOutcome("engine_error")
		metrics.ObserveCaseStateTransition(StateQueued)
		recovered++
	}
	return recovered, nil
}

// RecordNotificationAttempt inserts a notification_attempts row and appends
// a notification_attempted case_event in the same SQLite tx so the timeline
// stays consistent with handoff_attempts.
func (s *Store) RecordNotificationAttempt(ctx context.Context, a NotificationAttempt) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO notification_attempts (
				attempt_id, case_id, channel, event, attempted_at,
				result, error, attempt_index
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			a.AttemptID, a.CaseID, a.Channel, a.Event, a.AttemptedAt,
			a.Result, a.Error, a.AttemptIndex,
		); err != nil {
			return fmt.Errorf("insert notification_attempt: %w", err)
		}
		payload, _ := json.Marshal(map[string]any{
			"channel":       a.Channel,
			"event":         a.Event,
			"result":        a.Result,
			"attempt_index": a.AttemptIndex,
		})
		return appendEventTx(ctx, tx, a.CaseID, nil, nil, "notification_attempted", payload, a.AttemptedAt)
	})
}

// SetNotificationStatus updates the per-case notification_status field.
// Per the seed, notification failures NEVER alter case state, so this is the
// only field touched.
func (s *Store) SetNotificationStatus(ctx context.Context, caseID, status string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE cases SET notification_status = ?, updated_at = ? WHERE case_id = ?`,
		status, nowUTC(), caseID,
	)
	if err != nil {
		return fmt.Errorf("update notification_status: %w", err)
	}
	return nil
}

// InitNotificationStatusDisabled flips every case whose notification_status
// is still "pending" to "disabled". Called at startup when no notification
// channel is configured so the API surface is honest about why nothing was
// ever delivered. Idempotent.
func (s *Store) InitNotificationStatusDisabled(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE cases SET notification_status = 'disabled', updated_at = ?
		 WHERE notification_status = 'pending'`,
		nowUTC(),
	)
	return err
}

// MarkFailed transitions a running case to state=failed with outcome=engine_error
// and the supplied failure_kind. handoff_status is set to skipped in the same
// SQLite transaction (engine_error cases never fan out).
func (s *Store) MarkFailed(ctx context.Context, caseID, failureKind string) error {
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		now := nowUTC()
		if _, err := tx.ExecContext(ctx, `
			UPDATE cases
			   SET state          = 'failed',
			       outcome        = 'engine_error',
			       failure_kind   = ?,
			       handoff_status = 'skipped',
			       updated_at     = ?
			 WHERE case_id = ? AND state = 'running'`,
			failureKind, now, caseID,
		); err != nil {
			return fmt.Errorf("mark failed update: %w", err)
		}
		payload, _ := json.Marshal(map[string]string{
			"outcome":      "engine_error",
			"failure_kind": failureKind,
		})
		return appendEventTx(ctx, tx, caseID, ptr(StateRunning), ptr(StateFailed), "state_transition", payload, now)
	})
	if err != nil {
		return err
	}
	metrics.ObserveCaseStateTransition(StateFailed)
	metrics.ObserveCaseOutcome("engine_error")
	return nil
}
