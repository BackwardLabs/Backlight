package mention

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

const xMCPProtocolVersion = "2025-06-18"

// MCPConfig starts the local xurl MCP bridge. The bridge is long-lived and
// serializes the small number of user lookup calls made during publication.
type MCPConfig struct {
	Command    string
	Args       []string
	Env        map[string]string
	LookupTool string
	SearchTool string
}

// MCPDirectory is a minimal stdio MCP client for X user identity tools. It does
// not expose write tools to the publication workflow.
type MCPDirectory struct {
	config MCPConfig

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	nextID  int64
	tools   map[string]mcpTool
}

type mcpTool struct {
	Name        string         `json:"name"`
	InputSchema map[string]any `json:"inputSchema"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    any    `json:"data,omitempty"`
	} `json:"error,omitempty"`
}

func NewMCPDirectory(cfg MCPConfig) *MCPDirectory {
	if strings.TrimSpace(cfg.Command) == "" {
		cfg.Command = "xurl"
	}
	if len(cfg.Args) == 0 {
		cfg.Args = []string{"mcp", "https://api.x.com/mcp"}
	}
	if cfg.LookupTool == "" {
		cfg.LookupTool = "getUsersByUsername"
	}
	if cfg.SearchTool == "" {
		cfg.SearchTool = "searchUsers"
	}
	return &MCPDirectory{config: cfg, nextID: 1}
}

func (c *MCPDirectory) LookupUser(ctx context.Context, username string) (XUser, error) {
	return lookupMCPUser(ctx, c, username, c.config.LookupTool)
}

func (c *MCPDirectory) SearchUsers(ctx context.Context, query string) ([]XUser, error) {
	return searchMCPUsers(ctx, c, query, c.config.SearchTool)
}

type mcpToolCaller interface {
	callTool(context.Context, string, func(mcpTool) map[string]any) (any, error)
}

func lookupMCPUser(ctx context.Context, caller mcpToolCaller, username, toolName string) (XUser, error) {
	username = strings.TrimLeft(strings.TrimSpace(username), "@")
	if username == "" {
		return XUser{}, errors.New("x mcp lookup username is empty")
	}
	payload, err := caller.callTool(ctx, toolName, func(tool mcpTool) map[string]any {
		args := map[string]any{"username": username}
		addUserFields(args, tool)
		return args
	})
	if err != nil {
		return XUser{}, err
	}
	users := usersFromMCPPayload(payload)
	for _, user := range users {
		if strings.EqualFold(user.Username, username) {
			return user, nil
		}
	}
	if len(users) == 1 {
		return users[0], nil
	}
	return XUser{}, fmt.Errorf("x mcp lookup returned no user for @%s", username)
}

func searchMCPUsers(ctx context.Context, caller mcpToolCaller, query, toolName string) ([]XUser, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("x mcp search query is empty")
	}
	payload, err := caller.callTool(ctx, toolName, func(tool mcpTool) map[string]any {
		args := map[string]any{"query": query}
		if toolHasProperty(tool, "max_results") {
			args["max_results"] = 10
		}
		addUserFields(args, tool)
		return args
	})
	if err != nil {
		return nil, err
	}
	users := usersFromMCPPayload(payload)
	return users, nil
}

func (c *MCPDirectory) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopLocked()
}

func (c *MCPDirectory) callTool(ctx context.Context, requestedName string, arguments func(mcpTool) map[string]any) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.startLocked(ctx); err != nil {
		return nil, err
	}
	tool, ok := findMCPTool(c.tools, requestedName)
	if !ok {
		return nil, fmt.Errorf("x mcp tool %q is not available", requestedName)
	}
	result, err := c.requestLocked(ctx, "tools/call", map[string]any{
		"name":      tool.Name,
		"arguments": arguments(tool),
	})
	if err != nil {
		return nil, fmt.Errorf("call x mcp tool %s: %w", tool.Name, err)
	}
	return decodeMCPToolResult(result)
}

func (c *MCPDirectory) startLocked(ctx context.Context) error {
	if c.cmd != nil && c.cmd.Process != nil && c.scanner != nil {
		return nil
	}
	cmd := exec.Command(c.config.Command, c.config.Args...)
	cmd.Env = mergeEnv(os.Environ(), c.config.Env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open x mcp stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("open x mcp stdout: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start x mcp bridge %s: %w", c.config.Command, err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.scanner = bufio.NewScanner(stdout)
	c.scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	c.tools = nil

	if _, err := c.requestLocked(ctx, "initialize", map[string]any{
		"protocolVersion": xMCPProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "backlight-x-mention-resolver",
			"version": "1.0.0",
		},
	}); err != nil {
		_ = c.stopLocked()
		return fmt.Errorf("initialize x mcp bridge: %w", err)
	}
	if err := c.notifyLocked("notifications/initialized", map[string]any{}); err != nil {
		_ = c.stopLocked()
		return err
	}
	listRaw, err := c.requestLocked(ctx, "tools/list", map[string]any{})
	if err != nil {
		_ = c.stopLocked()
		return fmt.Errorf("list x mcp tools: %w", err)
	}
	var list struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := json.Unmarshal(listRaw, &list); err != nil {
		_ = c.stopLocked()
		return fmt.Errorf("decode x mcp tools: %w", err)
	}
	c.tools = make(map[string]mcpTool, len(list.Tools))
	for _, tool := range list.Tools {
		c.tools[normalizeToolName(tool.Name)] = tool
	}
	return nil
}

func (c *MCPDirectory) requestLocked(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c.stdin == nil || c.scanner == nil {
		return nil, errors.New("x mcp bridge is not running")
	}
	id := c.nextID
	c.nextID++
	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		_ = c.stopLocked()
		return nil, fmt.Errorf("write x mcp request: %w", err)
	}

	type responseResult struct {
		result json.RawMessage
		err    error
	}
	responseCh := make(chan responseResult, 1)
	scanner := c.scanner
	go func() {
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			var response mcpResponse
			if json.Unmarshal(line, &response) != nil || len(response.ID) == 0 {
				continue
			}
			if strings.TrimSpace(string(response.ID)) != strconv.FormatInt(id, 10) {
				continue
			}
			if response.Error != nil {
				responseCh <- responseResult{err: fmt.Errorf("mcp error %d: %s", response.Error.Code, response.Error.Message)}
				return
			}
			responseCh <- responseResult{result: response.Result}
			return
		}
		err := scanner.Err()
		if err == nil {
			err = errors.New("x mcp bridge closed stdout")
		}
		responseCh <- responseResult{err: err}
	}()

	select {
	case response := <-responseCh:
		if response.err != nil {
			_ = c.stopLocked()
		}
		return response.result, response.err
	case <-ctx.Done():
		_ = c.stopLocked()
		return nil, ctx.Err()
	}
}

func (c *MCPDirectory) notifyLocked(method string, params any) error {
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		return err
	}
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write x mcp notification: %w", err)
	}
	return nil
}

func (c *MCPDirectory) stopLocked() error {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	var err error
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		err = c.cmd.Wait()
		if _, ok := err.(*exec.ExitError); ok {
			err = nil
		}
	}
	c.cmd = nil
	c.stdin = nil
	c.scanner = nil
	c.tools = nil
	return err
}

func findMCPTool(tools map[string]mcpTool, requested string) (mcpTool, bool) {
	wanted := normalizeToolName(requested)
	if tool, ok := tools[wanted]; ok {
		return tool, true
	}
	var match mcpTool
	matches := 0
	for name, tool := range tools {
		if strings.HasSuffix(name, wanted) || strings.HasSuffix(wanted, name) {
			match = tool
			matches++
		}
	}
	return match, matches == 1
}

func normalizeToolName(value string) string {
	value = strings.ToLower(value)
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, value)
}

func toolHasProperty(tool mcpTool, name string) bool {
	props, _ := tool.InputSchema["properties"].(map[string]any)
	_, ok := props[name]
	return ok
}

func addUserFields(args map[string]any, tool mcpTool) {
	fields := []string{"description", "entities", "public_metrics", "url", "verified"}
	for _, name := range []string{"user.fields", "user_fields"} {
		props, _ := tool.InputSchema["properties"].(map[string]any)
		property, ok := props[name].(map[string]any)
		if !ok {
			continue
		}
		// X's hosted MCP exposes user.fields as the underlying HTTP query
		// parameter, which is a comma-separated string. Some bridges expose
		// the same field as a JSON array, so preserve that representation when
		// it is explicitly declared in the tool schema.
		if property["type"] == "array" {
			args[name] = fields
		} else {
			args[name] = strings.Join(fields, ",")
		}
		return
	}
}

func decodeMCPToolResult(raw json.RawMessage) (any, error) {
	var result struct {
		StructuredContent any `json:"structuredContent"`
		Content           []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode x mcp result: %w", err)
	}
	if result.IsError {
		return nil, errors.New("x mcp tool returned isError=true")
	}
	if result.StructuredContent != nil {
		return result.StructuredContent, nil
	}
	for _, content := range result.Content {
		if content.Type != "text" || strings.TrimSpace(content.Text) == "" {
			continue
		}
		if payload, ok := decodeJSONText(content.Text); ok {
			return payload, nil
		}
	}
	return nil, errors.New("x mcp tool result did not contain structured JSON")
}

func decodeJSONText(value string) (any, bool) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "```json")
	value = strings.TrimPrefix(value, "```")
	value = strings.TrimSuffix(value, "```")
	value = strings.TrimSpace(value)
	starts := []string{"{", "["}
	for _, marker := range starts {
		if idx := strings.Index(value, marker); idx >= 0 {
			var out any
			decoder := json.NewDecoder(strings.NewReader(value[idx:]))
			decoder.UseNumber()
			if decoder.Decode(&out) == nil {
				return out, true
			}
		}
	}
	return nil, false
}

func usersFromMCPPayload(payload any) []XUser {
	seen := map[string]bool{}
	out := make([]XUser, 0)
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			id := stringValue(typed["id"])
			username := stringValue(typed["username"])
			if id != "" && username != "" {
				key := id + ":" + strings.ToLower(username)
				if !seen[key] {
					seen[key] = true
					out = append(out, XUser{
						ID:          id,
						Username:    strings.TrimLeft(username, "@"),
						Name:        stringValue(typed["name"]),
						Description: stringValue(typed["description"]),
						Website:     expandedWebsite(typed),
						Followers:   followersValue(typed),
						Verified:    boolValue(typed["verified"]),
						Status:      "active",
					})
				}
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		case string:
			if decoded, ok := decodeJSONText(typed); ok {
				walk(decoded)
			}
		}
	}
	walk(payload)
	return out
}

func expandedWebsite(user map[string]any) string {
	entities, _ := user["entities"].(map[string]any)
	urlEntity, _ := entities["url"].(map[string]any)
	urls, _ := urlEntity["urls"].([]any)
	for _, item := range urls {
		entry, _ := item.(map[string]any)
		if value := firstTextMention(stringValue(entry["expanded_url"]), stringValue(entry["unwound_url"])); value != "" {
			return value
		}
	}
	return stringValue(user["url"])
}

func followersValue(user map[string]any) int64 {
	metrics, _ := user["public_metrics"].(map[string]any)
	switch value := metrics["followers_count"].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case json.Number:
		n, _ := value.Int64()
		return n
	default:
		return 0
	}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	default:
		return ""
	}
}

func boolValue(value any) bool {
	valueBool, _ := value.(bool)
	return valueBool
}

func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	values := make(map[string]string, len(base)+len(overrides))
	for _, item := range base {
		if key, value, ok := strings.Cut(item, "="); ok {
			values[key] = value
		}
	}
	for key, value := range overrides {
		if strings.TrimSpace(value) != "" {
			values[key] = value
		}
	}
	out := make([]string, 0, len(values))
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}
