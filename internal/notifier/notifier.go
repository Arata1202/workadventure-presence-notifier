package notifier

import (
	"context"

	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

// Notifier sends a presence snapshot to an external service.
type Notifier interface {
	Notify(ctx context.Context, snapshot presence.Snapshot) error
}
