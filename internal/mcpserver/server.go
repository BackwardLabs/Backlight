// Package mcpserver exposes Helios case artifacts over a minimal stdio MCP server.
package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/UPside-Lumos-V2/helios/internal/api"
	"github.com/UPside-Lumos-V2/helios/internal/artifacts"
	"github.com/UPside-Lumos-V2/helios/internal/heliosclient"
)

const protocolVersion = "2025-06-18"

// HeliosClient is the read-only subset of the Helios API used by MCP tools.
type HeliosClient interface {
	ListCases(ctx context.Context, opts heliosclient.ListOptions) (*api.CaseListResponse, error)
	GetCase(ctx context.Context, caseID string) (*api.CaseDetailResponse, error)
}

// ArtifactReader is the read-only product artifact gateway.
type ArtifactReader interface {
	AllowedPaths() []string
	List(c api.CaseSummary) ([]artifacts.FileInfo, error)
	Read(c api.CaseSummary, rel string, perCallMax int64) (*artifacts.ReadResult, error)
}

// Server handles newline-delimited JSON-RPC messages on stdin/stdout.
type Server struct {
	Client    HeliosClient
	Artifacts ArtifactReader
	Logger    *slog.Logger
}

// Serve reads JSON-RPC requests from r and writes JSON-RPC responses to w. Logs
// must go to stderr through Logger; stdout is reserved for valid MCP messages.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	if s.Client == nil {
		return fmt.Errorf("mcp server missing Helios client")
	}
	if s.Artifacts == nil {
		return fmt.Errorf("mcp server missing artifact reader")
	}
	if s.Logger == nil {
		s.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	out := bufio.NewWriter(w)
	defer out.Flush()

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			if writeErr := writeRPC(out, rpcErrorResponse(nil, -32700, "parse error", err.Error())); writeErr != nil {
				return writeErr
			}
			continue
		}
		if req.ID == nil {
			s.handleNotification(req)
			continue
		}
		resp := s.handleRequest(ctx, req)
		if err := writeRPC(out, resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s *Server) handleNotification(req rpcRequest) {
	switch req.Method {
	case "notifications/initialized", "$/cancelRequest", "exit":
		return
	default:
		s.Logger.Debug("ignored MCP notification", "method", req.Method)
	}
}

func (s *Server) handleRequest(ctx context.Context, req rpcRequest) rpcResponse {
	if req.JSONRPC != "2.0" || req.Method == "" {
		return rpcErrorResponse(req.ID, -32600, "invalid request", "jsonrpc=2.0 and method are required")
	}
	switch req.Method {
	case "initialize":
		return rpcResultResponse(req.ID, initializeResult())
	case "ping":
		return rpcResultResponse(req.ID, map[string]any{})
	case "tools/list":
		return rpcResultResponse(req.ID, map[string]any{"tools": s.tools()})
	case "tools/call":
		result, err := s.callTool(ctx, req.Params)
		if err != nil {
			return rpcErrorResponse(req.ID, -32602, "tool call failed", err.Error())
		}
		return rpcResultResponse(req.ID, result)
	case "resources/list":
		return rpcResultResponse(req.ID, map[string]any{"resources": []any{}})
	case "prompts/list":
		return rpcResultResponse(req.ID, map[string]any{"prompts": []any{}})
	default:
		return rpcErrorResponse(req.ID, -32601, "method not found", req.Method)
	}
}

func initializeResult() map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    "helios-mcp",
			"version": "0.1.0",
		},
		"instructions": "Read-only Helios artifact gateway. Exposes case metadata and exactly four product artifacts: summary.json, summary.md, rca.md, and PoC.t.sol.",
	}
}

func (s *Server) tools() []toolDef {
	readOnly := map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	return []toolDef{
		{
			Name:        "helios.list_cases",
			Title:       "List Helios cases",
			Description: "List Helios cases via GET /cases. Read-only.",
			InputSchema: schema(map[string]any{
				"limit":        intProp("Maximum rows to return, default 50, max 500"),
				"offset":       intProp("Pagination offset"),
				"state":        stringProp("Filter by queued, running, done, handed-off, or failed"),
				"outcome":      stringProp("Filter by verified, partial, unverified, or engine_error"),
				"chain":        stringProp("Filter by chain label"),
				"tx_hash":      stringProp("Filter by transaction hash"),
				"created_from": stringProp("Filter by ISO8601 created_at lower bound"),
				"created_to":   stringProp("Filter by ISO8601 created_at upper bound"),
			}, nil),
			Annotations: readOnly,
		},
		{
			Name:        "helios.get_case",
			Title:       "Get Helios case",
			Description: "Fetch one Helios case detail via GET /cases/{case_id}. Read-only.",
			InputSchema: schema(map[string]any{
				"case_id": stringProp("Helios case id"),
			}, []string{"case_id"}),
			Annotations: readOnly,
		},
		{
			Name:        "helios.list_artifacts",
			Title:       "List allowed case artifacts",
			Description: "List existence and size for the four allowlisted product artifacts for a case.",
			InputSchema: schema(map[string]any{
				"case_id": stringProp("Helios case id"),
			}, []string{"case_id"}),
			Annotations: readOnly,
		},
		{
			Name:        "helios.read_artifact",
			Title:       "Read allowed case artifact",
			Description: "Read exactly one allowlisted artifact: summary.json, summary.md, rca.md, or PoC.t.sol.",
			InputSchema: schema(map[string]any{
				"case_id":   stringProp("Helios case id"),
				"path":      stringProp("One of: summary.json, summary.md, rca.md, PoC.t.sol"),
				"max_bytes": intProp("Optional per-call max bytes; can only lower the server cap"),
			}, []string{"case_id", "path"}),
			Annotations: readOnly,
		},
	}
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (toolResult, error) {
	var params toolCallParams
	if len(raw) == 0 {
		return toolResult{}, fmt.Errorf("missing params")
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return toolResult{}, err
	}
	if strings.TrimSpace(params.Name) == "" {
		return toolResult{}, fmt.Errorf("tool name is required")
	}
	switch params.Name {
	case "helios.list_cases":
		var args listCasesArgs
		if err := decodeArgs(params.Arguments, &args); err != nil {
			return toolResult{}, err
		}
		resp, err := s.Client.ListCases(ctx, heliosclient.ListOptions{
			Limit:       args.Limit,
			Offset:      args.Offset,
			State:       args.State,
			Outcome:     args.Outcome,
			Chain:       args.Chain,
			TxHash:      args.TxHash,
			CreatedFrom: args.CreatedFrom,
			CreatedTo:   args.CreatedTo,
		})
		return jsonToolResult(resp, err)
	case "helios.get_case":
		var args caseIDArgs
		if err := decodeArgs(params.Arguments, &args); err != nil {
			return toolResult{}, err
		}
		resp, err := s.Client.GetCase(ctx, args.CaseID)
		return jsonToolResult(resp, err)
	case "helios.list_artifacts":
		var args caseIDArgs
		if err := decodeArgs(params.Arguments, &args); err != nil {
			return toolResult{}, err
		}
		c, err := s.Client.GetCase(ctx, args.CaseID)
		if err != nil {
			return errorToolResult(err), nil
		}
		files, err := s.Artifacts.List(c.CaseSummary)
		return jsonToolResult(map[string]any{"case_id": args.CaseID, "allowed": s.Artifacts.AllowedPaths(), "files": files}, err)
	case "helios.read_artifact":
		var args readArtifactArgs
		if err := decodeArgs(params.Arguments, &args); err != nil {
			return toolResult{}, err
		}
		c, err := s.Client.GetCase(ctx, args.CaseID)
		if err != nil {
			return errorToolResult(err), nil
		}
		res, err := s.Artifacts.Read(c.CaseSummary, args.Path, int64(args.MaxBytes))
		return jsonToolResult(map[string]any{"case_id": args.CaseID, "artifact": res}, err)
	default:
		return toolResult{}, fmt.Errorf("unknown tool %q", params.Name)
	}
}

func decodeArgs(raw json.RawMessage, out any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte(`{}`)
	}
	return json.Unmarshal(raw, out)
}

func jsonToolResult(v any, err error) (toolResult, error) {
	if err != nil {
		return errorToolResult(err), nil
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolResult{}, err
	}
	return toolResult{
		Content:           []contentBlock{{Type: "text", Text: string(data)}},
		StructuredContent: v,
		IsError:           false,
	}, nil
}

func errorToolResult(err error) toolResult {
	return toolResult{Content: []contentBlock{{Type: "text", Text: err.Error()}}, IsError: true}
}

func schema(properties map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func writeRPC(w *bufio.Writer, resp rpcResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if err := w.WriteByte('\n'); err != nil {
		return err
	}
	return w.Flush()
}

type listCasesArgs struct {
	Limit       int    `json:"limit"`
	Offset      int    `json:"offset"`
	State       string `json:"state"`
	Outcome     string `json:"outcome"`
	Chain       string `json:"chain"`
	TxHash      string `json:"tx_hash"`
	CreatedFrom string `json:"created_from"`
	CreatedTo   string `json:"created_to"`
}

type caseIDArgs struct {
	CaseID string `json:"case_id"`
}

type readArtifactArgs struct {
	CaseID   string `json:"case_id"`
	Path     string `json:"path"`
	MaxBytes int    `json:"max_bytes"`
}

type rpcRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func rpcResultResponse(id *json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: responseID(id), Result: result}
}

func rpcErrorResponse(id *json.RawMessage, code int, message string, data any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: responseID(id), Error: &rpcError{Code: code, Message: message, Data: data}}
}

func responseID(id *json.RawMessage) json.RawMessage {
	if id == nil || len(*id) == 0 {
		return json.RawMessage("null")
	}
	return *id
}

type toolDef struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type toolResult struct {
	Content           []contentBlock `json:"content"`
	StructuredContent any            `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
