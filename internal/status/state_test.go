package status

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFileStoreRoundTripUsesPrivateAtomicFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "nested", "state.json")
	store := NewFileStore(path)
	want := State{
		ChannelID: "C123", MessageTS: "123.456000", Stale: []string{"122.000000"},
		Pending: &Pending{OperationID: "11111111-2222-4333-8444-555555555555", AttemptedAt: "100.000000"},
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

func TestFileStoreDoesNotPersistLegacyContentHashes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewFileStore(path)
	if err := store.Save(State{ChannelID: "C123", MessageTS: "123.456000"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "content_hash") {
		t.Fatalf("state contains legacy content hash field: %s", data)
	}
}

func TestFileStoreLoadRejectsImpossibleState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"message_ts":"123.000000"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(path).Load(); err == nil {
		t.Fatal("Load() error = nil for tracked state without a channel")
	}
}

func TestFileStoreRejectsInvalidStateInvariants(t *testing.T) {
	tests := []struct {
		name  string
		state State
	}{
		{name: "missing channel", state: State{MessageTS: "123.000000"}},
		{name: "invalid current timestamp", state: State{ChannelID: "C1", MessageTS: "invalid"}},
		{name: "invalid stale timestamp", state: State{ChannelID: "C1", Stale: []string{"invalid"}}},
		{name: "duplicate current and stale", state: State{ChannelID: "C1", MessageTS: "123.000000", Stale: []string{"123.000000"}}},
		{name: "duplicate stale", state: State{ChannelID: "C1", Stale: []string{"123.000000", "123.000000"}}},
		{name: "pending without operation", state: State{ChannelID: "C1", Pending: &Pending{}}},
		{name: "invalid attempt timestamp", state: State{ChannelID: "C1", Pending: &Pending{OperationID: "op", AttemptedAt: "invalid"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := NewFileStore(filepath.Join(t.TempDir(), "state.json")).Save(test.state); err == nil {
				t.Fatal("Save() error = nil")
			}
		})
	}
}
