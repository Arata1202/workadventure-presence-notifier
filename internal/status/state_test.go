package status

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/notifier"
)

func TestFileStoreRoundTripUsesPrivateAtomicFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "nested", "state.json")
	store := NewFileStore(path)
	attemptedAt := time.Unix(100, 123).UTC()
	want := State{
		DestinationID: "slack:C123", Current: "123.456000", Stale: []notifier.MessageRef{"122.000000"},
		Pending: &Pending{OperationID: "11111111-2222-4333-8444-555555555555", AttemptedAt: &attemptedAt},
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want.Version = stateVersion
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("directory entries = %#v", entries)
	}
}

func TestFileStoreMissingAndInvalidState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewFileStore(path)
	state, err := store.Load()
	if err != nil || state.Version != stateVersion {
		t.Fatalf("Load() = %#v, %v", state, err)
	}
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load() error = nil for invalid state")
	}
}

func TestFileStoreDoesNotPersistRenderedContentHashes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewFileStore(path)
	if err := store.Save(State{DestinationID: "slack:C123", Current: "123.456000"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "content_hash") {
		t.Fatalf("state contains rendered content hash field: %s", data)
	}
}

func TestFileStoreLoadRejectsUnknownFieldsIncompatibleVersionsAndInvalidState(t *testing.T) {
	for _, content := range []string{
		`{"version":1,"unexpected":true}`,
		`{"version":99,"destination_id":"slack:C123","current_message_ref":"123.000000"}`,
		`{"version":1,"current_message_ref":"123.000000"}`,
	} {
		path := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileStore(path).Load(); err == nil {
			t.Fatalf("Load() error = nil for %s", content)
		}
	}
}

func TestFileStoreRejectsInvalidStateInvariants(t *testing.T) {
	tests := []struct {
		name  string
		state State
	}{
		{name: "missing destination", state: State{Current: "123.000000"}},
		{name: "invalid destination", state: State{DestinationID: " slack:C1", Current: "123.000000"}},
		{name: "invalid current reference", state: State{DestinationID: "slack:C1", Current: " invalid"}},
		{name: "empty stale reference", state: State{DestinationID: "slack:C1", Stale: []notifier.MessageRef{""}}},
		{name: "duplicate current and stale", state: State{DestinationID: "slack:C1", Current: "123.000000", Stale: []notifier.MessageRef{"123.000000"}}},
		{name: "duplicate stale", state: State{DestinationID: "slack:C1", Stale: []notifier.MessageRef{"123.000000", "123.000000"}}},
		{name: "pending without operation", state: State{DestinationID: "slack:C1", Pending: &Pending{}}},
		{name: "zero attempt time", state: State{DestinationID: "slack:C1", Pending: &Pending{OperationID: "op", AttemptedAt: &time.Time{}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := NewFileStore(filepath.Join(t.TempDir(), "state.json")).Save(test.state); err == nil {
				t.Fatal("Save() error = nil")
			}
		})
	}
}
