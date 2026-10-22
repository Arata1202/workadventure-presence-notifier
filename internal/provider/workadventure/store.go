package workadventure

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

var ErrNotReady = errors.New("presence data is not ready")

type userEntry struct {
	name  string
	count int
}

type roomState struct {
	users map[string]userEntry
	ready bool
}

type store struct {
	mu           sync.RWMutex
	rooms        map[string]*roomState
	discovered   bool
	readyCh      chan struct{}
	changeCh     chan struct{}
	lastState    string
	lastSnapshot presence.Snapshot
	initialized  bool
}

func newStore() *store {
	return &store{
		rooms:    make(map[string]*roomState),
		readyCh:  make(chan struct{}),
		changeCh: make(chan struct{}, 1),
	}
}

func (s *store) setRooms(roomIDs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing := make(map[string]struct{}, len(roomIDs))
	for _, roomID := range roomIDs {
		existing[roomID] = struct{}{}
		if _, ok := s.rooms[roomID]; !ok {
			s.rooms[roomID] = &roomState{users: make(map[string]userEntry)}
		}
	}
	for roomID := range s.rooms {
		if _, ok := existing[roomID]; !ok {
			delete(s.rooms, roomID)
		}
	}
	s.discovered = true
	s.publishChangeLocked()
}

func (s *store) beginRoomSync(roomID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if room, ok := s.rooms[roomID]; ok {
		room.ready = false
	}
}

func (s *store) replaceRoom(roomID string, users map[string]userEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	room, ok := s.rooms[roomID]
	if !ok {
		return
	}
	room.users = users
	room.ready = true
	s.publishChangeLocked()
}

func (s *store) addUser(roomID, uuid, name string) {
	if uuid == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	room, ok := s.rooms[roomID]
	if !ok {
		return
	}
	entry := room.users[uuid]
	entry.count++
	if name != "" {
		entry.name = name
	}
	room.users[uuid] = entry
	s.publishChangeLocked()
}

func (s *store) removeUser(roomID, uuid string) {
	if uuid == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	room, ok := s.rooms[roomID]
	if !ok {
		return
	}
	entry, ok := room.users[uuid]
	if !ok {
		return
	}
	entry.count--
	if entry.count <= 0 {
		delete(room.users, uuid)
	} else {
		room.users[uuid] = entry
	}
	s.publishChangeLocked()
}

func (s *store) snapshot() (presence.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.initialized {
		return presence.Snapshot{}, ErrNotReady
	}
	return cloneSnapshot(s.lastSnapshot), nil
}

func (s *store) snapshotLocked() presence.Snapshot {
	usersByID := make(map[string]presence.User)
	for _, room := range s.rooms {
		for id, entry := range room.users {
			existing, exists := usersByID[id]
			if !exists || preferredName(existing.Name, entry.name) == entry.name {
				usersByID[id] = presence.User{ID: id, Name: entry.name}
			}
		}
	}

	users := make([]presence.User, 0, len(usersByID))
	for _, user := range usersByID {
		users = append(users, user)
	}
	sort.Slice(users, func(i, j int) bool {
		if users[i].Name == users[j].Name {
			return users[i].ID < users[j].ID
		}
		return users[i].Name < users[j].Name
	})
	return presence.Snapshot{Users: users, CreatedAt: time.Now().UTC()}
}

func (s *store) waitReady() <-chan struct{} { return s.readyCh }
func (s *store) changes() <-chan struct{}   { return s.changeCh }

func (s *store) isReadyLocked() bool {
	if !s.discovered {
		return false
	}
	for _, room := range s.rooms {
		if !room.ready {
			return false
		}
	}
	return true
}

func (s *store) publishChangeLocked() {
	if !s.isReadyLocked() {
		return
	}
	snapshot := s.snapshotLocked()
	state := fingerprint(snapshot)
	if !s.initialized {
		s.initialized = true
		s.lastState = state
		s.lastSnapshot = snapshot
		close(s.readyCh)
		return
	}
	if state == s.lastState {
		return
	}
	s.lastState = state
	s.lastSnapshot = snapshot
	select {
	case s.changeCh <- struct{}{}:
	default:
	}
}

func fingerprint(snapshot presence.Snapshot) string {
	users := append([]presence.User(nil), snapshot.Users...)
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
	var state strings.Builder
	for _, user := range users {
		state.WriteString(user.ID)
		state.WriteByte(0)
		state.WriteString(user.Name)
		state.WriteByte(0)
	}
	return state.String()
}

func preferredName(existing, candidate string) string {
	if existing == "" {
		return candidate
	}
	if candidate == "" || existing < candidate {
		return existing
	}
	return candidate
}

func cloneSnapshot(snapshot presence.Snapshot) presence.Snapshot {
	return presence.Snapshot{Users: append([]presence.User(nil), snapshot.Users...), CreatedAt: snapshot.CreatedAt}
}
