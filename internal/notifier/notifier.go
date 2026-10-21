package notifier

import (
	"context"
	"errors"

	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

type MessageRef string

var ErrMessageNotFound = errors.New("destination message not found")

// Status maintains one presence status in an external destination.
type Status interface {
	// DestinationID returns a stable, non-secret identity for persisted message references.
	DestinationID() string
	Render(presence.Snapshot) (string, error)
	Post(context.Context, string) (MessageRef, error)
	Delete(context.Context, MessageRef) error
}
