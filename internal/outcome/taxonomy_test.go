package outcome

import (
	"strings"
	"testing"
)

func TestClassifyEngineError(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want string
	}{
		{"tx not found", Input{ExitCode: 1, Stderr: []byte("warning: eth_getTransactionByHash unavailable\n[unavailable:tx_not_found] Error: tx not found: 0x4d5e")}, FailureTxNotFound},
		{"tx_not_found wins over timeout text", Input{ExitCode: 1, Stderr: []byte("tx not found after a timeout")}, FailureTxNotFound},
		{"rpc rate limit", Input{ExitCode: 1, Stderr: []byte("json-rpc error: 429 Too Many Requests")}, FailureRPCUnavailable},
		{"rpc dial timeout", Input{ExitCode: 1, Stderr: []byte("Post https://rpc: dial tcp: i/o timeout")}, FailureRPCUnavailable},
		{"trace unavailable", Input{ExitCode: 1, Stderr: []byte("method debug_traceTransaction not supported by node")}, FailureTraceUnavailable},
		{"unsupported chain", Input{ExitCode: 1, Stderr: []byte("unsupported chain: foochain")}, FailureUnsupportedChain},
		{"binary exec on negative exit", Input{ExitCode: -1, Stderr: []byte("fork/exec bin/lumoskit: no such file or directory")}, FailureBinaryExecError},
		{"toolchain on negative exit", Input{ExitCode: -1, Stderr: []byte("forge: command not found")}, FailureToolchainError},
		{"classify from summary message", Input{ExitCode: 1, SummaryBytes: []byte(`{"status":"fail","failure":{"kind":"engine_error","message":"transaction not found"}}`)}, FailureTxNotFound},
		{"unclassified falls through", Input{ExitCode: 1, Stderr: []byte("some unexpected panic in lifting stage")}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyEngineError(tc.in); got != tc.want {
				t.Errorf("classifyEngineError = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsRetryableFailureKind(t *testing.T) {
	for _, k := range []string{FailureTxNotFound, FailureUnsupportedChain, FailureBinaryExecError, FailureToolchainError} {
		if IsRetryableFailureKind(k) {
			t.Errorf("%q should be non-retryable", k)
		}
	}
	for _, k := range []string{FailureRPCUnavailable, FailureTraceUnavailable, FailureLumoskitNonzeroExit, FailureSummaryMissing, "", "something_unknown"} {
		if !IsRetryableFailureKind(k) {
			t.Errorf("%q should default to retryable", k)
		}
	}
}

func TestRedactStderr(t *testing.T) {
	in := []byte("connecting https://eth-mainnet.g.alchemy.com/v2/SECRETKEY1234567890 with Bearer abcdef.token.value and ?apikey=topsecretvalue done")
	out := redactStderr(in)
	for _, leak := range []string{"SECRETKEY1234567890", "abcdef.token.value", "topsecretvalue"} {
		if strings.Contains(out, leak) {
			t.Errorf("stderr not redacted, leaked %q in %q", leak, out)
		}
	}
	if !strings.Contains(out, "[redacted]") {
		t.Errorf("expected [redacted] marker, got %q", out)
	}

	big := make([]byte, maxStderrTailBytes+500)
	for i := range big {
		big[i] = 'x'
	}
	if got := len(redactStderr(big)); got != maxStderrTailBytes {
		t.Errorf("expected tail capped to %d, got %d", maxStderrTailBytes, got)
	}
	if redactStderr(nil) != "" {
		t.Errorf("empty stderr should redact to empty string")
	}
}

func TestMapClassifiesNonzeroExitToFinerKind(t *testing.T) {
	got := Map(Input{ExitCode: 1, Stderr: []byte("Error: tx not found: 0xabc")})
	if got.Rule != "O5" || got.Outcome != OutcomeEngineError {
		t.Fatalf("want O5/engine_error, got %s/%s", got.Rule, got.Outcome)
	}
	if got.FailureKind == nil || *got.FailureKind != FailureTxNotFound {
		t.Errorf("want failure_kind=%q, got %v", FailureTxNotFound, got.FailureKind)
	}
	if got.RerunReason != FailureTxNotFound {
		t.Errorf("want rerun_reason=%q, got %q", FailureTxNotFound, got.RerunReason)
	}
}

func TestMapFallsBackToNonzeroExitWhenUnclassified(t *testing.T) {
	got := Map(Input{ExitCode: 1, Stderr: []byte("mysterious panic")})
	if got.FailureKind == nil || *got.FailureKind != FailureLumoskitNonzeroExit {
		t.Errorf("want fallback %q, got %v", FailureLumoskitNonzeroExit, got.FailureKind)
	}
}

func TestTerminalEventPayloadEngineErrorDiagnosis(t *testing.T) {
	in := Input{ExitCode: 1, Stderr: []byte("Error: tx not found: 0xabc apikey=secret123")}
	payload := TerminalEventPayload(Map(in), in)

	if payload["engine_error_kind"] != FailureTxNotFound {
		t.Errorf("engine_error_kind = %v, want %q", payload["engine_error_kind"], FailureTxNotFound)
	}
	diag, ok := payload["diagnosis"].(map[string]any)
	if !ok {
		t.Fatalf("diagnosis missing or wrong type: %T", payload["diagnosis"])
	}
	if diag["owner"] != "upstream_input" || diag["retryable"] != false {
		t.Errorf("diagnosis = %v", diag)
	}
	tail, _ := payload["stderr_tail"].(string)
	if tail == "" || strings.Contains(tail, "secret123") {
		t.Errorf("stderr_tail missing or unredacted: %q", tail)
	}
}
