package status

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

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
	findOldest  []string
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
	s.findOldest = append(s.findOldest, oldest)
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
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", MessageTS: "1000.000000"}, trace: &trace}
	destination := &fakeSlack{channel: "C1", postTS: "2000.000000", deleteErr: map[string]error{}, trace: &trace}
	reconciler, err := NewReconciler(destination, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(destination.calls, []string{"post:Alice", "delete:1000.000000"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
	if len(store.saves) != 4 || store.saves[0].Pending == nil || store.saves[0].Pending.AttemptedAt != "" ||
		store.saves[1].Pending.AttemptedAt == "" || store.saves[2].MessageTS != "2000.000000" {
		t.Fatalf("saves = %#v", store.saves)
	}
	if !reflect.DeepEqual(trace, []string{"save", "save", "post", "save", "save"}) {
		t.Fatalf("operation order = %#v", trace)
	}
}

func TestAcceptedPostWithLostResponseRecoveredThroughHistory(t *testing.T) {
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", MessageTS: "1000.000000"}}
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
	destination.findResults = [][]slack.Message{{{Timestamp: "2000.000000"}}}
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(destination.calls, []string{"post:Alice", "find:" + operationID, "update:2000.000000:Alice:" + operationID, "delete:1000.000000"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
}

func TestMultipleMetadataMatchesChooseNewestAndCleanOthers(t *testing.T) {
	pending := &Pending{OperationID: "op", AttemptedAt: "1000.000000"}
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", MessageTS: "800.000000", Pending: pending}}
	destination := &fakeSlack{channel: "C1", deleteErr: map[string]error{}, findResults: [][]slack.Message{{
		{Timestamp: "901.000001"}, {Timestamp: "902.000001"},
	}}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err != nil {
		t.Fatal(err)
	}
	if store.state.MessageTS != "902.000001" || store.state.Pending != nil {
		t.Fatalf("state = %#v", store.state)
	}
	if !reflect.DeepEqual(destination.calls, []string{"find:op", "update:902.000001:Alice:op", "delete:901.000001", "delete:800.000000"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
	if !reflect.DeepEqual(destination.findOldest, []string{"700.000000"}) {
		t.Fatalf("history oldest = %#v", destination.findOldest)
	}
}

func TestMissingHistoryScopeRetainsOldStateAndDoesNotRepost(t *testing.T) {
	pending := &Pending{OperationID: "op", AttemptedAt: "100.000000"}
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", MessageTS: "90.000000", Pending: pending}}
	destination := &fakeSlack{channel: "C1", findErr: errors.New("missing_scope"), deleteErr: map[string]error{}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err == nil {
		t.Fatal("Reconcile() error = nil")
	}
	if store.state.MessageTS != "90.000000" || store.state.Pending != pending {
		t.Fatalf("state = %#v", store.state)
	}
	if !reflect.DeepEqual(destination.calls, []string{"find:op"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
}

func TestUpdateMessageNotFoundRechecksHistoryThenPostsFresh(t *testing.T) {
	pending := &Pending{OperationID: "op", AttemptedAt: "100.000000"}
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", Pending: pending}}
	destination := &fakeSlack{
		channel: "C1", postTS: "300.000000", updateErr: slack.ErrMessageNotFound, deleteErr: map[string]error{},
		findResults: [][]slack.Message{{{Timestamp: "200.000000"}}, nil},
	}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), snapshot("Bob"), false); err != nil {
		t.Fatal(err)
	}
	if store.state.MessageTS != "300.000000" || store.state.Pending != nil {
		t.Fatalf("state = %#v", store.state)
	}
	wantPrefix := []string{"find:op", "update:200.000000:Bob:op", "find:op", "post:Bob", "delete:200.000000"}
	if !reflect.DeepEqual(destination.calls, wantPrefix) {
		t.Fatalf("calls = %#v", destination.calls)
	}
}

func TestZeroUsersDeletesKnownAndAttemptedPendingMatches(t *testing.T) {
	pending := &Pending{OperationID: "op", AttemptedAt: "100.000000"}
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", MessageTS: "300.000000", Stale: []string{"200.000000"}, Pending: pending}}
	destination := &fakeSlack{channel: "C1", deleteErr: map[string]error{}, findResults: [][]slack.Message{{{Timestamp: "400.000000"}}}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), presence.Snapshot{}, false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(destination.calls, []string{"delete:300.000000", "delete:200.000000", "find:op", "delete:400.000000"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
	if store.state.MessageTS != "" || store.state.Pending != nil || store.state.ChannelID != "" {
		t.Fatalf("state = %#v", store.state)
	}
}

func TestZeroUsersClearsNeverAttemptedPendingWithoutSlackLookup(t *testing.T) {
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", Pending: &Pending{OperationID: "op"}}}
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
	pending := &Pending{OperationID: "op", AttemptedAt: "100.000000"}
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", MessageTS: "300.000000", Pending: pending}}
	destination := &fakeSlack{channel: "C1", findErr: errors.New("missing_scope"), deleteErr: map[string]error{}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), presence.Snapshot{}, false); err == nil {
		t.Fatal("Reconcile() error = nil")
	}
	if !reflect.DeepEqual(destination.calls, []string{"delete:300.000000", "find:op"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
	if store.state.MessageTS != "" || store.state.Pending == nil {
		t.Fatalf("state = %#v", store.state)
	}
}

func TestPendingSaveFailureMakesNoSlackCall(t *testing.T) {
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", MessageTS: "100.000000"}, saveErr: errors.New("disk full")}
	destination := &fakeSlack{channel: "C1", postTS: "200.000000", deleteErr: map[string]error{}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err == nil {
		t.Fatal("Reconcile() error = nil")
	}
	if len(destination.calls) != 0 {
		t.Fatalf("calls = %#v", destination.calls)
	}
}

func TestNoMatchWaitsForRecoveryGraceBeforeReposting(t *testing.T) {
	now := time.Unix(1000, 0)
	pending := &Pending{OperationID: "op", AttemptedAt: "1000.000000"}
	store := &memoryStore{state: State{Version: 1, ChannelID: "C1", Pending: pending}}
	destination := &fakeSlack{channel: "C1", postTS: "1100.000000", deleteErr: map[string]error{}}
	reconciler, err := NewReconciler(destination, store, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); !errors.Is(err, ErrPendingRecoveryGrace) {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if store.state.Pending.AttemptedAt != "1000.000000" || containsCall(destination.calls, "post:Alice") {
		t.Fatalf("calls = %#v, state = %#v", destination.calls, store.state)
	}
	now = now.Add(DefaultRecoveryGracePeriod)
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err != nil {
		t.Fatal(err)
	}
	if store.state.MessageTS != "1100.000000" || !containsCall(destination.calls, "post:Alice") {
		t.Fatalf("calls = %#v, state = %#v", destination.calls, store.state)
	}
}

func TestUnchangedReconciliationUsesOnlyInMemoryRenderedHash(t *testing.T) {
	store := &memoryStore{state: State{Version: 1}}
	destination := &fakeSlack{channel: "C1", postTS: "1000.000000", deleteErr: map[string]error{}}
	reconciler, err := NewReconciler(destination, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err != nil {
		t.Fatal(err)
	}
	callsAfterPost := append([]string(nil), destination.calls...)
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(destination.calls, callsAfterPost) {
		t.Fatalf("unchanged reconciliation calls = %#v, want %#v", destination.calls, callsAfterPost)
	}
}

func TestNewReconcilerRejectsInvalidLoadedState(t *testing.T) {
	tests := []State{
		{Version: 1, MessageTS: "100.000000"},
		{Version: 1, ChannelID: "C2", MessageTS: "100.000000"},
		{Version: 1, ChannelID: "C1", MessageTS: "invalid"},
		{Version: 1, ChannelID: "C1", MessageTS: "100.000000", Stale: []string{"100.000000"}},
		{Version: 99},
	}
	for _, state := range tests {
		store := &memoryStore{state: state}
		if _, err := NewReconciler(&fakeSlack{channel: "C1"}, store); err == nil {
			t.Fatalf("NewReconciler() error = nil for %#v", state)
		}
	}
}

func containsCall(calls []string, target string) bool {
	for _, call := range calls {
		if call == target {
			return true
		}
	}
	return false
}

func snapshot(name string) presence.Snapshot {
	return presence.Snapshot{Users: []presence.User{{ID: "1", Name: name}}}
}
