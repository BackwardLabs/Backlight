package mention

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestMCPDirectoryLookupAndSearch(t *testing.T) {
	directory := NewMCPDirectory(MCPConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env:     map[string]string{"BACKLIGHT_TEST_X_MCP_HELPER": "1"},
	})
	t.Cleanup(func() { _ = directory.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	user, err := directory.LookupUser(ctx, "@EdelFinance")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if user.ID != "42" || user.Username != "EdelFinance" || user.Website != "https://edel.finance" || user.Followers != 12345 || !user.Verified {
		t.Fatalf("user = %+v", user)
	}

	users, err := directory.SearchUsers(ctx, "Edel Finance")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(users) != 2 || users[0].ID != "42" || users[1].Username != "EdelFan" {
		t.Fatalf("users = %+v", users)
	}
}

func TestMCPDirectoryCancelsHungBridge(t *testing.T) {
	directory := NewMCPDirectory(MCPConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env: map[string]string{
			"BACKLIGHT_TEST_X_MCP_HELPER": "1",
			"BACKLIGHT_TEST_X_MCP_SLOW":   "1",
		},
	})
	t.Cleanup(func() { _ = directory.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := directory.LookupUser(ctx, "EdelFinance"); err == nil {
		t.Fatal("lookup unexpectedly succeeded")
	}
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("BACKLIGHT_TEST_X_MCP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		if request.ID == nil {
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "initialize":
			response["result"] = map[string]any{
				"protocolVersion": xMCPProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "test-x-mcp", "version": "1"},
			}
		case "tools/list":
			response["result"] = map[string]any{"tools": []any{
				map[string]any{"name": "getUsersByUsername", "inputSchema": map[string]any{
					"type": "object", "properties": map[string]any{"username": map[string]any{"type": "string"}, "user.fields": map[string]any{"type": "array"}},
				}},
				map[string]any{"name": "searchUsers", "inputSchema": map[string]any{
					"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}, "max_results": map[string]any{"type": "integer"}},
				}},
			}}
		case "tools/call":
			if os.Getenv("BACKLIGHT_TEST_X_MCP_SLOW") == "1" {
				time.Sleep(5 * time.Second)
			}
			name, _ := request.Params["name"].(string)
			if name == "getUsersByUsername" {
				response["result"] = map[string]any{"structuredContent": map[string]any{"data": testMCPUser("42", "EdelFinance", 12345)}}
			} else {
				payload, _ := json.Marshal(map[string]any{"data": []any{
					testMCPUser("42", "EdelFinance", 12345),
					testMCPUser("77", "EdelFan", 10),
				}})
				response["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(payload)}}}
			}
		default:
			response["error"] = map[string]any{"code": -32601, "message": "not found"}
		}
		_ = encoder.Encode(response)
	}
	os.Exit(0)
}

func testMCPUser(id, username string, followers int64) map[string]any {
	return map[string]any{
		"id": id, "username": username, "name": "Edel Finance",
		"description": "Official Edel Finance account", "verified": true,
		"url": "https://t.co/short",
		"entities": map[string]any{"url": map[string]any{"urls": []any{
			map[string]any{"expanded_url": "https://edel.finance"},
		}}},
		"public_metrics": map[string]any{"followers_count": followers},
	}
}
