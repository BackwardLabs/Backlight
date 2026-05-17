// Package artifacts reads a narrow, read-only subset of lumoskit output files.
package artifacts

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/UPside-Lumos-V2/helios/internal/api"
)

const DefaultMaxBytes int64 = 1 << 20 // 1 MiB

var defaultAllowlist = []string{"summary.json", "summary.md", "rca.md", "PoC.t.sol"}

// Reader enforces exact-path allowlisting and output-root containment.
type Reader struct {
	OutputBase string
	MaxBytes   int64
	Allowed    map[string]struct{}
}

// FileInfo describes one allowed artifact under a case output root.
type FileInfo struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Size   int64  `json:"size,omitempty"`
}

// ReadResult is returned by Read.
type ReadResult struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Text string `json:"text"`
}

func NewReader(outputBase string, maxBytes int64) (*Reader, error) {
	if strings.TrimSpace(outputBase) == "" {
		return nil, fmt.Errorf("HELIOS_OUTPUT_BASE is required")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	allowed := make(map[string]struct{}, len(defaultAllowlist))
	for _, path := range defaultAllowlist {
		allowed[path] = struct{}{}
	}
	return &Reader{OutputBase: outputBase, MaxBytes: maxBytes, Allowed: allowed}, nil
}

// AllowedPaths returns the exact artifact paths exposed by this reader.
func (r *Reader) AllowedPaths() []string {
	paths := make([]string, 0, len(r.Allowed))
	for p := range r.Allowed {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// List reports which allowlisted artifacts currently exist for a case.
func (r *Reader) List(c api.CaseSummary) ([]FileInfo, error) {
	root, err := r.validOutputRoot(c)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(r.Allowed))
	for _, rel := range r.AllowedPaths() {
		info := FileInfo{Path: rel}
		path, err := r.safeFilePath(root, rel)
		if err == nil {
			if st, statErr := os.Stat(path); statErr == nil && !st.IsDir() {
				info.Exists = true
				info.Size = st.Size()
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// Read reads one exact allowlisted artifact for a case. perCallMax may lower the
// configured reader cap, but cannot raise it.
func (r *Reader) Read(c api.CaseSummary, rel string, perCallMax int64) (*ReadResult, error) {
	root, err := r.validOutputRoot(c)
	if err != nil {
		return nil, err
	}
	path, err := r.safeFilePath(root, rel)
	if err != nil {
		return nil, err
	}
	maxBytes := r.MaxBytes
	if perCallMax > 0 && perCallMax < maxBytes {
		maxBytes = perCallMax
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("artifact %s exceeds max_bytes=%d", rel, maxBytes)
	}
	return &ReadResult{Path: rel, Size: int64(len(data)), Text: string(data)}, nil
}

func (r *Reader) validOutputRoot(c api.CaseSummary) (string, error) {
	if c.OutputRoot == nil || strings.TrimSpace(*c.OutputRoot) == "" {
		return "", fmt.Errorf("case %s has no output_root", c.CaseID)
	}
	baseReal, err := filepath.EvalSymlinks(r.OutputBase)
	if err != nil {
		return "", fmt.Errorf("resolve HELIOS_OUTPUT_BASE: %w", err)
	}
	rootReal, err := filepath.EvalSymlinks(*c.OutputRoot)
	if err != nil {
		return "", fmt.Errorf("resolve case output_root: %w", err)
	}
	if !isWithin(baseReal, rootReal) {
		return "", fmt.Errorf("case output_root is outside HELIOS_OUTPUT_BASE")
	}
	return rootReal, nil
}

func (r *Reader) safeFilePath(root, rel string) (string, error) {
	if _, ok := r.Allowed[rel]; !ok {
		return "", fmt.Errorf("artifact path %q is not allowlisted", rel)
	}
	if filepath.IsAbs(rel) || filepath.Clean(rel) != rel || strings.Contains(rel, string(filepath.Separator)) {
		return "", fmt.Errorf("artifact path %q is not a safe relative file name", rel)
	}
	path := filepath.Join(root, rel)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, nil
		}
		return "", err
	}
	if !isWithin(root, resolved) {
		return "", fmt.Errorf("artifact path escapes output_root")
	}
	return resolved, nil
}

func isWithin(base, candidate string) bool {
	rel, err := filepath.Rel(base, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != "" && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}
