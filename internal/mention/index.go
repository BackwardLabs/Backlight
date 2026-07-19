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
	"context"
	"strings"

	"github.com/UPside-Lumos-V2/helios/internal/store"
)

// Decision is the result of resolving one raw protocol name.
type Decision struct {
	CanonicalID  string
	EntityName   string // store's canonical name when matched, else the raw input
	XID          string // immutable X user id when known
	Handle       string // bare handle (no leading '@') when ShouldTag
	ShouldTag    bool
	Reason       string // tagged | miss | ambiguous | suppress | review | no_handle | inactive | empty
	Verification string // static | verified | replaced | cached | unresolved | unavailable
}

// Resolver performs the final, context-aware mention decision. Index implements
// this interface with a local lookup; VerifiedResolver adds an X MCP check.
type Resolver interface {
	ResolveContext(context.Context, string) Decision
}

// Mention returns the "@handle" token to inject, or "" when the post must stay
// plain text.
func (d Decision) Mention() string {
	if d.ShouldTag && d.Handle != "" {
		return "@" + d.Handle
	}
	return ""
}

// FormatTag renders the protocol display string for a post: "Name (@handle)"
// when the raw name resolves to a taggable official account, otherwise the raw
// name unchanged. A nil index (feature disabled) always returns the raw name.
// This is the single source of the mention render format so the xfeed and
// xpublish renderers cannot drift apart.
func FormatTag(ix *Index, rawName string) string {
	if ix == nil {
		return rawName
	}
	if m := ix.Resolve(rawName).Mention(); m != "" {
		return rawName + " (" + m + ")"
	}
	return rawName
}

// FormatTagContext is the context-aware equivalent used by the publishing hot
// path. It renders plain text whenever the resolver cannot prove the account.
func FormatTagContext(ctx context.Context, resolver Resolver, rawName string) (string, Decision) {
	if resolver == nil {
		return rawName, Decision{EntityName: rawName, Reason: "disabled", Verification: "unavailable"}
	}
	d := resolver.ResolveContext(ctx, rawName)
	if m := d.Mention(); m != "" {
		return rawName + " (" + m + ")", d
	}
	return rawName, d
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

// ResolveContext lets the immutable local index satisfy Resolver.
func (ix *Index) ResolveContext(_ context.Context, raw string) Decision {
	d := ix.Resolve(raw)
	d.Verification = "static"
	return d
}

// Entity returns the stored entity behind a resolved canonical id.
func (ix *Index) Entity(canonicalID string) (store.MentionEntity, bool) {
	if ix == nil {
		return store.MentionEntity{}, false
	}
	e, ok := ix.byCanon[canonicalID]
	return e, ok
}

func (ix *Index) decide(canonicalID, raw string) Decision {
	e := ix.byCanon[canonicalID]
	d := Decision{CanonicalID: canonicalID, EntityName: e.EntityName, XID: e.XID}
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
