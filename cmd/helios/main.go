package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/api"
	"github.com/UPside-Lumos-V2/helios/internal/config"
	"github.com/UPside-Lumos-V2/helios/internal/githubpublish"
	"github.com/UPside-Lumos-V2/helios/internal/handoff"
	"github.com/UPside-Lumos-V2/helios/internal/incidentresolver"
	"github.com/UPside-Lumos-V2/helios/internal/lumoskit"
	"github.com/UPside-Lumos-V2/helios/internal/mention"
	"github.com/UPside-Lumos-V2/helios/internal/metrics"
	"github.com/UPside-Lumos-V2/helios/internal/notify"
	"github.com/UPside-Lumos-V2/helios/internal/prelumos"
	"github.com/UPside-Lumos-V2/helios/internal/store"
	"github.com/UPside-Lumos-V2/helios/internal/telegrambot"
	"github.com/UPside-Lumos-V2/helios/internal/worker"
	"github.com/UPside-Lumos-V2/helios/internal/xfeed"
	"github.com/UPside-Lumos-V2/helios/internal/xpublish"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		logger.Error("store open failed", "err", err, "db_path", cfg.DBPath)
		os.Exit(2)
	}
	defer st.Close()

	metrics.Init(st.DB())

	recovered, err := st.RecoverRunningCases(ctx)
	if err != nil {
		logger.Error("restart recovery failed", "err", err)
		os.Exit(2)
	}
	if recovered > 0 {
		logger.Info("restart recovery completed", "orphans_recovered", recovered)
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}

	channels := buildChannels(cfg, httpClient)
	notifier := &notify.Notifier{
		Store:       st,
		Channels:    channels,
		MaxAttempts: cfg.NotifyRetryMaxAttempts,
		BackoffBase: time.Duration(cfg.NotifyRetryBackoffBaseSeconds) * time.Second,
		BackoffMax:  time.Duration(cfg.NotifyRetryBackoffMaxSeconds) * time.Second,
		Logger:      logger,
	}
	logger.Info("notifier configured", "channels", channelNames(channels))
	if !notifier.Configured() {
		if err := st.InitNotificationStatusDisabled(ctx); err != nil {
			logger.Warn("init notification_status=disabled failed", "err", err)
		}
	}

	dispatcher := &handoff.Dispatcher{
		Store:       st,
		Client:      httpClient,
		URLs:        cfg.DownstreamURLs,
		BearerToken: cfg.DownstreamBearer,
		MaxAttempts: cfg.HandoffRetryMaxAttempts,
		BackoffBase: time.Duration(cfg.HandoffRetryBackoffBaseSeconds) * time.Second,
		BackoffMax:  time.Duration(cfg.HandoffRetryBackoffMaxSeconds) * time.Second,
		Notifier:    notifier,
		Logger:      logger,
	}
	githubPublisher := githubpublish.New(githubpublish.Config{
		Token:  cfg.GitHubToken,
		Owner:  cfg.GitHubOwner,
		Repo:   cfg.GitHubRepo,
		Branch: cfg.GitHubBranch,
	})
	githubPublisher.Client = httpClient
	logger.Info("github publisher configured",
		"enabled", githubPublisher.Configured(),
		"repo", cfg.GitHubOwner+"/"+cfg.GitHubRepo,
		"branch", cfg.GitHubBranch,
	)
	var mentionIndex *mention.Index
	if cfg.VictimMentionEnabled {
		entities, err := st.AllMentionEntities(ctx)
		if err != nil {
			logger.Error("load victim mention store failed", "err", err)
			os.Exit(2)
		}
		mentionIndex = mention.BuildIndex(entities)
		logger.Info("victim mention enabled", "entities", len(entities))
	} else {
		logger.Info("victim mention disabled")
	}
	xPublisher := xpublish.New(xpublish.Config{
		Enabled:          cfg.XPublishEnabled,
		ClientID:         cfg.XClientID,
		ClientSecret:     cfg.XClientSecret,
		RefreshToken:     cfg.XRefreshToken,
		RefreshTokenFile: cfg.XRefreshTokenFile,
		TemplatePath:     cfg.XPostTemplatePath,
		APIBase:          cfg.XAPIBase,
		Username:         cfg.XUsername,
		DryRun:           cfg.XDryRun,
		Mentions:         mentionIndex,
	})
	xPublisher.Client = httpClient
	logger.Info("x publisher configured",
		"enabled", xPublisher.Configured(),
		"dry_run", cfg.XDryRun,
		"username_set", cfg.XUsername != "",
	)
	xFeedRunner := &xfeed.Runner{
		Enabled:       cfg.XFeedEnabled,
		SkillDir:      cfg.XFeedSkillDir,
		CardEnabled:   cfg.XFeedCardEnabled,
		CardPythonBin: cfg.XFeedCardPythonBin,
		CardTimeout:   time.Duration(cfg.XFeedCardTimeoutSeconds) * time.Second,
		Mentions:      mentionIndex,
	}
	logger.Info("x feed runner configured",
		"enabled", xFeedRunner.Configured(),
		"skill_dir", cfg.XFeedSkillDir,
		"card_enabled", cfg.XFeedCardEnabled,
		"card_python_bin", cfg.XFeedCardPythonBin,
		"telegram_publish_enabled", cfg.TelegramPublishEnabled,
	)
	preLumosRunner := &prelumos.Runner{
		Enabled:       cfg.PreLumosEnabled,
		PythonBin:     cfg.PreLumosPythonBin,
		Script:        cfg.PreLumosAgentScript,
		SkillDir:      cfg.PreLumosSkillDir,
		SeedRoot:      cfg.PreLumosSeedRoot,
		Year:          cfg.PreLumosYear,
		Model:         cfg.PreLumosModel,
		OpenAIBaseURL: cfg.PreLumosOpenAIBaseURL,
		WebSearch:     cfg.PreLumosWebSearch,
	}
	incidentResolver := &incidentresolver.Resolver{
		Enabled:          cfg.IncidentResolverEnabled,
		EtherscanAPIKey:  cfg.EtherscanAPIKey,
		EtherscanBaseURL: cfg.EtherscanBaseURL,
		RPCURL:           cfg.IncidentRPCURL,
		Client:           httpClient,
	}
	logger.Info("pre-lumos agent configured",
		"enabled", preLumosRunner.Configured(),
		"seed_root_set", cfg.PreLumosSeedRoot != "",
		"openai_base_url", cfg.PreLumosOpenAIBaseURL,
		"web_search", cfg.PreLumosWebSearch,
	)
	logger.Info("incident resolver configured",
		"enabled", incidentResolver.Configured(),
		"etherscan_key_set", cfg.EtherscanAPIKey != "",
		"rpc_url_set", cfg.IncidentRPCURL != "",
	)

	w := &worker.Worker{
		Store:                       st,
		Runner:                      &lumoskit.Runner{Binary: cfg.LumoskitBin},
		Dispatcher:                  dispatcher,
		Notifier:                    notifier,
		GitHubPublisher:             githubPublisher,
		XFeedRunner:                 xFeedRunner,
		XPublisher:                  xPublisher,
		TelegramPublishEnabled:      cfg.TelegramPublishEnabled,
		PreLumosRunner:              preLumosRunner,
		PartialAutoRerunMaxAttempts: cfg.PartialAutoRerunMaxAttempts,
		OutputRootParent:            cfg.OutputRoot,
		MaxConcurrent:               cfg.MaxConcurrent,
		PollInterval:                time.Duration(cfg.WorkerPollMillis) * time.Millisecond,
		Logger:                      logger,
	}
	w.Start(ctx)

	telegramBot := telegrambot.New(telegrambot.Config{
		BotToken:       cfg.TelegramBotToken,
		APIBase:        cfg.TelegramAPIBase,
		AllowedChatIDs: cfg.TelegramCommandAllowedChats(),
		PollTimeout:    time.Duration(cfg.TelegramCommandPollTimeoutSeconds) * time.Second,
		Store:          st,
		Resolver:       incidentResolver,
		Logger:         logger,
	})
	if cfg.TelegramCommandEnabled {
		telegramBot.Start(ctx)
	}
	logger.Info("telegram command bot configured",
		"enabled", cfg.TelegramCommandEnabled && telegramBot.Configured(),
		"allowed_chats", len(cfg.TelegramCommandAllowedChats()),
		"poll_timeout_s", cfg.TelegramCommandPollTimeoutSeconds,
	)

	apiServer := api.NewServer(cfg, st, dispatcher)
	apiServer.Resolver = incidentResolver
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("backlight listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server exited", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown failed", "err", err)
	}
	w.Wait()
	dispatcher.Wait()
	notifier.Wait()
	telegramBot.Wait()
	logger.Info("backlight stopped")
}

// buildChannels assembles the operator notification channel list from env.
// A channel is included iff its required env vars are set.
func buildChannels(cfg *config.Config, client *http.Client) []notify.Channel {
	var out []notify.Channel
	if cfg.OperatorWebhookEnabled() {
		out = append(out, &notify.WebhookChannel{URL: cfg.OperatorWebhook, Client: client})
	}
	if cfg.TelegramEnabled() {
		out = append(out, &notify.TelegramChannel{
			BotToken: cfg.TelegramBotToken,
			ChatID:   cfg.TelegramChatID,
			APIBase:  cfg.TelegramAPIBase,
			Client:   client,
		})
	}
	return out
}

func channelNames(chs []notify.Channel) []string {
	out := make([]string, 0, len(chs))
	for _, c := range chs {
		out = append(out, c.Name())
	}
	return out
}
