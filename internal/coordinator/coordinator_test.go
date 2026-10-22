package coordinator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/notifier"
	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

type fakeProvider struct {
	snapshot presence.Snapshot
	changes  chan struct{}
}

func (p *fakeProvider) Run(context.Context) error            { return nil }
func (p *fakeProvider) WaitReady(context.Context) error      { return nil }
func (p *fakeProvider) Snapshot() (presence.Snapshot, error) { return p.snapshot, nil }
func (p *fakeProvider) Changes() <-chan struct{}             { return p.changes }

type fakeStatus struct {
	destinationID string
	mu            sync.Mutex
	nextRef       notifier.MessageRef
	actions       []string
	deleteErr     map[notifier.MessageRef]error
	onDelete      func(notifier.MessageRef)
}

func (s *fakeStatus) DestinationID() string {
	if s.destinationID != "" {
		return s.destinationID
	}
	return "slack:C123"
}
func (s *fakeStatus) Render(presence.Snapshot) (string, error) { return "online", nil }
func (s *fakeStatus) Post(context.Context, string) (notifier.MessageRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actions = append(s.actions, "post:"+string(s.nextRef))
	return s.nextRef, nil
}
func (s *fakeStatus) Delete(_ context.Context, ref notifier.MessageRef) error {
	s.mu.Lock()
	s.actions = append(s.actions, "delete:"+string(ref))
	err := s.deleteErr[ref]
	onDelete := s.onDelete
	s.mu.Unlock()
	if onDelete != nil {
		onDelete(ref)
	}
	return err
}

func (s *fakeStatus) actionsSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.actions...)
}

func (s *fakeStatus) setNextRef(ref notifier.MessageRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextRef = ref
}

func TestPublishPersistsReplacementBeforeDeletingPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := &stateFile{path: path}
	if err := state.save(persistedState{DestinationID: "slack:C123", Current: "111.222"}); err != nil {
		t.Fatal(err)
	}
	status := &fakeStatus{nextRef: "333.444", deleteErr: map[notifier.MessageRef]error{}}
	status.onDelete = func(ref notifier.MessageRef) {
		if ref != "111.222" {
			return
		}
		loaded, err := state.load()
		if err != nil {
			t.Error(err)
			return
		}
		want := persistedState{DestinationID: "slack:C123", Current: "333.444", Stale: []notifier.MessageRef{"111.222"}}
		if !reflect.DeepEqual(loaded, want) {
			t.Errorf("state during delete = %#v, want %#v", loaded, want)
		}
	}
	coordinator, err := New(0, &fakeProvider{}, status, path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Publish(context.Background(), onlineSnapshot()); err != nil {
		t.Fatal(err)
	}
	wantActions := []string{"post:333.444", "delete:111.222"}
	if actions := status.actionsSnapshot(); !reflect.DeepEqual(actions, wantActions) {
		t.Fatalf("actions = %v, want %v", actions, wantActions)
	}
	loaded, err := state.load()
	if err != nil || loaded.Current != "333.444" || len(loaded.Stale) != 0 {
		t.Fatalf("state.load() = %#v, %v", loaded, err)
	}
}

func TestPublishRetainsFailedStaleAndRetriesItBeforeLaterPost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := &stateFile{path: path}
	if err := state.save(persistedState{DestinationID: "slack:C123", Current: "111.222"}); err != nil {
		t.Fatal(err)
	}
	status := &fakeStatus{
		nextRef:   "333.444",
		deleteErr: map[notifier.MessageRef]error{"111.222": errors.New("denied")},
	}
	coordinator, err := New(0, &fakeProvider{}, status, path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Publish(context.Background(), onlineSnapshot()); err != nil {
		t.Fatal(err)
	}
	loaded, err := state.load()
	want := persistedState{DestinationID: "slack:C123", Current: "333.444", Stale: []notifier.MessageRef{"111.222"}}
	if err != nil || !reflect.DeepEqual(loaded, want) {
		t.Fatalf("state after failed delete = %#v, %v; want %#v", loaded, err, want)
	}

	delete(status.deleteErr, "111.222")
	status.setNextRef("555.666")
	if err := coordinator.Publish(context.Background(), onlineSnapshot()); err != nil {
		t.Fatal(err)
	}
	wantActions := []string{
		"post:333.444", "delete:111.222",
		"delete:111.222", "post:555.666", "delete:333.444",
	}
	if actions := status.actionsSnapshot(); !reflect.DeepEqual(actions, wantActions) {
		t.Fatalf("actions = %v, want %v", actions, wantActions)
	}
}

func TestPublishDoesNotPostWhenStaleLimitRemainsFull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	stale := []notifier.MessageRef{"111.111", "222.222", "333.333"}
	if err := (&stateFile{path: path}).save(persistedState{DestinationID: "slack:C123", Current: "444.444", Stale: stale}); err != nil {
		t.Fatal(err)
	}
	status := &fakeStatus{nextRef: "555.555", deleteErr: map[notifier.MessageRef]error{}}
	for _, ref := range stale {
		status.deleteErr[ref] = errors.New("denied")
	}
	coordinator, err := New(0, &fakeProvider{}, status, path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Publish(context.Background(), onlineSnapshot()); err == nil {
		t.Fatal("Publish() error = nil")
	}
	for _, action := range status.actionsSnapshot() {
		if len(action) >= 5 && action[:5] == "post:" {
			t.Fatalf("unexpected post action: %v", status.actionsSnapshot())
		}
	}
}

func TestPublishDeletesNewMessageWhenStateSaveFails(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(directory, "state.json")
	coordinator, err := New(0, &fakeProvider{}, &fakeStatus{}, path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	status := &fakeStatus{nextRef: "111.222", deleteErr: map[notifier.MessageRef]error{}}
	coordinator.status = status
	if err := os.WriteFile(directory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Publish(context.Background(), onlineSnapshot()); err == nil {
		t.Fatal("Publish() error = nil")
	}
	want := []string{"post:111.222", "delete:111.222"}
	if actions := status.actionsSnapshot(); !reflect.DeepEqual(actions, want) {
		t.Fatalf("actions = %v, want %v", actions, want)
	}
}

func TestEmptySnapshotDeletesAllKnownReferencesAndRetainsFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := &stateFile{path: path}
	initial := persistedState{DestinationID: "slack:C123", Current: "111.111", Stale: []notifier.MessageRef{"222.222", "333.333"}}
	if err := state.save(initial); err != nil {
		t.Fatal(err)
	}
	status := &fakeStatus{deleteErr: map[notifier.MessageRef]error{"222.222": errors.New("denied")}}
	coordinator, err := New(0, &fakeProvider{}, status, path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Publish(context.Background(), presence.Snapshot{}); err == nil {
		t.Fatal("Publish() error = nil")
	}
	loaded, err := state.load()
	want := persistedState{DestinationID: "slack:C123", Stale: []notifier.MessageRef{"222.222"}}
	if err != nil || !reflect.DeepEqual(loaded, want) {
		t.Fatalf("state = %#v, %v; want %#v", loaded, err, want)
	}
	delete(status.deleteErr, "222.222")
	if err := coordinator.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state file still exists: %v", err)
	}
}

func TestStateFileIsPrivateAndBoundsStaleReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.json")
	state := &stateFile{path: path}
	written := persistedState{DestinationID: "slack:C123", Current: "111.222", Stale: []notifier.MessageRef{"333.444"}}
	if err := state.save(written); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	loaded, err := state.load()
	if err != nil || !reflect.DeepEqual(loaded, written) {
		t.Fatalf("state.load() = %#v, %v", loaded, err)
	}
	tooMany := persistedState{DestinationID: "slack:C123", Stale: []notifier.MessageRef{"1.1", "2.2", "3.3", "4.4"}}
	if err := state.save(tooMany); err == nil {
		t.Fatal("save() error = nil for too many stale references")
	}
}

func TestNewRejectsPersistedDifferentDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := &stateFile{path: path}
	if err := state.save(persistedState{DestinationID: "slack:COLD", Current: "111.222"}); err != nil {
		t.Fatal(err)
	}
	_, err := New(0, &fakeProvider{}, &fakeStatus{destinationID: "slack:CNEW"}, path, testLogger())
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("New() error = %v", err)
	}
}

func TestRunDebouncesPresenceChanges(t *testing.T) {
	provider := &fakeProvider{snapshot: onlineSnapshot(), changes: make(chan struct{}, 2)}
	status := &fakeStatus{nextRef: "111.222", deleteErr: map[notifier.MessageRef]error{}}
	coordinator, err := New(20*time.Millisecond, provider, status, filepath.Join(t.TempDir(), "state.json"), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()

	deadline := time.Now().Add(time.Second)
	for len(status.actionsSnapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	status.setNextRef("333.444")
	provider.changes <- struct{}{}
	time.Sleep(5 * time.Millisecond)
	provider.changes <- struct{}{}
	time.Sleep(40 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	posts := 0
	for _, action := range status.actionsSnapshot() {
		if len(action) >= 5 && action[:5] == "post:" {
			posts++
		}
	}
	if posts != 2 {
		t.Fatalf("actions = %v, want one startup post and one debounced post", status.actionsSnapshot())
	}
}

func onlineSnapshot() presence.Snapshot {
	return presence.Snapshot{Users: []presence.User{{ID: "1", Name: "Alice"}}}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
