package main

import (
	"reflect"
	"testing"

	"github.com/UPside-Lumos-V2/helios/internal/store"
)

func TestNormalizeHandle(t *testing.T) {
	cases := []struct {
		in, disp, norm string
	}{
		{"@JMilei", "JMilei", "jmilei"},
		{"LidoFinance", "LidoFinance", "lidofinance"},
		{"@CapApp;@caponsui", "CapApp", "capapp"},
		{"", "", ""},
	}
	for _, c := range cases {
		d, n := normalizeHandle(c.in)
		if d != c.disp || n != c.norm {
			t.Errorf("normalizeHandle(%q) = (%q,%q), want (%q,%q)", c.in, d, n, c.disp, c.norm)
		}
	}
}

func TestNumericOnly(t *testing.T) {
	cases := map[string]string{"4020276615": "4020276615", "12a": "", "": "", "1.5": ""}
	for in, want := range cases {
		if got := numericOnly(in); got != want {
			t.Errorf("numericOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalID(t *testing.T) {
	if got := canonicalID("pid", "fid", "slug", "Name"); got != "pid" {
		t.Errorf("project_id should win, got %q", got)
	}
	if got := canonicalID("", "fid", "slug", "Name"); got != "fid" {
		t.Errorf("fund_id should be next, got %q", got)
	}
	if got := canonicalID("", "", "", "Some Name"); got != "seed:some-name" {
		t.Errorf("fallback from name, got %q", got)
	}
}

func TestAggregateByCanonicalPreservesAliases(t *testing.T) {
	rows := []store.MentionEntity{
		{CanonicalID: "A", EntityName: "Curve DAO", XHandle: "CurveFinance", HandleNorm: "curvefinance"},
		{CanonicalID: "A", EntityName: "Curve", TokenSymbol: "CRV"},
		{CanonicalID: "B", EntityName: "Solo"},
	}
	out := aggregateByCanonical(rows)
	if len(out) != 2 {
		t.Fatalf("got %d entities, want 2", len(out))
	}
	a := out[0]
	if a.CanonicalID != "A" {
		t.Fatalf("order not preserved: first cid = %q", a.CanonicalID)
	}
	if a.EntityName != "Curve" {
		t.Errorf("primary name = %q, want shortest 'Curve'", a.EntityName)
	}
	if !reflect.DeepEqual(a.Aliases, []string{"Curve", "Curve DAO"}) {
		t.Errorf("aliases = %v, want [Curve, Curve DAO]", a.Aliases)
	}
	if a.TokenSymbol != "CRV" {
		t.Errorf("token not merged from second row: %q", a.TokenSymbol)
	}
	if a.HandleNorm != "curvefinance" {
		t.Errorf("handle not merged: %q", a.HandleNorm)
	}
}

func TestResolvedAsMismatch(t *testing.T) {
	// Harmful: a genuinely different protocol -> return the resolved name.
	harmful := []struct{ name, notes, want string }{
		{"IPOR Protocol", "defi_tvl:175;defi_name_resolved_as:IQ Protocol", "IQ Protocol"},
		{"Raft", "defi_name_resolved_as:Metakraft", "Metakraft"},
		{"Pika Protocol", "defi_name_resolved_as:Pine Protocol", "Pine Protocol"},
	}
	for _, c := range harmful {
		if got := resolvedAsMismatch(c.name, c.notes); got != c.want {
			t.Errorf("resolvedAsMismatch(%q) = %q, want %q (harmful)", c.name, got, c.want)
		}
	}
	// Harmless variants of the same org -> "".
	harmless := []struct{ name, notes string }{
		{"Curve", "defi_name_resolved_as:Curve DAO"},
		{"TrueUSD", "defi_name_resolved_as:True USD"},
		{"Marinade", "defi_name_resolved_as:Marinade Staked SOL"},
		{"Kelp DAO", "defi_name_resolved_as:KelpDAO Restaked ETH"},
		{"Anything", "no resolution note here"},
		{"Self", "defi_name_resolved_as:Self"},
	}
	for _, c := range harmless {
		if got := resolvedAsMismatch(c.name, c.notes); got != "" {
			t.Errorf("resolvedAsMismatch(%q) = %q, want \"\" (harmless)", c.name, got)
		}
	}
}

func TestMistagGuards(t *testing.T) {
	// Two DISTINCT canonical entities sharing one handle -> review + shared override.
	// One curated landmine (jmilei) -> personal_account override.
	ents := []store.MentionEntity{
		{CanonicalID: "frax1", EntityName: "Frax Finance", HandleNorm: "fraxfinance", XHandle: "fraxfinance"},
		{CanonicalID: "frax2", EntityName: "Frax Other Org", HandleNorm: "fraxfinance", XHandle: "fraxfinance"},
		{CanonicalID: "libra", EntityName: "Libra", HandleNorm: "jmilei", XHandle: "JMilei"},
		{CanonicalID: "solo", EntityName: "Solo", HandleNorm: "solohandle", XHandle: "solohandle"},
	}
	reviewIDs, overrides := mistagGuards(ents)
	if len(reviewIDs) != 2 {
		t.Errorf("reviewIDs = %v, want 2 (frax1, frax2)", reviewIDs)
	}
	var shared, personal int
	for _, o := range overrides {
		switch o.Reason {
		case "shared_handle":
			shared++
		case "personal_account":
			personal++
		}
	}
	if shared != 2 {
		t.Errorf("shared_handle overrides = %d, want 2", shared)
	}
	if personal != 1 {
		t.Errorf("personal_account overrides = %d, want 1", personal)
	}
}
