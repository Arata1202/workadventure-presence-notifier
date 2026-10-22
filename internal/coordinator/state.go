package coordinator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Arata1202/workadventure-presence-notifier/internal/notifier"
)

const maxStaleReferences = 3

type persistedState struct {
	DestinationID string                `json:"destination_id,omitempty"`
	Current       notifier.MessageRef   `json:"current_message_ts,omitempty"`
	Stale         []notifier.MessageRef `json:"stale_message_ts,omitempty"`
}

func (state persistedState) empty() bool {
	return state.Current == "" && len(state.Stale) == 0
}

type stateFile struct{ path string }

func (s *stateFile) load() (persistedState, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return persistedState{}, nil
	}
	if err != nil {
		return persistedState{}, fmt.Errorf("read status state: %w", err)
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return persistedState{}, fmt.Errorf("decode status state: %w", err)
	}
	if err := validateState(state); err != nil {
		return persistedState{}, fmt.Errorf("decode status state: %w", err)
	}
	return state, nil
}

func (s *stateFile) save(state persistedState) error {
	if state.empty() {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove status state: %w", err)
		}
		return nil
	}
	if err := validateState(state); err != nil {
		return fmt.Errorf("write status state: %w", err)
	}
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create status state directory: %w", err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode status state: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".state-*")
	if err != nil {
		return fmt.Errorf("create temporary status state: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write status state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync status state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close status state: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace status state: %w", err)
	}
	removeTemporary = false
	return nil
}

func validateState(state persistedState) error {
	if !state.empty() && state.DestinationID == "" {
		return errors.New("destination ID is required when message timestamps are present")
	}
	if len(state.Stale) > maxStaleReferences {
		return fmt.Errorf("too many stale message timestamps: %d", len(state.Stale))
	}
	seen := make(map[notifier.MessageRef]struct{}, len(state.Stale)+1)
	if state.Current != "" {
		if !validMessageTS(string(state.Current)) {
			return errors.New("invalid current message timestamp")
		}
		seen[state.Current] = struct{}{}
	}
	for _, ref := range state.Stale {
		if !validMessageTS(string(ref)) {
			return errors.New("invalid stale message timestamp")
		}
		if _, exists := seen[ref]; exists {
			return errors.New("duplicate message timestamp")
		}
		seen[ref] = struct{}{}
	}
	return nil
}

func validMessageTS(value string) bool {
	if value == "" || len(value) > 64 || !strings.Contains(value, ".") {
		return false
	}
	for _, character := range value {
		if character != '.' && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}
