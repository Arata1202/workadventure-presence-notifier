package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Setenv("WORKADVENTURE_ENTRANCE_URL", "")
	t.Setenv("SLACK_CHANNEL_ID", "")
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
	t.Setenv("WORKADVENTURE_ENTRANCE_URL", "")
	t.Setenv("SLACK_CHANNEL_ID", "")
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
	t.Setenv(SlackBotTokenEnv, "")
	t.Setenv("WORKADVENTURE_ENTRANCE_URL", "")
	t.Setenv("SLACK_CHANNEL_ID", "")
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

func TestLoadEntrance(t *testing.T) {
	t.Setenv("WORKADVENTURE_ENTRANCE_URL", "")
	t.Setenv("SLACK_CHANNEL_ID", "")
	t.Setenv(SlackSigningSecretEnv, "test-signing-secret")
	t.Setenv(SlackBotTokenEnv, "xoxb-test")
	for _, test := range []struct {
		name, url, label, wantLabel string
		wantError                   bool
	}{
		{name: "absent", wantLabel: "Open WorkAdventure"},
		{name: "https", url: "https://office.example.invalid/entrance?source=status#start", wantLabel: "Open WorkAdventure"},
		{name: "http", url: "http://office.example.invalid/", label: "Enter office", wantLabel: "Enter office"},
		{name: "relative", url: "/office", wantError: true},
		{name: "scheme relative", url: "//office.example.invalid/", wantError: true},
		{name: "javascript", url: "javascript:alert(1)", wantError: true},
		{name: "data", url: "data:text/html,hello", wantError: true},
		{name: "file", url: "file:///office", wantError: true},
		{name: "missing host", url: "https:///office", wantError: true},
		{name: "credentials", url: "https://user:private@office.example.invalid/", wantError: true},
		{name: "username", url: "https://user@office.example.invalid/", wantError: true},
		{name: "space", url: "https://office.example.invalid/a b", wantError: true},
		{name: "backslash", url: `https://office.example.invalid/\office`, wantError: true},
		{name: "bad escape", url: "https://office.example.invalid/%zz", wantError: true},
		{name: "bad port", url: "https://office.example.invalid:bad/", wantError: true},
		{name: "zero port", url: "https://office.example.invalid:0/", wantError: true},
		{name: "port above maximum", url: "https://office.example.invalid:65536/", wantError: true},
		{name: "large port", url: "https://office.example.invalid:99999/", wantError: true},
		{name: "overflowing port", url: "https://office.example.invalid:999999999999999999999999999999/", wantError: true},
		{name: "minimum port", url: "https://office.example.invalid:1/", wantLabel: "Open WorkAdventure"},
		{name: "maximum port", url: "https://office.example.invalid:65535/", wantLabel: "Open WorkAdventure"},
		{name: "https port", url: "https://office.example.invalid:443/", wantLabel: "Open WorkAdventure"},
		{name: "empty port", url: "https://office.example.invalid:/", wantLabel: "Open WorkAdventure"},
		{name: "IPv6 port", url: "https://[::1]:8443/", wantLabel: "Open WorkAdventure"},
		{name: "IPv6 port above maximum", url: "https://[::1]:65536/", wantError: true},
		{name: "IPv6 empty port", url: "https://[::1]:/", wantLabel: "Open WorkAdventure"},
		{name: "long URL", url: "https://office.example.invalid/" + strings.Repeat("a", 3000), wantError: true},
		{name: "label boundary", url: "https://office.example.invalid/", label: strings.Repeat("入", 75), wantLabel: strings.Repeat("入", 75)},
		{name: "long label", url: "https://office.example.invalid/", label: strings.Repeat("入", 76), wantError: true},
		{name: "blank label", url: "https://office.example.invalid/", label: "   ", wantError: true},
		{name: "label only", label: "Enter office", wantLabel: "Enter office"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			content := "workadventure:\n  entrance_url: '" + test.url + "'\n  entrance_label: '" + test.label + "'\ndestination:\n  slack:\n    channel_id: C123\n"
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if test.wantError {
				if err == nil {
					t.Fatal("Load() error = nil, want invalid entrance error")
				}
				if strings.Contains(err.Error(), test.url) {
					t.Fatal("configuration error includes entrance URL")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.WorkAdventure.EntranceURL != test.url || cfg.WorkAdventure.EntranceLabel != test.wantLabel {
				t.Fatalf("entrance = %q, %q", cfg.WorkAdventure.EntranceURL, cfg.WorkAdventure.EntranceLabel)
			}
		})
	}
}

func TestExampleConfiguration(t *testing.T) {
	t.Setenv("WORKADVENTURE_ENTRANCE_URL", "")
	t.Setenv("SLACK_CHANNEL_ID", "")
	t.Setenv(SlackBotTokenEnv, "xoxb-test")
	cfg, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkAdventure.EntranceURL != "" {
		t.Fatal("example must keep the optional button disabled")
	}
}

func TestEntranceSigningSecret(t *testing.T) {
	t.Setenv("WORKADVENTURE_ENTRANCE_URL", "")
	t.Setenv("SLACK_CHANNEL_ID", "")
	t.Setenv(SlackBotTokenEnv, "xoxb-test")
	t.Setenv(SlackSigningSecretEnv, "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	write := func(entrance string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("workadventure:\n  entrance_url: '"+entrance+"'\ndestination:\n  slack:\n    channel_id: C123\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Slack.SigningSecret != "" || cfg.Slack.InteractionsListen != "127.0.0.1:8080" {
		t.Fatal("unexpected text-only receiver config")
	}
	write("https://office.example.invalid/")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), SlackSigningSecretEnv) {
		t.Fatal("missing signing secret accepted")
	}
	t.Setenv(SlackSigningSecretEnv, "test-only-secret")
	cfg, err = Load(path)
	if err != nil || cfg.Slack.SigningSecret != "test-only-secret" {
		t.Fatal("signing secret not loaded")
	}
}

func TestLoadEnvironmentOverrides(t *testing.T) {
	for _, test := range []struct {
		name, yaml, entrance, channel        string
		unset                                bool
		wantEntrance, wantChannel, wantError string
	}{
		{name: "precedence", yaml: "workadventure:\n  entrance_url: /invalid-yaml\ndestination:\n  slack:\n    channel_id: CYAML\n", entrance: "https://office.example.invalid/env", channel: "CENV", wantEntrance: "https://office.example.invalid/env", wantChannel: "CENV"},
		{name: "env only", yaml: "{}\n", entrance: "https://office.example.invalid/env", channel: "CENV", wantEntrance: "https://office.example.invalid/env", wantChannel: "CENV"},
		{name: "empty fallback", yaml: "workadventure:\n  entrance_url: https://office.example.invalid/yaml\ndestination:\n  slack:\n    channel_id: CYAML\n", wantEntrance: "https://office.example.invalid/yaml", wantChannel: "CYAML"},
		{name: "unset fallback", yaml: "workadventure:\n  entrance_url: https://office.example.invalid/yaml\ndestination:\n  slack:\n    channel_id: CYAML\n", unset: true, wantEntrance: "https://office.example.invalid/yaml", wantChannel: "CYAML"},
		{name: "invalid env", yaml: "workadventure:\n  entrance_url: https://office.example.invalid/yaml\n", entrance: "https://private.example.invalid:65536/", channel: "CENV", wantError: "workadventure.entrance_url"},
		{name: "missing channel", yaml: "{}\n", wantError: "destination.slack.channel_id is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(SlackBotTokenEnv, "xoxb-test")
			t.Setenv(SlackSigningSecretEnv, "test-signing-secret")
			t.Setenv("WORKADVENTURE_ENTRANCE_URL", test.entrance)
			t.Setenv("SLACK_CHANNEL_ID", test.channel)
			if test.unset {
				if err := os.Unsetenv("WORKADVENTURE_ENTRANCE_URL"); err != nil {
					t.Fatal(err)
				}
				if err := os.Unsetenv("SLACK_CHANNEL_ID"); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(test.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("Load() error = %v", err)
				}
				for _, value := range []string{test.entrance, test.channel} {
					if value != "" && strings.Contains(err.Error(), value) {
						t.Fatal("configuration error includes environment value")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.WorkAdventure.EntranceURL != test.wantEntrance || cfg.Slack.ChannelID != test.wantChannel {
				t.Fatal("environment precedence mismatch")
			}
		})
	}
}

func TestEnvironmentEntranceRequiresSigningSecret(t *testing.T) {
	t.Setenv(SlackBotTokenEnv, "xoxb-test")
	t.Setenv(SlackSigningSecretEnv, "")
	t.Setenv("WORKADVENTURE_ENTRANCE_URL", "https://office.example.invalid/env")
	t.Setenv("SLACK_CHANNEL_ID", "CENV")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), SlackSigningSecretEnv) {
		t.Fatal("environment entrance accepted without signing secret")
	}
}
