package presence

import "time"

// User is a WorkAdventure user included in a presence snapshot.
type User struct {
	ID   string
	Name string
}

// Snapshot is the current set of users known to be connected.
type Snapshot struct {
	Users     []User
	CreatedAt time.Time
}

// Count returns the number of unique users in the snapshot.
func (s Snapshot) Count() int {
	return len(s.Users)
}
