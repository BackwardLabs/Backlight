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
	"github.com/UPside-Lumos-V2/helios/internal/handoff"
	"github.com/UPside-Lumos-V2/helios/internal/lumoskit"
	"github.com/UPside-Lumos-V2/helios/internal/metrics"
	"github.com/UPside-Lumos-V2/helios/internal/notify"
	"github.com/UPside-Lumos-V2/helios/internal/store"
	"github.com/UPside-Lumos-V2/helios/internal/worker"
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

	w := &worker.Worker{
		Store:            st,
		Runner:           &lumoskit.Runner{Binary: cfg.LumoskitBin},
		Dispatcher:       dispatcher,
		Notifier:         notifier,
		OutputRootParent: cfg.OutputRoot,
		MaxConcurrent:    cfg.MaxConcurrent,
		PollInterval:     time.Duration(cfg.WorkerPollMillis) * time.Millisecond,
		Logger:           logger,
	}
	w.Start(ctx)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.NewServer(cfg, st, dispatcher).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("helios listening", "addr", cfg.ListenAddr)
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
	logger.Info("helios stopped")
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
