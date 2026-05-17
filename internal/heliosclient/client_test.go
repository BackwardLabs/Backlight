package heliosclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/UPside-Lumos-V2/helios/internal/api"
)

func TestListCasesSendsBearerAndQuery(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cases" {
			t.Fatalf("path = %q, want /api/cases", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		q := r.URL.Query()
		if q.Get("limit") != "25" || q.Get("state") != "done" || q.Get("chain") != "ethereum" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		writeJSON(t, w, api.CaseListResponse{
			Items: []api.CaseSummary{{CaseID: "case_1", Chain: "ethereum", State: "done"}},
			Total: 1,
			Limit: 25,
			Order: "created_at DESC",
		})
	}))
	defer ts.Close()

	c, err := New(ts.URL+"/api", "test-token", ts.Client())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.ListCases(context.Background(), ListOptions{Limit: 25, State: "done", Chain: "ethereum"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 || len(resp.Items) != 1 || resp.Items[0].CaseID != "case_1" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestGetCaseRejectsSlash(t *testing.T) {
	c, err := New("http://127.0.0.1:8080", "test-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetCase(context.Background(), "case/1"); err == nil || !strings.Contains(err.Error(), "slash") {
		t.Fatalf("GetCase accepted slash-containing id: %v", err)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatal(err)
	}
}
