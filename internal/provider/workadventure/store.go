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
	mu          sync.RWMutex
	rooms       map[string]*roomState
	discovered  bool
	readyOnce   sync.Once
	readyCh     chan struct{}
	changeCh    chan struct{}
	lastState   string
	initialized bool
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
	s.updateReadyLocked()
	s.publishChangeLocked()
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
	if !s.isReadyLocked() {
		return presence.Snapshot{}, ErrNotReady
	}
	usersByID := make(map[string]presence.User)
	for _, room := range s.rooms {
		for id, entry := range room.users {
			existing, exists := usersByID[id]
			if !exists || entry.name < existing.Name {
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
	return presence.Snapshot{Users: users, CreatedAt: time.Now().UTC()}, nil
}

func (s *store) waitReady() <-chan struct{} { return s.readyCh }
func (s *store) changes() <-chan struct{}   { return s.changeCh }

func (s *store) updateReadyLocked() {
	if s.isReadyLocked() {
		s.readyOnce.Do(func() { close(s.readyCh) })
	}
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

func (s *store) publishChangeLocked() {
	if !s.isReadyLocked() {
		return
	}
	state := s.fingerprintLocked()
	if !s.initialized {
		s.lastState = state
		s.initialized = true
		return
	}
	if state == s.lastState {
		return
	}
	s.lastState = state
	select {
	case s.changeCh <- struct{}{}:
	default:
	}
}

func (s *store) fingerprintLocked() string {
	users := make(map[string]string)
	for _, room := range s.rooms {
		for id, entry := range room.users {
			existing, exists := users[id]
			if !exists || entry.name < existing {
				users[id] = entry.name
			}
		}
	}
	ids := make([]string, 0, len(users))
	for id := range users {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var result strings.Builder
	for _, id := range ids {
		result.WriteString(id)
		result.WriteByte(0)
		result.WriteString(users[id])
		result.WriteByte(0)
	}
	return result.String()
}
