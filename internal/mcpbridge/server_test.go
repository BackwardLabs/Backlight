package mcpbridge

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestHandoffRequiresBearerWhenConfigured(t *testing.T) {
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := &Server{Store: st, Token: "bridge-token"}

	req := httptest.NewRequest(http.MethodPost, "/handoff", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status without token = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}
