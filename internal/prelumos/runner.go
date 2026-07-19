// Package prelumos invokes the vendored Pre-Lumos Codex SDK sidecar.
//
// The skill bundle remains under skills/pre-lumos and is treated as read-only
// prompt/context material. Backlight only decides when to run the sidecar and
// records its sync result on the case timeline.
package prelumos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

type Case struct {
	CaseID     string
	OutputRoot string
}

type Result struct {
	ExitCode    int
	StatusPath  string
	OutputPath  string
	RowCount    int
	Slugs       []string
	TargetFiles []string
	DryRun      bool
	Stdout      []byte
	Stderr      []byte
}

type Runner struct {
	Enabled   bool
	PythonBin string
	Script    string
	SkillDir  string
	SeedRoot  string
	Year      string
	Model     string
	WebSearch bool

	MaxStdoutBytes int
	MaxStderrBytes int
}

func (r *Runner) Configured() bool {
	return r != nil && r.Enabled && r.SeedRoot != ""
}

func (r *Runner) pythonBin() string {
	if r.PythonBin != "" {
		return r.PythonBin
	}
	return "python3"
}

func (r *Runner) script() string {
	if r.Script != "" {
		return r.Script
	}
	return "scripts/pre_lumos_agent.py"
}

func (r *Runner) skillDir() string {
	if r.SkillDir != "" {
		return r.SkillDir
	}
	return "skills/pre-lumos"
}

func (r *Runner) maxStdout() int {
	if r.MaxStdoutBytes > 0 {
		return r.MaxStdoutBytes
	}
	return 256 << 10
}

func (r *Runner) maxStderr() int {
	if r.MaxStderrBytes > 0 {
		return r.MaxStderrBytes
	}
	return 128 << 10
}

func (r *Runner) Run(ctx context.Context, c Case) (Result, error) {
	res := Result{}
	if !r.Configured() {
		return res, errors.New("pre-lumos runner is not configured")
	}
	if c.OutputRoot == "" {
		return res, errors.New("pre-lumos case output root is required")
	}
	if err := os.MkdirAll(c.OutputRoot, 0o755); err != nil {
		return res, fmt.Errorf("create output root: %w", err)
	}

	statusPath := filepath.Join(c.OutputRoot, "pre-lumos-status.json")
	outputPath := filepath.Join(c.OutputRoot, "pre-lumos.json")
	res.StatusPath = statusPath
	res.OutputPath = outputPath

	args := []string{
		r.script(),
		"--case-output-root", c.OutputRoot,
		"--seed-root", r.SeedRoot,
		"--skill-dir", r.skillDir(),
		"--output-path", outputPath,
		"--status-path", statusPath,
	}
	if r.Year != "" {
		args = append(args, "--year", r.Year)
	}
	if r.Model != "" {
		args = append(args, "--model", r.Model)
	}
	if r.WebSearch {
		args = append(args, "--web-search")
	} else {
		args = append(args, "--no-web-search")
	}

	cmd := exec.CommandContext(ctx, r.pythonBin(), args...)
	cmd.Env = os.Environ()
	stdoutBuf := newCappedBuffer(r.maxStdout())
	stderrBuf := newCappedBuffer(r.maxStderr())
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	err := cmd.Run()
	res.Stdout = stdoutBuf.Bytes()
	res.Stderr = stderrBuf.Bytes()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
			return res, fmt.Errorf("start pre-lumos agent: %w", err)
		}
	} else {
		res.ExitCode = 0
	}

	if err := readStatus(statusPath, &res); err != nil && res.ExitCode == 0 {
		return res, err
	}
	if res.ExitCode != 0 {
		return res, fmt.Errorf("pre-lumos agent exited with code %d", res.ExitCode)
	}
	return res, nil
}

func readStatus(path string, res *Result) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read pre-lumos status: %w", err)
	}
	var status struct {
		RowCount    int      `json:"row_count"`
		Slugs       []string `json:"slugs"`
		TargetFiles []string `json:"target_files"`
		OutputPath  string   `json:"output_path"`
		DryRun      bool     `json:"dry_run"`
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return fmt.Errorf("parse pre-lumos status: %w", err)
	}
	res.RowCount = status.RowCount
	res.Slugs = status.Slugs
	res.TargetFiles = status.TargetFiles
	if status.OutputPath != "" {
		res.OutputPath = status.OutputPath
	}
	res.DryRun = status.DryRun
	return nil
}

type cappedBuffer struct {
	cap  int
	data []byte
}

func newCappedBuffer(cap int) *cappedBuffer { return &cappedBuffer{cap: cap} }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.cap <= 0 {
		return io.Discard.Write(p)
	}
	b.data = append(b.data, p...)
	if len(b.data) > b.cap {
		b.data = b.data[len(b.data)-b.cap:]
	}
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte { return b.data }
