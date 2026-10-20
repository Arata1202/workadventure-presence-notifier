package status

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const stateVersion = 1

type State struct {
	Version   int      `json:"version"`
	ChannelID string   `json:"channel_id,omitempty"`
	MessageTS string   `json:"message_ts,omitempty"`
	Stale     []string `json:"stale_message_timestamps,omitempty"`
	Pending   *Pending `json:"pending_post,omitempty"`
}

type Pending struct {
	OperationID string `json:"operation_id"`
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
	if err := validateState(state); err != nil {
		return State{}, fmt.Errorf("validate status state: %w", err)
	}
	return state, nil
}

func (s *FileStore) Save(state State) error {
	state.Version = stateVersion
	if err := validateState(state); err != nil {
		return fmt.Errorf("validate status state: %w", err)
	}
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

func validateState(state State) error {
	tracked := state.MessageTS != "" || len(state.Stale) > 0 || state.Pending != nil
	if tracked && state.ChannelID == "" {
		return errors.New("tracked Slack state requires a channel ID")
	}
	if state.MessageTS != "" {
		if _, err := parseSlackTimestamp(state.MessageTS); err != nil {
			return fmt.Errorf("invalid current message timestamp: %w", err)
		}
	}
	seen := make(map[string]struct{}, len(state.Stale)+1)
	if state.MessageTS != "" {
		seen[state.MessageTS] = struct{}{}
	}
	for _, timestamp := range state.Stale {
		if _, err := parseSlackTimestamp(timestamp); err != nil {
			return fmt.Errorf("invalid stale message timestamp: %w", err)
		}
		if _, exists := seen[timestamp]; exists {
			return fmt.Errorf("duplicate tracked message timestamp %q", timestamp)
		}
		seen[timestamp] = struct{}{}
	}
	if state.Pending != nil {
		if state.Pending.OperationID == "" {
			return errors.New("pending Slack post requires an operation ID")
		}
		if state.Pending.AttemptedAt != "" {
			if _, err := parseSlackTimestamp(state.Pending.AttemptedAt); err != nil {
				return fmt.Errorf("invalid pending attempt timestamp: %w", err)
			}
		}
	}
	return nil
}

func parseSlackTimestamp(value string) (time.Time, error) {
	secondsText, microsText, found := strings.Cut(value, ".")
	if !found || secondsText == "" || len(microsText) != 6 {
		return time.Time{}, errors.New("expected seconds and six fractional digits")
	}
	seconds, err := strconv.ParseInt(secondsText, 10, 64)
	if err != nil || seconds < 0 {
		return time.Time{}, errors.New("invalid seconds")
	}
	micros, err := strconv.ParseInt(microsText, 10, 64)
	if err != nil || micros < 0 {
		return time.Time{}, errors.New("invalid fractional seconds")
	}
	return time.Unix(seconds, micros*int64(time.Microsecond)), nil
}
