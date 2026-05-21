package githubpublish

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	defaultAPIBase = "https://api.github.com"
	defaultOwner   = "UPside-Lumos-V2"
	defaultRepo    = "Q1-2026"
	defaultBranch  = "main"
)

type Config struct {
	Token   string
	Owner   string
	Repo    string
	Branch  string
	APIBase string
}

type Publisher struct {
	Config Config
	Client *http.Client
}

type Case struct {
	CaseID     string
	Chain      string
	TxHash     string
	OutputRoot string
}

type Result struct {
	CommitSHA  string   `json:"commit_sha"`
	TargetDir  string   `json:"target_dir"`
	PoCURL     string   `json:"poc_url"`
	ReportURL  string   `json:"report_url"`
	CommitURL  string   `json:"commit_url"`
	TargetURLs []string `json:"target_urls"`
}

type targetFile struct {
	Path    string
	Content []byte
}

type targetSpec struct {
	Month    string
	Protocol string
	Files    []targetFile
}

func New(cfg Config) *Publisher {
	cfg.Owner = defaultString(cfg.Owner, defaultOwner)
	cfg.Repo = defaultString(cfg.Repo, defaultRepo)
	cfg.Branch = defaultString(cfg.Branch, defaultBranch)
	cfg.APIBase = strings.TrimRight(defaultString(cfg.APIBase, defaultAPIBase), "/")
	return &Publisher{Config: cfg}
}

func (p *Publisher) Configured() bool {
	return strings.TrimSpace(p.Config.Token) != ""
}

func (p *Publisher) Publish(ctx context.Context, c Case) (*Result, error) {
	if !p.Configured() {
		return nil, errors.New("github publisher is not configured")
	}
	spec, err := buildTargetSpec(c.OutputRoot)
	if err != nil {
		return nil, err
	}
	commitSHA, err := p.commitFiles(ctx, c, spec)
	if err != nil {
		return nil, err
	}
	targetDir := path.Join("test", spec.Month, spec.Protocol)
	pocURL := p.githubBlobURL(spec.Files[0].Path)
	reportURL := p.githubBlobURL(spec.Files[1].Path)
	return &Result{
		CommitSHA: commitSHA,
		TargetDir: targetDir,
		PoCURL:    pocURL,
		ReportURL: reportURL,
		CommitURL: p.githubCommitURL(commitSHA),
		TargetURLs: []string{
			pocURL,
			reportURL,
		},
	}, nil
}

func buildTargetSpec(outputRoot string) (targetSpec, error) {
	outputRoot = strings.TrimSpace(outputRoot)
	if outputRoot == "" {
		return targetSpec{}, errors.New("output_root is required")
	}
	poc, err := os.ReadFile(filepath.Join(outputRoot, "PoC.t.sol"))
	if err != nil {
		return targetSpec{}, fmt.Errorf("read PoC.t.sol: %w", err)
	}
	reportPath, err := findReport(outputRoot)
	if err != nil {
		return targetSpec{}, err
	}
	report, err := os.ReadFile(reportPath)
	if err != nil {
		return targetSpec{}, fmt.Errorf("read Report.md: %w", err)
	}
	summary, _ := os.ReadFile(filepath.Join(outputRoot, "summary.json"))

	month := incidentMonth(summary, report)
	if month == "" {
		return targetSpec{}, errors.New("could not derive incident YYYY-MM from summary.json or Report.md")
	}
	protocol := incidentProtocol(summary, report)
	if protocol == "" {
		return targetSpec{}, errors.New("could not derive protocol from summary.json or Report.md")
	}
	safeProtocol, err := safeSegment(protocol)
	if err != nil {
		return targetSpec{}, fmt.Errorf("invalid protocol %q: %w", protocol, err)
	}

	targetDir := path.Join("test", month, safeProtocol)
	return targetSpec{
		Month:    month,
		Protocol: safeProtocol,
		Files: []targetFile{
			{Path: path.Join(targetDir, safeProtocol+".t.sol"), Content: poc},
			{Path: path.Join(targetDir, "README.md"), Content: report},
		},
	}, nil
}

func findReport(outputRoot string) (string, error) {
	for _, name := range []string{"Report.md", "REPORT.md", "REPORT.MD", "report.md"} {
		candidate := filepath.Join(outputRoot, name)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("Report.md is required for GitHub publish")
}

func (p *Publisher) commitFiles(ctx context.Context, c Case, spec targetSpec) (string, error) {
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}

	ref, err := p.getRef(ctx, client)
	if err != nil {
		return "", err
	}
	commit, err := p.getCommit(ctx, client, ref.Object.SHA)
	if err != nil {
		return "", err
	}

	entries := make([]treeEntry, 0, len(spec.Files))
	for _, file := range spec.Files {
		blob, err := p.createBlob(ctx, client, file.Content)
		if err != nil {
			return "", err
		}
		entries = append(entries, treeEntry{
			Path: file.Path,
			Mode: "100644",
			Type: "blob",
			SHA:  blob.SHA,
		})
	}

	tree, err := p.createTree(ctx, client, commit.Tree.SHA, entries)
	if err != nil {
		return "", err
	}
	message := commitMessage(c, spec)
	next, err := p.createCommit(ctx, client, message, tree.SHA, ref.Object.SHA)
	if err != nil {
		return "", err
	}
	if err := p.updateRef(ctx, client, next.SHA); err != nil {
		return "", err
	}
	return next.SHA, nil
}

func commitMessage(c Case, spec targetSpec) string {
	subject := fmt.Sprintf("Publish verified %s incident artifact bundle", spec.Protocol)
	return fmt.Sprintf(`%s

Constraint: Helios publishes from completed LumosKit product artifacts only
Confidence: medium
Scope-risk: narrow
Directive: Keep Report.md as the README source and PoC.t.sol as the executable source
Tested: Helios read PoC.t.sol and Report.md for case %s before creating this commit
Not-tested: Foundry re-run inside UPside-Lumos-V2/Q1-2026
`, subject, c.CaseID)
}

func (p *Publisher) githubBlobURL(filePath string) string {
	return fmt.Sprintf("https://github.com/%s/%s/blob/%s/%s", p.Config.Owner, p.Config.Repo, p.Config.Branch, filePath)
}

func (p *Publisher) githubCommitURL(sha string) string {
	return fmt.Sprintf("https://github.com/%s/%s/commit/%s", p.Config.Owner, p.Config.Repo, sha)
}

type gitRef struct {
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

type gitCommit struct {
	SHA  string `json:"sha"`
	Tree struct {
		SHA string `json:"sha"`
	} `json:"tree"`
}

type gitBlob struct {
	SHA string `json:"sha"`
}

type treeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

type gitTree struct {
	SHA string `json:"sha"`
}

func (p *Publisher) getRef(ctx context.Context, client *http.Client) (*gitRef, error) {
	var out gitRef
	err := p.doJSON(ctx, client, http.MethodGet, "/git/ref/heads/"+url.PathEscape(p.Config.Branch), nil, &out)
	return &out, err
}

func (p *Publisher) getCommit(ctx context.Context, client *http.Client, sha string) (*gitCommit, error) {
	var out gitCommit
	err := p.doJSON(ctx, client, http.MethodGet, "/git/commits/"+url.PathEscape(sha), nil, &out)
	return &out, err
}

func (p *Publisher) createBlob(ctx context.Context, client *http.Client, content []byte) (*gitBlob, error) {
	body := map[string]string{
		"content":  base64.StdEncoding.EncodeToString(content),
		"encoding": "base64",
	}
	var out gitBlob
	err := p.doJSON(ctx, client, http.MethodPost, "/git/blobs", body, &out)
	return &out, err
}

func (p *Publisher) createTree(ctx context.Context, client *http.Client, baseTree string, entries []treeEntry) (*gitTree, error) {
	body := map[string]any{
		"base_tree": baseTree,
		"tree":      entries,
	}
	var out gitTree
	err := p.doJSON(ctx, client, http.MethodPost, "/git/trees", body, &out)
	return &out, err
}

func (p *Publisher) createCommit(ctx context.Context, client *http.Client, message, treeSHA, parentSHA string) (*gitCommit, error) {
	body := map[string]any{
		"message": message,
		"tree":    treeSHA,
		"parents": []string{parentSHA},
	}
	var out gitCommit
	err := p.doJSON(ctx, client, http.MethodPost, "/git/commits", body, &out)
	return &out, err
}

func (p *Publisher) updateRef(ctx context.Context, client *http.Client, sha string) error {
	body := map[string]any{
		"sha":   sha,
		"force": false,
	}
	return p.doJSON(ctx, client, http.MethodPatch, "/git/refs/heads/"+url.PathEscape(p.Config.Branch), body, nil)
}

func (p *Publisher) doJSON(ctx context.Context, client *http.Client, method, suffix string, body any, out any) error {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.apiURL(suffix), payload)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "helios-github-publisher/1")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+p.Config.Token)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github %s %s returned HTTP %d: %s", method, suffix, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode github %s %s response: %w", method, suffix, err)
	}
	return nil
}

func (p *Publisher) apiURL(suffix string) string {
	return fmt.Sprintf("%s/repos/%s/%s%s", p.Config.APIBase, url.PathEscape(p.Config.Owner), url.PathEscape(p.Config.Repo), suffix)
}

var monthRe = regexp.MustCompile(`\b(20[0-9]{2})-(0[1-9]|1[0-2])(?:-[0-3][0-9])?\b`)

func incidentMonth(summary, report []byte) string {
	if month := firstMonth(string(report)); month != "" {
		return month
	}
	var parsed any
	if len(summary) > 0 && json.Unmarshal(summary, &parsed) == nil {
		if month := monthFromJSON(parsed); month != "" {
			return month
		}
		if month := firstMonth(string(summary)); month != "" {
			return month
		}
	}
	return ""
}

func firstMonth(text string) string {
	match := monthRe.FindStringSubmatch(text)
	if len(match) >= 3 {
		return match[1] + "-" + match[2]
	}
	return ""
}

func monthFromJSON(v any) string {
	var walk func(any) string
	walk = func(node any) string {
		switch x := node.(type) {
		case map[string]any:
			for k, val := range x {
				kl := strings.ToLower(k)
				if strings.Contains(kl, "created_at") {
					continue
				}
				if strings.Contains(kl, "date") || strings.Contains(kl, "timestamp") || strings.Contains(kl, "time") {
					if month := monthFromValue(val); month != "" {
						return month
					}
				}
			}
			for _, val := range x {
				if month := walk(val); month != "" {
					return month
				}
			}
		case []any:
			for _, val := range x {
				if month := walk(val); month != "" {
					return month
				}
			}
		}
		return ""
	}
	return walk(v)
}

func monthFromValue(v any) string {
	switch x := v.(type) {
	case string:
		if month := firstMonth(x); month != "" {
			return month
		}
	case float64:
		return monthFromUnix(x)
	case json.Number:
		n, _ := x.Float64()
		return monthFromUnix(n)
	}
	return ""
}

func monthFromUnix(n float64) string {
	if n <= 0 {
		return ""
	}
	seconds := int64(n)
	if seconds > 10_000_000_000 {
		seconds = seconds / 1000
	}
	return time.Unix(seconds, 0).UTC().Format("2006-01")
}

var protocolLineRe = regexp.MustCompile(`(?im)^\s*@?protocol(?:\s+name)?\s*[:|-]\s*([A-Za-z0-9][A-Za-z0-9 ._/-]*)\s*$`)
var headingRe = regexp.MustCompile(`(?m)^#\s+(.+?)\s*$`)

func incidentProtocol(summary, report []byte) string {
	if protocol := protocolFromReport(string(report)); protocol != "" {
		return protocol
	}
	var parsed any
	if len(summary) > 0 && json.Unmarshal(summary, &parsed) == nil {
		if protocol := protocolFromJSON(parsed); protocol != "" {
			return protocol
		}
	}
	return ""
}

func protocolFromReport(report string) string {
	if match := protocolLineRe.FindStringSubmatch(report); len(match) == 2 {
		return cleanProtocol(match[1])
	}
	for _, match := range headingRe.FindAllStringSubmatch(report, 5) {
		if len(match) != 2 {
			continue
		}
		candidate := cleanProtocol(match[1])
		lower := strings.ToLower(candidate)
		candidate = strings.TrimSpace(strings.NewReplacer(
			"Incident Report", "",
			"incident report", "",
			"Exploit Report", "",
			"exploit report", "",
			"Report", "",
			"report", "",
		).Replace(candidate))
		if candidate != "" && !strings.Contains(lower, "summary") && !strings.Contains(lower, "root cause") {
			return candidate
		}
	}
	return ""
}

func protocolFromJSON(v any) string {
	var walk func(any) string
	walk = func(node any) string {
		switch x := node.(type) {
		case map[string]any:
			for k, val := range x {
				kl := strings.ToLower(k)
				if kl == "protocol" || kl == "protocol_name" || kl == "project" || kl == "project_name" {
					if s, ok := val.(string); ok {
						return cleanProtocol(s)
					}
				}
			}
			for _, val := range x {
				if protocol := walk(val); protocol != "" {
					return protocol
				}
			}
		case []any:
			for _, val := range x {
				if protocol := walk(val); protocol != "" {
					return protocol
				}
			}
		}
		return ""
	}
	return walk(v)
}

func cleanProtocol(s string) string {
	s = strings.TrimSpace(s)
	for _, sep := range []string{"|", " — ", " - ", " -- "} {
		if idx := strings.Index(s, sep); idx >= 0 {
			s = strings.TrimSpace(s[:idx])
		}
	}
	return strings.Trim(s, "`*_ ")
}

func safeSegment(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("empty segment")
	}
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || unicode.IsSpace(r) || r == '/' || r == '\\':
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), ".-_")
	if out == "" || out == "." || out == ".." || strings.Contains(out, "/") || strings.Contains(out, "\\") {
		return "", errors.New("segment is not a safe path component")
	}
	return out, nil
}

func defaultString(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}
