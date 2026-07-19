package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// MentionEntity is one protocol/entity row in the protocol_mention_store.
// It is seeded from the Surf export and owned/updated by Backlight thereafter.
// x_id is the immutable numeric Twitter user id and is the anchor; x_handle is
// the mutable display handle. Empty strings denote "unknown/missing".
type MentionEntity struct {
	CanonicalID    string
	XID            string
	XHandle        string
	HandleNorm     string
	EntityName     string
	Aliases        []string // all distinct names Surf grouped under this canonical_id
	EntityType     string
	Slug           string
	TokenSymbol    string
	CategoryTags   string
	XDisplayName   string
	XFollowers     int64
	XStatus        string
	Website        string
	MentionPolicy  string // allow | suppress | review
	Source         string // surf_seed | drift_repair | manual | backfill
	PulledAt       string
	LastVerifiedAt string
	ReverifyDueAt  string
	Status         string // active | stale | needs_resolution
}

// MentionOverride is a manual/derived correction guarding against mistags
// (personal accounts, parent accounts, shared-handle collisions).
type MentionOverride struct {
	CanonicalID string
	Reason      string // personal_account | parent_account | shared_handle | manual
	Action      string // suppress | force_handle:@xxx
	Note        string
}

// MentionStoreStats is a coverage snapshot used to verify a seed run.
type MentionStoreStats struct {
	Total          int
	WithHandle     int
	WithXID        int
	PolicyAllow    int
	PolicySuppress int
	PolicyReview   int
	Overrides      int
}

// MentionVerification records the result of checking a mention candidate
// against X immediately before composing a post. CanonicalID is empty when the
// protocol was not present in the local mention store and X MCP discovered it.
type MentionVerification struct {
	CanonicalID   string
	EntityName    string
	XID           string
	XHandle       string
	XDisplayName  string
	XFollowers    int64
	XStatus       string
	Website       string
	Source        string
	VerifiedAt    string
	ReverifyDueAt string
	Outcome       string // verified | replaced | unresolved
}

func aliasesJSON(a []string) string {
	if len(a) == 0 {
		return "[]"
	}
	b, err := json.Marshal(a)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func (e MentionEntity) withDefaults() MentionEntity {
	if e.MentionPolicy == "" {
		e.MentionPolicy = "allow"
	}
	if e.Source == "" {
		e.Source = "surf_seed"
	}
	if e.Status == "" {
		e.Status = "active"
	}
	return e
}

// UpsertMentionEntities upserts a batch of entities in a single transaction.
// created_at is preserved across upserts; updated_at always advances.
func (s *Store) UpsertMentionEntities(ctx context.Context, ents []MentionEntity) error {
	if len(ents) == 0 {
		return nil
	}
	now := nowUTC()
	return s.Tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO protocol_mention_store (
				canonical_id, x_id, x_handle, handle_norm, entity_name, aliases, entity_type,
				slug, token_symbol, category_tags, x_display_name, x_followers, x_status,
				website, mention_policy, source, pulled_at, last_verified_at,
				reverify_due_at, status, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(canonical_id) DO UPDATE SET
				x_id = excluded.x_id,
				x_handle = excluded.x_handle,
				handle_norm = excluded.handle_norm,
				entity_name = excluded.entity_name,
				aliases = excluded.aliases,
				entity_type = excluded.entity_type,
				slug = excluded.slug,
				token_symbol = excluded.token_symbol,
				category_tags = excluded.category_tags,
				x_display_name = excluded.x_display_name,
				x_followers = excluded.x_followers,
				x_status = excluded.x_status,
				website = excluded.website,
				mention_policy = excluded.mention_policy,
				source = excluded.source,
				pulled_at = excluded.pulled_at,
				last_verified_at = excluded.last_verified_at,
				reverify_due_at = excluded.reverify_due_at,
				status = excluded.status,
				updated_at = excluded.updated_at`)
		if err != nil {
			return fmt.Errorf("prepare mention upsert: %w", err)
		}
		defer stmt.Close()
		for _, raw := range ents {
			e := raw.withDefaults()
			if e.CanonicalID == "" {
				return fmt.Errorf("mention entity has empty canonical_id (entity_name=%q)", e.EntityName)
			}
			if _, err := stmt.ExecContext(ctx,
				e.CanonicalID, e.XID, e.XHandle, e.HandleNorm, e.EntityName, aliasesJSON(e.Aliases), e.EntityType,
				e.Slug, e.TokenSymbol, e.CategoryTags, e.XDisplayName, e.XFollowers, e.XStatus,
				e.Website, e.MentionPolicy, e.Source, e.PulledAt, e.LastVerifiedAt,
				e.ReverifyDueAt, e.Status, now, now,
			); err != nil {
				return fmt.Errorf("upsert mention entity %q: %w", e.CanonicalID, err)
			}
		}
		return nil
	})
}

// UpsertMentionOverrides upserts override rows keyed by (canonical_id, reason).
func (s *Store) UpsertMentionOverrides(ctx context.Context, ovs []MentionOverride) error {
	if len(ovs) == 0 {
		return nil
	}
	now := nowUTC()
	return s.Tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO mention_override (
				canonical_id, reason, action, note, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(canonical_id, reason) DO UPDATE SET
				action = excluded.action,
				note = excluded.note,
				updated_at = excluded.updated_at`)
		if err != nil {
			return fmt.Errorf("prepare override upsert: %w", err)
		}
		defer stmt.Close()
		for _, o := range ovs {
			if o.CanonicalID == "" || o.Reason == "" || o.Action == "" {
				return fmt.Errorf("override missing required field: %+v", o)
			}
			if _, err := stmt.ExecContext(ctx,
				o.CanonicalID, o.Reason, o.Action, o.Note, now, now,
			); err != nil {
				return fmt.Errorf("upsert override %q/%q: %w", o.CanonicalID, o.Reason, err)
			}
		}
		return nil
	})
}

// SetMentionPolicy sets mention_policy for the given canonical_ids.
func (s *Store) SetMentionPolicy(ctx context.Context, canonicalIDs []string, policy string) error {
	if len(canonicalIDs) == 0 {
		return nil
	}
	now := nowUTC()
	return s.Tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			UPDATE protocol_mention_store
			SET mention_policy = ?, updated_at = ?
			WHERE canonical_id = ?`)
		if err != nil {
			return fmt.Errorf("prepare policy update: %w", err)
		}
		defer stmt.Close()
		for _, id := range canonicalIDs {
			if _, err := stmt.ExecContext(ctx, policy, now, id); err != nil {
				return fmt.Errorf("set policy for %q: %w", id, err)
			}
		}
		return nil
	})
}

// RecordMentionVerification updates only the mutable X identity fields for an
// existing entity. For a newly discovered protocol it reuses an existing row
// with the same immutable X user id, or creates an x_mcp:<id> entity. Manual
// mention policy is deliberately preserved and unresolved checks never erase a
// previously known handle.
func (s *Store) RecordMentionVerification(ctx context.Context, raw MentionVerification) error {
	v := raw
	v.CanonicalID = strings.TrimSpace(v.CanonicalID)
	v.EntityName = strings.TrimSpace(v.EntityName)
	v.XID = strings.TrimSpace(v.XID)
	v.XHandle = strings.TrimLeft(strings.TrimSpace(v.XHandle), "@")
	v.XDisplayName = strings.TrimSpace(v.XDisplayName)
	v.Website = strings.TrimSpace(v.Website)
	v.Source = strings.TrimSpace(v.Source)
	v.Outcome = strings.TrimSpace(v.Outcome)
	if v.Source == "" {
		v.Source = "x_mcp"
	}
	if v.VerifiedAt == "" {
		v.VerifiedAt = nowUTC()
	}
	if v.Outcome != "verified" && v.Outcome != "replaced" && v.Outcome != "unresolved" {
		return fmt.Errorf("invalid mention verification outcome %q", v.Outcome)
	}
	if v.CanonicalID == "" && v.Outcome == "unresolved" {
		return nil
	}
	if v.CanonicalID == "" && (v.XID == "" || v.XHandle == "" || v.EntityName == "") {
		return fmt.Errorf("new mention verification requires entity_name, x_id, and x_handle")
	}

	return s.Tx(ctx, func(tx *sql.Tx) error {
		canonicalID := v.CanonicalID
		discoveredExisting := false
		if canonicalID == "" {
			if err := tx.QueryRowContext(ctx,
				`SELECT canonical_id FROM protocol_mention_store WHERE x_id = ? ORDER BY updated_at DESC LIMIT 1`,
				v.XID,
			).Scan(&canonicalID); err == nil {
				discoveredExisting = true
			}
		}

		now := nowUTC()
		if canonicalID != "" {
			if v.Outcome == "unresolved" {
				_, err := tx.ExecContext(ctx, `
					UPDATE protocol_mention_store
					SET x_status = 'unresolved', status = 'needs_resolution',
					    pulled_at = ?, reverify_due_at = ?, updated_at = ?
					WHERE canonical_id = ?`,
					v.VerifiedAt, v.ReverifyDueAt, now, canonicalID,
				)
				return err
			}
			result, err := tx.ExecContext(ctx, `
				UPDATE protocol_mention_store
				SET x_id = ?, x_handle = ?, handle_norm = ?, x_display_name = ?,
				    x_followers = ?, x_status = ?,
				    website = CASE WHEN ? <> '' THEN ? ELSE website END,
				    source = CASE WHEN ? = 'replaced' THEN 'drift_repair' ELSE source END,
				    last_verified_at = ?, reverify_due_at = ?,
				    status = 'active', updated_at = ?
				WHERE canonical_id = ?`,
				v.XID, v.XHandle, strings.ToLower(v.XHandle), v.XDisplayName,
				v.XFollowers, firstNonEmptyMention(v.XStatus, "active"),
				v.Website, v.Website, v.Outcome, v.VerifiedAt, v.ReverifyDueAt,
				now, canonicalID,
			)
			if err != nil {
				return fmt.Errorf("update mention verification %q: %w", canonicalID, err)
			}
			if affected, _ := result.RowsAffected(); affected == 0 {
				return fmt.Errorf("mention verification canonical_id %q not found", canonicalID)
			}
			if discoveredExisting && v.EntityName != "" {
				if err := appendMentionAlias(ctx, tx, canonicalID, v.EntityName, now); err != nil {
					return err
				}
			}
			return nil
		}

		canonicalID = "x_mcp:" + v.XID
		aliases := aliasesJSON([]string{v.EntityName})
		_, err := tx.ExecContext(ctx, `
			INSERT INTO protocol_mention_store (
				canonical_id, x_id, x_handle, handle_norm, entity_name, aliases,
				x_display_name, x_followers, x_status, website, mention_policy,
				source, pulled_at, last_verified_at, reverify_due_at, status,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'allow', ?, ?, ?, ?, 'active', ?, ?)
			ON CONFLICT(canonical_id) DO UPDATE SET
				x_handle = excluded.x_handle,
				handle_norm = excluded.handle_norm,
				x_display_name = excluded.x_display_name,
				x_followers = excluded.x_followers,
				x_status = excluded.x_status,
				website = excluded.website,
				last_verified_at = excluded.last_verified_at,
				reverify_due_at = excluded.reverify_due_at,
				status = 'active',
				updated_at = excluded.updated_at`,
			canonicalID, v.XID, v.XHandle, strings.ToLower(v.XHandle), v.EntityName,
			aliases, v.XDisplayName, v.XFollowers, firstNonEmptyMention(v.XStatus, "active"),
			v.Website, v.Source, v.VerifiedAt, v.VerifiedAt, v.ReverifyDueAt, now, now,
		)
		if err != nil {
			return fmt.Errorf("insert discovered mention %q: %w", canonicalID, err)
		}
		return nil
	})
}

func appendMentionAlias(ctx context.Context, tx *sql.Tx, canonicalID, alias, now string) error {
	var raw string
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(aliases, '[]') FROM protocol_mention_store WHERE canonical_id = ?`,
		canonicalID,
	).Scan(&raw); err != nil {
		return fmt.Errorf("read aliases for %q: %w", canonicalID, err)
	}
	var aliases []string
	_ = json.Unmarshal([]byte(raw), &aliases)
	for _, existing := range aliases {
		if strings.EqualFold(strings.TrimSpace(existing), strings.TrimSpace(alias)) {
			return nil
		}
	}
	aliases = append(aliases, alias)
	if _, err := tx.ExecContext(ctx,
		`UPDATE protocol_mention_store SET aliases = ?, updated_at = ? WHERE canonical_id = ?`,
		aliasesJSON(aliases), now, canonicalID,
	); err != nil {
		return fmt.Errorf("append mention alias for %q: %w", canonicalID, err)
	}
	return nil
}

func firstNonEmptyMention(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// AllMentionEntities loads every entity (with aliases parsed) for building an
// in-memory resolution index at compose time. The store is read-mostly, so this
// is built once and reused.
func (s *Store) AllMentionEntities(ctx context.Context) ([]MentionEntity, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT canonical_id,
		       COALESCE(x_id, ''), COALESCE(x_handle, ''), COALESCE(handle_norm, ''),
		       entity_name, COALESCE(aliases, '[]'), COALESCE(entity_type, ''),
		       COALESCE(slug, ''), COALESCE(token_symbol, ''), COALESCE(category_tags, ''),
		       COALESCE(x_display_name, ''), x_followers, COALESCE(x_status, ''),
		       COALESCE(website, ''), mention_policy, COALESCE(source, ''),
		       COALESCE(pulled_at, ''), COALESCE(last_verified_at, ''),
		       COALESCE(reverify_due_at, ''), status
		FROM protocol_mention_store`)
	if err != nil {
		return nil, fmt.Errorf("query mention entities: %w", err)
	}
	defer rows.Close()
	var out []MentionEntity
	for rows.Next() {
		var e MentionEntity
		var aliasesRaw string
		if err := rows.Scan(&e.CanonicalID, &e.XID, &e.XHandle, &e.HandleNorm,
			&e.EntityName, &aliasesRaw, &e.EntityType, &e.Slug, &e.TokenSymbol,
			&e.CategoryTags, &e.XDisplayName, &e.XFollowers, &e.XStatus,
			&e.Website, &e.MentionPolicy, &e.Source, &e.PulledAt,
			&e.LastVerifiedAt, &e.ReverifyDueAt, &e.Status); err != nil {
			return nil, fmt.Errorf("scan mention entity: %w", err)
		}
		if aliasesRaw != "" && aliasesRaw != "[]" {
			_ = json.Unmarshal([]byte(aliasesRaw), &e.Aliases)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MentionStoreStats returns coverage counts for verifying a seed run.
func (s *Store) MentionStoreStats(ctx context.Context) (MentionStoreStats, error) {
	var st MentionStoreStats
	row := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COUNT(CASE WHEN handle_norm <> '' THEN 1 END),
			COUNT(CASE WHEN x_id <> '' THEN 1 END),
			COUNT(CASE WHEN mention_policy = 'allow' THEN 1 END),
			COUNT(CASE WHEN mention_policy = 'suppress' THEN 1 END),
			COUNT(CASE WHEN mention_policy = 'review' THEN 1 END)
		FROM protocol_mention_store`)
	if err := row.Scan(&st.Total, &st.WithHandle, &st.WithXID,
		&st.PolicyAllow, &st.PolicySuppress, &st.PolicyReview); err != nil {
		return st, fmt.Errorf("mention store stats: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mention_override`).Scan(&st.Overrides); err != nil {
		return st, fmt.Errorf("override count: %w", err)
	}
	return st, nil
}
