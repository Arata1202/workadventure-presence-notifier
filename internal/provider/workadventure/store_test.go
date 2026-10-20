package workadventure

import "testing"

func TestStoreDeduplicatesUsersAcrossConnectionsAndRooms(t *testing.T) {
	store := newStore()
	store.setRooms([]string{"room-a", "room-b"})
	store.addUser("room-a", "user-1", "Alice")
	store.addUser("room-a", "user-1", "Alice")
	store.addUser("room-b", "user-1", "Alice")
	store.addUser("room-b", "user-2", "Bob")
	store.setRoomReady("room-a")
	store.setRoomReady("room-b")

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
	store.setRoomReady("room-a")
	if _, err := store.snapshot(); err != nil {
		t.Fatalf("snapshot() error = %v after room sync", err)
	}
}

func TestStoreEmitsOnlyForReadyUniqueUserChanges(t *testing.T) {
	store := newStore()
	store.setRooms([]string{"room-a"})
	store.addUser("room-a", "user-1", "Alice")
	store.setRoomReady("room-a")
	assertNoChange(t, store.changes())
	store.addUser("room-a", "user-1", "Alice")
	assertNoChange(t, store.changes())
	store.removeUser("room-a", "user-1")
	assertNoChange(t, store.changes())
	store.addUser("room-a", "user-2", "Bob")
	assertChange(t, store.changes())
	store.removeUser("room-a", "user-2")
	assertChange(t, store.changes())
}

func TestStoreSuppressesReconnectReplay(t *testing.T) {
	store := newStore()
	store.setRooms([]string{"room-a"})
	store.addUser("room-a", "user-1", "Alice")
	store.setRoomReady("room-a")
	store.resetRoom("room-a")
	store.addUser("room-a", "user-1", "Alice")
	store.setRoomReady("room-a")
	assertNoChange(t, store.changes())
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
