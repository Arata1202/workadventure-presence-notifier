package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Setenv(SlackBotTokenEnv, "xoxb-test-token")
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`workadventure:
  address: "workadventure-back:50051"
  room_discovery_interval: "15s"
status:
  debounce: "2s"
  reconciliation_interval: "20m"
  shutdown_timeout: "4s"
  state_file: "/data/status.json"
notifiers:
  slack:
    request_timeout: "7s"
    channel_id: "C012345"
    message: "Online: {{ .Users }}"
    user_separator: " | "
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.WorkAdventure.Address != "workadventure-back:50051" {
		t.Fatalf("Address = %q", cfg.WorkAdventure.Address)
	}
	if cfg.WorkAdventure.RoomDiscoveryInterval != 15*time.Second {
		t.Fatalf("RoomDiscoveryInterval = %v", cfg.WorkAdventure.RoomDiscoveryInterval)
	}
	if cfg.Status.Debounce != 2*time.Second || cfg.Status.ReconciliationInterval != 20*time.Minute {
		t.Fatalf("Status = %#v", cfg.Status)
	}
	if cfg.Destination.Type != "slack" || cfg.Destination.Slack.BotToken != "xoxb-test-token" || cfg.Destination.Slack.ChannelID != "C012345" || cfg.Destination.Slack.RequestTimeout != 7*time.Second {
		t.Fatalf("Slack = %#v", cfg.Destination.Slack)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	t.Setenv(SlackBotTokenEnv, "token")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("unknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want an unknown-field error")
	}
}

func TestLoadRequiresSlackConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("notifiers:\n  slack:\n    channel_id: C123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want missing token error")
	}
	t.Setenv(SlackBotTokenEnv, "token")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want missing channel error")
	}
}
