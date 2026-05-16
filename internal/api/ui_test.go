package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/UPside-Lumos-V2/helios/internal/config"
)

func TestUIRoutesArePublic(t *testing.T) {
	h := NewServer(&config.Config{APIToken: "secret"}, nil, nil).Handler()

	for _, path := range []string{"/", "/ui"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", path, rr.Code, http.StatusOK)
		}
		if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Fatalf("GET %s content-type = %q, want text/html", path, ct)
		}
		if !strings.Contains(rr.Body.String(), "Helios Console") {
			t.Fatalf("GET %s did not return the Helios Console page", path)
		}
	}
}

func TestProtectedAPIsStillRequireToken(t *testing.T) {
	h := NewServer(&config.Config{APIToken: "secret"}, nil, nil).Handler()

	req := httptest.NewRequest(http.MethodGet, "/cases", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET /cases without token status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}
