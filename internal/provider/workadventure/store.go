package workadventure

import (
	"errors"
	"sort"
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
	mu         sync.RWMutex
	rooms      map[string]*roomState
	discovered bool
	readyOnce  sync.Once
	readyCh    chan struct{}
}

func newStore() *store {
	return &store{
		rooms:   make(map[string]*roomState),
		readyCh: make(chan struct{}),
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
	s.updateReadyLocked()
}

func (s *store) resetRoom(roomID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	room, ok := s.rooms[roomID]
	if !ok {
		return
	}
	room.users = make(map[string]userEntry)
	room.ready = false
}

func (s *store) setRoomReady(roomID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	room, ok := s.rooms[roomID]
	if !ok {
		return
	}
	room.ready = true
	s.updateReadyLocked()
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
		return
	}
	room.users[uuid] = entry
}

func (s *store) snapshot() (presence.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.isReadyLocked() {
		return presence.Snapshot{}, ErrNotReady
	}

	usersByID := make(map[string]presence.User)
	for _, room := range s.rooms {
		for id, entry := range room.users {
			if _, exists := usersByID[id]; exists {
				continue
			}
			usersByID[id] = presence.User{ID: id, Name: entry.name}
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

	return presence.Snapshot{Users: users, CreatedAt: time.Now().UTC()}, nil
}

func (s *store) waitReady() <-chan struct{} {
	return s.readyCh
}

func (s *store) updateReadyLocked() {
	if !s.isReadyLocked() {
		return
	}
	s.readyOnce.Do(func() { close(s.readyCh) })
}

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
