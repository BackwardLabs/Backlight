// Package lumoskit invokes the sibling LumosKit engine as a child process.
//
// Per ADR-0018 in the lumoskit repo, helios:
//   - spawns bin/lumoskit (no in-process embedding)
//   - passes --tx, --chain, --output-root, and optional --stage flags
//   - does NOT pass any --rpc-url; RPC env vars are inherited unchanged
//   - reads the product run summary after the process exits (any exit code)
package lumoskit

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

// Result captures everything the worker needs to apply outcome mapping.
type Result struct {
	ExitCode       int
	SummaryPath    string // selected expected path; always populated, even when missing/unreadable
	SummaryBytes   []byte // raw bytes when read succeeded
	SummaryMissing bool   // true iff the expected path does not exist after exit
	SummaryReadErr error  // non-nil read errors other than missing
	Stderr         []byte // captured stderr (truncated) for operator diagnosis
}

// RunOptions configures a LumosKit attempt. Empty Stage runs the default
// all-stage pipeline. Stage agent_poc runs agent_poc followed by rca so a PoC
// retry can still produce a complete product summary when it succeeds. Stage
// agent_poc_repair preserves the same follow-on RCA pass while letting LumosKit
// select a repair-oriented agent_poc prompt from the copied signal context.
type RunOptions struct {
	Stage string
}

func (o RunOptions) stages() []string {
	switch o.Stage {
	case "", "all", "pipeline":
		return []string{""}
	case "agent_poc", "poc":
		return []string{"agent_poc", "rca"}
	case "agent_poc_repair", "poc_repair", "economic_proof_repair":
		return []string{"agent_poc_repair", "rca"}
	default:
		return []string{o.Stage}
	}
}

// Runner spawns lumoskit child processes.
type Runner struct {
	// Binary is the lumoskit executable path. Defaults to "bin/lumoskit"
	// resolved against the process working directory; can be overridden via
	// env (HELIOS_LUMOSKIT_BIN) for tests and bundled images.
	Binary string

	// MaxStderrBytes bounds how much stderr we keep for diagnosis (avoids
	// runaway memory when a misbehaving engine spams stderr). 0 keeps a
	// reasonable default.
	MaxStderrBytes int
}

func (r *Runner) bin() string {
	if r.Binary != "" {
		return r.Binary
	}
	return "bin/lumoskit"
}

func (r *Runner) maxStderr() int {
	if r.MaxStderrBytes > 0 {
		return r.MaxStderrBytes
	}
	return 64 << 10 // 64 KiB
}

func (r *Runner) command() (path string, dir string) {
	path = r.bin()
	if filepath.IsAbs(path) {
		return path, lumoskitRootForBin(path)
	}
	if filepath.Dir(path) != "." {
		if abs, err := filepath.Abs(path); err == nil {
			return abs, lumoskitRootForBin(abs)
		}
	}
	return path, ""
}

func lumoskitRootForBin(binary string) string {
	binDir := filepath.Dir(binary)
	if filepath.Base(binDir) != "bin" {
		return ""
	}
	return filepath.Dir(binDir)
}

// Run dispatches a single lumoskit attempt. outputRoot MUST be unique per
// attempt; the directory is created if it does not exist. The function
// always returns a Result so the caller can map outcome even on errors.
//
// ctx cancellation kills the child process (the os/exec wiring sends SIGKILL
// when ctx is Done). The caller is expected to provide a parent ctx that gets
// cancelled on helios shutdown so in-flight cases unwind cleanly.
func (r *Runner) Run(ctx context.Context, chain, txHash, outputRoot string) Result {
	return r.RunWithOptions(ctx, chain, txHash, outputRoot, RunOptions{})
}

// RunWithOptions dispatches one attempt, optionally constrained to a LumosKit
// stage. outputRoot MUST be unique per attempt; for stage resumes, callers
// prepare any reusable upstream artifacts in outputRoot before invoking this.
func (r *Runner) RunWithOptions(ctx context.Context, chain, txHash, outputRoot string, opts RunOptions) Result {
	res := Result{
		SummaryPath: preferredSummaryPath(outputRoot),
	}

	if err := os.MkdirAll(outputRoot, 0o755); err != nil {
		res.SummaryReadErr = err
		return res
	}

	binary, workingDir := r.command()
	stderrBuf := newCappedBuffer(r.maxStderr())
	for _, stage := range opts.stages() {
		args := []string{
			"--tx", txHash,
			"--chain", chain,
			"--output-root", outputRoot,
		}
		if stage != "" {
			args = append([]string{"--stage", stage}, args...)
		}
		cmd := exec.CommandContext(ctx, binary, args...)
		if workingDir != "" {
			cmd.Dir = workingDir
		}
		// Pass through the entire env (lumoskit owns RPC resolution per ADR-0018).
		cmd.Env = os.Environ()
		cmd.Stdout = io.Discard
		cmd.Stderr = stderrBuf

		err := cmd.Run()
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				res.ExitCode = ee.ExitCode()
			} else {
				// process failed to start (e.g. binary missing) — surface as
				// non-zero so outcome mapping classifies as engine_error
				res.ExitCode = -1
				res.SummaryReadErr = err
			}
			break
		}
	}
	res.Stderr = stderrBuf.Bytes()

	// Always try to read the run summary after the child exits, regardless of
	// exit code. New LumosKit writes the product summary under report_bundle;
	// legacy top-level summary.json remains a compatibility fallback.
	summaryPath, data, readErr := readSummary(outputRoot)
	res.SummaryPath = summaryPath
	switch {
	case readErr == nil:
		res.SummaryBytes = data
	case errors.Is(readErr, fs.ErrNotExist):
		res.SummaryMissing = true
	default:
		res.SummaryReadErr = readErr
	}
	return res
}

func preferredSummaryPath(outputRoot string) string {
	return filepath.Join(outputRoot, "report_bundle", "report", "run_summary.json")
}

func summaryCandidates(outputRoot string) []string {
	return []string{
		preferredSummaryPath(outputRoot),
		filepath.Join(outputRoot, "summary.json"),
	}
}

func readSummary(outputRoot string) (string, []byte, error) {
	candidates := summaryCandidates(outputRoot)
	var firstErr error
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err == nil {
			return path, data, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return path, nil, err
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return candidates[0], nil, firstErr
}

// cappedBuffer keeps the LAST `cap` bytes written to it. Useful for stderr
// capture without unbounded memory growth.
type cappedBuffer struct {
	cap  int
	data []byte
}

func newCappedBuffer(cap int) *cappedBuffer { return &cappedBuffer{cap: cap} }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	if len(b.data) > b.cap {
		b.data = b.data[len(b.data)-b.cap:]
	}
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte { return b.data }
