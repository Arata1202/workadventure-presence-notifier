package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/config"
)

func TestRunVersionDoesNotRequireConfiguration(t *testing.T) {
	oldVersion := version
	version = "v0.2.0-test"
	t.Cleanup(func() { version = oldVersion })
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--version"}, &output); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != "v0.2.0-test" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestReadinessTimeoutCoversTwoRequestsAndRecoveryIntervals(t *testing.T) {
	cfg := config.Config{WorkAdventure: config.WorkAdventureConfig{
		RequestTimeout:        11 * time.Second,
		RoomDiscoveryInterval: 13 * time.Second,
		InitialSyncTimeout:    17 * time.Second,
		ReconnectInterval:     19 * time.Second,
	}}
	want := 2*cfg.WorkAdventure.RequestTimeout + cfg.WorkAdventure.RoomDiscoveryInterval +
		cfg.WorkAdventure.InitialSyncTimeout + cfg.WorkAdventure.ReconnectInterval
	if got := workAdventureReadinessTimeout(cfg); got != want {
		t.Fatalf("workAdventureReadinessTimeout() = %v, want %v", got, want)
	}
}

func TestSlackReconciliationTimeoutAccountsForEveryAttemptAndWait(t *testing.T) {
	cfg := config.Config{Slack: config.SlackConfig{RequestTimeout: 11 * time.Second}}
	want := 3*11*time.Second + 2*30*time.Second
	if got := slackReconciliationTimeout(cfg); got != want {
		t.Fatalf("slackReconciliationTimeout() = %v, want %v", got, want)
	}
}

func TestShutdownIntentIsReadFromContextAfterWorkerWins(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	cancel()
	worker := make(chan error, 1)
	worker <- nil
	<-worker
	if !shutdownWasIntentional(root) {
		t.Fatal("canceled root was not treated as intentional shutdown")
	}
}

func TestCoordinatorMustStopBeforeCleanupCanProceed(t *testing.T) {
	blocked := make(chan error)
	if _, stopped := waitForCoordinatorStop(blocked, time.Millisecond); stopped {
		t.Fatal("blocked coordinator reported stopped")
	}
	finished := make(chan error, 1)
	finished <- nil
	if err, stopped := waitForCoordinatorStop(finished, time.Second); !stopped || err != nil {
		t.Fatalf("waitForCoordinatorStop() = %v, %v", err, stopped)
	}
}

type recordingCleaner struct{ calls int }

func (c *recordingCleaner) DeleteAll(context.Context) error {
	c.calls++
	return nil
}

func TestCleanupStatusOnlyRunsForIntentionalShutdown(t *testing.T) {
	cleaner := &recordingCleaner{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cleanupStatus(false, time.Second, cleaner, logger)
	if cleaner.calls != 0 {
		t.Fatalf("DeleteAll() calls after application error = %d", cleaner.calls)
	}
	cleanupStatus(true, time.Second, cleaner, logger)
	if cleaner.calls != 1 {
		t.Fatalf("DeleteAll() calls after intentional shutdown = %d", cleaner.calls)
	}
}
