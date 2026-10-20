package status

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFileStoreRoundTripUsesPrivateAtomicFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "nested", "state.json")
	store := NewFileStore(path)
	want := State{
		ChannelID: "C123", MessageTS: "123.456", ContentHash: "hash", Stale: []string{"old"},
		Pending: &Pending{OperationID: "11111111-2222-4333-8444-555555555555", ContentHash: "pending-hash", AttemptedAt: "100.000000"},
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
