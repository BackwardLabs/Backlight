package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	APIToken         string
	DBPath           string
	OutputRoot       string
	ListenAddr       string
	MaxConcurrent    int
	LumoskitBin      string
	WorkerPollMillis int
	DownstreamURLs   []string
	DownstreamBearer string
	OperatorWebhook  string
	TelegramBotToken string
	TelegramChatID   string
	TelegramAPIBase  string

	HandoffRetryMaxAttempts        int
	HandoffRetryBackoffBaseSeconds int
	HandoffRetryBackoffMaxSeconds  int
	NotifyRetryMaxAttempts         int
	NotifyRetryBackoffBaseSeconds  int
	NotifyRetryBackoffMaxSeconds   int
}

func Load() (*Config, error) {
	c := &Config{
		APIToken:                       os.Getenv("HELIOS_API_TOKEN"),
		DBPath:                         os.Getenv("HELIOS_DB_PATH"),
		OutputRoot:                     os.Getenv("HELIOS_OUTPUT_ROOT"),
		ListenAddr:                     envDefault("HELIOS_LISTEN_ADDR", ":8080"),
		MaxConcurrent:                  envInt("HELIOS_MAX_CONCURRENT_LUMOSKIT", 2),
		LumoskitBin:                    envDefault("HELIOS_LUMOSKIT_BIN", "bin/lumoskit"),
		WorkerPollMillis:               envInt("HELIOS_WORKER_POLL_MILLIS", 1000),
		DownstreamURLs:                 splitCSV(os.Getenv("HELIOS_DOWNSTREAM_WEBHOOK_URLS")),
		DownstreamBearer:               os.Getenv("HELIOS_DOWNSTREAM_WEBHOOK_BEARER_TOKEN"),
		OperatorWebhook:                os.Getenv("OPERATOR_NOTIFY_WEBHOOK_URL"),
		TelegramBotToken:               os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:                 os.Getenv("TELEGRAM_CHAT_ID"),
		TelegramAPIBase:                os.Getenv("HELIOS_TELEGRAM_API_BASE"),
		HandoffRetryMaxAttempts:        envInt("HELIOS_HANDOFF_RETRY_MAX_ATTEMPTS", 5),
		HandoffRetryBackoffBaseSeconds: envInt("HELIOS_HANDOFF_RETRY_BACKOFF_BASE_SECONDS", 2),
		HandoffRetryBackoffMaxSeconds:  envInt("HELIOS_HANDOFF_RETRY_BACKOFF_MAX_SECONDS", 300),
		NotifyRetryMaxAttempts:         envInt("HELIOS_NOTIFY_RETRY_MAX_ATTEMPTS", 5),
		NotifyRetryBackoffBaseSeconds:  envInt("HELIOS_NOTIFY_RETRY_BACKOFF_BASE_SECONDS", 2),
		NotifyRetryBackoffMaxSeconds:   envInt("HELIOS_NOTIFY_RETRY_BACKOFF_MAX_SECONDS", 300),
	}

	if c.APIToken == "" {
		return nil, errors.New("HELIOS_API_TOKEN is required")
	}
	if c.DBPath == "" {
		return nil, errors.New("HELIOS_DB_PATH is required")
	}
	if c.OutputRoot == "" {
		return nil, errors.New("HELIOS_OUTPUT_ROOT is required")
	}
	return c, nil
}

func (c *Config) TelegramEnabled() bool {
	return c.TelegramBotToken != "" && c.TelegramChatID != ""
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

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
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
