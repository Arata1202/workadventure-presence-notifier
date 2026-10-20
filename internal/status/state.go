package status

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Arata1202/workadventure-presence-notifier/internal/notifier"
)

const (
	stateVersion            = 1
	maximumIdentifierLength = 1024
)

type State struct {
	Version       int                   `json:"version"`
	DestinationID string                `json:"destination_id,omitempty"`
	Current       notifier.MessageRef   `json:"current_message_ref,omitempty"`
	Stale         []notifier.MessageRef `json:"stale_message_refs,omitempty"`
	Pending       *Pending              `json:"pending_post,omitempty"`
}

type Pending struct {
	OperationID string     `json:"operation_id"`
	AttemptedAt *time.Time `json:"attempted_at,omitempty"`
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
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return State{}, fmt.Errorf("decode status state: %w", err)
	}
	if header.Version != stateVersion {
		return State{}, fmt.Errorf("unsupported status state version %d", header.Version)
	}
	var state State
	if err := decodeStrictJSON(data, &state); err != nil {
		return State{}, fmt.Errorf("decode status state: %w", err)
	}
	if err := validateState(state); err != nil {
		return State{}, fmt.Errorf("validate status state: %w", err)
	}
	return state, nil
}

func (s *FileStore) Save(state State) error {
	if state.Version != 0 && state.Version != stateVersion {
		return fmt.Errorf("unsupported status state version %d", state.Version)
	}
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
	if err := json.NewEncoder(temporary).Encode(state); err != nil {
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
	if state.Version != stateVersion {
		return fmt.Errorf("unsupported status state version %d", state.Version)
	}
	tracked := state.Current != "" || len(state.Stale) > 0 || state.Pending != nil
	if tracked && state.DestinationID == "" {
		return errors.New("tracked state requires a destination ID")
	}
	if state.DestinationID != "" {
		if err := validateIdentifier("destination ID", state.DestinationID); err != nil {
			return err
		}
	}
	seen := make(map[notifier.MessageRef]struct{}, len(state.Stale)+1)
	if state.Current != "" {
		if err := validateIdentifier("current message reference", string(state.Current)); err != nil {
			return err
		}
		seen[state.Current] = struct{}{}
	}
	for _, ref := range state.Stale {
		if err := validateIdentifier("stale message reference", string(ref)); err != nil {
			return err
		}
		if _, exists := seen[ref]; exists {
			return fmt.Errorf("duplicate tracked message reference %q", ref)
		}
		seen[ref] = struct{}{}
	}
	if state.Pending != nil {
		if err := validateIdentifier("pending operation ID", state.Pending.OperationID); err != nil {
			return err
		}
		if state.Pending.AttemptedAt != nil && state.Pending.AttemptedAt.IsZero() {
			return errors.New("pending attempt time must not be zero")
		}
	}
	return nil
}

func validateIdentifier(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > maximumIdentifierLength {
		return fmt.Errorf("%s exceeds %d bytes", name, maximumIdentifierLength)
	}
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is invalid", name)
	}
	return nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
