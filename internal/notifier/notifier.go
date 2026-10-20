package notifier

import (
	"context"
	"errors"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

// MessageRef is an opaque, destination-specific message identifier.
type MessageRef string

// ErrMessageNotFound indicates that a referenced destination message no longer exists.
var ErrMessageNotFound = errors.New("destination message not found")

// Destination renders and maintains one recoverable presence status.
// Implementations own all platform-specific API, metadata, and history behavior.
type Destination interface {
	ID() string
	Render(presence.Snapshot) (string, error)
	Post(context.Context, string, string) (MessageRef, error)
	Update(context.Context, MessageRef, string, string) error
	Delete(context.Context, MessageRef) error
	// FindByOperation returns matching messages in newest-first order.
	FindByOperation(context.Context, string, time.Time) ([]MessageRef, error)
}
