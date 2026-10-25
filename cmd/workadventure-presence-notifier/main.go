package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
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
		EntranceURL: cfg.WorkAdventure.EntranceURL, EntranceLabel: cfg.WorkAdventure.EntranceLabel,
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
	var receiverErrors <-chan error
	if cfg.WorkAdventure.EntranceURL != "" {
		server, failures, done, err := startInteractions(cfg.Slack.InteractionsListen, cfg.Slack.SigningSecret)
		if err != nil {
			return err
		}
		receiverErrors = failures
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Status.ShutdownTimeout)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				_ = server.Close()
			}
			<-done
		}()
	}
	var workers sync.WaitGroup
	defer func() { stop(); workers.Wait() }()
	providerErrors := make(chan error, 1)
	workers.Add(1)
	go func() { defer workers.Done(); providerErrors <- presenceProvider.Run(ctx) }()

	logger.Info("presence notifier started", "debounce", cfg.Status.Debounce)
	coordinatorErrors := make(chan error, 1)
	workers.Add(1)
	go func() { defer workers.Done(); coordinatorErrors <- statusCoordinator.Run(ctx) }()
	cleanup := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cfg.Status.ShutdownTimeout)
		defer cancel()
		if err := statusCoordinator.Clear(cleanupCtx); err != nil {
			return fmt.Errorf("remove Slack status during shutdown: %w", err)
		}
		return nil
	}

	var runErr error
	clearStatus := false
	select {
	case <-ctx.Done():
		clearStatus = true
	case runErr = <-providerErrors:
	case runErr = <-coordinatorErrors:
	case runErr = <-receiverErrors:
		clearStatus = true
	}
	clearStatus = clearStatus || ctx.Err() != nil
	stop()
	workers.Wait()
	if errors.Is(runErr, context.Canceled) {
		runErr = nil
	}
	if clearStatus {
		return errors.Join(runErr, cleanup())
	}
	return runErr
}

// Bind synchronously so no status is published if the receiver cannot start.
func startInteractions(address, secret string) (*http.Server, <-chan error, <-chan struct{}, error) {
	handler, err := slacknotifier.NewInteractionHandler(secret)
	if err != nil {
		return nil, nil, nil, err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, nil, nil, errors.New("start Slack interaction receiver: cannot bind destination.slack.interactions_listen")
	}
	server := &http.Server{
		Addr: listener.Addr().String(), Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second,
		WriteTimeout: 2 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	failures := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failures <- errors.New("Slack interaction receiver stopped unexpectedly")
		}
	}()
	return server, failures, done, nil
}
