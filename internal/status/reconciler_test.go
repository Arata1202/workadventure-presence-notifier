package status

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/notifier"
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

type fakeDestination struct {
	destinationID string
	postTS        notifier.MessageRef
	postErr       error
	updateErr     error
	deleteErr     map[string]error
	findResults   [][]notifier.MessageRef
	findErr       error
	calls         []string
	postIDs       []string
	findOldest    []time.Time
	trace         *[]string
}

func (s *fakeDestination) ID() string { return s.destinationID }
func (s *fakeDestination) Render(snapshot presence.Snapshot) (string, error) {
	return snapshot.Users[0].Name, nil
}
func (s *fakeDestination) Post(_ context.Context, text, operationID string) (notifier.MessageRef, error) {
	s.calls = append(s.calls, "post:"+text)
	s.postIDs = append(s.postIDs, operationID)
	if s.trace != nil {
		*s.trace = append(*s.trace, "post")
	}
	return s.postTS, s.postErr
}
func (s *fakeDestination) Update(_ context.Context, ref notifier.MessageRef, text, operationID string) error {
	s.calls = append(s.calls, "update:"+string(ref)+":"+text+":"+operationID)
	return s.updateErr
}
func (s *fakeDestination) Delete(_ context.Context, ref notifier.MessageRef) error {
	s.calls = append(s.calls, "delete:"+string(ref))
	return s.deleteErr[string(ref)]
}
func (s *fakeDestination) FindByOperation(_ context.Context, operationID string, oldest time.Time) ([]notifier.MessageRef, error) {
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
	store := &memoryStore{state: State{Version: stateVersion, DestinationID: "destination-1", Current: "1000.000000"}, trace: &trace}
	destination := &fakeDestination{destinationID: "destination-1", postTS: "2000.000000", deleteErr: map[string]error{}, trace: &trace}
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
	if len(store.saves) != 4 || store.saves[0].Pending == nil || store.saves[0].Pending.AttemptedAt != nil ||
		store.saves[1].Pending.AttemptedAt == nil || store.saves[2].Current != "2000.000000" {
		t.Fatalf("saves = %#v", store.saves)
	}
	if !reflect.DeepEqual(trace, []string{"save", "save", "post", "save", "save"}) {
		t.Fatalf("operation order = %#v", trace)
	}
}

func TestAcceptedPostWithLostResponseRecoveredThroughHistory(t *testing.T) {
	store := &memoryStore{state: State{Version: stateVersion, DestinationID: "destination-1", Current: "1000.000000"}}
	destination := &fakeDestination{destinationID: "destination-1", postErr: errors.New("lost response"), deleteErr: map[string]error{}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err == nil {
		t.Fatal("first Reconcile() error = nil")
	}
	operationID := store.state.Pending.OperationID
	if store.state.Pending.AttemptedAt == nil {
		t.Fatal("attempt was not persisted")
	}
	destination.postErr = nil
	destination.findResults = [][]notifier.MessageRef{{"2000.000000"}}
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(destination.calls, []string{"post:Alice", "find:" + operationID, "update:2000.000000:Alice:" + operationID, "delete:1000.000000"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
}

func TestMultipleMetadataMatchesChooseNewestAndCleanOthers(t *testing.T) {
	pending := &Pending{OperationID: "op", AttemptedAt: timePtr(time.Unix(1000, 0))}
	store := &memoryStore{state: State{Version: stateVersion, DestinationID: "destination-1", Current: "800.000000", Pending: pending}}
	destination := &fakeDestination{destinationID: "destination-1", deleteErr: map[string]error{}, findResults: [][]notifier.MessageRef{{
		"902.000001", "901.000001",
	}}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err != nil {
		t.Fatal(err)
	}
	if store.state.Current != "902.000001" || store.state.Pending != nil {
		t.Fatalf("state = %#v", store.state)
	}
	if !reflect.DeepEqual(destination.calls, []string{"find:op", "update:902.000001:Alice:op", "delete:901.000001", "delete:800.000000"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
	if !reflect.DeepEqual(destination.findOldest, []time.Time{time.Unix(700, 0)}) {
		t.Fatalf("history oldest = %#v", destination.findOldest)
	}
}

func TestMissingHistoryScopeRetainsOldStateAndDoesNotRepost(t *testing.T) {
	pending := &Pending{OperationID: "op", AttemptedAt: timePtr(time.Unix(100, 0))}
	store := &memoryStore{state: State{Version: stateVersion, DestinationID: "destination-1", Current: "90.000000", Pending: pending}}
	destination := &fakeDestination{destinationID: "destination-1", findErr: errors.New("missing_scope"), deleteErr: map[string]error{}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err == nil {
		t.Fatal("Reconcile() error = nil")
	}
	if store.state.Current != "90.000000" || store.state.Pending != pending {
		t.Fatalf("state = %#v", store.state)
	}
	if !reflect.DeepEqual(destination.calls, []string{"find:op"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
}

func TestUpdateMessageNotFoundRechecksHistoryThenPostsFresh(t *testing.T) {
	pending := &Pending{OperationID: "op", AttemptedAt: timePtr(time.Unix(100, 0))}
	store := &memoryStore{state: State{Version: stateVersion, DestinationID: "destination-1", Pending: pending}}
	destination := &fakeDestination{
		destinationID: "destination-1", postTS: "300.000000", updateErr: notifier.ErrMessageNotFound, deleteErr: map[string]error{},
		findResults: [][]notifier.MessageRef{{"200.000000"}, nil},
	}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), snapshot("Bob"), false); err != nil {
		t.Fatal(err)
	}
	if store.state.Current != "300.000000" || store.state.Pending != nil {
		t.Fatalf("state = %#v", store.state)
	}
	wantPrefix := []string{"find:op", "update:200.000000:Bob:op", "find:op", "post:Bob", "delete:200.000000"}
	if !reflect.DeepEqual(destination.calls, wantPrefix) {
		t.Fatalf("calls = %#v", destination.calls)
	}
}

func TestZeroUsersDeletesKnownAndAttemptedPendingMatches(t *testing.T) {
	pending := &Pending{OperationID: "op", AttemptedAt: timePtr(time.Unix(100, 0))}
	store := &memoryStore{state: State{Version: stateVersion, DestinationID: "destination-1", Current: "300.000000", Stale: []notifier.MessageRef{"200.000000"}, Pending: pending}}
	destination := &fakeDestination{destinationID: "destination-1", deleteErr: map[string]error{}, findResults: [][]notifier.MessageRef{{"400.000000"}}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), presence.Snapshot{}, false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(destination.calls, []string{"delete:300.000000", "delete:200.000000", "find:op", "delete:400.000000"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
	if store.state.Current != "" || store.state.Pending != nil || store.state.DestinationID != "" {
		t.Fatalf("state = %#v", store.state)
	}
}

func TestZeroUsersClearsNeverAttemptedPendingWithoutDestinationLookup(t *testing.T) {
	store := &memoryStore{state: State{Version: stateVersion, DestinationID: "destination-1", Pending: &Pending{OperationID: "op"}}}
	destination := &fakeDestination{destinationID: "destination-1", deleteErr: map[string]error{}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), presence.Snapshot{}, false); err != nil {
		t.Fatal(err)
	}
	if len(destination.calls) != 0 || store.state.Pending != nil {
		t.Fatalf("calls = %#v, state = %#v", destination.calls, store.state)
	}
}

func TestZeroUsersHistoryFailureDeletesKnownButRetainsPending(t *testing.T) {
	pending := &Pending{OperationID: "op", AttemptedAt: timePtr(time.Unix(100, 0))}
	store := &memoryStore{state: State{Version: stateVersion, DestinationID: "destination-1", Current: "300.000000", Pending: pending}}
	destination := &fakeDestination{destinationID: "destination-1", findErr: errors.New("missing_scope"), deleteErr: map[string]error{}}
	reconciler, _ := NewReconciler(destination, store)
	if err := reconciler.Reconcile(context.Background(), presence.Snapshot{}, false); err == nil {
		t.Fatal("Reconcile() error = nil")
	}
	if !reflect.DeepEqual(destination.calls, []string{"delete:300.000000", "find:op"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
	if store.state.Current != "" || store.state.Pending == nil {
		t.Fatalf("state = %#v", store.state)
	}
}

func TestPendingSaveFailureMakesNoDestinationCall(t *testing.T) {
	store := &memoryStore{state: State{Version: stateVersion, DestinationID: "destination-1", Current: "100.000000"}, saveErr: errors.New("disk full")}
	destination := &fakeDestination{destinationID: "destination-1", postTS: "200.000000", deleteErr: map[string]error{}}
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
	pending := &Pending{OperationID: "op", AttemptedAt: timePtr(time.Unix(1000, 0))}
	store := &memoryStore{state: State{Version: stateVersion, DestinationID: "destination-1", Pending: pending}}
	destination := &fakeDestination{destinationID: "destination-1", postTS: "1100.000000", deleteErr: map[string]error{}}
	reconciler, err := NewReconciler(destination, store, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); !errors.Is(err, ErrPendingRecoveryGrace) {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if store.state.Pending.AttemptedAt == nil || !store.state.Pending.AttemptedAt.Equal(time.Unix(1000, 0)) || containsCall(destination.calls, "post:Alice") {
		t.Fatalf("calls = %#v, state = %#v", destination.calls, store.state)
	}
	now = now.Add(DefaultRecoveryGracePeriod)
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), false); err != nil {
		t.Fatal(err)
	}
	if store.state.Current != "1100.000000" || !containsCall(destination.calls, "post:Alice") {
		t.Fatalf("calls = %#v, state = %#v", destination.calls, store.state)
	}
}

func TestUnchangedReconciliationUsesOnlyInMemoryRenderedHash(t *testing.T) {
	store := &memoryStore{state: State{Version: stateVersion}}
	destination := &fakeDestination{destinationID: "destination-1", postTS: "1000.000000", deleteErr: map[string]error{}}
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

func TestForcedReconciliationRefreshesCurrent(t *testing.T) {
	store := &memoryStore{state: State{Version: stateVersion, DestinationID: "destination-1", Current: "100.000000"}}
	destination := &fakeDestination{destinationID: "destination-1", deleteErr: map[string]error{}}
	reconciler, err := NewReconciler(destination, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), true); err != nil {
		t.Fatal(err)
	}
	if len(destination.calls) != 1 || !strings.HasPrefix(destination.calls[0], "update:100.000000:Alice:") {
		t.Fatalf("calls = %#v", destination.calls)
	}
	if store.state.Current != "100.000000" {
		t.Fatalf("state = %#v", store.state)
	}
}

func TestForcedReconciliationReplacesChangedPresence(t *testing.T) {
	store := &memoryStore{state: State{Version: stateVersion, DestinationID: "destination-1", Current: "100.000000"}}
	destination := &fakeDestination{destinationID: "destination-1", postTS: "200.000000", deleteErr: map[string]error{}}
	reconciler, err := NewReconciler(destination, store)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.renderedHash = contentHash("Alice")
	reconciler.hasRenderedHash = true
	if err := reconciler.Reconcile(context.Background(), snapshot("Bob"), true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(destination.calls, []string{"post:Bob", "delete:100.000000"}) {
		t.Fatalf("calls = %#v", destination.calls)
	}
}

func TestForcedReconciliationRepostsMissingCurrent(t *testing.T) {
	store := &memoryStore{state: State{Version: stateVersion, DestinationID: "destination-1", Current: "100.000000"}}
	destination := &fakeDestination{
		destinationID: "destination-1", postTS: "200.000000", updateErr: notifier.ErrMessageNotFound, deleteErr: map[string]error{},
	}
	reconciler, err := NewReconciler(destination, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background(), snapshot("Alice"), true); err != nil {
		t.Fatal(err)
	}
	if len(destination.calls) != 2 || !strings.HasPrefix(destination.calls[0], "update:100.000000:Alice:") || destination.calls[1] != "post:Alice" {
		t.Fatalf("calls = %#v", destination.calls)
	}
	if store.state.Current != "200.000000" || store.state.Pending != nil {
		t.Fatalf("state = %#v", store.state)
	}
}

func TestNewReconcilerRejectsInvalidLoadedState(t *testing.T) {
	tests := []State{
		{Version: stateVersion, Current: "100.000000"},
		{Version: stateVersion, DestinationID: "destination-2", Current: "100.000000"},
		{Version: stateVersion, DestinationID: "destination-1", Current: " invalid"},
		{Version: stateVersion, DestinationID: "destination-1", Current: "100.000000", Stale: []notifier.MessageRef{"100.000000"}},
		{Version: 99},
	}
	for _, state := range tests {
		store := &memoryStore{state: state}
		if _, err := NewReconciler(&fakeDestination{destinationID: "destination-1"}, store); err == nil {
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

func timePtr(value time.Time) *time.Time { return &value }
