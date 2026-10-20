package status

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Arata1202/workadventure-presence-notifier/internal/notifier/slack"
	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

type memoryStore struct {
	state   State
	saveErr error
	saves   []State
	trace   *[]string
}

func (s *memoryStore) Load() (State, error) { return s.state, nil }
func (s *memoryStore) Save(state State) error {
	if s.trace != nil {
		*s.trace = append(*s.trace, "save")
	}
	if s.saveErr != nil {
		return s.saveErr
	}
	s.state = state
	s.saves = append(s.saves, state)
	return nil
}

type fakeSlack struct {
	channel     string
	postTS      string
	postErr     error
	updateErr   error
	deleteErr   map[string]error
	findResults [][]slack.Message
	findErr     error
	calls       []string
	postIDs     []string
	trace       *[]string
}

func (s *fakeSlack) ChannelID() string { return s.channel }
func (s *fakeSlack) Render(snapshot presence.Snapshot) (string, error) {
	return snapshot.Users[0].Name, nil
}
func (s *fakeSlack) Post(_ context.Context, text, operationID string) (string, error) {
	s.calls = append(s.calls, "post:"+text)
	s.postIDs = append(s.postIDs, operationID)
	if s.trace != nil {
		*s.trace = append(*s.trace, "post")
	}
	return s.postTS, s.postErr
}
func (s *fakeSlack) Update(_ context.Context, ts, text, operationID string) error {
	s.calls = append(s.calls, "update:"+ts+":"+text+":"+operationID)
	return s.updateErr
}
func (s *fakeSlack) Delete(_ context.Context, ts string) error {
	s.calls = append(s.calls, "delete:"+ts)
	return s.deleteErr[ts]
}
func (s *fakeSlack) FindByOperation(_ context.Context, operationID, oldest string) ([]slack.Message, error) {
	s.calls = append(s.calls, "find:"+operationID)
	if s.findErr != nil {
		return nil, s.findErr
	}
	if len(s.findResults) == 0 {
		return nil, nil
	}
	result := s.findResults[0]
	s.findResults = s.findResults[1:]
	return result, nil
}

func TestReconcilePersistsOperationBeforePostThenDeletesOld(t *testing.T) {
	var trace []string
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", MessageTS: "old", ContentHash: "old-hash"}, trace: &trace}
	destination := &fakeSlack{channel: "C1", postTS: "new", deleteErr: map[string]error{}, trace: &trace}
	reconciler, err := NewReconciler(destination, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(destination.calls, []string{"post:Alice", "delete:old"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
	if len(store.saves) != 4 || store.saves[0].Pending == nil || store.saves[0].Pending.AttemptedAt != "" ||
		store.saves[1].Pending.AttemptedAt == "" || store.saves[2].MessageTS != "new" {
		t.Fatalf("saves = %#v", store.saves)
	}
	if !reflect.DeepEqual(trace, []string{"save", "save", "post", "save", "save"}) {
		t.Fatalf("operation order = %#v", trace)
	}
}

func TestAcceptedPostWithLostResponseRecoveredThroughHistory(t *testing.T) {
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", MessageTS: "old", ContentHash: "old-hash"}}
	destination := &fakeSlack{channel: "C1", postErr: errors.New("lost response"), deleteErr: map[string]error{}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err == nil {
		t.Fatal("first Reconcile() error = nil")
	}
	operationID := store.state.Pending.OperationID
	if store.state.Pending.AttemptedAt == "" {
		t.Fatal("attempt was not persisted")
	}
	destination.postErr = nil
	destination.findResults = [][]slack.Message{{{Timestamp: "new"}}}
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(destination.calls, []string{"post:Alice", "find:" + operationID, "delete:old"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
}

func TestMultipleMetadataMatchesChooseNewestAndCleanOthers(t *testing.T) {
	pending := &Pending{OperationID: "op", ContentHash: contentHash("Alice"), AttemptedAt: "100.000000"}
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", MessageTS: "old", Pending: pending}}
	destination := &fakeSlack{channel: "C1", deleteErr: map[string]error{}, findResults: [][]slack.Message{{
		{Timestamp: "101.000001"}, {Timestamp: "102.000001"},
	}}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err != nil {
		t.Fatal(err)
	}
	if store.state.MessageTS != "102.000001" || store.state.Pending != nil {
		t.Fatalf("state = %#v", store.state)
	}
	if !reflect.DeepEqual(destination.calls, []string{"find:op", "delete:101.000001", "delete:old"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
}

func TestMissingHistoryScopeRetainsOldStateAndDoesNotRepost(t *testing.T) {
	pending := &Pending{OperationID: "op", ContentHash: contentHash("Alice"), AttemptedAt: "100.000000"}
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", MessageTS: "old", ContentHash: "old-hash", Pending: pending}}
	destination := &fakeSlack{channel: "C1", findErr: errors.New("missing_scope"), deleteErr: map[string]error{}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err == nil {
		t.Fatal("Reconcile() error = nil")
	}
	if store.state.MessageTS != "old" || store.state.Pending != pending {
		t.Fatalf("state = %#v", store.state)
	}
	if !reflect.DeepEqual(destination.calls, []string{"find:op"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
}

func TestUpdateMessageNotFoundRechecksHistoryThenPostsFresh(t *testing.T) {
	pending := &Pending{OperationID: "op", ContentHash: contentHash("Alice"), AttemptedAt: "100.000000"}
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", Pending: pending}}
	destination := &fakeSlack{
		channel: "C1", postTS: "new", updateErr: slack.ErrMessageNotFound, deleteErr: map[string]error{},
		findResults: [][]slack.Message{{{Timestamp: "missing"}}, nil},
	}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), snapshot("Bob"), false); err != nil {
		t.Fatal(err)
	}
	if store.state.MessageTS != "new" || store.state.Pending != nil {
		t.Fatalf("state = %#v", store.state)
	}
	wantPrefix := []string{"find:op", "update:missing:Bob:op", "find:op", "post:Bob", "delete:missing"}
	if !reflect.DeepEqual(destination.calls, wantPrefix) {
		t.Fatalf("calls = %#v", destination.calls)
	}
}

func TestZeroUsersDeletesKnownAndAttemptedPendingMatches(t *testing.T) {
	pending := &Pending{OperationID: "op", ContentHash: "hash", AttemptedAt: "100.000000"}
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", MessageTS: "current", Stale: []string{"stale"}, Pending: pending}}
	destination := &fakeSlack{channel: "C1", deleteErr: map[string]error{}, findResults: [][]slack.Message{{{Timestamp: "pending"}}}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), presence.Snapshot{}, false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(destination.calls, []string{"delete:current", "delete:stale", "find:op", "delete:pending"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
	if store.state.MessageTS != "" || store.state.Pending != nil || store.state.ChannelID != "" {
		t.Fatalf("state = %#v", store.state)
	}
}

func TestZeroUsersClearsNeverAttemptedPendingWithoutSlackLookup(t *testing.T) {
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", Pending: &Pending{OperationID: "op", ContentHash: "hash"}}}
	destination := &fakeSlack{channel: "C1", deleteErr: map[string]error{}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), presence.Snapshot{}, false); err != nil {
		t.Fatal(err)
	}
	if len(destination.calls) != 0 || store.state.Pending != nil {
		t.Fatalf("calls = %#v, state = %#v", destination.calls, store.state)
	}
}

func TestZeroUsersHistoryFailureDeletesKnownButRetainsPending(t *testing.T) {
	pending := &Pending{OperationID: "op", ContentHash: "hash", AttemptedAt: "100.000000"}
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", MessageTS: "current", Pending: pending}}
	destination := &fakeSlack{channel: "C1", findErr: errors.New("missing_scope"), deleteErr: map[string]error{}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), presence.Snapshot{}, false); err == nil {
		t.Fatal("Reconcile() error = nil")
	}
	if !reflect.DeepEqual(destination.calls, []string{"delete:current", "find:op"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
	if store.state.MessageTS != "" || store.state.Pending == nil {
		t.Fatalf("state = %#v", store.state)
	}
}

func TestPendingSaveFailureMakesNoSlackCall(t *testing.T) {
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", MessageTS: "old"}, saveErr: errors.New("disk full")}
	destination := &fakeSlack{channel: "C1", postTS: "new", deleteErr: map[string]error{}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err == nil {
		t.Fatal("Reconcile() error = nil")
	}
	if len(destination.calls) != 0 {
		t.Fatalf("calls = %#v", destination.calls)
	}
}

func snapshot(name string) presence.Snapshot {
	return presence.Snapshot{Users: []presence.User{{ID: "1", Name: name}}}
}
