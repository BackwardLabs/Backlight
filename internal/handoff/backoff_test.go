package handoff

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBackoff_ExponentialUpToCeiling(t *testing.T) {
	d := &Dispatcher{
		BackoffBase: 2 * time.Second,
		BackoffMax:  60 * time.Second,
	}
	cases := []struct {
		completedAttempt int
		want             time.Duration
	}{
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 16 * time.Second},
		{5, 32 * time.Second},
		{6, 60 * time.Second}, // would be 64 — capped
		{8, 60 * time.Second}, // capped
	}
	for _, tc := range cases {
		got := d.backoff(tc.completedAttempt)
		if got != tc.want {
			t.Errorf("backoff(%d) = %v, want %v", tc.completedAttempt, got, tc.want)
		}
	}
}

func TestPostOnceSendsBearerToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer bridge-token" {
			t.Fatalf("Authorization = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	d := &Dispatcher{Client: ts.Client(), BearerToken: "bridge-token"}
	status, err := d.postOnce(context.Background(), ts.URL, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", status, http.StatusNoContent)
	}
}

func TestBackoff_ZeroOrNegativeAttempt(t *testing.T) {
	d := &Dispatcher{BackoffBase: time.Second, BackoffMax: time.Minute}
	if got := d.backoff(0); got != time.Second {
		t.Errorf("backoff(0) = %v, want %v", got, time.Second)
	}
	if got := d.backoff(-3); got != time.Second {
		t.Errorf("backoff(-3) = %v, want %v", got, time.Second)
	}
}

func TestClassifyResult(t *testing.T) {
	cases := []struct {
		name   string
		status int
		err    error
		want   string
	}{
		{"2xx success", 200, nil, "success"},
		{"3xx not success", 301, errors.New("downstream returned HTTP 301"), "retryable_failure"},
		{"4xx permanent", 404, errors.New("downstream returned HTTP 404"), "permanent_failure"},
		{"4xx permanent (auth)", 401, errors.New("..."), "permanent_failure"},
		{"5xx retryable", 503, errors.New("..."), "retryable_failure"},
		{"transport error (no status)", 0, errors.New("connection refused"), "retryable_failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyResult(tc.status, tc.err); got != tc.want {
				t.Errorf("classifyResult(%d, %v) = %q, want %q", tc.status, tc.err, got, tc.want)
			}
		})
	}
}
