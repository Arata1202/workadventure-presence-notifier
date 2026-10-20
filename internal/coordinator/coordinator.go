package coordinator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
	"github.com/Arata1202/workadventure-presence-notifier/internal/provider"
)

const defaultRetryInitial = 30 * time.Second

type Reconciler interface {
	Reconcile(context.Context, presence.Snapshot, bool) error
}

type Coordinator struct {
	debounce               time.Duration
	reconciliationInterval time.Duration
	retryInitial           time.Duration
	provider               provider.Provider
	reconciler             Reconciler
	logger                 *slog.Logger
}

func New(debounce, reconciliationInterval time.Duration, source provider.Provider, reconciler Reconciler, logger *slog.Logger) *Coordinator {
	retryInitial := defaultRetryInitial
	if reconciliationInterval < retryInitial {
		retryInitial = reconciliationInterval
	}
	return &Coordinator{
		debounce:               debounce,
		reconciliationInterval: reconciliationInterval,
		retryInitial:           retryInitial,
		provider:               source,
		reconciler:             reconciler,
		logger:                 logger,
	}
}

func (c *Coordinator) WaitReady(ctx context.Context) error {
	if err := c.provider.WaitReady(ctx); err != nil {
		return fmt.Errorf("wait for WorkAdventure presence: %w", err)
	}
	return nil
}

func (c *Coordinator) ReconcileReady(ctx context.Context) error {
	return c.reconcile(ctx, true)
}

func (c *Coordinator) ReconcileOnce(ctx context.Context) error {
	if err := c.WaitReady(ctx); err != nil {
		return err
	}
	return c.ReconcileReady(ctx)
}

func (c *Coordinator) Run(ctx context.Context) error {
	if err := c.WaitReady(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}

	ticker := time.NewTicker(c.reconciliationInterval)
	defer ticker.Stop()
	var debounceTimer, retryTimer *time.Timer
	var debounceC, retryC <-chan time.Time
	defer func() {
		stopTimer(debounceTimer)
		stopTimer(retryTimer)
	}()

	nextRetry := c.retryInitial
	resetRetry := func() {
		stopTimer(retryTimer)
		retryTimer = nil
		retryC = nil
		nextRetry = c.retryInitial
	}
	scheduleRetry := func() {
		stopTimer(retryTimer)
		retryTimer = time.NewTimer(nextRetry)
		retryC = retryTimer.C
		nextRetry = minDuration(nextRetry*2, c.reconciliationInterval)
	}
	reconcile := func(force bool, message string) {
		if err := c.reconcile(ctx, force); err != nil {
			c.logger.Error(message, "error", err)
			scheduleRetry()
			return
		}
		resetRetry()
	}

	reconcile(true, "failed to perform initial presence reconciliation")

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.provider.Changes():
			resetRetry()
			if debounceTimer == nil {
				debounceTimer = time.NewTimer(c.debounce)
			} else {
				stopTimer(debounceTimer)
				debounceTimer.Reset(c.debounce)
			}
			debounceC = debounceTimer.C
		case <-debounceC:
			debounceC = nil
			reconcile(false, "failed to reconcile presence status")
		case <-retryC:
			retryC = nil
			reconcile(false, "failed to retry presence reconciliation")
		case <-ticker.C:
			reconcile(false, "failed to reconcile presence status")
		}
	}
}

func (c *Coordinator) reconcile(ctx context.Context, force bool) error {
	snapshot, err := c.provider.Snapshot()
	if err != nil {
		return fmt.Errorf("get presence snapshot: %w", err)
	}
	return c.reconciler.Reconcile(ctx, snapshot, force)
}

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
