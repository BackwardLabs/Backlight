package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
