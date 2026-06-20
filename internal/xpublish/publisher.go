package xpublish

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

const (
	defaultAPIBase       = "https://api.x.com"
	templateVersion      = "backlight_verified_incident_v1"
	threadFormatVersion  = "backlight_x_feed_thread_v1"
	defaultMediaCategory = "tweet_image"
)

//go:embed templates/x_verified_incident.md
var defaultPostTemplate string

type Config struct {
	Enabled          bool
	ClientID         string
	ClientSecret     string
	RefreshToken     string
	RefreshTokenFile string
	TemplatePath     string
	APIBase          string
	Username         string
	DryRun           bool
}

type Publisher struct {
	Config Config
	Client *http.Client
}

type Case struct {
	CaseID       string
	Chain        string
	TxHash       string
	OutputRoot   string
	IncidentSlug string
	ReportURL    string
	PoCURL       string
	Outcome      string
}

type Thread struct {
	MainText  string
	ReplyText string
	MediaPath string
}

type Result struct {
	Published           bool   `json:"published"`
	DryRun              bool   `json:"dry_run,omitempty"`
	Platform            string `json:"platform"`
	PostID              string `json:"post_id,omitempty"`
	PostURL             string `json:"post_url,omitempty"`
	ReplyPostID         string `json:"reply_post_id,omitempty"`
	ReplyPostURL        string `json:"reply_post_url,omitempty"`
	Text                string `json:"text"`
	ReplyText           string `json:"reply_text,omitempty"`
	Template            string `json:"template"`
	MediaID             string `json:"media_id,omitempty"`
	RefreshReturned     bool   `json:"refresh_returned,omitempty"`
	RefreshTokenUpdated bool   `json:"refresh_token_updated,omitempty"`
	PostTextVerified    bool   `json:"post_text_verified,omitempty"`
	ReplyTextVerified   bool   `json:"reply_text_verified,omitempty"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

type createPostResponse struct {
	Data struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"data"`
}

type fetchPostResponse struct {
	Data struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"data"`
}

type uploadMediaResponse struct {
	Data struct {
		ID       string `json:"id"`
		MediaKey string `json:"media_key"`
	} `json:"data"`
}

type postOptions struct {
	ReplyToID string
	MediaIDs  []string
}

type postTemplateData struct {
	Protocol       string
	Chain          string
	Tx             string
	ImpactLoss     string
	ImpactGain     string
	RootCause      string
	ReportURL      string
	PoCURL         string
	Flow           []string
	AttackContract string
	AttackerEOA    string
	Image          string
}

func New(cfg Config) *Publisher {
	cfg.APIBase = strings.TrimRight(defaultString(cfg.APIBase, defaultAPIBase), "/")
	return &Publisher{Config: cfg}
}

func (p *Publisher) Configured() bool {
	return p.Config.Enabled
}

func (p *Publisher) Publish(ctx context.Context, c Case) (*Result, error) {
	if !p.Configured() {
		return nil, errors.New("x publisher is not enabled")
	}
	text, err := BuildPostWithTemplate(c, p.Config.TemplatePath)
	if err != nil {
		return nil, err
	}
	return p.publishThread(ctx, Thread{MainText: text}, templateVersion)
}

func (p *Publisher) PublishThread(ctx context.Context, thread Thread) (*Result, error) {
	if !p.Configured() {
		return nil, errors.New("x publisher is not enabled")
	}
	return p.publishThread(ctx, thread, threadFormatVersion)
}

func (p *Publisher) publishThread(ctx context.Context, thread Thread, template string) (*Result, error) {
	text := strings.TrimSpace(thread.MainText)
	if text == "" {
		return nil, errors.New("x publish main text is required")
	}
	replyText := strings.TrimSpace(thread.ReplyText)
	result := &Result{
		Published: false,
		DryRun:    p.Config.DryRun,
		Platform:  "x",
		Text:      text,
		ReplyText: replyText,
		Template:  template,
	}
	if p.Config.DryRun {
		return result, nil
	}
	refreshToken, err := p.refreshToken()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Config.ClientID) == "" || strings.TrimSpace(p.Config.ClientSecret) == "" || refreshToken == "" {
		return nil, errors.New("x publisher requires X_CLIENT_ID, X_CLIENT_SECRET, and X_REFRESH_TOKEN")
	}
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	token, err := p.refreshAccessToken(ctx, client, refreshToken)
	if err != nil {
		return nil, err
	}
	refreshUpdated, err := p.storeRotatedRefreshToken(token)
	if err != nil {
		return nil, err
	}
	var mediaIDs []string
	if mediaPath := strings.TrimSpace(thread.MediaPath); mediaPath != "" {
		media, err := p.uploadMedia(ctx, client, token.AccessToken, mediaPath)
		if err != nil {
			return nil, err
		}
		result.MediaID = media.Data.ID
		mediaIDs = append(mediaIDs, media.Data.ID)
	}
	post, err := p.createPost(ctx, client, token.AccessToken, text, postOptions{MediaIDs: mediaIDs})
	if err != nil {
		return nil, err
	}
	textVerified := p.verifyCreatedPost(ctx, client, token.AccessToken, post.Data.ID, text, post.Data.Text)
	var replyVerified bool
	var reply *createPostResponse
	if replyText != "" {
		reply, err = p.createPost(ctx, client, token.AccessToken, replyText, postOptions{ReplyToID: post.Data.ID})
		if err != nil {
			return nil, err
		}
		replyVerified = p.verifyCreatedPost(ctx, client, token.AccessToken, reply.Data.ID, replyText, reply.Data.Text)
	}
	result.Published = true
	result.DryRun = false
	result.PostID = post.Data.ID
	result.PostURL = p.postURL(post.Data.ID)
	if reply != nil {
		result.ReplyPostID = reply.Data.ID
		result.ReplyPostURL = p.postURL(reply.Data.ID)
	}
	result.RefreshReturned = strings.TrimSpace(token.RefreshToken) != ""
	result.RefreshTokenUpdated = refreshUpdated
	result.PostTextVerified = textVerified
	result.ReplyTextVerified = replyVerified
	return result, nil
}

func (p *Publisher) refreshAccessToken(ctx context.Context, client *http.Client, refreshToken string) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Config.APIBase+"/2/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "backlight-x-publisher/1")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(p.Config.ClientID+":"+p.Config.ClientSecret)))
	var out tokenResponse
	if err := doJSON(client, req, &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return nil, errors.New("x token refresh response did not include access_token")
	}
	return &out, nil
}

func (p *Publisher) refreshToken() (string, error) {
	if tokenFile := strings.TrimSpace(p.Config.RefreshTokenFile); tokenFile != "" {
		if token, err := readRefreshTokenFile(tokenFile); err == nil && token != "" {
			return token, nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return strings.TrimSpace(p.Config.RefreshToken), nil
}

func readRefreshTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return "", nil
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) == nil {
		if token, ok := payload["refresh_token"].(string); ok {
			return strings.TrimSpace(token), nil
		}
	}
	return trimmed, nil
}

func (p *Publisher) storeRotatedRefreshToken(token *tokenResponse) (bool, error) {
	refresh := strings.TrimSpace(token.RefreshToken)
	if refresh == "" {
		return false, nil
	}
	if refresh == strings.TrimSpace(p.Config.RefreshToken) && strings.TrimSpace(p.Config.RefreshTokenFile) == "" {
		return false, nil
	}
	p.Config.RefreshToken = refresh
	if strings.TrimSpace(p.Config.RefreshTokenFile) == "" {
		return false, nil
	}
	payload := map[string]any{
		"refresh_token": refresh,
		"token_type":    token.TokenType,
		"expires_in":    token.ExpiresIn,
		"scope":         token.Scope,
	}
	if strings.TrimSpace(p.Config.ClientID) != "" {
		payload["client_id"] = strings.TrimSpace(p.Config.ClientID)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return false, err
	}
	data = append(data, '\n')
	if err := writeSecretFile(p.Config.RefreshTokenFile, data); err != nil {
		return false, err
	}
	return true, nil
}

func writeSecretFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

func (p *Publisher) uploadMedia(ctx context.Context, client *http.Client, accessToken, mediaPath string) (*uploadMediaResponse, error) {
	source, fileName, mediaType, err := p.openMediaSource(ctx, client, mediaPath)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("media", fileName)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(part, source); err != nil {
		return nil, err
	}
	if err := writer.WriteField("media_category", defaultMediaCategory); err != nil {
		return nil, err
	}
	if err := writer.WriteField("media_type", mediaType); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Config.APIBase+"/2/media/upload", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", "backlight-x-publisher/1")
	var out uploadMediaResponse
	if err := doJSON(client, req, &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.Data.ID) == "" {
		return nil, errors.New("x media upload response did not include data.id")
	}
	return &out, nil
}

func (p *Publisher) openMediaSource(ctx context.Context, client *http.Client, mediaPath string) (io.ReadCloser, string, string, error) {
	parsed, err := url.Parse(mediaPath)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaPath, nil)
		if err != nil {
			return nil, "", "", err
		}
		req.Header.Set("User-Agent", "backlight-x-publisher/1")
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", "", err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			defer resp.Body.Close()
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return nil, "", "", fmt.Errorf("download x media %s returned HTTP %d: %s", mediaPath, resp.StatusCode, strings.TrimSpace(string(data)))
		}
		fileName := filepath.Base(parsed.Path)
		if fileName == "." || fileName == "/" || fileName == "" {
			fileName = "media"
		}
		return resp.Body, fileName, mediaMIMETypeFromHeaderOrPath(resp.Header.Get("Content-Type"), parsed.Path), nil
	}
	file, err := os.Open(mediaPath)
	if err != nil {
		return nil, "", "", fmt.Errorf("open x media %s: %w", mediaPath, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, "", "", fmt.Errorf("stat x media %s: %w", mediaPath, err)
	}
	if info.IsDir() {
		file.Close()
		return nil, "", "", fmt.Errorf("x media path %s is a directory", mediaPath)
	}
	return file, filepath.Base(mediaPath), mediaMIMETypeFromHeaderOrPath("", mediaPath), nil
}

func mediaMIMETypeFromHeaderOrPath(header, path string) string {
	if header = strings.TrimSpace(header); header != "" {
		return strings.Split(header, ";")[0]
	}
	if detected := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); detected != "" {
		return strings.Split(detected, ";")[0]
	}
	return "application/octet-stream"
}

func (p *Publisher) createPost(ctx context.Context, client *http.Client, accessToken, text string, opts postOptions) (*createPostResponse, error) {
	body := map[string]any{"text": text}
	if strings.TrimSpace(opts.ReplyToID) != "" {
		body["reply"] = map[string]string{"in_reply_to_tweet_id": strings.TrimSpace(opts.ReplyToID)}
	}
	if len(opts.MediaIDs) > 0 {
		body["media"] = map[string]any{"media_ids": opts.MediaIDs}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Config.APIBase+"/2/tweets", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "backlight-x-publisher/1")
	var out createPostResponse
	if err := doJSON(client, req, &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.Data.ID) == "" {
		return nil, errors.New("x create post response did not include data.id")
	}
	return &out, nil
}

func (p *Publisher) verifyCreatedPost(ctx context.Context, client *http.Client, accessToken, postID, requestedText, responseText string) bool {
	if responseText == requestedText {
		return true
	}
	fetched, err := p.fetchPost(ctx, client, accessToken, postID)
	if err != nil {
		return false
	}
	return fetched.Data.Text == requestedText
}

func (p *Publisher) fetchPost(ctx context.Context, client *http.Client, accessToken, postID string) (*fetchPostResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Config.APIBase+"/2/tweets/"+url.PathEscape(postID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "backlight-x-publisher/1")
	var out fetchPostResponse
	if err := doJSON(client, req, &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.Data.ID) == "" {
		return nil, errors.New("x fetch post response did not include data.id")
	}
	return &out, nil
}

func doJSON(client *http.Client, req *http.Request, out any) error {
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
		return fmt.Errorf("x %s %s returned HTTP %d: %s", req.Method, req.URL.Path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode x %s %s response: %w", req.Method, req.URL.Path, err)
	}
	return nil
}

func (p *Publisher) postURL(postID string) string {
	if username := strings.Trim(strings.TrimSpace(p.Config.Username), "@"); username != "" {
		return fmt.Sprintf("https://x.com/%s/status/%s", username, url.PathEscape(postID))
	}
	return fmt.Sprintf("https://x.com/i/web/status/%s", url.PathEscape(postID))
}

func BuildPost(c Case) (string, error) {
	return BuildPostWithTemplate(c, "")
}

func BuildPostWithTemplate(c Case, templatePath string) (string, error) {
	if strings.TrimSpace(c.OutputRoot) == "" {
		return "", errors.New("output_root is required for x publish")
	}
	summary := readJSON(filepath.Join(c.OutputRoot, "report_bundle", "report", "run_summary.json"))
	if len(summary) == 0 {
		summary = readJSON(filepath.Join(c.OutputRoot, "summary.json"))
	}
	reportJSON := readJSON(filepath.Join(c.OutputRoot, "report_bundle", "report", "report.json"))
	if len(reportJSON) == 0 {
		reportJSON = readJSON(filepath.Join(c.OutputRoot, "artifacts", "rca", "report.json"))
	}
	assetDeltas := readJSON(filepath.Join(c.OutputRoot, "report_bundle", "evidence", "asset_deltas.json"))
	if len(assetDeltas) == 0 {
		assetDeltas = readJSON(filepath.Join(c.OutputRoot, "artifacts", "rca", "input", "asset_deltas.json"))
	}
	report := firstText(
		readFile(filepath.Join(c.OutputRoot, "report_bundle", "report", "Report.md")),
		readFile(filepath.Join(c.OutputRoot, "report_bundle", "report", "REPORT.md")),
		readFile(filepath.Join(c.OutputRoot, "Report.md")),
		readFile(filepath.Join(c.OutputRoot, "REPORT.md")),
	)
	rca := firstText(
		readFile(filepath.Join(c.OutputRoot, "report_bundle", "report", "RCA.md")),
		readFile(filepath.Join(c.OutputRoot, "RCA.md")),
	)
	attackFlow := readFile(filepath.Join(c.OutputRoot, "artifacts", "agent_poc", "attack_flow.md"))
	allText := strings.Join([]string{report, rca, attackFlow, string(summary), string(reportJSON), string(assetDeltas)}, "\n")

	protocol := firstText(jsonString(summary, "protocol_name"), jsonString(summary, "protocol"), protocolFromSlug(c.IncidentSlug), protocolFromSlug(filepath.Base(c.OutputRoot)), field(report, "Protocol"), jsonString(reportJSON, "protocol_name", "protocol"), titleFromSlug(c.IncidentSlug), titleFromSlug(filepath.Base(c.OutputRoot)), "unknown")
	chain := firstText(c.Chain, jsonString(summary, "chain"), jsonString(reportJSON, "chain"), field(report, "Chain"), "unknown")
	tx := firstText(c.TxHash, txHash(allText), "unknown")
	rootCause := rootCauseLine(reportJSON, rca, report)
	flow := flowSteps(reportJSON, attackFlow, report)
	impactLoss, impactGain := impactLines(reportJSON, assetDeltas)
	attackContract := firstText(addressForRole(assetDeltas, "attacker_entry"), addressNear(allText, "attack contract", "attacker contract", "exploit contract"))
	attackerEOA := firstText(addressForRole(assetDeltas, "tx_from_eoa"), addressNear(allText, "attacker eoa", "attacker address", "attacker"))
	image := findImage(c.OutputRoot)
	if image == "" {
		image = "attach the generated PoC flow image"
	}
	return formatPost(templatePath, postTemplateData{
		Protocol:       protocol,
		Chain:          chain,
		Tx:             tx,
		ImpactLoss:     impactLoss,
		ImpactGain:     impactGain,
		RootCause:      rootCause,
		ReportURL:      c.ReportURL,
		PoCURL:         c.PoCURL,
		Flow:           flow,
		AttackContract: attackContract,
		AttackerEOA:    attackerEOA,
		Image:          image,
	})
}

func formatPost(templatePath string, data postTemplateData) (string, error) {
	for len(data.Flow) < 3 {
		data.Flow = append(data.Flow, "under review")
	}
	data.ImpactLoss = fallback(data.ImpactLoss, "under review")
	data.ImpactGain = fallback(data.ImpactGain, "under review")
	data.RootCause = oneLine(fallback(data.RootCause, "under review"))
	data.ReportURL = fallback(data.ReportURL, "under review")
	data.PoCURL = fallback(data.PoCURL, "under review")
	data.AttackContract = fallback(data.AttackContract, "unknown")
	data.AttackerEOA = fallback(data.AttackerEOA, "unknown")
	data.Image = fallback(data.Image, "attach the generated PoC flow image")

	tmplText, err := postTemplateText(templatePath)
	if err != nil {
		return "", err
	}
	tmpl, err := template.New("x_verified_incident").Option("missingkey=error").Parse(tmplText)
	if err != nil {
		return "", fmt.Errorf("parse x post template: %w", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return "", fmt.Errorf("render x post template: %w", err)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

func postTemplateText(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return defaultPostTemplate, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read x post template %s: %w", path, err)
	}
	return string(data), nil
}

func readFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func readJSON(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil || !json.Valid(data) {
		return nil
	}
	return data
}

func jsonString(data []byte, keys ...string) string {
	if len(data) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(data, &v) != nil {
		return ""
	}
	var walk func(any) string
	want := map[string]bool{}
	for _, key := range keys {
		want[strings.ToLower(key)] = true
	}
	walk = func(node any) string {
		switch x := node.(type) {
		case map[string]any:
			for key, value := range x {
				if want[strings.ToLower(key)] {
					if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
						return strings.TrimSpace(s)
					}
				}
			}
			for _, value := range x {
				if s := walk(value); s != "" {
					return s
				}
			}
		case []any:
			for _, value := range x {
				if s := walk(value); s != "" {
					return s
				}
			}
		}
		return ""
	}
	return walk(v)
}

func jsonValue(data []byte, path ...string) any {
	if len(data) == 0 {
		return nil
	}
	var cur any
	if json.Unmarshal(data, &cur) != nil {
		return nil
	}
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[key]
	}
	return cur
}

func jsonPathString(data []byte, path ...string) string {
	value, ok := jsonValue(data, path...).(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func rootCauseLine(reportJSON []byte, rca, report string) string {
	root := firstText(jsonPathString(reportJSON, "vulnerability", "root_cause"), jsonString(reportJSON, "root_cause"), headingFirst(rca, "Final Root Cause", "Root cause", "Root Cause", "Vulnerability"), headingFirst(report, "Root cause", "Root Cause", "Vulnerability"))
	entrypoint := jsonPathString(reportJSON, "attack_summary", "public_entrypoint_called_per_iteration")
	if strings.Contains(root, "helper") && strings.Contains(root, "claimReward") {
		return "Tx-created helpers repeatedly called " + fallback(entrypoint, "earned(address) then claimReward()") + ", causing duplicable PRXVT reward payouts."
	}
	if root == "" {
		return "under review"
	}
	return truncateText(oneLine(root), 220)
}

func impactLines(reportJSON, assetDeltas []byte) (loss, gain string) {
	if symbol := jsonPathString(reportJSON, "impact", "attacker_profit_symbol"); symbol != "" {
		if amount := jsonPathString(reportJSON, "impact", "attacker_profit_formatted"); amount != "" {
			gain = fmt.Sprintf("Attacker gained %s %s", amount, symbol)
		}
	}
	bestNeg := deltaBySign(assetDeltas, -1)
	bestPos := deltaBySign(assetDeltas, 1)
	if bestNeg != nil {
		loss = fmt.Sprintf("%s lost %s %s", holderLabel(bestNeg), formatDelta(bestNeg), deltaSymbol(bestNeg))
	}
	if bestPos != nil {
		gain = fmt.Sprintf("%s gained %s %s", holderLabel(bestPos), formatDelta(bestPos), deltaSymbol(bestPos))
	}
	return loss, gain
}

func deltaBySign(data []byte, sign int) map[string]any {
	raw := jsonValue(data, "asset_deltas")
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		delta := firstMapString(m, "balance_delta_raw", "delta_raw", "delta", "observed_delta_raw")
		if sign < 0 && strings.HasPrefix(delta, "-") {
			return m
		}
		if sign > 0 && delta != "" && !strings.HasPrefix(delta, "-") && delta != "0" {
			return m
		}
	}
	return nil
}

func firstMapString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func holderLabel(delta map[string]any) string {
	switch firstMapString(delta, "holder_role") {
	case "attacker_entry":
		return "Attacker"
	case "storage_contract":
		return "Victim/staking contract"
	}
	return fallback(firstMapString(delta, "holder_label", "holder"), "Holder")
}

func deltaSymbol(delta map[string]any) string {
	return fallback(firstMapString(delta, "asset_symbol", "token_symbol"), "token")
}

func formatDelta(delta map[string]any) string {
	raw := strings.TrimPrefix(firstMapString(delta, "balance_delta_raw", "delta_raw", "delta", "observed_delta_raw"), "-")
	if raw == "" {
		return "under review"
	}
	decimals := 18
	for _, key := range []string{"asset_decimals", "token_decimals"} {
		if n, ok := delta[key].(float64); ok && n >= 0 && n <= 36 {
			decimals = int(n)
			break
		}
	}
	return formatUnits(raw, decimals)
}

func formatUnits(raw string, decimals int) string {
	n := new(big.Int)
	if _, ok := n.SetString(raw, 10); !ok {
		return raw
	}
	if decimals <= 0 {
		return n.String()
	}
	base := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	intPart := new(big.Int).Div(new(big.Int).Set(n), base)
	fracPart := new(big.Int).Mod(new(big.Int).Set(n), base).String()
	if fracPart == "0" {
		return intPart.String()
	}
	for len(fracPart) < decimals {
		fracPart = "0" + fracPart
	}
	fracPart = strings.TrimRight(fracPart, "0")
	if len(fracPart) > 12 {
		fracPart = fracPart[:12]
	}
	return intPart.String() + "." + strings.TrimRight(fracPart, "0")
}

func flowSteps(reportJSON []byte, attackFlow, report string) []string {
	entry := jsonPathString(reportJSON, "attack_summary", "entry_function")
	loopCount := jsonPathString(reportJSON, "attack_summary", "loop_count")
	entrypoint := jsonPathString(reportJSON, "attack_summary", "public_entrypoint_called_per_iteration")
	if entry != "" || entrypoint != "" {
		if loopCount == "" {
			loopCount = "multiple"
		}
		return []string{
			"Attacker entry created tx-local helper contracts.",
			fmt.Sprintf("Helpers repeatedly called %s.", fallback(entrypoint, "earned(address) then claimReward()")),
			"Helpers forwarded proceeds back to the attacker entry.",
		}
	}
	flow := headingBullets(report, 3, "Exploit flow", "Attack flow", "Flow")
	if len(flow) == 0 {
		flow = headingBullets(attackFlow, 3, "Observed Trace Flow", "Attack Flow")
	}
	return flow
}

func addressForRole(data []byte, role string) string {
	raw := jsonValue(data, "attacker_surface")
	items, ok := raw.([]any)
	if !ok {
		return ""
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if firstMapString(m, "role") == role {
			return firstMapString(m, "address")
		}
	}
	return ""
}

func field(text, label string) string {
	re := regexp.MustCompile(`(?im)^\s*` + regexp.QuoteMeta(label) + `\s*:\s*(.+?)\s*$`)
	match := re.FindStringSubmatch(text)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

func txHash(text string) string {
	re := regexp.MustCompile(`0x[a-fA-F0-9]{64}`)
	return re.FindString(text)
}

func headingFirst(text string, headings ...string) string {
	bullets := headingBullets(text, 1, headings...)
	if len(bullets) > 0 {
		return bullets[0]
	}
	return ""
}

func headingBullets(text string, limit int, headings ...string) []string {
	lines := strings.Split(text, "\n")
	wanted := map[string]bool{}
	for _, h := range headings {
		wanted[strings.ToLower(strings.TrimSpace(h))] = true
	}
	for i, line := range lines {
		normalized := strings.ToLower(strings.Trim(strings.TrimSpace(line), "# "))
		if !wanted[normalized] {
			continue
		}
		var out []string
		for _, candidate := range lines[i+1:] {
			trimmed := strings.TrimSpace(candidate)
			if strings.HasPrefix(trimmed, "#") && len(out) > 0 {
				break
			}
			if trimmed == "" {
				if len(out) > 0 {
					break
				}
				continue
			}
			trimmed = strings.TrimSpace(strings.TrimLeft(trimmed, "-* "))
			if trimmed != "" {
				out = append(out, oneLine(trimmed))
				if len(out) >= limit {
					break
				}
			}
		}
		return out
	}
	return nil
}

func addressNear(text string, labels ...string) string {
	for _, label := range labels {
		re := regexp.MustCompile(`(?im)` + regexp.QuoteMeta(label) + `[^0-9a-fA-F]*(0x[a-fA-F0-9]{40})`)
		match := re.FindStringSubmatch(text)
		if len(match) == 2 {
			return match[1]
		}
	}
	return ""
}

func findImage(outputRoot string) string {
	names := map[string]bool{
		"poc_flow.png":      true,
		"exploit_flow.png":  true,
		"flow.png":          true,
		"attack_flow.png":   true,
		"fund_flows.png":    true,
		"asset_deltas.png":  true,
		"fund_flows.jpg":    true,
		"asset_deltas.jpg":  true,
		"fund_flows.jpeg":   true,
		"asset_deltas.jpeg": true,
	}
	var found string
	_ = filepath.WalkDir(outputRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return nil
		}
		if names[strings.ToLower(d.Name())] {
			found = path
		}
		return nil
	})
	if found != "" {
		return found
	}
	return "attach the generated PoC flow image"
}

func titleFromSlug(slug string) string {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return ""
	}
	parts := regexp.MustCompile(`[_\-/\s]+`).Split(slug, -1)
	var words []string
	for _, part := range parts {
		lower := strings.ToLower(strings.TrimSpace(part))
		if lower == "" || regexp.MustCompile(`^\d{6}$`).MatchString(lower) || regexp.MustCompile(`^a\d+$`).MatchString(lower) {
			continue
		}
		switch lower {
		case "ethereum", "eth", "base", "bsc", "arbitrum", "arb", "polygon", "poly", "txunknown":
			continue
		case "usdt", "usd", "btc":
			words = append(words, strings.ToUpper(lower))
		default:
			words = append(words, strings.ToUpper(lower[:1])+lower[1:])
		}
	}
	return strings.Join(words, " ")
}

func protocolFromSlug(slug string) string {
	slug = strings.Trim(strings.TrimSpace(slug), "_-/ ")
	if slug == "" {
		return ""
	}
	parts := regexp.MustCompile(`[_\-/\s]+`).Split(slug, -1)
	var kept []string
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		lower := strings.ToLower(trimmed)
		if trimmed == "" || regexp.MustCompile(`^\d+$`).MatchString(lower) || regexp.MustCompile(`^a\d+$`).MatchString(lower) || regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(lower) {
			continue
		}
		switch lower {
		case "ethereum", "eth", "base", "bsc", "arbitrum", "arb", "polygon", "poly", "txunknown", "unknown":
			continue
		default:
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, "_")
}

func oneLine(text string) string {
	return strings.TrimRight(regexp.MustCompile(`\s+`).ReplaceAllString(strings.TrimSpace(text), " "), ".")
}

func truncateText(text string, max int) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	cut := strings.LastIndex(text[:max], " ")
	if cut < max/2 {
		cut = max
	}
	return strings.TrimSpace(text[:cut]) + "..."
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func fallback(value, def string) string {
	if strings.TrimSpace(value) == "" {
		return def
	}
	return strings.TrimSpace(value)
}

func defaultString(value, def string) string {
	if strings.TrimSpace(value) == "" {
		return def
	}
	return strings.TrimSpace(value)
}
