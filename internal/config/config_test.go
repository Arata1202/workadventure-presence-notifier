package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Setenv(SlackBotTokenEnv, "xoxb-test")
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`workadventure:
  address: "workadventure-back:50051"
  room_discovery_interval: "15s"
  reconnect_interval: "3s"
  initial_sync_timeout: "1s"
  request_timeout: "4s"
status:
  debounce: "6s"
  shutdown_timeout: "8s"
  state_file: "/data/status.json"
destination:
  slack:
    channel_id: "C123"
    message: "Online: {{ .Users }}"
    user_separator: " | "
    request_timeout: "7s"
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.WorkAdventure.Address != "workadventure-back:50051" || cfg.WorkAdventure.RoomDiscoveryInterval != 15*time.Second {
		t.Fatalf("WorkAdventure config = %#v", cfg.WorkAdventure)
	}
	if cfg.Status.Debounce != 6*time.Second || cfg.Status.StateFile != "/data/status.json" {
		t.Fatalf("Status config = %#v", cfg.Status)
	}
	if cfg.Slack.BotToken != "xoxb-test" || cfg.Slack.ChannelID != "C123" || cfg.Slack.RequestTimeout != 7*time.Second {
		t.Fatalf("Slack config = %#v", cfg.Slack)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	t.Setenv(SlackBotTokenEnv, "xoxb-test")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("unknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want an unknown-field error")
	}
}

func TestLoadRequiresSlackBotTokenAndChannel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("destination:\n  slack:\n    channel_id: C123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want missing token error")
	}
	t.Setenv(SlackBotTokenEnv, "xoxb-test")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want missing channel error")
	}
}
