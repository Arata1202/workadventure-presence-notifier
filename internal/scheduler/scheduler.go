package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/notifier"
	"github.com/Arata1202/workadventure-presence-notifier/internal/provider"
)

type Scheduler struct {
	interval  time.Duration
	provider  provider.Provider
	notifiers []notifier.Notifier
	logger    *slog.Logger
}

func New(interval time.Duration, source provider.Provider, notifiers []notifier.Notifier, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		interval:  interval,
		provider:  source,
		notifiers: notifiers,
		logger:    logger,
	}
}

func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.NotifyOnce(ctx); err != nil {
				s.logger.Error("failed to send presence notification", "error", err)
			}
		}
	}
}

func (s *Scheduler) NotifyOnce(ctx context.Context) error {
	snapshot, err := s.provider.Snapshot()
	if err != nil {
		return fmt.Errorf("get presence snapshot: %w", err)
	}

	var notificationErrors []error
	for _, destination := range s.notifiers {
		if err := destination.Notify(ctx, snapshot); err != nil {
			notificationErrors = append(notificationErrors, err)
		}
	}
	return errors.Join(notificationErrors...)
}
