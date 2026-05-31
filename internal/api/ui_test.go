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
		if !strings.Contains(rr.Body.String(), "Analysis result") {
			t.Fatalf("GET %s did not include the analysis result section", path)
		}
		if !strings.Contains(rr.Body.String(), "Judgment") {
			t.Fatalf("GET %s did not include the judgment section", path)
		}
		if !strings.Contains(rr.Body.String(), "Helios report") {
			t.Fatalf("GET %s did not include the Helios report section", path)
		}
		if !strings.Contains(rr.Body.String(), `data-route="artifacts"`) {
			t.Fatalf("GET %s did not include the artifacts route", path)
		}
		if !strings.Contains(rr.Body.String(), "Artifact preview") {
			t.Fatalf("GET %s did not include the artifact preview section", path)
		}
		body := rr.Body.String()
		for _, marker := range []string{
			`var visibleArtifacts=["PoC.t.sol","REPORT.md","RCA.md"];`,
			"Artifact scheme</b><span>REPORT.md, RCA.md, PoC.t.sol</span>",
			`id="artifactText" class="artifact-preview plain-preview"`,
			"function renderMarkdown(md)",
			"function renderArtifactPreview(path,text)",
			"markdown-preview",
			`id="protocol"`,
			"metadata.protocol=protocol",
			"function loadCases(quiet)",
			"data-artifact",
		} {
			if !strings.Contains(body, marker) {
				t.Fatalf("GET %s missing product artifact UI marker %q", path, marker)
			}
		}
		for _, hidden := range []string{"attack_flow.md", "multi_leg_reconciliation.md"} {
			if strings.Contains(body, hidden) {
				t.Fatalf("GET %s unexpectedly exposes hidden artifact %q", path, hidden)
			}
		}
		if strings.Contains(body, `onclick="readArtifact`) {
			t.Fatalf("GET %s uses an inline artifact click handler", path)
		}
		if strings.Contains(body, `<pre id="artifactText"`) {
			t.Fatalf("GET %s still renders artifact preview as raw pre", path)
		}
		if !strings.Contains(body, "function pickArtifact(files,name)") {
			t.Fatalf("GET %s missing artifact label/path picker", path)
		}
		for _, marker := range []string{
			"overflow-x:hidden",
			"max-width:min(1728px,100%)",
			"grid-template-columns:minmax(220px,280px) minmax(0,1fr)",
			"@media(max-width:980px)",
			".table-wrap{max-width:100%;overflow-x:auto",
			".verdict-main{display:flex;align-items:flex-start;justify-content:space-between;gap:var(--space-12);margin-bottom:var(--space-12);flex-wrap:wrap;min-width:0}",
		} {
			if !strings.Contains(body, marker) {
				t.Fatalf("GET %s missing responsive overflow marker %q", path, marker)
			}
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
