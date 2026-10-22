package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/config"
	"github.com/Arata1202/workadventure-presence-notifier/internal/coordinator"
	slacknotifier "github.com/Arata1202/workadventure-presence-notifier/internal/notifier/slack"
	workadventureprovider "github.com/Arata1202/workadventure-presence-notifier/internal/provider/workadventure"
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
	once := flag.Bool("once", false, "publish one live status and exit without deleting it")
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
		Address: cfg.WorkAdventure.Address, RoomDiscoveryInterval: cfg.WorkAdventure.RoomDiscoveryInterval,
		ReconnectInterval: cfg.WorkAdventure.ReconnectInterval, InitialSyncTimeout: cfg.WorkAdventure.InitialSyncTimeout,
		RequestTimeout: cfg.WorkAdventure.RequestTimeout,
	}, logger)
	slack, err := slacknotifier.New(slacknotifier.Config{
		BotToken: cfg.Slack.BotToken, ChannelID: cfg.Slack.ChannelID,
		Message: cfg.Slack.Message, UserSeparator: cfg.Slack.UserSeparator,
	}, &http.Client{Timeout: cfg.Slack.RequestTimeout})
	if err != nil {
		return err
	}
	statusCoordinator, err := coordinator.New(cfg.Status.Debounce, presenceProvider, slack, cfg.Status.StateFile, logger)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	providerErrors := make(chan error, 1)
	go func() { providerErrors <- presenceProvider.Run(ctx) }()

	if *once {
		waitTimeout := cfg.WorkAdventure.RequestTimeout + cfg.WorkAdventure.RoomDiscoveryInterval + cfg.WorkAdventure.InitialSyncTimeout + cfg.WorkAdventure.ReconnectInterval
		waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
		defer cancel()
		if err := statusCoordinator.WaitReady(waitCtx); err != nil {
			return err
		}
		if err := statusCoordinator.PublishCurrent(ctx); err != nil {
			return err
		}
		logger.Info("presence status published")
		stop()
		return waitForProvider(providerErrors, time.Second)
	}

	logger.Info("presence notifier started", "debounce", cfg.Status.Debounce)
	coordinatorErrors := make(chan error, 1)
	go func() { coordinatorErrors <- statusCoordinator.Run(ctx) }()
	cleanup := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cfg.Status.ShutdownTimeout)
		defer cancel()
		if err := statusCoordinator.Clear(cleanupCtx); err != nil {
			return fmt.Errorf("remove Slack status during shutdown: %w", err)
		}
		return nil
	}

	select {
	case <-ctx.Done():
		return cleanup()
	case err := <-providerErrors:
		if ctx.Err() != nil {
			return cleanup()
		}
		if err == nil || errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case err := <-coordinatorErrors:
		if ctx.Err() != nil {
			return cleanup()
		}
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
