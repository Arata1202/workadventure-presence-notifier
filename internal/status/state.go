package status

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const stateVersion = 1

type State struct {
	Version     int      `json:"version"`
	ChannelID   string   `json:"channel_id,omitempty"`
	MessageTS   string   `json:"message_ts,omitempty"`
	ContentHash string   `json:"content_hash,omitempty"`
	Stale       []string `json:"stale_message_timestamps,omitempty"`
	Pending     *Pending `json:"pending_post,omitempty"`
}

type Pending struct {
	OperationID string `json:"operation_id"`
	ContentHash string `json:"content_hash"`
	AttemptedAt string `json:"attempted_at,omitempty"`
}

type FileStore struct{ path string }

func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

func (s *FileStore) Load() (State, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return State{Version: stateVersion}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read status state: %w", err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("decode status state: %w", err)
	}
	if state.Version != stateVersion {
		return State{}, fmt.Errorf("unsupported status state version %d", state.Version)
	}
	return state, nil
}

func (s *FileStore) Save(state State) error {
	state.Version = stateVersion
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create status state directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary status state: %w", err)
	}
	temporaryName := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("set temporary status state permissions: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(state); err != nil {
		return fmt.Errorf("encode status state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary status state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary status state: %w", err)
	}
	if err := os.Rename(temporaryName, s.path); err != nil {
		return fmt.Errorf("replace status state: %w", err)
	}
	committed = true
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open status state directory: %w", err)
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return fmt.Errorf("sync status state directory: %w", err)
	}
	return nil
}
