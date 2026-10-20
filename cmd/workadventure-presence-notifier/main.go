package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/config"
	"github.com/Arata1202/workadventure-presence-notifier/internal/coordinator"
	"github.com/Arata1202/workadventure-presence-notifier/internal/notifier"
	slacknotifier "github.com/Arata1202/workadventure-presence-notifier/internal/notifier/slack"
	workadventureprovider "github.com/Arata1202/workadventure-presence-notifier/internal/provider/workadventure"
	"github.com/Arata1202/workadventure-presence-notifier/internal/status"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		slog.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func run(root context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("workadventure-presence-notifier", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "config.yaml", "path to the YAML configuration file")
	once := flags.Bool("once", false, "reconcile the destination status once and exit")
	showVersion := flags.Bool("version", false, "print the version and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		_, err := fmt.Fprintln(output, version)
		return err
	}

	logger := slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	source := workadventureprovider.New(workadventureprovider.Options{
		Address:               cfg.WorkAdventure.Address,
		RoomDiscoveryInterval: cfg.WorkAdventure.RoomDiscoveryInterval,
		ReconnectInterval:     cfg.WorkAdventure.ReconnectInterval,
		InitialSyncTimeout:    cfg.WorkAdventure.InitialSyncTimeout,
		RequestTimeout:        cfg.WorkAdventure.RequestTimeout,
	}, logger)
	destination, err := newDestination(cfg.Destination)
	if err != nil {
		return err
	}
	reconciler, err := status.NewReconciler(destination.client, status.NewFileStore(cfg.Status.StateFile))
	if err != nil {
		return err
	}
	changeCoordinator := coordinator.New(
		cfg.Status.Debounce, cfg.Status.ReconciliationInterval, source, reconciler, logger,
	)

	ctx, cancel := context.WithCancel(root)
	defer cancel()
	providerErrors := make(chan error, 1)
	go func() { providerErrors <- source.Run(ctx) }()

	if *once {
		waitCtx, waitCancel := context.WithTimeout(ctx, workAdventureReadinessTimeout(cfg))
		if err := changeCoordinator.WaitReady(waitCtx); err != nil {
			waitCancel()
			return err
		}
		waitCancel()
		reconcileCtx, reconcileCancel := context.WithTimeout(ctx, destination.reconciliationTimeout)
		reconcileErr := changeCoordinator.ReconcileReady(reconcileCtx)
		reconcileCancel()
		if reconcileErr == nil {
			logger.Info("presence status reconciled")
		}
		cancel()
		return errors.Join(reconcileErr, waitForStop(providerErrors, cfg.Status.ShutdownTimeout))
	}

	logger.Info("presence status notifier started")
	coordinatorErrors := make(chan error, 1)
	go func() { coordinatorErrors <- changeCoordinator.Run(ctx) }()

	var runErr error
	coordinatorStopped := false
	select {
	case <-root.Done():
	case err := <-providerErrors:
		if err != nil && !errors.Is(err, context.Canceled) {
			runErr = err
		}
	case err := <-coordinatorErrors:
		coordinatorStopped = true
		if err != nil && !errors.Is(err, context.Canceled) {
			runErr = err
		}
	}
	cancel()
	if !coordinatorStopped {
		err, stopped := waitForCoordinatorStop(coordinatorErrors, cfg.Status.ShutdownTimeout)
		if !stopped {
			logger.Error("coordinator did not stop before shutdown cleanup")
			return errors.Join(runErr, errors.New("coordinator did not stop before shutdown cleanup"))
		}
		if err != nil && !errors.Is(err, context.Canceled) && runErr == nil {
			runErr = err
		}
	}
	intentionalShutdown := shutdownWasIntentional(root)
	cleanupErr := cleanupStatus(intentionalShutdown, cfg.Status.ShutdownTimeout, reconciler)
	return errors.Join(runErr, cleanupErr)
}

type statusCleaner interface {
	DeleteAll(context.Context) error
}

func cleanupStatus(intentionalShutdown bool, timeout time.Duration, cleaner statusCleaner) error {
	if !intentionalShutdown {
		return nil
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), timeout)
	defer shutdownCancel()
	if err := cleaner.DeleteAll(shutdownCtx); err != nil {
		return fmt.Errorf("remove destination status during shutdown: %w", err)
	}
	return nil
}

func workAdventureReadinessTimeout(cfg config.Config) time.Duration {
	return 2*cfg.WorkAdventure.RequestTimeout + cfg.WorkAdventure.RoomDiscoveryInterval +
		cfg.WorkAdventure.InitialSyncTimeout + cfg.WorkAdventure.ReconnectInterval
}

type configuredDestination struct {
	client                notifier.Destination
	reconciliationTimeout time.Duration
}

func newDestination(cfg config.DestinationConfig) (configuredDestination, error) {
	switch cfg.Type {
	case "slack":
		client, err := slacknotifier.New(slacknotifier.Config{
			BotToken:      cfg.Slack.BotToken,
			ChannelID:     cfg.Slack.ChannelID,
			Message:       cfg.Slack.Message,
			UserSeparator: cfg.Slack.UserSeparator,
			MaxAttempts:   slacknotifier.DefaultAttempts,
			MaxRetryWait:  slacknotifier.MaximumRetryWait,
		}, &http.Client{Timeout: cfg.Slack.RequestTimeout})
		return configuredDestination{
			client: client,
			reconciliationTimeout: slacknotifier.DefaultAttempts*cfg.Slack.RequestTimeout +
				time.Duration(slacknotifier.DefaultAttempts-1)*slacknotifier.MaximumRetryWait,
		}, err
	default:
		return configuredDestination{}, fmt.Errorf("unsupported destination %q", cfg.Type)
	}
}

func shutdownWasIntentional(root context.Context) bool { return root.Err() != nil }

func waitForCoordinatorStop(errorsChannel <-chan error, timeout time.Duration) (error, bool) {
	select {
	case err := <-errorsChannel:
		return err, true
	case <-time.After(timeout):
		return nil, false
	}
}

func waitForStop(errorsChannel <-chan error, timeout time.Duration) error {
	select {
	case err := <-errorsChannel:
		if err == nil || errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case <-time.After(timeout):
		return nil
	}
}
