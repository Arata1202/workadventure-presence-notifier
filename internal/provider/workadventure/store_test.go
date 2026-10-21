package workadventure

import "testing"

func TestStoreDeduplicatesUsersAcrossConnectionsAndRooms(t *testing.T) {
	store := newStore()
	store.setRooms([]string{"room-a", "room-b"})
	store.replaceRoom("room-a", map[string]userEntry{"user-1": {name: "Alice", count: 2}})
	store.replaceRoom("room-b", map[string]userEntry{
		"user-1": {name: "Alice", count: 1},
		"user-2": {name: "Bob", count: 1},
	})
	store.addUser("room-a", "user-1", "Alice")
	store.addUser("room-a", "user-1", "Alice")

	snapshot, err := store.snapshot()
	if err != nil {
		t.Fatalf("snapshot() error = %v", err)
	}
	if snapshot.Count() != 2 {
		t.Fatalf("snapshot.Count() = %d, want 2", snapshot.Count())
	}
	if snapshot.Users[0].Name != "Alice" || snapshot.Users[1].Name != "Bob" {
		t.Fatalf("snapshot.Users = %#v", snapshot.Users)
	}

	store.removeUser("room-a", "user-1")
	snapshot, err = store.snapshot()
	if err != nil {
		t.Fatalf("snapshot() error = %v", err)
	}
	if snapshot.Count() != 2 {
		t.Fatalf("snapshot.Count() after one connection left = %d, want 2", snapshot.Count())
	}
}

func TestStoreIsNotReadyUntilDiscoveryAndRoomSync(t *testing.T) {
	store := newStore()
	if _, err := store.snapshot(); err == nil {
		t.Fatal("snapshot() error = nil before discovery")
	}
	store.setRooms([]string{"room-a"})
	if _, err := store.snapshot(); err == nil {
		t.Fatal("snapshot() error = nil before room sync")
	}
	store.replaceRoom("room-a", map[string]userEntry{})
	if _, err := store.snapshot(); err != nil {
		t.Fatalf("snapshot() error = %v after room sync", err)
	}
}

func TestStoreEmitsOnlyForReadyGlobalChanges(t *testing.T) {
	store := newStore()
	store.setRooms([]string{"room-a"})
	store.replaceRoom("room-a", map[string]userEntry{"user-1": {name: "Alice", count: 1}})
	assertNoChange(t, store.changes())
	store.addUser("room-a", "user-1", "Alice")
	assertNoChange(t, store.changes())
	store.addUser("room-a", "user-2", "Bob")
	assertChange(t, store.changes())
	store.removeUser("room-a", "user-2")
	assertChange(t, store.changes())
}

func TestStoreReplacementRetainsSnapshotUntilCommitted(t *testing.T) {
	store := newStore()
	store.setRooms([]string{"room-a"})
	store.replaceRoom("room-a", map[string]userEntry{"user-1": {name: "Alice", count: 1}})

	snapshot, err := store.snapshot()
	if err != nil || snapshot.Count() != 1 || snapshot.Users[0].Name != "Alice" {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	store.replaceRoom("room-a", map[string]userEntry{"user-2": {name: "Bob", count: 1}})
	assertChange(t, store.changes())
	snapshot, err = store.snapshot()
	if err != nil || snapshot.Count() != 1 || snapshot.Users[0].Name != "Bob" {
		t.Fatalf("replacement snapshot = %#v, %v", snapshot, err)
	}
}

func assertChange(t *testing.T, changes <-chan struct{}) {
	t.Helper()
	select {
	case <-changes:
	default:
		t.Fatal("change was not emitted")
	}
}

func assertNoChange(t *testing.T, changes <-chan struct{}) {
	t.Helper()
	select {
	case <-changes:
		t.Fatal("unexpected change")
	default:
	}
}

func TestStoreUsesDeterministicNameForDuplicateUUID(t *testing.T) {
	store := newStore()
	store.setRooms([]string{"room-a", "room-b"})
	store.replaceRoom("room-a", map[string]userEntry{"user-1": {name: "Alice", count: 1}})
	store.replaceRoom("room-b", map[string]userEntry{"user-1": {name: "Zoe", count: 1}})

	snapshot, err := store.snapshot()
	if err != nil || snapshot.Count() != 1 || snapshot.Users[0].Name != "Alice" {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	store.setRooms([]string{"room-b"})
	assertChange(t, store.changes())
	snapshot, err = store.snapshot()
	if err != nil || snapshot.Count() != 1 || snapshot.Users[0].Name != "Zoe" {
		t.Fatalf("snapshot after room removal = %#v, %v", snapshot, err)
	}
}

func TestStoreRetainsLastSnapshotWhileNewRoomSynchronizes(t *testing.T) {
	store := newStore()
	store.setRooms([]string{"room-a"})
	store.replaceRoom("room-a", map[string]userEntry{"user-1": {name: "Alice", count: 1}})

	store.setRooms([]string{"room-a", "room-b"})
	snapshot, err := store.snapshot()
	if err != nil || snapshot.Count() != 1 || snapshot.Users[0].Name != "Alice" {
		t.Fatalf("snapshot during room sync = %#v, %v", snapshot, err)
	}
	assertNoChange(t, store.changes())

	store.replaceRoom("room-b", map[string]userEntry{"user-2": {name: "Bob", count: 1}})
	assertChange(t, store.changes())
	snapshot, err = store.snapshot()
	if err != nil || snapshot.Count() != 2 {
		t.Fatalf("snapshot after room sync = %#v, %v", snapshot, err)
	}
}

func TestStoreRetainsGlobalSnapshotWhileExistingRoomReconnects(t *testing.T) {
	store := newStore()
	store.setRooms([]string{"room-a", "room-b"})
	store.replaceRoom("room-a", map[string]userEntry{"user-1": {name: "Alice", count: 1}})
	store.replaceRoom("room-b", map[string]userEntry{"user-2": {name: "Bob", count: 1}})

	store.beginRoomSync("room-a")
	store.addUser("room-b", "user-3", "Cara")
	assertNoChange(t, store.changes())
	snapshot, err := store.snapshot()
	if err != nil || snapshot.Count() != 2 {
		t.Fatalf("snapshot during reconnect = %#v, %v", snapshot, err)
	}

	store.replaceRoom("room-a", map[string]userEntry{})
	assertChange(t, store.changes())
	snapshot, err = store.snapshot()
	if err != nil || snapshot.Count() != 2 || snapshot.Users[0].Name != "Bob" || snapshot.Users[1].Name != "Cara" {
		t.Fatalf("snapshot after reconnect = %#v, %v", snapshot, err)
	}
}
