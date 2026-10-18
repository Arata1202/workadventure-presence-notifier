package scheduler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/notifier"
	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

type fakeProvider struct {
	snapshot presence.Snapshot
	err      error
}

func (f fakeProvider) Run(context.Context) error       { return nil }
func (f fakeProvider) WaitReady(context.Context) error { return nil }
func (f fakeProvider) Snapshot() (presence.Snapshot, error) {
	return f.snapshot, f.err
}

type recordingNotifier struct {
	snapshots []presence.Snapshot
}

func (r *recordingNotifier) Notify(_ context.Context, snapshot presence.Snapshot) error {
	r.snapshots = append(r.snapshots, snapshot)
	return nil
}

var _ notifier.Notifier = (*recordingNotifier)(nil)

func TestNotifyOnce(t *testing.T) {
	destination := &recordingNotifier{}
	scheduler := New(
		time.Minute,
		fakeProvider{snapshot: presence.Snapshot{Users: []presence.User{{ID: "1", Name: "Alice"}}}},
		[]notifier.Notifier{destination},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	if err := scheduler.NotifyOnce(context.Background()); err != nil {
		t.Fatalf("NotifyOnce() error = %v", err)
	}
	if len(destination.snapshots) != 1 || destination.snapshots[0].Count() != 1 {
		t.Fatalf("snapshots = %#v", destination.snapshots)
	}
}
