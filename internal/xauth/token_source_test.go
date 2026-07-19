package xauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestTokenSourceCachesAndPersistsRotation(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "x-refresh.json")
	if err := os.WriteFile(tokenFile, []byte(`{"refresh_token":"refresh-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if !strings.HasPrefix(req.Header.Get("Authorization"), "Basic ") {
			t.Fatalf("authorization = %q", req.Header.Get("Authorization"))
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		wantRefresh := "refresh-1"
		if calls == 2 {
			wantRefresh = "refresh-2"
		}
		if got := req.Form.Get("refresh_token"); got != wantRefresh {
			t.Fatalf("refresh token = %q, want %q", got, wantRefresh)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-" + strconv.Itoa(calls),
			"refresh_token": "refresh-" + strconv.Itoa(calls+1),
			"expires_in":    7200,
			"token_type":    "bearer",
			"scope":         "users.read offline.access",
		})
	}))
	defer server.Close()

	source := NewTokenSource(Config{
		APIBase:          server.URL,
		ClientID:         "client-id",
		ClientSecret:     "client-secret",
		RefreshTokenFile: tokenFile,
		Client:           server.Client(),
	})
	first, err := source.AccessToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.AccessToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first != "access-1" || second != first || calls != 1 {
		t.Fatalf("first=%q second=%q calls=%d", first, second, calls)
	}
	source.InvalidateAccessToken(first)
	third, err := source.AccessToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if third != "access-2" || calls != 2 {
		t.Fatalf("third=%q calls=%d", third, calls)
	}
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "refresh-3") {
		t.Fatalf("rotated token not persisted: %s", data)
	}
	info, err := os.Stat(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %v", info.Mode().Perm())
	}
}
