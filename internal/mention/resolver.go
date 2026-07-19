package mention

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/UPside-Lumos-V2/helios/internal/store"
)

// XUser is the small, transport-independent identity surface needed to decide
// whether an X account belongs to a protocol.
type XUser struct {
	ID          string
	Username    string
	Name        string
	Description string
	Website     string
	Followers   int64
	Verified    bool
	Status      string
}

// Directory is implemented by the X MCP client and faked by resolver tests.
type Directory interface {
	LookupUser(context.Context, string) (XUser, error)
	SearchUsers(context.Context, string) ([]XUser, error)
}

// VerificationRecorder persists the latest identity check without coupling the
// resolver to a concrete Store.
type VerificationRecorder interface {
	RecordMentionVerification(context.Context, store.MentionVerification) error
}

// VerifiedResolver treats the local DB as a candidate cache and X MCP as the
// current identity source. Hard policy gates (suppress/review) always win.
type VerifiedResolver struct {
	Index         *Index
	Directory     Directory
	Recorder      VerificationRecorder
	Timeout       time.Duration
	CachedMaxAge  time.Duration
	ReverifyAfter time.Duration
	Now           func() time.Time
	Logger        *slog.Logger
}

func (r *VerifiedResolver) ResolveContext(ctx context.Context, raw string) Decision {
	if r == nil || r.Index == nil {
		return Decision{EntityName: raw, Reason: "disabled", Verification: "unavailable"}
	}
	base := r.Index.Resolve(raw)
	base.Verification = "unresolved"
	if base.Reason == "empty" || base.Reason == "ambiguous" || base.Reason == "suppress" || base.Reason == "review" {
		return base
	}
	if r.Directory == nil {
		return r.localFallback(base, raw, errors.New("x mcp directory is not configured"))
	}

	lookupCtx := ctx
	cancel := func() {}
	if timeout := r.timeout(); timeout > 0 {
		lookupCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	entity, hasEntity := r.Index.Entity(base.CanonicalID)
	var lookupErr error
	contradictoryLookup := false
	if hasEntity && strings.TrimSpace(entity.XHandle) != "" {
		user, err := r.Directory.LookupUser(lookupCtx, entity.XHandle)
		if err == nil && user.ID != "" && r.acceptExact(user, entity, raw) {
			return r.verifiedDecision(lookupCtx, base, entity, user, "verified")
		}
		if err == nil && user.ID != "" {
			contradictoryLookup = true
		}
		lookupErr = err
	}

	users, searchErr := r.Directory.SearchUsers(lookupCtx, raw)
	if searchErr == nil {
		if user, ok := chooseUser(users, entity, raw); ok {
			outcome := "verified"
			if hasEntity && !strings.EqualFold(strings.TrimLeft(entity.XHandle, "@"), strings.TrimLeft(user.Username, "@")) {
				outcome = "replaced"
			}
			return r.verifiedDecision(lookupCtx, base, entity, user, outcome)
		}
		r.record(lookupCtx, store.MentionVerification{
			CanonicalID:   base.CanonicalID,
			EntityName:    raw,
			VerifiedAt:    r.now().Format(time.RFC3339Nano),
			ReverifyDueAt: r.now().Add(r.reverifyAfter()).Format(time.RFC3339Nano),
			Outcome:       "unresolved",
		})
		base.ShouldTag = false
		base.Handle = ""
		base.Reason = "x_mcp_unresolved"
		base.Verification = "unresolved"
		return base
	}
	if contradictoryLookup {
		r.record(lookupCtx, store.MentionVerification{
			CanonicalID:   base.CanonicalID,
			EntityName:    raw,
			VerifiedAt:    r.now().Format(time.RFC3339Nano),
			ReverifyDueAt: r.now().Add(r.reverifyAfter()).Format(time.RFC3339Nano),
			Outcome:       "unresolved",
		})
		base.ShouldTag = false
		base.Handle = ""
		base.Reason = "x_mcp_identity_mismatch"
		base.Verification = "unresolved"
		return base
	}

	if lookupErr != nil {
		searchErr = errors.Join(lookupErr, searchErr)
	}
	return r.localFallback(base, raw, searchErr)
}

func (r *VerifiedResolver) verifiedDecision(ctx context.Context, base Decision, entity store.MentionEntity, user XUser, outcome string) Decision {
	now := r.now()
	canonicalID := base.CanonicalID
	r.record(ctx, store.MentionVerification{
		CanonicalID:   canonicalID,
		EntityName:    firstTextMention(entity.EntityName, base.EntityName),
		XID:           user.ID,
		XHandle:       user.Username,
		XDisplayName:  user.Name,
		XFollowers:    user.Followers,
		XStatus:       firstTextMention(user.Status, "active"),
		Website:       user.Website,
		Source:        "x_mcp",
		VerifiedAt:    now.Format(time.RFC3339Nano),
		ReverifyDueAt: now.Add(r.reverifyAfter()).Format(time.RFC3339Nano),
		Outcome:       outcome,
	})
	base.XID = user.ID
	base.Handle = strings.TrimLeft(strings.TrimSpace(user.Username), "@")
	base.ShouldTag = base.Handle != ""
	base.Reason = "x_mcp_" + outcome
	base.Verification = outcome
	return base
}

func (r *VerifiedResolver) localFallback(base Decision, raw string, err error) Decision {
	entity, ok := r.Index.Entity(base.CanonicalID)
	if base.ShouldTag && ok && recentlyVerified(entity.LastVerifiedAt, r.now(), r.CachedMaxAge) {
		base.Reason = "x_mcp_unavailable_recent_cache"
		base.Verification = "cached"
		r.log("warn", "x mention verification unavailable; using recent cache", raw, base, err)
		return base
	}
	base.ShouldTag = false
	base.Handle = ""
	base.Reason = "x_mcp_unavailable"
	base.Verification = "unavailable"
	r.log("warn", "x mention verification unavailable; omitting tag", raw, base, err)
	return base
}

func (r *VerifiedResolver) acceptExact(user XUser, entity store.MentionEntity, raw string) bool {
	if entity.XID != "" {
		return user.ID == entity.XID
	}
	return userScore(user, entity, raw) >= 7 && corroboratedUser(user, entity)
}

func chooseUser(users []XUser, entity store.MentionEntity, raw string) (XUser, bool) {
	if entity.XID != "" {
		for _, user := range users {
			if user.ID == entity.XID && strings.TrimSpace(user.Username) != "" {
				return user, true
			}
		}
		return XUser{}, false
	}
	bestScore, secondScore := -1, -1
	var best XUser
	for _, user := range users {
		if strings.TrimSpace(user.ID) == "" || strings.TrimSpace(user.Username) == "" {
			continue
		}
		score := userScore(user, entity, raw)
		if score > bestScore {
			secondScore = bestScore
			bestScore = score
			best = user
		} else if score > secondScore {
			secondScore = score
		}
	}
	return best, bestScore >= 7 && corroboratedUser(best, entity) && (secondScore < 0 || bestScore-secondScore >= 2)
}

func corroboratedUser(user XUser, entity store.MentionEntity) bool {
	return sameWebsite(entity.Website, user.Website) || user.Verified || user.Followers >= 1_000
}

func userScore(user XUser, entity store.MentionEntity, raw string) int {
	score := 0
	if entity.XID != "" && user.ID == entity.XID {
		return 100
	}
	if entity.XHandle != "" && strings.EqualFold(strings.TrimLeft(entity.XHandle, "@"), strings.TrimLeft(user.Username, "@")) {
		score += 4
	}
	wantedNames := []string{raw, entity.EntityName, entity.Slug}
	userName := normalizeIdentityText(user.Name)
	for _, wanted := range wantedNames {
		if n := normalizeIdentityText(wanted); n != "" && n == userName {
			score += 3
			break
		}
	}
	if identityTokensMatch(raw, user.Name+" "+user.Description) {
		score += 2
	}
	if n := normalizeIdentityText(raw); n != "" && strings.Contains(normalizeIdentityText(user.Description), n) {
		score += 2
	}
	if sameWebsite(entity.Website, user.Website) {
		score += 5
	}
	if user.Verified {
		score++
	}
	if user.Followers >= 1_000 {
		score++
	}
	return score
}

func identityTokensMatch(want, haystack string) bool {
	wantTokens := identityTokens(want)
	if len(wantTokens) == 0 {
		return false
	}
	hay := " " + normalizeIdentityText(haystack) + " "
	matched := 0
	for _, token := range wantTokens {
		if len(token) <= 2 {
			continue
		}
		if strings.Contains(hay, " "+token+" ") {
			matched++
		}
	}
	return matched > 0 && matched == len(filterIdentityTokens(wantTokens))
}

func identityTokens(value string) []string {
	return filterIdentityTokens(strings.Fields(normalizeIdentityText(value)))
}

func filterIdentityTokens(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if len(token) <= 2 || token == "protocol" || token == "finance" || token == "network" || token == "official" {
			continue
		}
		out = append(out, token)
	}
	return out
}

func normalizeIdentityText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.Join(strings.FieldsFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

func sameWebsite(left, right string) bool {
	a, b := websiteHost(left), websiteHost(right)
	return a != "" && b != "" && a == b
}

func websiteHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
	return host
}

func recentlyVerified(value string, now time.Time, maxAge time.Duration) bool {
	if maxAge <= 0 || strings.TrimSpace(value) == "" {
		return false
	}
	verified, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		verified, err = time.Parse(time.RFC3339, value)
	}
	return err == nil && !verified.After(now) && now.Sub(verified) <= maxAge
}

func (r *VerifiedResolver) record(ctx context.Context, v store.MentionVerification) {
	if r.Recorder == nil {
		return
	}
	if err := r.Recorder.RecordMentionVerification(ctx, v); err != nil {
		r.log("warn", "record x mention verification failed", v.EntityName, Decision{CanonicalID: v.CanonicalID, XID: v.XID, Handle: v.XHandle, Verification: v.Outcome}, err)
	}
}

func (r *VerifiedResolver) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return 15 * time.Second
}

func (r *VerifiedResolver) reverifyAfter() time.Duration {
	if r.ReverifyAfter > 0 {
		return r.ReverifyAfter
	}
	return 24 * time.Hour
}

func (r *VerifiedResolver) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *VerifiedResolver) log(level, message, raw string, decision Decision, err error) {
	if r.Logger == nil {
		return
	}
	args := []any{"protocol", raw, "canonical_id", decision.CanonicalID, "x_id", decision.XID, "handle", decision.Handle, "verification", decision.Verification}
	if err != nil {
		args = append(args, "err", err)
	}
	if level == "warn" {
		r.Logger.Warn(message, args...)
	} else {
		r.Logger.Info(message, args...)
	}
}

func firstTextMention(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
