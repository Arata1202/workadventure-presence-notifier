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
	mu            sync.Mutex
	debounce      time.Duration
	provider      provider.Provider
	status        notifier.Status
	state         *stateFile
	known         persistedState
	destinationID string
	logger        *slog.Logger
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
		return fmt.Errorf("post Slack status: %w", err)
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
		rollbackErr := c.status.Delete(ctx, next)
		return errors.Join(err, wrapRollback(rollbackErr))
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
	if err := c.PublishCurrent(ctx); err != nil {
		return fmt.Errorf("publish initial presence status: %w", err)
	}

	var timer *time.Timer
	var timerC <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.provider.Changes():
			if timer == nil {
				timer = time.NewTimer(c.debounce)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(c.debounce)
			}
			timerC = timer.C
		case <-timerC:
			timerC = nil
			if err := c.PublishCurrent(ctx); err != nil && ctx.Err() == nil {
				c.logger.Error("failed to publish presence status", "error", err)
			}
		}
	}
}

func wrapRollback(err error) error {
	if err == nil || errors.Is(err, notifier.ErrMessageNotFound) {
		return nil
	}
	return fmt.Errorf("rollback new Slack status: %w", err)
}
