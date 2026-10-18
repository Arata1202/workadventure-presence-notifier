package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Setenv(SlackWebhookEnv, "https://hooks.slack.test/example")
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`workadventure:
  address: "workadventure-back:50051"
  room_discovery_interval: "15s"
  reconnect_interval: "3s"
  initial_sync_timeout: "1s"
  request_timeout: "4s"
notification:
  interval: "20m"
notifiers:
  slack:
    message: "Online: {{ .Users }}"
    empty_message: "Nobody is online."
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
	if cfg.Notification.Interval != 20*time.Minute {
		t.Fatalf("Interval = %v", cfg.Notification.Interval)
	}
	if cfg.Slack.WebhookURL != "https://hooks.slack.test/example" {
		t.Fatal("WebhookURL was not loaded from the environment")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	t.Setenv(SlackWebhookEnv, "https://hooks.slack.test/example")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("unknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want an unknown-field error")
	}
}

func TestLoadRequiresSlackWebhook(t *testing.T) {
	t.Setenv(SlackWebhookEnv, "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want missing webhook error")
	}
}
