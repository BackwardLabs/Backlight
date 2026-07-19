package mention

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const defaultXMCPURL = "https://api.x.com/mcp"

// HTTPMCPConfig connects directly to X's hosted Streamable HTTP MCP endpoint.
// Exact public-account lookup accepts an app-only token, while search_users
// requires the OAuth user-context token supplied by BearerTokenSource.
type HTTPMCPConfig struct {
	URL               string
	BearerToken       string
	BearerTokenSource BearerTokenSource
	LookupTool        string
	SearchTool        string
	Client            *http.Client
}

// BearerTokenSource supplies a cached OAuth 2.0 user-context token. The
// invalidation hook lets the MCP client recover once from an early 401.
type BearerTokenSource interface {
	AccessToken(context.Context) (string, error)
	InvalidateAccessToken(string)
}

// HTTPMCPDirectory implements Directory without a local xurl process.
type HTTPMCPDirectory struct {
	config HTTPMCPConfig

	mu        sync.Mutex
	nextID    int64
	sessionID string
	tools     map[string]mcpTool
}

func NewHTTPMCPDirectory(cfg HTTPMCPConfig) *HTTPMCPDirectory {
	if strings.TrimSpace(cfg.URL) == "" {
		cfg.URL = defaultXMCPURL
	}
	if cfg.LookupTool == "" {
		cfg.LookupTool = "getUsersByUsername"
	}
	if cfg.SearchTool == "" {
		cfg.SearchTool = "searchUsers"
	}
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	return &HTTPMCPDirectory{config: cfg, nextID: 1}
}

func (c *HTTPMCPDirectory) LookupUser(ctx context.Context, username string) (XUser, error) {
	return lookupMCPUser(ctx, c, username, c.config.LookupTool)
}

func (c *HTTPMCPDirectory) SearchUsers(ctx context.Context, query string) ([]XUser, error) {
	return searchMCPUsers(ctx, c, query, c.config.SearchTool)
}

func (c *HTTPMCPDirectory) Close() error { return nil }

func (c *HTTPMCPDirectory) callTool(ctx context.Context, requestedName string, arguments func(mcpTool) map[string]any) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := c.startLocked(ctx); err != nil {
			lastErr = err
			c.resetLocked()
			continue
		}
		tool, ok := findMCPTool(c.tools, requestedName)
		if !ok {
			return nil, fmt.Errorf("x mcp tool %q is not available", requestedName)
		}
		result, err := c.requestLocked(ctx, "tools/call", map[string]any{
			"name":      tool.Name,
			"arguments": arguments(tool),
		})
		if err == nil {
			return decodeMCPToolResult(result)
		}
		lastErr = fmt.Errorf("call x mcp tool %s: %w", tool.Name, err)
		c.resetLocked()
	}
	return nil, lastErr
}

func (c *HTTPMCPDirectory) resetLocked() {
	c.sessionID = ""
	c.tools = nil
}

func (c *HTTPMCPDirectory) startLocked(ctx context.Context) error {
	if c.tools != nil {
		return nil
	}
	if strings.TrimSpace(c.config.BearerToken) == "" && c.config.BearerTokenSource == nil {
		return errors.New("x hosted mcp requires a bearer token or token source")
	}
	if _, err := c.requestLocked(ctx, "initialize", map[string]any{
		"protocolVersion": xMCPProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "backlight-x-mention-resolver",
			"version": "1.0.0",
		},
	}); err != nil {
		return fmt.Errorf("initialize hosted x mcp: %w", err)
	}
	if err := c.notifyLocked(ctx, "notifications/initialized", map[string]any{}); err != nil {
		return err
	}
	listRaw, err := c.requestLocked(ctx, "tools/list", map[string]any{})
	if err != nil {
		return fmt.Errorf("list hosted x mcp tools: %w", err)
	}
	var list struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := json.Unmarshal(listRaw, &list); err != nil {
		return fmt.Errorf("decode hosted x mcp tools: %w", err)
	}
	c.tools = make(map[string]mcpTool, len(list.Tools))
	for _, tool := range list.Tools {
		c.tools[normalizeToolName(tool.Name)] = tool
	}
	return nil
}

func (c *HTTPMCPDirectory) requestLocked(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID
	c.nextID++
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	return c.postLocked(ctx, payload, strconv.FormatInt(id, 10), true)
}

func (c *HTTPMCPDirectory) notifyLocked(ctx context.Context, method string, params any) error {
	payload := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	_, err := c.postLocked(ctx, payload, "", false)
	return err
}

func (c *HTTPMCPDirectory) postLocked(ctx context.Context, payload any, wantedID string, expectResponse bool) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	bearer := strings.TrimSpace(c.config.BearerToken)
	if c.config.BearerTokenSource != nil {
		bearer, err = c.config.BearerTokenSource.AccessToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("get x mcp user access token: %w", err)
		}
	}
	if strings.TrimSpace(bearer) == "" {
		return nil, errors.New("x hosted mcp bearer token is empty")
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearer))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", xMCPProtocolVersion)
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	resp, err := c.config.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if session := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id")); session != "" {
		c.sessionID = session
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusUnauthorized && c.config.BearerTokenSource != nil {
			c.config.BearerTokenSource.InvalidateAccessToken(bearer)
		}
		return nil, fmt.Errorf("hosted x mcp returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if !expectResponse {
		return nil, nil
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	responses := []mcpResponse{}
	if mediaType == "text/event-stream" {
		responses = decodeMCPSSE(data)
	} else {
		var response mcpResponse
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, fmt.Errorf("decode hosted x mcp response: %w", err)
		}
		responses = append(responses, response)
	}
	for _, response := range responses {
		if strings.TrimSpace(string(response.ID)) != wantedID {
			continue
		}
		if response.Error != nil {
			return nil, fmt.Errorf("mcp error %d: %s", response.Error.Code, response.Error.Message)
		}
		return response.Result, nil
	}
	return nil, fmt.Errorf("hosted x mcp response did not include id %s", wantedID)
}

func decodeMCPSSE(data []byte) []mcpResponse {
	var responses []mcpResponse
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var eventData strings.Builder
	flush := func() {
		if eventData.Len() == 0 {
			return
		}
		var response mcpResponse
		if json.Unmarshal([]byte(eventData.String()), &response) == nil {
			responses = append(responses, response)
		}
		eventData.Reset()
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if eventData.Len() > 0 {
				eventData.WriteByte('\n')
			}
			eventData.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	return responses
}
