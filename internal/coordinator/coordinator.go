package coordinator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/notifier"
	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
	"github.com/Arata1202/workadventure-presence-notifier/internal/provider"
)

type Coordinator struct {
	mu             sync.Mutex
	debounce       time.Duration
	provider       provider.Provider
	status         notifier.Status
	state          *stateFile
	known          persistedState
	destinationID  string
	retryNotBefore time.Time
	logger         *slog.Logger
}

func New(debounce time.Duration, source provider.Provider, status notifier.Status, statePath string, logger *slog.Logger) (*Coordinator, error) {
	destinationID := status.DestinationID()
	if destinationID == "" {
		return nil, errors.New("status destination ID is required")
	}
	state := &stateFile{path: statePath}
	known, err := state.load()
	if err != nil {
		return nil, err
	}
	if !known.empty() && known.DestinationID != destinationID {
		return nil, fmt.Errorf("persisted status destination %q does not match configured destination %q; clean up the old destination before resetting the state file", known.DestinationID, destinationID)
	}
	return &Coordinator{debounce: debounce, provider: source, status: status, state: state, known: known, destinationID: destinationID, logger: logger}, nil
}

func (c *Coordinator) WaitReady(ctx context.Context) error {
	if err := c.provider.WaitReady(ctx); err != nil {
		return fmt.Errorf("wait for WorkAdventure presence: %w", err)
	}
	return nil
}

func (c *Coordinator) PublishCurrent(ctx context.Context) error {
	snapshot, err := c.provider.Snapshot()
	if err != nil {
		return fmt.Errorf("get presence snapshot: %w", err)
	}
	return c.Publish(ctx, snapshot)
}

func (c *Coordinator) Publish(ctx context.Context, snapshot presence.Snapshot) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if snapshot.Count() == 0 {
		return c.clear(ctx)
	}
	if err := c.cleanupStale(ctx); err != nil {
		c.logger.Warn("failed to remove stale Slack status", "error", err)
	}
	if len(c.known.Stale) >= maxStaleReferences {
		return fmt.Errorf("stale Slack status limit reached (%d); refusing to post another replacement", maxStaleReferences)
	}
	text, err := c.status.Render(snapshot)
	if err != nil {
		return err
	}
	next, err := c.status.Post(ctx, text)
	if err != nil {
		var throttled *notifier.RateLimitError
		if errors.As(err, &throttled) {
			c.retryNotBefore = time.Now().Add(throttled.RetryAfter)
			return err
		}
		return errors.Join(errPostUnconfirmed, err)
	}

	nextState := persistedState{
		DestinationID: c.destinationID,
		Current:       next,
		Stale:         append([]notifier.MessageRef(nil), c.known.Stale...),
	}
	if c.known.Current != "" {
		nextState.Stale = append(nextState.Stale, c.known.Current)
	}
	if err := c.state.save(nextState); err != nil {
		var syncErr directorySyncError
		if errors.As(err, &syncErr) {
			c.known = nextState
			return errors.Join(errPostCommitted, err)
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if rollbackErr := c.deleteKnown(cleanupCtx, next); rollbackErr != nil {
			c.known.DestinationID = c.destinationID
			c.known.Stale = append(c.known.Stale, next)
			return errors.Join(err, rollbackErr, c.state.save(c.known))
		}
		return err
	}
	c.known = nextState
	if err := c.cleanupStale(ctx); err != nil {
		c.logger.Warn("failed to remove stale Slack status", "error", err)
	}
	return nil
}

func (c *Coordinator) Clear(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clear(ctx)
}

func (c *Coordinator) clear(ctx context.Context) error {
	remaining := persistedState{DestinationID: c.destinationID}
	var cleanupErrors []error
	if c.known.Current != "" {
		if err := c.deleteKnown(ctx, c.known.Current); err != nil {
			remaining.Current = c.known.Current
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	for _, ref := range c.known.Stale {
		if err := c.deleteKnown(ctx, ref); err != nil {
			remaining.Stale = append(remaining.Stale, ref)
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if err := c.state.save(remaining); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	} else {
		if remaining.empty() {
			remaining.DestinationID = ""
		}
		c.known = remaining
	}
	return errors.Join(cleanupErrors...)
}

func (c *Coordinator) cleanupStale(ctx context.Context) error {
	if len(c.known.Stale) == 0 {
		return nil
	}
	remaining := make([]notifier.MessageRef, 0, len(c.known.Stale))
	var cleanupErrors []error
	for _, ref := range c.known.Stale {
		if err := c.deleteKnown(ctx, ref); err != nil {
			remaining = append(remaining, ref)
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	nextState := persistedState{DestinationID: c.destinationID, Current: c.known.Current, Stale: remaining}
	if err := c.state.save(nextState); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	} else {
		c.known = nextState
	}
	return errors.Join(cleanupErrors...)
}

func (c *Coordinator) deleteKnown(ctx context.Context, ref notifier.MessageRef) error {
	if err := c.status.Delete(ctx, ref); err != nil && !errors.Is(err, notifier.ErrMessageNotFound) {
		var throttled *notifier.RateLimitError
		if errors.As(err, &throttled) {
			c.retryNotBefore = time.Now().Add(throttled.RetryAfter)
		}
		return fmt.Errorf("delete Slack status %s: %w", ref, err)
	}
	return nil
}

func (c *Coordinator) Run(ctx context.Context) error {
	if err := c.WaitReady(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	err := c.PublishCurrent(ctx)
	var throttled *notifier.RateLimitError
	if err != nil && !errors.Is(err, errPostCommitted) && !errors.As(err, &throttled) {
		return fmt.Errorf("publish initial presence status: %w", err)
	}

	retryDelay := time.Second
	maintenance := err == nil || errors.Is(err, errPostCommitted)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	var wake <-chan time.Time
	schedule := func(delay time.Duration) {
		c.mu.Lock()
		delay = max(delay, time.Until(c.retryNotBefore))
		c.mu.Unlock()
		timer.Reset(delay)
		wake = timer.C
	}
	if err != nil || c.hasPendingCleanup() {
		schedule(retryDelay)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.provider.Changes():
			maintenance = false
			retryDelay = time.Second
			schedule(c.debounce)
		case <-wake:
			wake = nil
			if maintenance {
				err = c.retryCleanup(ctx)
			} else {
				err = c.PublishCurrent(ctx)
				maintenance = err == nil || errors.Is(err, errPostCommitted)
			}
			if err != nil {
				c.logger.Error("failed to update presence status", "error", err)
				if errors.Is(err, errPostUnconfirmed) {
					continue
				}
			}
			if err != nil || c.hasPendingCleanup() {
				schedule(retryDelay)
				retryDelay = min(2*retryDelay, time.Minute)
			}
		}
	}
}

func (c *Coordinator) hasPendingCleanup() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.syncPending || len(c.known.Stale) > 0
}

func (c *Coordinator) retryCleanup(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.state.retrySync(); err != nil {
		return err
	}
	return c.cleanupStale(ctx)
}

var errPostUnconfirmed = errors.New("post Slack status unconfirmed")
var errPostCommitted = errors.New("Slack status was posted")
