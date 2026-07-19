package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	APIToken                          string
	ECWExportToken                    string
	ECWExportMaxBytes                 int64
	DBPath                            string
	OutputRoot                        string
	ListenAddr                        string
	MaxConcurrent                     int
	LumoskitBin                       string
	WorkerPollMillis                  int
	DownstreamURLs                    []string
	DownstreamBearer                  string
	OperatorWebhook                   string
	TelegramBotToken                  string
	TelegramChatID                    string
	TelegramAPIBase                   string
	TelegramCommandEnabled            bool
	TelegramAllowedChatIDs            []string
	TelegramCommandPollTimeoutSeconds int
	GitHubToken                       string
	GitHubOwner                       string
	GitHubRepo                        string
	GitHubBranch                      string
	XPublishEnabled                   bool
	XClientID                         string
	XClientSecret                     string
	XRefreshToken                     string
	XRefreshTokenFile                 string
	XPostTemplatePath                 string
	XAPIBase                          string
	XUsername                         string
	XDryRun                           bool
	XFeedEnabled                      bool
	XFeedSkillDir                     string
	XFeedCardEnabled                  bool
	XFeedCardPythonBin                string
	XFeedCardTimeoutSeconds           int
	VictimMentionEnabled              bool
	XMCPEnabled                       bool
	XMCPURL                           string
	XMCPBearerToken                   string
	XMCPCommand                       string
	XMCPArgs                          []string
	XMCPTimeoutSeconds                int
	XMentionCachedMaxAgeHours         int
	XMentionReverifyHours             int
	TelegramPublishEnabled            bool
	PreLumosEnabled                   bool
	PreLumosPythonBin                 string
	PreLumosAgentScript               string
	PreLumosSkillDir                  string
	PreLumosSeedRoot                  string
	PreLumosYear                      string
	PreLumosModel                     string
	PreLumosWebSearch                 bool
	IncidentResolverEnabled           bool
	EtherscanAPIKey                   string
	EtherscanBaseURL                  string
	IncidentRPCURL                    string

	PartialAutoRerunMaxAttempts    int
	HandoffRetryMaxAttempts        int
	HandoffRetryBackoffBaseSeconds int
	HandoffRetryBackoffMaxSeconds  int
	NotifyRetryMaxAttempts         int
	NotifyRetryBackoffBaseSeconds  int
	NotifyRetryBackoffMaxSeconds   int
}

func Load() (*Config, error) {
	loadDotEnvFiles(".env", ".env.local")

	c := &Config{
		APIToken:                          os.Getenv("BACKLIGHT_API_TOKEN"),
		ECWExportToken:                    os.Getenv("BACKLIGHT_ECW_EXPORT_TOKEN"),
		ECWExportMaxBytes:                 envInt64("BACKLIGHT_ECW_EXPORT_MAX_BYTES", 0),
		DBPath:                            os.Getenv("BACKLIGHT_DB_PATH"),
		OutputRoot:                        os.Getenv("BACKLIGHT_OUTPUT_ROOT"),
		ListenAddr:                        envDefault("BACKLIGHT_LISTEN_ADDR", ":8080"),
		MaxConcurrent:                     envInt("BACKLIGHT_MAX_CONCURRENT_LUMOSKIT", 2),
		LumoskitBin:                       envDefault("BACKLIGHT_LUMOSKIT_BIN", "bin/lumoskit"),
		WorkerPollMillis:                  envInt("BACKLIGHT_WORKER_POLL_MILLIS", 1000),
		DownstreamURLs:                    splitCSV(os.Getenv("BACKLIGHT_DOWNSTREAM_WEBHOOK_URLS")),
		DownstreamBearer:                  os.Getenv("BACKLIGHT_DOWNSTREAM_WEBHOOK_BEARER_TOKEN"),
		OperatorWebhook:                   os.Getenv("OPERATOR_NOTIFY_WEBHOOK_URL"),
		TelegramBotToken:                  os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:                    os.Getenv("TELEGRAM_CHAT_ID"),
		TelegramAPIBase:                   os.Getenv("BACKLIGHT_TELEGRAM_API_BASE"),
		TelegramCommandEnabled:            envBool("TELEGRAM_COMMAND_ENABLED", false),
		TelegramAllowedChatIDs:            splitCSV(os.Getenv("TELEGRAM_ALLOWED_CHAT_IDS")),
		TelegramCommandPollTimeoutSeconds: envInt("TELEGRAM_COMMAND_POLL_TIMEOUT_SECONDS", 30),
		GitHubToken:                       firstNonEmpty(os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN")),
		GitHubOwner:                       envDefault("BACKLIGHT_GITHUB_PUBLISH_OWNER", "BackwardLabs"),
		GitHubRepo:                        envDefault("BACKLIGHT_GITHUB_PUBLISH_REPO", "Q1-2026"),
		GitHubBranch:                      envDefault("BACKLIGHT_GITHUB_PUBLISH_BRANCH", "main"),
		XPublishEnabled:                   envBool("X_PUBLISH_ENABLED", false),
		XClientID:                         os.Getenv("X_CLIENT_ID"),
		XClientSecret:                     os.Getenv("X_CLIENT_SECRET"),
		XRefreshToken:                     os.Getenv("X_REFRESH_TOKEN"),
		XRefreshTokenFile:                 os.Getenv("X_REFRESH_TOKEN_FILE"),
		XPostTemplatePath:                 os.Getenv("X_POST_TEMPLATE_PATH"),
		XAPIBase:                          envDefault("X_API_BASE", "https://api.x.com"),
		XUsername:                         os.Getenv("X_ACCOUNT_USERNAME"),
		XDryRun:                           envBool("X_DRY_RUN", true),
		XFeedEnabled:                      envBool("BACKLIGHT_X_FEED_ENABLED", false),
		XFeedSkillDir:                     envDefault("BACKLIGHT_X_FEED_SKILL_DIR", "skills/x-feed"),
		XFeedCardEnabled:                  envBool("BACKLIGHT_X_FEED_CARD_ENABLED", true),
		XFeedCardPythonBin:                envDefault("BACKLIGHT_X_FEED_CARD_PYTHON_BIN", "python3"),
		XFeedCardTimeoutSeconds:           envInt("BACKLIGHT_X_FEED_CARD_TIMEOUT_SECONDS", 20),
		VictimMentionEnabled:              envBool("BACKLIGHT_VICTIM_MENTION", true),
		XMCPEnabled:                       envBool("BACKLIGHT_X_MCP_ENABLED", false),
		XMCPURL:                           envDefault("BACKLIGHT_X_MCP_URL", "https://api.x.com/mcp"),
		XMCPBearerToken:                   firstNonEmpty(os.Getenv("BACKLIGHT_X_MCP_BEARER_TOKEN"), os.Getenv("X_BEARER_TOKEN")),
		XMCPCommand:                       envDefault("BACKLIGHT_X_MCP_COMMAND", "xurl"),
		XMCPArgs:                          strings.Fields(envDefault("BACKLIGHT_X_MCP_ARGS", "mcp https://api.x.com/mcp")),
		XMCPTimeoutSeconds:                envInt("BACKLIGHT_X_MCP_TIMEOUT_SECONDS", 20),
		XMentionCachedMaxAgeHours:         envInt("BACKLIGHT_X_MENTION_CACHE_MAX_AGE_HOURS", 168),
		XMentionReverifyHours:             envInt("BACKLIGHT_X_MENTION_REVERIFY_HOURS", 24),
		TelegramPublishEnabled:            envBool("TELEGRAM_PUBLISH_ENABLED", false),
		PreLumosEnabled:                   envBool("BACKLIGHT_PRE_LUMOS_ENABLED", false),
		PreLumosPythonBin:                 envDefault("BACKLIGHT_PRE_LUMOS_PYTHON_BIN", "python3"),
		PreLumosAgentScript:               envDefault("BACKLIGHT_PRE_LUMOS_AGENT_SCRIPT", "scripts/pre_lumos_agent.py"),
		PreLumosSkillDir:                  envDefault("BACKLIGHT_PRE_LUMOS_SKILL_DIR", "skills/pre-lumos"),
		PreLumosSeedRoot:                  os.Getenv("BACKLIGHT_PRE_LUMOS_SEED_ROOT"),
		PreLumosYear:                      os.Getenv("BACKLIGHT_PRE_LUMOS_YEAR"),
		PreLumosModel:                     os.Getenv("BACKLIGHT_PRE_LUMOS_MODEL"),
		PreLumosWebSearch:                 envBool("BACKLIGHT_PRE_LUMOS_WEB_SEARCH", false),
		IncidentResolverEnabled:           envBool("BACKLIGHT_INCIDENT_RESOLVER_ENABLED", true),
		EtherscanAPIKey:                   firstNonEmpty(os.Getenv("BACKLIGHT_ETHERSCAN_API_KEY"), os.Getenv("ETHERSCAN_API_KEY")),
		EtherscanBaseURL:                  envDefault("BACKLIGHT_ETHERSCAN_BASE_URL", "https://api.etherscan.io/v2/api"),
		IncidentRPCURL:                    firstNonEmpty(os.Getenv("BACKLIGHT_INCIDENT_RPC_URL"), os.Getenv("CEFG_LIVE_RPC_URL"), os.Getenv("RPC_URL"), os.Getenv("ETH_RPC_URL")),
		PartialAutoRerunMaxAttempts:       envInt("BACKLIGHT_PARTIAL_AUTO_RERUN_MAX_ATTEMPTS", 3),
		HandoffRetryMaxAttempts:           envInt("BACKLIGHT_HANDOFF_RETRY_MAX_ATTEMPTS", 5),
		HandoffRetryBackoffBaseSeconds:    envInt("BACKLIGHT_HANDOFF_RETRY_BACKOFF_BASE_SECONDS", 2),
		HandoffRetryBackoffMaxSeconds:     envInt("BACKLIGHT_HANDOFF_RETRY_BACKOFF_MAX_SECONDS", 300),
		NotifyRetryMaxAttempts:            envInt("BACKLIGHT_NOTIFY_RETRY_MAX_ATTEMPTS", 5),
		NotifyRetryBackoffBaseSeconds:     envInt("BACKLIGHT_NOTIFY_RETRY_BACKOFF_BASE_SECONDS", 2),
		NotifyRetryBackoffMaxSeconds:      envInt("BACKLIGHT_NOTIFY_RETRY_BACKOFF_MAX_SECONDS", 300),
	}

	if c.APIToken == "" {
		return nil, errors.New("BACKLIGHT_API_TOKEN is required")
	}
	if c.DBPath == "" {
		return nil, errors.New("BACKLIGHT_DB_PATH is required")
	}
	if c.OutputRoot == "" {
		return nil, errors.New("BACKLIGHT_OUTPUT_ROOT is required")
	}
	if strings.TrimSpace(c.ECWExportToken) != "" && strings.TrimSpace(c.ECWExportToken) == strings.TrimSpace(c.APIToken) {
		return nil, errors.New("BACKLIGHT_ECW_EXPORT_TOKEN must differ from BACKLIGHT_API_TOKEN")
	}
	if c.PreLumosEnabled {
		if c.PreLumosSeedRoot == "" {
			return nil, errors.New("BACKLIGHT_PRE_LUMOS_SEED_ROOT is required when BACKLIGHT_PRE_LUMOS_ENABLED=true")
		}
	}
	return c, nil
}

func (c *Config) TelegramEnabled() bool {
	return c.TelegramBotToken != "" && c.TelegramChatID != ""
}

// TelegramCommandAllowedChats resolves the chat allowlist for the inbound
// command bot. An explicit TELEGRAM_ALLOWED_CHAT_IDS wins; otherwise it falls
// back to the single operator TELEGRAM_CHAT_ID so the command surface defaults
// to the same chat that already receives outbound alerts.
func (c *Config) TelegramCommandAllowedChats() []string {
	if len(c.TelegramAllowedChatIDs) > 0 {
		return c.TelegramAllowedChatIDs
	}
	if c.TelegramChatID != "" {
		return []string{c.TelegramChatID}
	}
	return nil
}

func (c *Config) OperatorWebhookEnabled() bool {
	return c.OperatorWebhook != ""
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := strings.TrimSpace(strings.ToLower(os.Getenv(key))); v != "" {
		switch v {
		case "1", "true", "yes", "y", "on":
			return true
		case "0", "false", "no", "n", "off":
			return false
		}
	}
	return def
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
