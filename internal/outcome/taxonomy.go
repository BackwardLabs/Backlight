package outcome

import (
	"regexp"
	"strings"
)

// Finer engine-error kinds derived from a nonzero lumoskit exit by inspecting
// stderr + the summary failure message. These refine the opaque
// lumoskit_nonzero_exit so an operator can decide the next action from the
// Telegram/API surface alone (who owns it, is it retryable, what to check).
const (
	FailureTxNotFound       = "tx_not_found"
	FailureRPCUnavailable   = "rpc_unavailable"
	FailureTraceUnavailable = "trace_unavailable"
	FailureUnsupportedChain = "unsupported_chain"
	FailureBinaryExecError  = "binary_exec_error"
	FailureToolchainError   = "toolchain_error"
)

// Diagnosis is the operator-facing explanation attached to engine-error
// case_events. Stored under the "diagnosis" key of the terminal event payload.
type Diagnosis struct {
	Category  string
	Owner     string
	Retryable bool
	Action    string
}

var engineErrorDiagnoses = map[string]Diagnosis{
	FailureTxNotFound:                  {FailureTxNotFound, "upstream_input", false, "verify the tx hash exists on the submitted chain and that the chain label is correct"},
	FailureRPCUnavailable:              {FailureRPCUnavailable, "infrastructure", true, "check RPC provider status, env (CEFG_LIVE_RPC_URL/RPC_URL/ETH_RPC_URL), rate limits and auth"},
	FailureTraceUnavailable:            {FailureTraceUnavailable, "infrastructure", true, "use an archive/debug-trace capable RPC (debug_traceTransaction / trace API)"},
	FailureUnsupportedChain:            {FailureUnsupportedChain, "configuration", false, "add the chain alias/config to the lumoskit RPC resolver"},
	FailureBinaryExecError:             {FailureBinaryExecError, "deployment", false, "check BACKLIGHT_LUMOSKIT_BIN path, executable bit and architecture"},
	FailureToolchainError:              {FailureToolchainError, "deployment", false, "check systemd PATH/drop-in for cast/forge/node/python"},
	FailureSummaryMissing:              {FailureSummaryMissing, "engine", false, "check the output root and lumoskit stderr"},
	FailureSummaryUnreadable:           {FailureSummaryUnreadable, "engine", false, "inspect lumoskit stderr_tail and the run summary file"},
	FailureLumoskitReportedEngineError: {FailureLumoskitReportedEngineError, "engine", false, "inspect lumoskit stderr_tail and the reported failure.message"},
	FailureLumoskitUnexpectedSummary:   {FailureLumoskitUnexpectedSummary, "engine", false, "inspect the run summary shape and lumoskit stderr_tail"},
	FailureLumoskitNonzeroExit:         {FailureLumoskitNonzeroExit, "engine", false, "inspect lumoskit stderr_tail; the reason was not auto-classified"},
}

// DiagnosisFor returns the operator diagnosis for a failure kind.
func DiagnosisFor(kind string) (Diagnosis, bool) {
	d, ok := engineErrorDiagnoses[kind]
	return d, ok
}

// IsRetryableFailureKind reports whether re-running lumoskit on the same input
// could plausibly succeed. Only kinds we are confident are deterministic
// failures (bad input / deployment) are non-retryable; everything else
// (including unclassified lumoskit_nonzero_exit and unknown kinds) defaults to
// retryable so the existing "failed leaf → child attempt" dedup behavior is
// preserved. Used by store dedup to skip wasteful reruns of bad input.
func IsRetryableFailureKind(kind string) bool {
	switch kind {
	case FailureTxNotFound, FailureUnsupportedChain, FailureBinaryExecError, FailureToolchainError:
		return false
	default:
		return true
	}
}

// Regexes are RE2 (no lookahead/backref). Matched against lowercased
// stderr-tail + summary failure message.
var (
	reTxNotFound       = regexp.MustCompile(`tx[_ ]not[_ ]found|transaction not found|unavailable:tx_not_found|gettransactionbyhash[^a-z]*.*not found`)
	reUnsupportedChain = regexp.MustCompile(`unsupported chain|unknown chain|chain not (supported|configured|found)|unrecognized chain|no rpc.*for chain`)
	reTraceUnavailable = regexp.MustCompile(`debug_tracetransaction|trace_transaction|trace api|tracing not (available|supported|enabled)|trace not (available|supported|enabled)|requires.*archive`)
	reRPCUnavailable   = regexp.MustCompile(`rate.?limit|429|too many requests|connection refused|connection reset|dial tcp|timeout|timed out|i/o timeout|\beof\b|unauthorized|401|403|forbidden|no such host|json-?rpc error`)
	reToolchain        = regexp.MustCompile(`(cast|forge|anvil|node|python3?|solc)[^a-z]*(not found|command not found|no such file)|executable file not found`)
)

// classifyEngineError derives a finer engine-error kind from a nonzero lumoskit
// exit. Returns "" when no specific pattern matches (caller falls back to
// lumoskit_nonzero_exit). Non-retryable kinds are only produced from specific,
// high-confidence patterns so dedup never skips a genuinely transient failure.
func classifyEngineError(in Input) string {
	hay := strings.ToLower(string(in.Stderr))
	if msg := summaryFailureMessage(in); msg != "" {
		hay += "\n" + strings.ToLower(msg)
	}

	// Process failed to start (runner sets ExitCode<0): deployment-level.
	if in.ExitCode < 0 {
		if reToolchain.MatchString(hay) {
			return FailureToolchainError
		}
		return FailureBinaryExecError
	}

	// Process ran but exited nonzero — classify from stderr/summary message.
	// Specific (non-retryable) patterns are checked before the broad RPC one.
	switch {
	case reTxNotFound.MatchString(hay):
		return FailureTxNotFound
	case reUnsupportedChain.MatchString(hay):
		return FailureUnsupportedChain
	case reTraceUnavailable.MatchString(hay):
		return FailureTraceUnavailable
	case reRPCUnavailable.MatchString(hay):
		return FailureRPCUnavailable
	}
	return ""
}

func summaryFailureMessage(in Input) string {
	s, ok := parseSummaryForPayload(in)
	if !ok {
		return ""
	}
	return s.Failure.Message
}

const maxStderrTailBytes = 4096

// Redact obvious secrets before persisting stderr to the case event store.
// Word-boundary anchored so it catches both URL query params (?apikey=...) and
// bare key=value secrets in log lines (apikey=...).
var (
	reRedactSecretKV = regexp.MustCompile(`(?i)\b((?:api[_-]?key|apikey|secret|token|auth|access[_-]?token|password|passwd)=)[^&\s]+`)
	reRedactBearer   = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._\-]+`)
	reRedactRPCKey   = regexp.MustCompile(`(?i)(https?://[^/\s]+/v[0-9]+/)[A-Za-z0-9._\-]{12,}`)
)

// redactStderr returns the last maxStderrTailBytes of stderr with API keys,
// bearer tokens and RPC-path keys redacted, for safe storage in case_events.
func redactStderr(stderr []byte) string {
	if len(stderr) == 0 {
		return ""
	}
	if len(stderr) > maxStderrTailBytes {
		stderr = stderr[len(stderr)-maxStderrTailBytes:]
	}
	out := string(stderr)
	out = reRedactSecretKV.ReplaceAllString(out, "${1}[redacted]")
	out = reRedactBearer.ReplaceAllString(out, "${1}[redacted]")
	out = reRedactRPCKey.ReplaceAllString(out, "${1}[redacted]")
	return out
}
