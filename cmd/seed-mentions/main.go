// Command seed-mentions seeds the protocol_mention_store from a Surf export CSV.
//
// It is an offline ops tool (not part of the hot path): it loads our own owned
// store from the Surf seed, normalizes missing values (the export encodes
// missing as the literal "없음"), anchors on the immutable numeric x_id, and
// installs mistag guards — shared-handle collisions are flagged `review`, and a
// small curated landmine list (personal/parent accounts) is `suppress`ed.
//
// Usage (from the repo root):
//
//	BACKLIGHT_DB_PATH=/path/var/backlight.db go run ./cmd/seed-mentions
//
// SURF_EXPORT_CSV overrides the seed file (default: the committed
// seeds/surf-full-x-account-db.csv), so an operator only needs to point at the
// service's DB.
package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/UPside-Lumos-V2/helios/internal/store"
)

// curatedLandmines maps a normalized handle to a suppression reason. These are
// accounts that must never be tagged in a hack post even though the export maps
// a victim name to them (personal accounts, parent/chain accounts).
var curatedLandmines = map[string]string{
	"jmilei":   "personal_account", // Libra -> @JMilei (Javier Milei, politician)
	"bnbchain": "parent_account",   // Binance / Binance Staked SOL -> @BNBCHAIN
}

type config struct {
	DBPath  string
	CSVPath string
}

// defaultSurfCSV is the committed seed shipped in the repo, so seeding needs no
// separate data hand-off — only BACKLIGHT_DB_PATH. Resolved relative to the working
// directory (run from the repo root).
const defaultSurfCSV = "seeds/surf-full-x-account-db.csv"

func loadConfig() (*config, error) {
	cfg := &config{
		DBPath:  os.Getenv("BACKLIGHT_DB_PATH"),
		CSVPath: os.Getenv("SURF_EXPORT_CSV"),
	}
	if cfg.DBPath == "" {
		return nil, fmt.Errorf("BACKLIGHT_DB_PATH is required")
	}
	if cfg.CSVPath == "" {
		cfg.CSVPath = defaultSurfCSV
	}
	return cfg, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(2)
	}

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		logger.Error("store open failed", "err", err, "db_path", cfg.DBPath)
		os.Exit(2)
	}
	defer st.Close()

	rawRows, mismatchByCanon, err := parseSurfCSV(cfg.CSVPath)
	if err != nil {
		logger.Error("parse surf csv failed", "err", err, "csv", cfg.CSVPath)
		os.Exit(1)
	}
	// Surf assigns one project_id to a parent + its name variants/products
	// (e.g. Curve/Curve DAO, Binance/Binance Staked SOL) sharing one X account.
	// Collapse to one canonical entity, preserving every name as an alias so the
	// writer can still match on any of them.
	ents := aggregateByCanonical(rawRows)
	logger.Info("parsed surf export", "raw_rows", len(rawRows), "canonical_entities", len(ents))

	if err := st.UpsertMentionEntities(ctx, ents); err != nil {
		logger.Error("seed entities failed", "err", err)
		os.Exit(1)
	}

	// Mistag guards: shared-handle collisions + Surf name-resolution mismatches
	// -> review; curated landmines (personal/parent accounts) -> suppress.
	reviewIDs, overrides := mistagGuards(ents)
	mismatchCanons := make([]string, 0, len(mismatchByCanon))
	for cid := range mismatchByCanon {
		mismatchCanons = append(mismatchCanons, cid)
	}
	sort.Strings(mismatchCanons)
	for _, cid := range mismatchCanons {
		reviewIDs = append(reviewIDs, cid)
		overrides = append(overrides, store.MentionOverride{
			CanonicalID: cid, Reason: "name_resolved_mismatch", Action: "suppress",
			Note: "surf resolved name to: " + mismatchByCanon[cid],
		})
	}
	if err := st.SetMentionPolicy(ctx, reviewIDs, "review"); err != nil {
		logger.Error("flag review failed", "err", err)
		os.Exit(1)
	}
	curatedReasons := map[string]bool{"personal_account": true, "parent_account": true}
	suppressIDs := make([]string, 0)
	for _, o := range overrides {
		if curatedReasons[o.Reason] {
			suppressIDs = append(suppressIDs, o.CanonicalID)
		}
	}
	if err := st.SetMentionPolicy(ctx, suppressIDs, "suppress"); err != nil {
		logger.Error("suppress landmines failed", "err", err)
		os.Exit(1)
	}
	if err := st.UpsertMentionOverrides(ctx, overrides); err != nil {
		logger.Error("seed overrides failed", "err", err)
		os.Exit(1)
	}
	logger.Info("name-resolution mismatches flagged review", "count", len(mismatchCanons))

	stats, err := st.MentionStoreStats(ctx)
	if err != nil {
		logger.Error("stats failed", "err", err)
		os.Exit(1)
	}
	pct := func(n int) string {
		if stats.Total == 0 {
			return "0%"
		}
		return fmt.Sprintf("%.2f%%", 100*float64(n)/float64(stats.Total))
	}
	logger.Info("seed complete",
		"total", stats.Total,
		"with_handle", fmt.Sprintf("%d (%s)", stats.WithHandle, pct(stats.WithHandle)),
		"with_x_id", fmt.Sprintf("%d (%s)", stats.WithXID, pct(stats.WithXID)),
		"policy_allow", stats.PolicyAllow,
		"policy_review", stats.PolicyReview,
		"policy_suppress", stats.PolicySuppress,
		"overrides", stats.Overrides,
	)
}

// parseSurfCSV reads the Surf export into MentionEntity rows. It also returns a
// map of canonical_id -> resolved-to name for rows where Surf fuzzy-resolved the
// entity_name to a DIFFERENT protocol (e.g. "IPOR Protocol" -> IQ Protocol's
// @QHUB_); those are mistag landmines to flag `review`.
func parseSurfCSV(path string) ([]store.MentionEntity, map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open csv: %w", err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // tolerate ragged rows; we access by header index

	header, err := r.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("read header: %w", err)
	}
	if len(header) > 0 {
		header[0] = strings.TrimPrefix(header[0], "\ufeff") // strip UTF-8 BOM
	}
	col := map[string]int{}
	for i, name := range header {
		col[strings.TrimSpace(name)] = i
	}
	required := []string{"entity_name", "x_handle", "x_id"}
	for _, c := range required {
		if _, ok := col[c]; !ok {
			return nil, nil, fmt.Errorf("missing required column %q in header", c)
		}
	}

	var ents []store.MentionEntity
	mismatch := map[string]string{}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read row: %w", err)
		}
		get := func(name string) string {
			idx, ok := col[name]
			if !ok || idx < 0 || idx >= len(rec) {
				return ""
			}
			return cleanField(rec[idx])
		}

		name := get("entity_name")
		if name == "" {
			continue // a row with no entity name is unusable
		}
		handleDisp, handleNorm := normalizeHandle(get("x_handle"))
		e := store.MentionEntity{
			CanonicalID:  canonicalID(get("project_id"), get("fund_id"), get("slug"), name),
			XID:          numericOnly(get("x_id")),
			XHandle:      handleDisp,
			HandleNorm:   handleNorm,
			EntityName:   name,
			EntityType:   get("entity_type"),
			Slug:         strings.ToLower(get("slug")),
			TokenSymbol:  get("token_symbol"),
			CategoryTags: get("category_tags"),
			XDisplayName: get("x_display_name"),
			XFollowers:   parseInt(get("x_followers")),
			XStatus:      get("x_status"),
			Website:      get("website"),
			Source:       "surf_seed",
			PulledAt:     get("data_as_of_utc"),
		}
		if ra := resolvedAsMismatch(name, get("notes")); ra != "" {
			if _, ok := mismatch[e.CanonicalID]; !ok {
				mismatch[e.CanonicalID] = ra
			}
		}
		ents = append(ents, e)
	}
	return ents, mismatch, nil
}

// aggregateByCanonical collapses raw export rows that share a canonical_id into
// one entity. EntityName is the shortest name (the base brand); every distinct
// name is kept in Aliases; other scalar fields take the first non-empty value.
// Input order is preserved for deterministic output.
func aggregateByCanonical(rows []store.MentionEntity) []store.MentionEntity {
	order := make([]string, 0)
	groups := make(map[string][]store.MentionEntity)
	for _, e := range rows {
		if _, ok := groups[e.CanonicalID]; !ok {
			order = append(order, e.CanonicalID)
		}
		groups[e.CanonicalID] = append(groups[e.CanonicalID], e)
	}
	out := make([]store.MentionEntity, 0, len(order))
	for _, cid := range order {
		g := groups[cid]
		merged := g[0]
		nameSet := make(map[string]struct{})
		for _, e := range g {
			if e.EntityName != "" {
				nameSet[e.EntityName] = struct{}{}
			}
			merged = mergeNonEmpty(merged, e)
		}
		names := make([]string, 0, len(nameSet))
		for n := range nameSet {
			names = append(names, n)
		}
		sort.Strings(names)
		merged.EntityName = primaryName(names)
		merged.Aliases = names
		out = append(out, merged)
	}
	return out
}

// mergeNonEmpty fills empty scalar fields of dst from src.
func mergeNonEmpty(dst, src store.MentionEntity) store.MentionEntity {
	if dst.XID == "" {
		dst.XID = src.XID
	}
	if dst.XHandle == "" {
		dst.XHandle = src.XHandle
		dst.HandleNorm = src.HandleNorm
	}
	if dst.EntityType == "" {
		dst.EntityType = src.EntityType
	}
	if dst.Slug == "" {
		dst.Slug = src.Slug
	}
	if dst.TokenSymbol == "" {
		dst.TokenSymbol = src.TokenSymbol
	}
	if dst.CategoryTags == "" {
		dst.CategoryTags = src.CategoryTags
	}
	if dst.XDisplayName == "" {
		dst.XDisplayName = src.XDisplayName
	}
	if dst.XFollowers == 0 {
		dst.XFollowers = src.XFollowers
	}
	if dst.XStatus == "" {
		dst.XStatus = src.XStatus
	}
	if dst.Website == "" {
		dst.Website = src.Website
	}
	if dst.PulledAt == "" {
		dst.PulledAt = src.PulledAt
	}
	return dst
}

// primaryName picks the shortest name (alphabetical tiebreak via pre-sorted input).
func primaryName(sortedNames []string) string {
	if len(sortedNames) == 0 {
		return ""
	}
	best := sortedNames[0]
	for _, n := range sortedNames[1:] {
		if len(n) < len(best) {
			best = n
		}
	}
	return best
}

// mistagGuards returns canonical_ids to flag `review` (shared-handle collisions)
// and the override rows to install (shared_handle + curated landmines).
func mistagGuards(ents []store.MentionEntity) (reviewIDs []string, overrides []store.MentionOverride) {
	byHandle := map[string][]string{}
	for _, e := range ents {
		if e.HandleNorm == "" {
			continue
		}
		byHandle[e.HandleNorm] = append(byHandle[e.HandleNorm], e.CanonicalID)
	}
	for handle, ids := range byHandle {
		if len(ids) < 2 {
			continue
		}
		note := fmt.Sprintf("@%s shared by %d entities", handle, len(ids))
		for _, id := range ids {
			reviewIDs = append(reviewIDs, id)
			overrides = append(overrides, store.MentionOverride{
				CanonicalID: id, Reason: "shared_handle", Action: "suppress", Note: note,
			})
		}
	}
	for _, e := range ents {
		if reason, ok := curatedLandmines[e.HandleNorm]; ok {
			overrides = append(overrides, store.MentionOverride{
				CanonicalID: e.CanonicalID, Reason: reason, Action: "suppress",
				Note: fmt.Sprintf("%s -> @%s", e.EntityName, e.XHandle),
			})
		}
	}
	return reviewIDs, overrides
}

// cleanField trims whitespace and treats the export's "없음" placeholder as empty.
func cleanField(s string) string {
	s = strings.TrimSpace(s)
	if s == "없음" {
		return ""
	}
	return s
}

// normalizeHandle returns (display, normalized) for an x_handle cell. Multi-handle
// cells ("@a;@b") use the first handle. Display drops a leading '@'; normalized is
// additionally lowercased for indexing/collision detection.
func normalizeHandle(s string) (display, norm string) {
	if s == "" {
		return "", ""
	}
	if i := strings.IndexAny(s, ";,"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "@"))
	return s, strings.ToLower(s)
}

// numericOnly returns s if it is all digits, else "" (the immutable x_id anchor).
func numericOnly(s string) string {
	if s == "" {
		return ""
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}

func parseInt(s string) int64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// canonicalID prefers the stable Surf project_id, then fund_id, then a derived
// seed:<slug-or-name> fallback for the rare row missing both.
func canonicalID(projectID, fundID, slug, name string) string {
	if projectID != "" {
		return projectID
	}
	if fundID != "" {
		return fundID
	}
	base := slug
	if base == "" {
		base = name
	}
	return "seed:" + strings.ToLower(strings.ReplaceAll(strings.TrimSpace(base), " ", "-"))
}

var seedGenericSuffix = map[string]bool{"protocol": true, "finance": true, "network": true}

// resolvedAsMismatch detects Surf's `defi_name_resolved_as:X` provenance and
// returns X when it is a DIFFERENT protocol than entity_name (a mistag landmine
// like "IPOR Protocol" -> IQ Protocol). It returns "" for harmless variants of
// the same org (Curve -> Curve DAO, TrueUSD -> True USD), which share a brand
// token or are substrings of each other.
func resolvedAsMismatch(name, notes string) string {
	const key = "defi_name_resolved_as:"
	i := strings.Index(notes, key)
	if i < 0 {
		return ""
	}
	ra := notes[i+len(key):]
	if j := strings.IndexByte(ra, ';'); j >= 0 {
		ra = ra[:j]
	}
	ra = strings.TrimSpace(ra)
	if ra == "" {
		return ""
	}
	na, nb := nrm(name), nrm(ra)
	da, db := strings.ReplaceAll(na, " ", ""), strings.ReplaceAll(nb, " ", "")
	if da == "" || db == "" {
		return ""
	}
	lo, hi := da, db
	if len(hi) < len(lo) {
		lo, hi = hi, lo
	}
	if strings.HasPrefix(hi, lo) {
		// same brand prefix: Curve / Curve DAO, TrueUSD / True USD, Kelp DAO /
		// KelpDAO Restaked ETH. (Prefix only — an internal substring like "raft"
		// in "Metakraft" is coincidental, not a variant.)
		return ""
	}
	tokens := map[string]bool{}
	for _, t := range strings.Fields(stripGen(na)) {
		tokens[t] = true
	}
	for _, t := range strings.Fields(stripGen(nb)) {
		if tokens[t] {
			return "" // shares a brand token -> same org
		}
	}
	return ra
}

func nrm(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

func stripGen(s string) string {
	parts := strings.Fields(s)
	for len(parts) > 1 && seedGenericSuffix[parts[len(parts)-1]] {
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, " ")
}
