package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/config"
	"github.com/Arata1202/workadventure-presence-notifier/internal/notifier"
	slacknotifier "github.com/Arata1202/workadventure-presence-notifier/internal/notifier/slack"
	workadventureprovider "github.com/Arata1202/workadventure-presence-notifier/internal/provider/workadventure"
	"github.com/Arata1202/workadventure-presence-notifier/internal/scheduler"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.yaml", "path to the YAML configuration file")
	once := flag.Bool("once", false, "send one notification and exit")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	presenceProvider := workadventureprovider.New(workadventureprovider.Options{
		Address:               cfg.WorkAdventure.Address,
		RoomDiscoveryInterval: cfg.WorkAdventure.RoomDiscoveryInterval,
		ReconnectInterval:     cfg.WorkAdventure.ReconnectInterval,
		InitialSyncTimeout:    cfg.WorkAdventure.InitialSyncTimeout,
		RequestTimeout:        cfg.WorkAdventure.RequestTimeout,
	}, logger)

	slack, err := slacknotifier.New(slacknotifier.Config{
		WebhookURL:    cfg.Slack.WebhookURL,
		Message:       cfg.Slack.Message,
		EmptyMessage:  cfg.Slack.EmptyMessage,
		UserSeparator: cfg.Slack.UserSeparator,
	}, nil)
	if err != nil {
		return err
	}

	notificationScheduler := scheduler.New(
		cfg.Notification.Interval,
		presenceProvider,
		[]notifier.Notifier{slack},
		logger,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	providerErrors := make(chan error, 1)
	go func() { providerErrors <- presenceProvider.Run(ctx) }()

	if *once {
		waitTimeout := cfg.WorkAdventure.RequestTimeout + cfg.WorkAdventure.RoomDiscoveryInterval + cfg.WorkAdventure.InitialSyncTimeout + cfg.WorkAdventure.ReconnectInterval
		waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
		defer cancel()
		if err := presenceProvider.WaitReady(waitCtx); err != nil {
			return fmt.Errorf("wait for WorkAdventure presence: %w", err)
		}
		if err := notificationScheduler.NotifyOnce(ctx); err != nil {
			return err
		}
		logger.Info("presence notification sent")
		stop()
		return waitForProvider(providerErrors, time.Second)
	}

	logger.Info("presence notifier started", "interval", cfg.Notification.Interval)
	schedulerErrors := make(chan error, 1)
	go func() { schedulerErrors <- notificationScheduler.Run(ctx) }()

	select {
	case <-ctx.Done():
		return nil
	case err := <-providerErrors:
		if err == nil || errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case err := <-schedulerErrors:
		return err
	}
}

func waitForProvider(providerErrors <-chan error, timeout time.Duration) error {
	select {
	case err := <-providerErrors:
		if err == nil || errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case <-time.After(timeout):
		return nil
	}
}
