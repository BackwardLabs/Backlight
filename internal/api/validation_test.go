package api

import "testing"

func TestValidateSubmission_TxHashRegex(t *testing.T) {
	cases := []struct {
		name    string
		tx      string
		wantErr bool
	}{
		{"64 hex no prefix", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false},
		{"64 hex with 0x prefix", "0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", false},
		{"too short", "0x123", true},
		{"too long", "0x" + repeat("a", 65), true},
		{"non-hex", "0x" + repeat("z", 64), true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &SubmissionRequest{Chain: "ethereum", TxHash: tc.tx}
			errs := validateSubmission(req)
			hasTxErr := false
			for _, e := range errs {
				if e.Field == "tx_hash" {
					hasTxErr = true
					break
				}
			}
			if tc.wantErr && !hasTxErr {
				t.Fatalf("expected tx_hash validation error, got none (errs=%v)", errs)
			}
			if !tc.wantErr && hasTxErr {
				t.Fatalf("did NOT expect tx_hash validation error, got %v", errs)
			}
		})
	}
}

func TestValidateSubmission_RequiredFields(t *testing.T) {
	errs := validateSubmission(&SubmissionRequest{})
	wantFields := map[string]bool{"chain": false, "tx_hash": false}
	for _, e := range errs {
		if _, ok := wantFields[e.Field]; ok {
			wantFields[e.Field] = true
		}
	}
	for f, seen := range wantFields {
		if !seen {
			t.Errorf("expected required-field error for %q, got none (errs=%v)", f, errs)
		}
	}
}

func TestValidateSubmission_DetectedAtISO8601(t *testing.T) {
	good := "2026-05-16T09:00:00Z"
	goodNano := "2026-05-16T09:00:00.123456789Z"
	bad := "yesterday"

	for _, ts := range []string{good, goodNano} {
		errs := validateSubmission(&SubmissionRequest{
			Chain:      "ethereum",
			TxHash:     "0x" + repeat("a", 64),
			DetectedAt: &ts,
		})
		for _, e := range errs {
			if e.Field == "detected_at" {
				t.Errorf("unexpected detected_at error for %q: %v", ts, e)
			}
		}
	}

	errs := validateSubmission(&SubmissionRequest{
		Chain:      "ethereum",
		TxHash:     "0x" + repeat("a", 64),
		DetectedAt: &bad,
	})
	found := false
	for _, e := range errs {
		if e.Field == "detected_at" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected detected_at error for %q, got none (errs=%v)", bad, errs)
	}
}

func TestIsISO8601(t *testing.T) {
	good := []string{"2026-05-16T09:00:00Z", "2026-05-16T09:00:00.5Z", "2026-05-16T09:00:00+09:00"}
	bad := []string{"", "tomorrow", "2026-05-16", "not-a-date"}
	for _, s := range good {
		if !isISO8601(s) {
			t.Errorf("expected ISO8601 true for %q, got false", s)
		}
	}
	for _, s := range bad {
		if isISO8601(s) {
			t.Errorf("expected ISO8601 false for %q, got true", s)
		}
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
