// Package mention resolves a raw victim-protocol name (as emitted by the engine
// output) to a decision about whether to @-tag the protocol's official X account
// in a hack post. It is the "맞으면 태그, 애매하면 평문" gate: it tags only when a
// single canonical entity matches with policy=allow; anything ambiguous, missing,
// suppressed, or under review falls back to plain text.
//
// Resolution is an in-memory O(1) map lookup over the protocol_mention_store,
// built once and reused — no network, no DB call on the hot compose path.
package mention

import (
	"strings"

	"github.com/UPside-Lumos-V2/helios/internal/store"
)

// Decision is the result of resolving one raw protocol name.
type Decision struct {
	CanonicalID string
	EntityName  string // store's canonical name when matched, else the raw input
	Handle      string // bare handle (no leading '@') when ShouldTag
	ShouldTag   bool
	Reason      string // tagged | miss | ambiguous | suppress | review | no_handle | inactive | empty
}

// Mention returns the "@handle" token to inject, or "" when the post must stay
// plain text.
func (d Decision) Mention() string {
	if d.ShouldTag && d.Handle != "" {
		return "@" + d.Handle
	}
	return ""
}

// Index is an immutable lookup built from the store's entities.
type Index struct {
	byCanon  map[string]store.MentionEntity
	primary  map[string]map[string]struct{} // normalized name/alias/slug -> canonical set
	stripped map[string]map[string]struct{} // generic-suffix-stripped name -> canonical set
	token    map[string]map[string]struct{} // normalized token symbol -> canonical set
}

// genericSuffixes are dropped when a strict match misses, so "IPOR Protocol"
// (engine) can still reach a stored "IPOR". Kept deliberately small to avoid
// collapsing distinct protocols.
var genericSuffixes = map[string]struct{}{
	"protocol": {},
	"finance":  {},
	"network":  {},
}

// BuildIndex constructs the lookup. Entities sharing a normalized key make that
// key ambiguous (it maps to >1 canonical), which forces plain text.
func BuildIndex(entities []store.MentionEntity) *Index {
	ix := &Index{
		byCanon:  make(map[string]store.MentionEntity, len(entities)),
		primary:  make(map[string]map[string]struct{}),
		stripped: make(map[string]map[string]struct{}),
		token:    make(map[string]map[string]struct{}),
	}
	for _, e := range entities {
		if e.CanonicalID == "" {
			continue
		}
		ix.byCanon[e.CanonicalID] = e
		keys := make([]string, 0, len(e.Aliases)+2)
		keys = append(keys, e.EntityName, e.Slug)
		keys = append(keys, e.Aliases...)
		for _, k := range keys {
			n := normName(k)
			if n == "" {
				continue
			}
			add(ix.primary, n, e.CanonicalID)
			add(ix.stripped, stripSuffix(n), e.CanonicalID)
		}
		if tn := normName(e.TokenSymbol); tn != "" {
			add(ix.token, tn, e.CanonicalID)
		}
	}
	return ix
}

// Resolve maps a raw protocol name to a tag/plain-text decision.
func (ix *Index) Resolve(raw string) Decision {
	n := normName(raw)
	if n == "" {
		return Decision{EntityName: raw, Reason: "empty"}
	}
	if cid, ok := unique(ix.primary[n]); ok {
		return ix.decide(cid, raw)
	}
	if cid, ok := unique(ix.stripped[stripSuffix(n)]); ok {
		return ix.decide(cid, raw)
	}
	if cid, ok := unique(ix.token[n]); ok {
		return ix.decide(cid, raw)
	}
	if len(ix.primary[n]) > 1 || len(ix.stripped[stripSuffix(n)]) > 1 || len(ix.token[n]) > 1 {
		return Decision{EntityName: raw, Reason: "ambiguous"}
	}
	return Decision{EntityName: raw, Reason: "miss"}
}

func (ix *Index) decide(canonicalID, raw string) Decision {
	e := ix.byCanon[canonicalID]
	d := Decision{CanonicalID: canonicalID, EntityName: e.EntityName}
	switch {
	case e.MentionPolicy == "suppress":
		d.Reason = "suppress"
	case e.MentionPolicy == "review":
		d.Reason = "review"
	case e.Status != "active":
		d.Reason = "inactive"
	case e.XHandle == "":
		d.Reason = "no_handle"
	default:
		d.ShouldTag = true
		d.Handle = e.XHandle
		d.Reason = "tagged"
	}
	return d
}

func add(m map[string]map[string]struct{}, key, canonicalID string) {
	set := m[key]
	if set == nil {
		set = make(map[string]struct{})
		m[key] = set
	}
	set[canonicalID] = struct{}{}
}

func unique(set map[string]struct{}) (string, bool) {
	if len(set) != 1 {
		return "", false
	}
	for cid := range set {
		return cid, true
	}
	return "", false
}

// normName lowercases, drops a leading '@'/'$', and collapses whitespace.
func normName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimLeft(s, "@$")
	return strings.Join(strings.Fields(s), " ")
}

// stripSuffix removes trailing generic suffixes (down to a 1-token minimum).
func stripSuffix(norm string) string {
	parts := strings.Fields(norm)
	for len(parts) > 1 {
		if _, ok := genericSuffixes[parts[len(parts)-1]]; !ok {
			break
		}
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, " ")
}
