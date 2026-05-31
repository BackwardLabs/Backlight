package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxProtocolSlugLen = 32

func uniqueOutputRootTx(ctx context.Context, tx *sql.Tx, outputRootParent string, c *Case) (string, error) {
	base := incidentOutputSlug(c)
	for i := 1; i <= 10_000; i++ {
		slug := base
		if i > 1 {
			slug = fmt.Sprintf("%s-%d", base, i)
		}
		candidate := filepath.Join(outputRootParent, slug)
		used, err := outputRootUsedTx(ctx, tx, candidate)
		if err != nil {
			return "", err
		}
		if used {
			continue
		}
		exists, err := pathExists(candidate)
		if err != nil {
			return "", err
		}
		if exists {
			continue
		}
		return candidate, nil
	}
	return "", fmt.Errorf("could not allocate output_root for slug %q", base)
}

func outputRootUsedTx(ctx context.Context, tx *sql.Tx, outputRoot string) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM cases WHERE output_root = ? LIMIT 1`, outputRoot).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check output_root collision: %w", err)
	}
	return true, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("check output_root path: %w", err)
}

// IncidentSlug returns the deterministic display slug for a case. Explicit
// signal/user identity fields win over post-run inferred protocol labels.
func IncidentSlug(c *Case) string {
	if slug := incidentSlugFromMetadata(c.Metadata); slug != "" {
		return slug
	}
	return incidentOutputSlug(c)
}

// HasIncidentIdentity reports whether metadata can produce a non-generic
// incident slug at case creation time. API callers use this to avoid creating
// immutable case IDs and output roots with an "unknown" protocol segment.
func HasIncidentIdentity(metadata json.RawMessage) bool {
	if incidentSlugFromMetadata(metadata) != "" {
		return true
	}
	return protocolSlug(protocolFromMetadata(metadata)) != "unknown"
}

func incidentSlugFromMetadata(metadata json.RawMessage) string {
	if len(metadata) == 0 {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(metadata, &decoded); err != nil {
		return ""
	}
	return incidentSlugFromValue(decoded)
}

func incidentSlugFromValue(v any) string {
	x, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	for _, target := range []string{"incidentslug", "displayslug"} {
		if s := stringFromCurrentMapByCompactKey(x, target); s != "" {
			return cleanIncidentSlug(s)
		}
	}
	return ""
}

func stringFromCurrentMapByCompactKey(m map[string]any, target string) string {
	keys := sortedMapKeys(m)
	for _, key := range keys {
		if compactKey(key) != target {
			continue
		}
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func cleanIncidentSlug(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" || strings.Contains(raw, "/") || strings.Contains(raw, "\\") {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	slug := strings.Trim(b.String(), "_")
	if slug == "" || slug == "unknown" || isAddressLike(slug) {
		return ""
	}
	return slug
}

func incidentOutputSlug(c *Case) string {
	if slug := incidentSlugFromMetadata(c.Metadata); slug != "" {
		return slug
	}
	return strings.Join([]string{
		incidentDateToken(c),
		chainAlias(c.Chain),
		protocolSlug(protocolFromMetadata(c.Metadata)),
	}, "_")
}

func incidentDateToken(c *Case) string {
	for _, candidate := range []string{deref(c.DetectedAt), c.CreatedAt} {
		if token := dateToken(candidate); token != "" {
			return token
		}
	}
	return time.Now().UTC().Format("060102")
}

func dateToken(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if len(raw) == 6 && allDigits(raw) {
		return raw
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02", "2006/01/02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC().Format("060102")
		}
	}
	if len(raw) >= len("2006-01-02") {
		prefix := raw[:len("2006-01-02")]
		if t, err := time.Parse("2006-01-02", prefix); err == nil {
			return t.UTC().Format("060102")
		}
	}
	return ""
}

func chainAlias(chain string) string {
	key := compactKey(chain)
	aliases := map[string]string{
		"1":                 "eth",
		"eth":               "eth",
		"ethereum":          "eth",
		"ethereummainnet":   "eth",
		"mainnet":           "eth",
		"56":                "bsc",
		"binance":           "bsc",
		"binancesmartchain": "bsc",
		"bnb":               "bsc",
		"bsc":               "bsc",
		"bscmainnet":        "bsc",
		"137":               "polygon",
		"matic":             "polygon",
		"polygon":           "polygon",
		"polygonpos":        "polygon",
		"42161":             "arb",
		"arb":               "arb",
		"arbitrum":          "arb",
		"arbitrumone":       "arb",
		"10":                "op",
		"op":                "op",
		"optimism":          "op",
		"8453":              "base",
		"base":              "base",
		"43114":             "avax",
		"avalanche":         "avax",
		"avalanchecchain":   "avax",
		"avax":              "avax",
		"250":               "ftm",
		"fantom":            "ftm",
		"ftm":               "ftm",
	}
	if alias, ok := aliases[key]; ok {
		return alias
	}
	return protocolSlug(chain)
}

func protocolFromMetadata(metadata json.RawMessage) string {
	if len(metadata) == 0 {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(metadata, &decoded); err != nil {
		return ""
	}
	return protocolFromValue(decoded)
}

func protocolFromValue(v any) string {
	switch x := v.(type) {
	case map[string]any:
		if s := protocolFromCurrentMap(x); s != "" {
			return s
		}
		for _, key := range sortedMapKeys(x) {
			if s := protocolFromValue(x[key]); s != "" {
				return s
			}
		}
	case []any:
		for _, val := range x {
			if s := protocolFromValue(val); s != "" {
				return s
			}
		}
	}
	return ""
}

func protocolFromCurrentMap(m map[string]any) string {
	// Stable publish/display identity priority at each metadata level:
	// 1. signal/user protocol_name
	// 2. signal/user project_name
	// 3. signal/user display_name
	// 4. fallback protocol/project labels, including post-run inference
	for _, target := range []string{"protocolname", "projectname", "displayname", "protocol", "project"} {
		if s := stringFromCurrentMapByCompactKey(m, target); s != "" {
			return s
		}
	}
	return ""
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func protocolSlug(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "unknown") || isAddressLike(raw) {
		return "unknown"
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(raw) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	slug := strings.Trim(b.String(), "_")
	if slug == "" || slug == "unknown" || isAddressLike(slug) {
		return "unknown"
	}
	if len(slug) > maxProtocolSlugLen {
		slug = strings.TrimRight(slug[:maxProtocolSlugLen], "_")
		if slug == "" {
			return "unknown"
		}
	}
	return slug
}

func isAddressLike(raw string) bool {
	s := strings.TrimSpace(strings.ToLower(raw))
	if !strings.HasPrefix(s, "0x") || len(s) < 6 {
		return false
	}
	for _, r := range s[2:] {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || r == '.' {
			continue
		}
		return false
	}
	return true
}

func compactKey(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
