// Package lumoskit invokes the sibling LumosKit engine as a child process.
//
// Per ADR-0018 in the lumoskit repo, helios:
//   - spawns bin/lumoskit (no in-process embedding)
//   - passes --tx, --chain, --output-root flags only
//   - does NOT pass any --rpc-url; RPC env vars are inherited unchanged
//   - reads <output-root>/summary.json after the process exits (any exit code)
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
	SummaryPath    string // expected path; always populated, even when missing/unreadable
	SummaryBytes   []byte // raw bytes when read succeeded
	SummaryMissing bool   // true iff the expected path does not exist after exit
	SummaryReadErr error  // non-nil read errors other than missing
	Stderr         []byte // captured stderr (truncated) for operator diagnosis
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

// Run dispatches a single lumoskit attempt. outputRoot MUST be unique per
// attempt; the directory is created if it does not exist. The function
// always returns a Result so the caller can map outcome even on errors.
//
// ctx cancellation kills the child process (the os/exec wiring sends SIGKILL
// when ctx is Done). The caller is expected to provide a parent ctx that gets
// cancelled on helios shutdown so in-flight cases unwind cleanly.
func (r *Runner) Run(ctx context.Context, chain, txHash, outputRoot string) Result {
	res := Result{
		SummaryPath: filepath.Join(outputRoot, "summary.json"),
	}

	if err := os.MkdirAll(outputRoot, 0o755); err != nil {
		res.SummaryReadErr = err
		return res
	}

	cmd := exec.CommandContext(ctx, r.bin(),
		"--tx", txHash,
		"--chain", chain,
		"--output-root", outputRoot,
	)
	// Pass through the entire env (lumoskit owns RPC resolution per ADR-0018).
	cmd.Env = os.Environ()
	cmd.Stdout = io.Discard

	stderrBuf := newCappedBuffer(r.maxStderr())
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
	}
	res.Stderr = stderrBuf.Bytes()

	// Always try to read summary.json after the child exits, regardless of
	// exit code. The seed mapping rules need both signals to classify O4/O7/O8.
	data, readErr := os.ReadFile(res.SummaryPath)
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
