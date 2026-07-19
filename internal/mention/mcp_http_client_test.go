package mention

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPMCPDirectoryUsesBearerSessionAndSSE(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.Header.Get("Authorization") != "Bearer app-bearer" {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		var request struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if request.Method != "initialize" && req.Header.Get("Mcp-Session-Id") != "session-42" {
			http.Error(w, "missing session", http.StatusBadRequest)
			return
		}
		switch request.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session-42")
			writeMCPJSON(t, w, request.ID, map[string]any{"protocolVersion": xMCPProtocolVersion})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeMCPSSE(t, w, request.ID, map[string]any{"tools": []any{
				map[string]any{"name": "x_getUsersByUsername", "inputSchema": map[string]any{
					"type": "object", "properties": map[string]any{
						"username":    map[string]any{"type": "string"},
						"user.fields": map[string]any{"type": "string"},
					},
				}},
				map[string]any{"name": "searchUsers", "inputSchema": map[string]any{
					"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}},
				}},
			}})
		case "tools/call":
			name, _ := request.Params["name"].(string)
			if name == "x_getUsersByUsername" {
				arguments, _ := request.Params["arguments"].(map[string]any)
				if got := arguments["user.fields"]; got != "description,entities,public_metrics,url,verified" {
					t.Errorf("user.fields = %#v", got)
				}
				writeMCPJSON(t, w, request.ID, map[string]any{"structuredContent": map[string]any{"data": testMCPUser("42", "EdelFinance", 12345)}})
				return
			}
			payload, _ := json.Marshal(map[string]any{"data": []any{testMCPUser("42", "EdelFinance", 12345)}})
			writeMCPSSE(t, w, request.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": string(payload)}}})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	directory := NewHTTPMCPDirectory(HTTPMCPConfig{
		URL: server.URL, BearerToken: "app-bearer", Client: server.Client(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	user, err := directory.LookupUser(ctx, "EdelFinance")
	if err != nil || user.ID != "42" || user.Website != "https://edel.finance" {
		t.Fatalf("lookup user=%+v err=%v", user, err)
	}
	users, err := directory.SearchUsers(ctx, "Edel Finance")
	if err != nil || len(users) != 1 || users[0].Username != "EdelFinance" {
		t.Fatalf("search users=%+v err=%v", users, err)
	}
	if calls != 5 {
		t.Fatalf("HTTP calls = %d, want initialize+notification+list+lookup+search", calls)
	}
}

func writeMCPJSON(t *testing.T, w http.ResponseWriter, id, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil {
		t.Errorf("write response: %v", err)
	}
}

func writeMCPSSE(t *testing.T, w http.ResponseWriter, id, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
}
