package store

import (
	"context"
	"path/filepath"
	"testing"
)

func newMentionTestStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestUpsertMentionEntitiesAndStats(t *testing.T) {
	ctx := context.Background()
	s := newMentionTestStore(t)

	ents := []MentionEntity{
		{
			CanonicalID: "surf:lido",
			XID:         "1453",
			XHandle:     "LidoFinance",
			HandleNorm:  "lidofinance",
			EntityName:  "Lido",
			EntityType:  "project",
			Slug:        "lido",
			TokenSymbol: "LDO",
			XFollowers:  100,
			PulledAt:    "2026-06-27 06:59:35 UTC",
		},
		{
			// no handle, no x_id -> should not count toward coverage
			CanonicalID: "surf:longtail",
			EntityName:  "Longtail Protocol",
			EntityType:  "project",
		},
	}
	if err := s.UpsertMentionEntities(ctx, ents); err != nil {
		t.Fatalf("upsert entities: %v", err)
	}

	st, err := s.MentionStoreStats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Total != 2 {
		t.Fatalf("Total = %d, want 2", st.Total)
	}
	if st.WithHandle != 1 {
		t.Fatalf("WithHandle = %d, want 1", st.WithHandle)
	}
	if st.WithXID != 1 {
		t.Fatalf("WithXID = %d, want 1", st.WithXID)
	}
	if st.PolicyAllow != 2 {
		t.Fatalf("PolicyAllow = %d, want 2 (default)", st.PolicyAllow)
	}
}

func TestUpsertMentionEntitiesPreservesCreatedAt(t *testing.T) {
	ctx := context.Background()
	s := newMentionTestStore(t)

	e := MentionEntity{CanonicalID: "surf:x", EntityName: "X", XHandle: "xhandle", HandleNorm: "xhandle"}
	if err := s.UpsertMentionEntities(ctx, []MentionEntity{e}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	var created1 string
	if err := s.db.QueryRowContext(ctx, `SELECT created_at FROM protocol_mention_store WHERE canonical_id = ?`, "surf:x").Scan(&created1); err != nil {
		t.Fatalf("read created_at: %v", err)
	}

	// Re-upsert with a changed handle: created_at must be preserved, handle updated.
	e.XHandle = "xhandle2"
	if err := s.UpsertMentionEntities(ctx, []MentionEntity{e}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	var created2, handle string
	if err := s.db.QueryRowContext(ctx, `SELECT created_at, x_handle FROM protocol_mention_store WHERE canonical_id = ?`, "surf:x").Scan(&created2, &handle); err != nil {
		t.Fatalf("read after re-upsert: %v", err)
	}
	if created1 != created2 {
		t.Fatalf("created_at changed on upsert: %q -> %q", created1, created2)
	}
	if handle != "xhandle2" {
		t.Fatalf("handle not updated: got %q", handle)
	}
}

func TestSetMentionPolicyAndOverrides(t *testing.T) {
	ctx := context.Background()
	s := newMentionTestStore(t)

	ents := []MentionEntity{
		{CanonicalID: "surf:libra", EntityName: "Libra", XHandle: "JMilei", HandleNorm: "jmilei", XID: "4020276615"},
		{CanonicalID: "surf:frax1", EntityName: "Frax Finance", XHandle: "fraxfinance", HandleNorm: "fraxfinance"},
		{CanonicalID: "surf:frax2", EntityName: "Frax Ether", XHandle: "fraxfinance", HandleNorm: "fraxfinance"},
	}
	if err := s.UpsertMentionEntities(ctx, ents); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// shared handle -> review; personal account -> suppress
	if err := s.SetMentionPolicy(ctx, []string{"surf:frax1", "surf:frax2"}, "review"); err != nil {
		t.Fatalf("set review: %v", err)
	}
	if err := s.SetMentionPolicy(ctx, []string{"surf:libra"}, "suppress"); err != nil {
		t.Fatalf("set suppress: %v", err)
	}
	if err := s.UpsertMentionOverrides(ctx, []MentionOverride{
		{CanonicalID: "surf:libra", Reason: "personal_account", Action: "suppress", Note: "Javier Milei personal"},
		{CanonicalID: "surf:frax1", Reason: "shared_handle", Action: "suppress", Note: "fraxfinance x3"},
		{CanonicalID: "surf:frax2", Reason: "shared_handle", Action: "suppress", Note: "fraxfinance x3"},
	}); err != nil {
		t.Fatalf("overrides: %v", err)
	}

	st, err := s.MentionStoreStats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.PolicyReview != 2 {
		t.Fatalf("PolicyReview = %d, want 2", st.PolicyReview)
	}
	if st.PolicySuppress != 1 {
		t.Fatalf("PolicySuppress = %d, want 1", st.PolicySuppress)
	}
	if st.PolicyAllow != 0 {
		t.Fatalf("PolicyAllow = %d, want 0", st.PolicyAllow)
	}
	if st.Overrides != 3 {
		t.Fatalf("Overrides = %d, want 3", st.Overrides)
	}

	// Re-upserting an override with same (canonical_id, reason) must not duplicate.
	if err := s.UpsertMentionOverrides(ctx, []MentionOverride{
		{CanonicalID: "surf:libra", Reason: "personal_account", Action: "suppress", Note: "updated"},
	}); err != nil {
		t.Fatalf("override re-upsert: %v", err)
	}
	st2, err := s.MentionStoreStats(ctx)
	if err != nil {
		t.Fatalf("stats2: %v", err)
	}
	if st2.Overrides != 3 {
		t.Fatalf("Overrides after re-upsert = %d, want 3", st2.Overrides)
	}
}

func TestRecordMentionVerificationUpdatesIdentityWithoutOverwritingPolicy(t *testing.T) {
	ctx := context.Background()
	s := newMentionTestStore(t)
	if err := s.UpsertMentionEntities(ctx, []MentionEntity{{
		CanonicalID: "surf:edel", EntityName: "Edel Finance", Aliases: []string{"Edel"},
		XID: "42", XHandle: "EdelFinance", HandleNorm: "edelfinance",
		MentionPolicy: "review", Website: "https://old.example", Status: "active",
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := s.RecordMentionVerification(ctx, MentionVerification{
		CanonicalID: "surf:edel", EntityName: "Edel Finance", XID: "42",
		XHandle: "EdelProtocol", XDisplayName: "Edel Finance", XFollowers: 1234,
		Website: "https://edel.finance", VerifiedAt: "2026-07-19T01:00:00Z",
		ReverifyDueAt: "2026-07-20T01:00:00Z", Outcome: "replaced",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	entities, err := s.AllMentionEntities(ctx)
	if err != nil || len(entities) != 1 {
		t.Fatalf("entities=%+v err=%v", entities, err)
	}
	got := entities[0]
	if got.XID != "42" || got.XHandle != "EdelProtocol" || got.HandleNorm != "edelprotocol" || got.XDisplayName != "Edel Finance" {
		t.Fatalf("identity = %+v", got)
	}
	if got.MentionPolicy != "review" {
		t.Fatalf("mention policy overwritten: %+v", got)
	}
	if got.Website != "https://edel.finance" || got.LastVerifiedAt != "2026-07-19T01:00:00Z" || got.Status != "active" {
		t.Fatalf("verification metadata = %+v", got)
	}
}

func TestRecordMentionVerificationCreatesAndReusesDiscoveredXID(t *testing.T) {
	ctx := context.Background()
	s := newMentionTestStore(t)
	verification := MentionVerification{
		EntityName: "New Protocol", XID: "99", XHandle: "NewProtocol",
		XDisplayName: "New Protocol", XFollowers: 5000, Outcome: "verified",
	}
	if err := s.RecordMentionVerification(ctx, verification); err != nil {
		t.Fatalf("first record: %v", err)
	}
	verification.XHandle = "NewProtocolDAO"
	verification.Outcome = "replaced"
	if err := s.RecordMentionVerification(ctx, verification); err != nil {
		t.Fatalf("second record: %v", err)
	}
	entities, err := s.AllMentionEntities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 1 || entities[0].CanonicalID != "x_mcp:99" || entities[0].XHandle != "NewProtocolDAO" {
		t.Fatalf("entities = %+v", entities)
	}
}

func TestRecordMentionVerificationReusesExistingXIDAndCachesNewAlias(t *testing.T) {
	ctx := context.Background()
	s := newMentionTestStore(t)
	if err := s.UpsertMentionEntities(ctx, []MentionEntity{{
		CanonicalID: "surf:new", EntityName: "New Protocol DAO", Aliases: []string{"New Protocol DAO"},
		XID: "99", XHandle: "NewProtocol", Status: "active",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordMentionVerification(ctx, MentionVerification{
		EntityName: "New Protocol", XID: "99", XHandle: "NewProtocol",
		XDisplayName: "New Protocol", Outcome: "verified",
	}); err != nil {
		t.Fatal(err)
	}
	entities, err := s.AllMentionEntities(ctx)
	if err != nil || len(entities) != 1 {
		t.Fatalf("entities=%+v err=%v", entities, err)
	}
	if entities[0].CanonicalID != "surf:new" || len(entities[0].Aliases) != 2 || entities[0].Aliases[1] != "New Protocol" {
		t.Fatalf("entity = %+v", entities[0])
	}
}

func TestRecordMentionVerificationMarksExistingCandidateUnresolvedWithoutErasingHandle(t *testing.T) {
	ctx := context.Background()
	s := newMentionTestStore(t)
	if err := s.UpsertMentionEntities(ctx, []MentionEntity{{
		CanonicalID: "surf:edel", EntityName: "Edel", XID: "42", XHandle: "EdelFinance", Status: "active",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordMentionVerification(ctx, MentionVerification{
		CanonicalID: "surf:edel", EntityName: "Edel", Outcome: "unresolved",
		VerifiedAt: "2026-07-19T01:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	entities, err := s.AllMentionEntities(ctx)
	if err != nil || len(entities) != 1 {
		t.Fatalf("entities=%+v err=%v", entities, err)
	}
	if entities[0].Status != "needs_resolution" || entities[0].XStatus != "unresolved" || entities[0].XHandle != "EdelFinance" {
		t.Fatalf("entity = %+v", entities[0])
	}
}
