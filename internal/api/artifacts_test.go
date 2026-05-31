package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UPside-Lumos-V2/helios/internal/config"
	"github.com/UPside-Lumos-V2/helios/internal/store"
)

func TestArtifactAPIReadsOnlyAllowlistedCaseFiles(t *testing.T) {
	ctx := context.Background()
	outputRoot := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	c, _, err := st.SubmitCase(ctx, "ethereum", strings.Repeat("0", 66), nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err = st.ClaimNextQueued(ctx, outputRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(*c.OutputRoot, "report_bundle", "report"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(*c.OutputRoot, "report_bundle", "poc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*c.OutputRoot, "report_bundle", "report", "REPORT.md"), []byte("# report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*c.OutputRoot, "RCA.md"), []byte("# RCA\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*c.OutputRoot, "report_bundle", "poc", "PoC.t.sol"), []byte("contract PoC {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewServer(&config.Config{APIToken: "secret", OutputRoot: outputRoot}, st, nil).Handler()

	req := httptest.NewRequest(http.MethodGet, "/cases/"+c.CaseID+"/artifacts/REPORT.md", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET artifact status = %d: %s", rr.Code, rr.Body.String())
	}
	var body ArtifactReadResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Artifact == nil || body.Artifact.Path != "REPORT.md" || body.Artifact.Text != "# report\n" {
		t.Fatalf("unexpected artifact body: %+v", body.Artifact)
	}

	for _, rel := range []string{"RCA.md", "PoC.t.sol"} {
		req = httptest.NewRequest(http.MethodGet, "/cases/"+c.CaseID+"/artifacts/"+rel, nil)
		req.Header.Set("Authorization", "Bearer secret")
		rr = httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s artifact status = %d: %s", rel, rr.Code, rr.Body.String())
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/cases/"+c.CaseID+"/artifacts/report_bundle/report/REPORT.md", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("report_bundle artifact status = %d: %s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/cases/"+c.CaseID+"/artifacts/artifacts/secret.json", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("non-product artifact status = %d, want 409", rr.Code)
	}
}
