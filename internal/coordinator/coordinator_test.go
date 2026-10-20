package coordinator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

type fakeProvider struct {
	ready    chan struct{}
	changes  chan struct{}
	snapshot presence.Snapshot
}

func (f *fakeProvider) Run(context.Context) error { return nil }
func (f *fakeProvider) WaitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.ready:
		return nil
	}
}
func (f *fakeProvider) Snapshot() (presence.Snapshot, error) { return f.snapshot, nil }
func (f *fakeProvider) Changes() <-chan struct{}             { return f.changes }

type recordingReconciler struct {
	mu     sync.Mutex
	forces []bool
	times  []time.Time
	calls  chan struct{}
	err    error
}

func (r *recordingReconciler) Reconcile(_ context.Context, _ presence.Snapshot, force bool) error {
	r.mu.Lock()
	r.forces = append(r.forces, force)
	r.times = append(r.times, time.Now())
	err := r.err
	r.mu.Unlock()
	select {
	case r.calls <- struct{}{}:
	default:
	}
	return err
}

func (r *recordingReconciler) setError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func TestRunWaitsForInitialSnapshotAndCoalescesChanges(t *testing.T) {
	ready := make(chan struct{})
	source := &fakeProvider{ready: ready, changes: make(chan struct{}, 4), snapshot: presence.Snapshot{Users: []presence.User{{ID: "1", Name: "Alice"}}}}
	destination := &recordingReconciler{calls: make(chan struct{}, 4)}
	coordinator := New(25*time.Millisecond, time.Hour, source, destination, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	select {
	case <-destination.calls:
		t.Fatal("reconciled before ready")
	case <-time.After(20 * time.Millisecond):
	}
	close(ready)
	waitCall(t, destination.calls)
	source.changes <- struct{}{}
	source.changes <- struct{}{}
	waitCall(t, destination.calls)
	time.Sleep(40 * time.Millisecond)
	destination.mu.Lock()
	forces := append([]bool(nil), destination.forces...)
	destination.mu.Unlock()
	if len(forces) != 2 || !forces[0] || forces[1] {
		t.Fatalf("forces = %#v", forces)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunContinuesAfterInitialReconciliationFailure(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	source := &fakeProvider{ready: ready, changes: make(chan struct{}, 1), snapshot: presence.Snapshot{Users: []presence.User{{ID: "1", Name: "Alice"}}}}
	destination := &recordingReconciler{calls: make(chan struct{}, 4), err: errors.New("Slack unavailable")}
	coordinator := New(5*time.Millisecond, time.Hour, source, destination, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	waitCall(t, destination.calls)
	select {
	case err := <-done:
		t.Fatalf("Run() stopped after initial failure: %v", err)
	default:
	}
	destination.setError(nil)
	source.changes <- struct{}{}
	waitCall(t, destination.calls)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunDoesNotTightlyRetryFailedChange(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	source := &fakeProvider{ready: ready, changes: make(chan struct{}, 1), snapshot: presence.Snapshot{Users: []presence.User{{ID: "1", Name: "Alice"}}}}
	destination := &recordingReconciler{calls: make(chan struct{}, 4)}
	coordinator := New(5*time.Millisecond, time.Hour, source, destination, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	waitCall(t, destination.calls)
	destination.setError(errors.New("permanent"))
	source.changes <- struct{}{}
	waitCall(t, destination.calls)
	select {
	case <-destination.calls:
		t.Fatal("unexpected automatic retry")
	case <-time.After(40 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunRetriesFailuresWithCappedExponentialBackoff(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	source := &fakeProvider{ready: ready, changes: make(chan struct{}, 1), snapshot: presence.Snapshot{Users: []presence.User{{ID: "1", Name: "Alice"}}}}
	destination := &recordingReconciler{calls: make(chan struct{}, 8), err: errors.New("temporary")}
	changeCoordinator := New(time.Millisecond, 40*time.Millisecond, source, destination, slog.New(slog.NewTextHandler(io.Discard, nil)))
	changeCoordinator.retryInitial = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- changeCoordinator.Run(ctx) }()
	waitCall(t, destination.calls)
	waitCall(t, destination.calls)
	waitCall(t, destination.calls)
	destination.mu.Lock()
	times := append([]time.Time(nil), destination.times...)
	destination.mu.Unlock()
	if first := times[1].Sub(times[0]); first < 8*time.Millisecond {
		t.Fatalf("first retry delay = %v", first)
	}
	if second := times[2].Sub(times[1]); second < 18*time.Millisecond {
		t.Fatalf("second retry delay = %v", second)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestNewChangeResetsRetryDelay(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	source := &fakeProvider{ready: ready, changes: make(chan struct{}, 1), snapshot: presence.Snapshot{Users: []presence.User{{ID: "1", Name: "Alice"}}}}
	destination := &recordingReconciler{calls: make(chan struct{}, 8), err: errors.New("temporary")}
	changeCoordinator := New(time.Millisecond, time.Hour, source, destination, slog.New(slog.NewTextHandler(io.Discard, nil)))
	changeCoordinator.retryInitial = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- changeCoordinator.Run(ctx) }()
	waitCall(t, destination.calls)
	waitCall(t, destination.calls)
	source.changes <- struct{}{}
	waitCall(t, destination.calls)
	waitCall(t, destination.calls)
	destination.mu.Lock()
	times := append([]time.Time(nil), destination.times...)
	destination.mu.Unlock()
	if resetDelay := times[3].Sub(times[2]); resetDelay < 8*time.Millisecond || resetDelay > 30*time.Millisecond {
		t.Fatalf("retry delay after new change = %v", resetDelay)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func waitCall(t *testing.T, calls <-chan struct{}) {
	t.Helper()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconciliation")
	}
}
