package mention

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/store"
)

type fakeDirectory struct {
	lookupUser  XUser
	lookupErr   error
	searchUsers []XUser
	searchErr   error
	lookups     int
	searches    int
}

func (f *fakeDirectory) LookupUser(context.Context, string) (XUser, error) {
	f.lookups++
	return f.lookupUser, f.lookupErr
}

func (f *fakeDirectory) SearchUsers(context.Context, string) ([]XUser, error) {
	f.searches++
	return f.searchUsers, f.searchErr
}

type fakeVerificationRecorder struct {
	records []store.MentionVerification
	err     error
}

func (f *fakeVerificationRecorder) RecordMentionVerification(_ context.Context, v store.MentionVerification) error {
	f.records = append(f.records, v)
	return f.err
}

func resolverIndex(lastVerified string) *Index {
	return BuildIndex([]store.MentionEntity{
		{
			CanonicalID: "edel", EntityName: "Edel Finance", Aliases: []string{"Edel", "Edel Finance"},
			Slug: "edel-finance", XID: "42", XHandle: "EdelFinance", Website: "https://edel.finance",
			MentionPolicy: "allow", Status: "active", LastVerifiedAt: lastVerified,
		},
		{
			CanonicalID: "blocked", EntityName: "Blocked Protocol", XID: "7", XHandle: "blocked",
			MentionPolicy: "suppress", Status: "active",
		},
	})
}

func TestVerifiedResolverChecksDBHitAgainstImmutableXID(t *testing.T) {
	now := time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC)
	directory := &fakeDirectory{lookupUser: XUser{
		ID: "42", Username: "EdelFinance", Name: "Edel Finance", Website: "https://edel.finance",
		Followers: 12_000, Status: "active",
	}}
	recorder := &fakeVerificationRecorder{}
	resolver := &VerifiedResolver{Index: resolverIndex(""), Directory: directory, Recorder: recorder, Now: func() time.Time { return now }}

	d := resolver.ResolveContext(context.Background(), "Edel Finance")
	if !d.ShouldTag || d.Mention() != "@EdelFinance" || d.Verification != "verified" {
		t.Fatalf("decision = %+v", d)
	}
	if directory.lookups != 1 || directory.searches != 0 {
		t.Fatalf("calls lookup=%d search=%d", directory.lookups, directory.searches)
	}
	if len(recorder.records) != 1 || recorder.records[0].CanonicalID != "edel" || recorder.records[0].XID != "42" || recorder.records[0].Outcome != "verified" {
		t.Fatalf("records = %+v", recorder.records)
	}
}

func TestVerifiedResolverRepairsRenamedHandleByXID(t *testing.T) {
	directory := &fakeDirectory{
		lookupErr: errors.New("not found"),
		searchUsers: []XUser{{
			ID: "42", Username: "EdelProtocol", Name: "Edel Finance", Website: "https://edel.finance",
		}},
	}
	recorder := &fakeVerificationRecorder{}
	resolver := &VerifiedResolver{Index: resolverIndex(""), Directory: directory, Recorder: recorder}

	d := resolver.ResolveContext(context.Background(), "Edel Finance")
	if !d.ShouldTag || d.Mention() != "@EdelProtocol" || d.Verification != "replaced" {
		t.Fatalf("decision = %+v", d)
	}
	if directory.lookups != 1 || directory.searches != 1 {
		t.Fatalf("calls lookup=%d search=%d", directory.lookups, directory.searches)
	}
	if len(recorder.records) != 1 || recorder.records[0].Outcome != "replaced" || recorder.records[0].XHandle != "EdelProtocol" {
		t.Fatalf("records = %+v", recorder.records)
	}
}

func TestVerifiedResolverSearchesDBMissAndRequiresHighConfidence(t *testing.T) {
	directory := &fakeDirectory{searchUsers: []XUser{{
		ID: "99", Username: "NewProtocol", Name: "New Protocol",
		Description: "The official New Protocol account", Verified: true, Followers: 5_000,
	}}}
	recorder := &fakeVerificationRecorder{}
	resolver := &VerifiedResolver{Index: resolverIndex(""), Directory: directory, Recorder: recorder}

	d := resolver.ResolveContext(context.Background(), "New Protocol")
	if !d.ShouldTag || d.Mention() != "@NewProtocol" || d.Verification != "verified" {
		t.Fatalf("decision = %+v", d)
	}
	if directory.lookups != 0 || directory.searches != 1 {
		t.Fatalf("calls lookup=%d search=%d", directory.lookups, directory.searches)
	}
	if len(recorder.records) != 1 || recorder.records[0].CanonicalID != "" || recorder.records[0].XID != "99" {
		t.Fatalf("records = %+v", recorder.records)
	}
}

func TestVerifiedResolverRejectsUncorroboratedExactNameOnDBMiss(t *testing.T) {
	directory := &fakeDirectory{searchUsers: []XUser{{
		ID: "99", Username: "NewProtocol", Name: "New Protocol",
		Description: "The official New Protocol account",
	}}}
	resolver := &VerifiedResolver{Index: resolverIndex(""), Directory: directory}
	d := resolver.ResolveContext(context.Background(), "New Protocol")
	if d.ShouldTag || d.Verification != "unresolved" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestVerifiedResolverRejectsContradictorySearchWithoutCacheFallback(t *testing.T) {
	now := time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC)
	directory := &fakeDirectory{
		lookupUser:  XUser{ID: "hijacked", Username: "EdelFinance", Name: "Edel Finance"},
		searchUsers: []XUser{{ID: "another", Username: "EdelOfficial", Name: "Edel Finance"}},
	}
	resolver := &VerifiedResolver{
		Index: resolverIndex(now.Add(-time.Hour).Format(time.RFC3339)), Directory: directory,
		CachedMaxAge: 24 * time.Hour, Now: func() time.Time { return now },
	}

	d := resolver.ResolveContext(context.Background(), "Edel Finance")
	if d.ShouldTag || d.Verification != "unresolved" || d.Reason != "x_mcp_unresolved" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestVerifiedResolverDoesNotUseCacheAfterExactIdentityMismatchAndSearchFailure(t *testing.T) {
	now := time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC)
	directory := &fakeDirectory{
		lookupUser: XUser{ID: "hijacked", Username: "EdelFinance", Name: "Edel Finance"},
		searchErr:  errors.New("search unavailable"),
	}
	resolver := &VerifiedResolver{
		Index: resolverIndex(now.Add(-time.Hour).Format(time.RFC3339)), Directory: directory,
		CachedMaxAge: 24 * time.Hour, Now: func() time.Time { return now },
	}
	d := resolver.ResolveContext(context.Background(), "Edel Finance")
	if d.ShouldTag || d.Verification != "unresolved" || d.Reason != "x_mcp_identity_mismatch" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestVerifiedResolverUsesOnlyRecentCacheWhenMCPUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC)
	unavailable := &fakeDirectory{lookupErr: errors.New("bridge down"), searchErr: errors.New("bridge down")}
	recent := &VerifiedResolver{
		Index: resolverIndex(now.Add(-time.Hour).Format(time.RFC3339)), Directory: unavailable,
		CachedMaxAge: 24 * time.Hour, Now: func() time.Time { return now },
	}
	if d := recent.ResolveContext(context.Background(), "Edel Finance"); !d.ShouldTag || d.Verification != "cached" {
		t.Fatalf("recent decision = %+v", d)
	}

	stale := &VerifiedResolver{
		Index:        resolverIndex(now.Add(-48 * time.Hour).Format(time.RFC3339)),
		Directory:    &fakeDirectory{lookupErr: errors.New("bridge down"), searchErr: errors.New("bridge down")},
		CachedMaxAge: 24 * time.Hour, Now: func() time.Time { return now },
	}
	if d := stale.ResolveContext(context.Background(), "Edel Finance"); d.ShouldTag || d.Verification != "unavailable" {
		t.Fatalf("stale decision = %+v", d)
	}
}

func TestVerifiedResolverPreservesHardSuppressionWithoutMCPCall(t *testing.T) {
	directory := &fakeDirectory{}
	resolver := &VerifiedResolver{Index: resolverIndex(""), Directory: directory}
	d := resolver.ResolveContext(context.Background(), "Blocked Protocol")
	if d.ShouldTag || d.Reason != "suppress" {
		t.Fatalf("decision = %+v", d)
	}
	if directory.lookups != 0 || directory.searches != 0 {
		t.Fatalf("suppressed account called MCP lookup=%d search=%d", directory.lookups, directory.searches)
	}
}
