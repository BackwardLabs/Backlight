package mention

import (
	"testing"

	"github.com/UPside-Lumos-V2/helios/internal/store"
)

func testIndex() *Index {
	return BuildIndex([]store.MentionEntity{
		{
			CanonicalID: "curve", EntityName: "Curve", Aliases: []string{"Curve", "Curve DAO"},
			Slug: "curve", XHandle: "CurveFinance", HandleNorm: "curvefinance",
			MentionPolicy: "allow", Status: "active",
		},
		{
			CanonicalID: "binance", EntityName: "Binance", Aliases: []string{"Binance", "Binance Staked SOL"},
			XHandle: "BNBCHAIN", HandleNorm: "bnbchain", MentionPolicy: "suppress", Status: "active",
		},
		{
			CanonicalID: "ipor", EntityName: "IPOR", Aliases: []string{"IPOR"},
			Slug: "ipor", XHandle: "ipor_io", HandleNorm: "ipor_io", MentionPolicy: "allow", Status: "active",
		},
		// two distinct entities share the normalized name "apollo" -> ambiguous
		{CanonicalID: "apollo1", EntityName: "Apollo", Aliases: []string{"Apollo"}, XHandle: "ApolloDAO", MentionPolicy: "allow", Status: "active"},
		{CanonicalID: "apollo2", EntityName: "Apollo", Aliases: []string{"Apollo"}, XHandle: "apollocrypto", MentionPolicy: "allow", Status: "active"},
		// shared-handle collision flagged review
		{CanonicalID: "fraxA", EntityName: "Frax A", Aliases: []string{"Frax A"}, XHandle: "fraxfinance", MentionPolicy: "review", Status: "active"},
		// allow but no handle
		{CanonicalID: "nohandle", EntityName: "Handleless", Aliases: []string{"Handleless"}, XHandle: "", MentionPolicy: "allow", Status: "active"},
		// token-only match
		{CanonicalID: "wow", EntityName: "Wonder World", Aliases: []string{"Wonder World"}, TokenSymbol: "WOW", XHandle: "wonderworld", MentionPolicy: "allow", Status: "active"},
	})
}

func TestResolveTagsExactAndAlias(t *testing.T) {
	ix := testIndex()
	for _, name := range []string{"Curve", "curve", "  Curve DAO ", "@Curve"} {
		d := ix.Resolve(name)
		if !d.ShouldTag || d.Mention() != "@CurveFinance" {
			t.Errorf("Resolve(%q) = %+v, want tag @CurveFinance", name, d)
		}
	}
}

func TestResolveSuffixStrippingBoostsCoverage(t *testing.T) {
	ix := testIndex()
	// engine emits "IPOR Protocol"; store has "IPOR" -> strip "protocol" and tag
	d := ix.Resolve("IPOR Protocol")
	if !d.ShouldTag || d.Mention() != "@ipor_io" {
		t.Errorf("Resolve(IPOR Protocol) = %+v, want tag @ipor_io", d)
	}
	// "Curve Protocol" -> strip -> curve
	if d := ix.Resolve("Curve Protocol"); !d.ShouldTag || d.CanonicalID != "curve" {
		t.Errorf("Resolve(Curve Protocol) = %+v, want curve", d)
	}
}

func TestResolveSuppressedNeverTags(t *testing.T) {
	ix := testIndex()
	d := ix.Resolve("Binance")
	if d.ShouldTag || d.Mention() != "" || d.Reason != "suppress" {
		t.Errorf("Resolve(Binance) = %+v, want suppress/no-tag", d)
	}
	// the product variant alias must also stay suppressed
	if d := ix.Resolve("Binance Staked SOL"); d.ShouldTag {
		t.Errorf("Resolve(Binance Staked SOL) tagged, want suppressed")
	}
}

func TestResolveAmbiguousFallsBackToPlainText(t *testing.T) {
	ix := testIndex()
	d := ix.Resolve("Apollo")
	if d.ShouldTag || d.Reason != "ambiguous" {
		t.Errorf("Resolve(Apollo) = %+v, want ambiguous/no-tag", d)
	}
	if d.EntityName != "Apollo" {
		t.Errorf("ambiguous should preserve raw name, got %q", d.EntityName)
	}
}

func TestResolveReviewAndNoHandleAndMiss(t *testing.T) {
	ix := testIndex()
	if d := ix.Resolve("Frax A"); d.ShouldTag || d.Reason != "review" {
		t.Errorf("Resolve(Frax A) = %+v, want review/no-tag", d)
	}
	if d := ix.Resolve("Handleless"); d.ShouldTag || d.Reason != "no_handle" {
		t.Errorf("Resolve(Handleless) = %+v, want no_handle", d)
	}
	if d := ix.Resolve("Totally Unknown XYZ"); d.ShouldTag || d.Reason != "miss" {
		t.Errorf("Resolve(unknown) = %+v, want miss", d)
	}
}

func TestFormatTag(t *testing.T) {
	ix := testIndex()
	if got := FormatTag(ix, "Curve"); got != "Curve (@CurveFinance)" {
		t.Errorf("FormatTag tagged = %q", got)
	}
	if got := FormatTag(ix, "Binance"); got != "Binance" {
		t.Errorf("FormatTag suppressed should be plain, got %q", got)
	}
	if got := FormatTag(ix, "Totally Unknown XYZ"); got != "Totally Unknown XYZ" {
		t.Errorf("FormatTag miss should be plain, got %q", got)
	}
	if got := FormatTag(nil, "Curve"); got != "Curve" {
		t.Errorf("FormatTag nil index (disabled) should be plain, got %q", got)
	}
}

func TestResolveTokenFallback(t *testing.T) {
	ix := testIndex()
	d := ix.Resolve("WOW")
	if !d.ShouldTag || d.CanonicalID != "wow" {
		t.Errorf("Resolve(WOW) = %+v, want token match wow", d)
	}
}
