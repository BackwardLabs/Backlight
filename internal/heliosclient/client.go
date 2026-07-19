// Package heliosclient provides a small read-only HTTP client for Backlight APIs.
package heliosclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/api"
)

const defaultTimeout = 30 * time.Second

// Client calls a running Backlight HTTP server using the same Bearer token as the
// browser UI and operator API.
type Client struct {
	BaseURL    *url.URL
	APIToken   string
	HTTPClient *http.Client
}

// New constructs a client for a Backlight base URL such as http://127.0.0.1:8080.
func New(baseURL, apiToken string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("BACKLIGHT_BASE_URL is required")
	}
	if strings.TrimSpace(apiToken) == "" {
		return nil, fmt.Errorf("BACKLIGHT_API_TOKEN is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse BACKLIGHT_BASE_URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("BACKLIGHT_BASE_URL must include scheme and host")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{BaseURL: parsed, APIToken: apiToken, HTTPClient: httpClient}, nil
}

// ListOptions mirrors the supported GET /cases filters.
type ListOptions struct {
	Limit       int
	Offset      int
	State       string
	Outcome     string
	Chain       string
	TxHash      string
	CreatedFrom string
	CreatedTo   string
}

// ListCases fetches GET /cases.
func (c *Client) ListCases(ctx context.Context, opts ListOptions) (*api.CaseListResponse, error) {
	u := c.urlFor("/cases")
	q := u.Query()
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Offset > 0 {
		q.Set("offset", strconv.Itoa(opts.Offset))
	}
	setIfNotEmpty(q, "state", opts.State)
	setIfNotEmpty(q, "outcome", opts.Outcome)
	setIfNotEmpty(q, "chain", opts.Chain)
	setIfNotEmpty(q, "tx_hash", opts.TxHash)
	setIfNotEmpty(q, "created_from", opts.CreatedFrom)
	setIfNotEmpty(q, "created_to", opts.CreatedTo)
	u.RawQuery = q.Encode()

	var out api.CaseListResponse
	if err := c.doJSON(ctx, http.MethodGet, u.String(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetCase fetches GET /cases/{case_id}.
func (c *Client) GetCase(ctx context.Context, caseID string) (*api.CaseDetailResponse, error) {
	caseID = strings.TrimSpace(caseID)
	if caseID == "" {
		return nil, fmt.Errorf("case_id is required")
	}
	if strings.Contains(caseID, "/") {
		return nil, fmt.Errorf("case_id must not contain slash")
	}
	u := c.urlFor("/cases/" + url.PathEscape(caseID))
	var out api.CaseDetailResponse
	if err := c.doJSON(ctx, http.MethodGet, u.String(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListArtifacts fetches GET /cases/{case_id}/artifacts.
func (c *Client) ListArtifacts(ctx context.Context, caseID string) (*api.ArtifactListResponse, error) {
	caseID = strings.TrimSpace(caseID)
	if caseID == "" {
		return nil, fmt.Errorf("case_id is required")
	}
	if strings.Contains(caseID, "/") {
		return nil, fmt.Errorf("case_id must not contain slash")
	}
	u := c.urlFor("/cases/" + url.PathEscape(caseID) + "/artifacts")
	var out api.ArtifactListResponse
	if err := c.doJSON(ctx, http.MethodGet, u.String(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReadArtifact fetches GET /cases/{case_id}/artifacts/{path}.
func (c *Client) ReadArtifact(ctx context.Context, caseID string, path string, maxBytes int64) (*api.ArtifactReadResponse, error) {
	caseID = strings.TrimSpace(caseID)
	path = strings.TrimSpace(path)
	if caseID == "" {
		return nil, fmt.Errorf("case_id is required")
	}
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	if strings.Contains(caseID, "/") {
		return nil, fmt.Errorf("case_id must not contain slash")
	}
	if !safeArtifactPath(path) {
		return nil, fmt.Errorf("path must be a clean relative artifact path")
	}
	u := c.urlFor("/cases/" + url.PathEscape(caseID) + "/artifacts/" + escapeArtifactPath(path))
	if maxBytes > 0 {
		q := u.Query()
		q.Set("max_bytes", strconv.FormatInt(maxBytes, 10))
		u.RawQuery = q.Encode()
	}
	var out api.ArtifactReadResponse
	if err := c.doJSON(ctx, http.MethodGet, u.String(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) doJSON(ctx context.Context, method, requestURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, requestURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode Backlight response: %w", err)
	}
	return nil
}

func (c *Client) urlFor(path string) *url.URL {
	u := *c.BaseURL
	u.Path = strings.TrimRight(c.BaseURL.Path, "/") + path
	u.RawQuery = ""
	return &u
}

func safeArtifactPath(path string) bool {
	return path != "" && !strings.HasPrefix(path, "/") && path == strings.Trim(path, "/") && path == cleanSlashPath(path) && !strings.HasPrefix(path, "../") && path != ".."
}

func cleanSlashPath(path string) string {
	parts := strings.Split(path, "/")
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(clean) == 0 {
				return ".."
			}
			clean = clean[:len(clean)-1]
		default:
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, "/")
}

func escapeArtifactPath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func setIfNotEmpty(q url.Values, key, value string) {
	if strings.TrimSpace(value) != "" {
		q.Set(key, value)
	}
}

// HTTPError captures non-2xx Backlight responses.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("backlight HTTP %d: %s", e.StatusCode, strings.TrimSpace(e.Body))
}
