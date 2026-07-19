// Package xauth owns the shared OAuth 2.0 user-context access token used by
// X publishing and the hosted X MCP client.
package xauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Config struct {
	APIBase          string
	ClientID         string
	ClientSecret     string
	RefreshToken     string
	RefreshTokenFile string
	Client           *http.Client
}

type TokenSource struct {
	config Config

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

func NewTokenSource(cfg Config) *TokenSource {
	cfg.APIBase = strings.TrimRight(strings.TrimSpace(cfg.APIBase), "/")
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.x.com"
	}
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	return &TokenSource{config: cfg}
}

// Configured reports whether the source has enough client and refresh-token
// configuration to mint a user-context access token.
func (s *TokenSource) Configured() bool {
	if s == nil {
		return false
	}
	return strings.TrimSpace(s.config.ClientID) != "" &&
		strings.TrimSpace(s.config.ClientSecret) != "" &&
		(strings.TrimSpace(s.config.RefreshToken) != "" || strings.TrimSpace(s.config.RefreshTokenFile) != "")
}

// AccessToken returns a cached user-context token or refreshes it. Refreshes
// are serialized so mention verification and publishing cannot race while X
// rotates the same refresh token.
func (s *TokenSource) AccessToken(ctx context.Context) (string, error) {
	if !s.Configured() {
		return "", errors.New("x oauth token source is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accessToken != "" && time.Until(s.expiresAt) > time.Minute {
		return s.accessToken, nil
	}
	refreshToken, err := s.refreshTokenLocked()
	if err != nil {
		return "", err
	}
	if refreshToken == "" {
		return "", errors.New("x oauth refresh token is empty")
	}
	token, err := s.refreshLocked(ctx, refreshToken)
	if err != nil {
		return "", err
	}
	if err := s.storeRefreshTokenLocked(token); err != nil {
		return "", err
	}
	s.accessToken = strings.TrimSpace(token.AccessToken)
	lifetime := time.Duration(token.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = time.Hour
	}
	s.expiresAt = time.Now().Add(lifetime)
	return s.accessToken, nil
}

// InvalidateAccessToken forces the next call to refresh, but only when the
// rejected token is still the cached token.
func (s *TokenSource) InvalidateAccessToken(rejected string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rejected == "" || rejected == s.accessToken {
		s.accessToken = ""
		s.expiresAt = time.Time{}
	}
}

func (s *TokenSource) refreshLocked(ctx context.Context, refreshToken string) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.config.APIBase+"/2/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "backlight-x-oauth/1")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(s.config.ClientID+":"+s.config.ClientSecret)))
	resp, err := s.config.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("x oauth token refresh returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var token tokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, fmt.Errorf("decode x oauth token refresh: %w", err)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return nil, errors.New("x oauth token refresh did not include access_token")
	}
	return &token, nil
}

func (s *TokenSource) refreshTokenLocked() (string, error) {
	if path := strings.TrimSpace(s.config.RefreshTokenFile); path != "" {
		token, err := readRefreshTokenFile(path)
		if err == nil && token != "" {
			return token, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return strings.TrimSpace(s.config.RefreshToken), nil
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

func (s *TokenSource) storeRefreshTokenLocked(token *tokenResponse) error {
	refresh := strings.TrimSpace(token.RefreshToken)
	if refresh == "" {
		return nil
	}
	s.config.RefreshToken = refresh
	path := strings.TrimSpace(s.config.RefreshTokenFile)
	if path == "" {
		return nil
	}
	payload := map[string]any{
		"refresh_token": refresh,
		"token_type":    token.TokenType,
		"expires_in":    token.ExpiresIn,
		"scope":         token.Scope,
		"client_id":     strings.TrimSpace(s.config.ClientID),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return writeSecretFile(path, append(data, '\n'))
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
	remove := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		remove()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
