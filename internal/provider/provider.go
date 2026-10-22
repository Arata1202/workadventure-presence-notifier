package provider

import (
	"context"

	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

// Provider supplies current WorkAdventure presence information.
type Provider interface {
	Run(ctx context.Context) error
	WaitReady(ctx context.Context) error
	Snapshot() (presence.Snapshot, error)
	Changes() <-chan struct{}
}
