package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// IncidentSlug returns the display slug for a case. Unlike case_id, this may
// improve after analysis enriches case metadata with a protocol label.
func IncidentSlug(c *Case) string {
	return incidentOutputSlug(c)
}

func incidentOutputSlug(c *Case) string {
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
		for _, key := range []string{"protocol", "protocol_name", "protocolName", "project", "project_name", "projectName"} {
			if val, ok := x[key]; ok {
				if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
					return s
				}
			}
		}
		for key, val := range x {
			normalized := compactKey(key)
			if normalized == "protocol" || normalized == "protocolname" || normalized == "project" || normalized == "projectname" {
				if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
					return s
				}
			}
		}
		for _, val := range x {
			if s := protocolFromValue(val); s != "" {
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
