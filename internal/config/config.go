package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

const SlackBotTokenEnv = "SLACK_BOT_TOKEN"

type Config struct {
	WorkAdventure WorkAdventureConfig
	Status        StatusConfig
	Slack         SlackConfig
}

type WorkAdventureConfig struct {
	Address               string
	RoomDiscoveryInterval time.Duration
	ReconnectInterval     time.Duration
	InitialSyncTimeout    time.Duration
	RequestTimeout        time.Duration
}

type StatusConfig struct {
	Debounce        time.Duration
	ShutdownTimeout time.Duration
	StateFile       string
}

type SlackConfig struct {
	BotToken       string
	ChannelID      string
	Message        string
	UserSeparator  string
	RequestTimeout time.Duration
}

type fileConfig struct {
	WorkAdventure fileWorkAdventure `yaml:"workadventure"`
	Status        fileStatus        `yaml:"status"`
	Destination   fileDestination   `yaml:"destination"`
}

type fileWorkAdventure struct {
	Address               string `yaml:"address"`
	RoomDiscoveryInterval string `yaml:"room_discovery_interval"`
	ReconnectInterval     string `yaml:"reconnect_interval"`
	InitialSyncTimeout    string `yaml:"initial_sync_timeout"`
	RequestTimeout        string `yaml:"request_timeout"`
}

type fileStatus struct {
	Debounce        string `yaml:"debounce"`
	ShutdownTimeout string `yaml:"shutdown_timeout"`
	StateFile       string `yaml:"state_file"`
}

type fileDestination struct {
	Slack fileSlack `yaml:"slack"`
}

type fileSlack struct {
	ChannelID      string `yaml:"channel_id"`
	Message        string `yaml:"message"`
	UserSeparator  string `yaml:"user_separator"`
	RequestTimeout string `yaml:"request_timeout"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}
	var file fileConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("decode configuration: multiple YAML documents are not supported")
		}
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	cfg, err := file.toConfig()
	if err != nil {
		return Config{}, err
	}
	cfg.Slack.BotToken = os.Getenv(SlackBotTokenEnv)
	if cfg.Slack.BotToken == "" {
		return Config{}, fmt.Errorf("%s is required", SlackBotTokenEnv)
	}
	return cfg, nil
}

func (f fileConfig) toConfig() (Config, error) {
	cfg := Config{
		WorkAdventure: WorkAdventureConfig{
			Address: defaultString(f.WorkAdventure.Address, "back:50051"), RoomDiscoveryInterval: 30 * time.Second,
			ReconnectInterval: 5 * time.Second, InitialSyncTimeout: 2 * time.Second, RequestTimeout: 10 * time.Second,
		},
		Status: StatusConfig{
			Debounce: 5 * time.Second, ShutdownTimeout: 10 * time.Second,
			StateFile: defaultString(f.Status.StateFile, "/var/lib/workadventure-presence-notifier/state.json"),
		},
		Slack: SlackConfig{
			ChannelID:     f.Destination.Slack.ChannelID,
			Message:       defaultString(f.Destination.Slack.Message, "Currently online ({{ .Count }}): {{ .Users }}"),
			UserSeparator: defaultString(f.Destination.Slack.UserSeparator, ", "), RequestTimeout: 10 * time.Second,
		},
	}
	var err error
	for _, duration := range []struct {
		name, value string
		target      *time.Duration
	}{
		{"workadventure.room_discovery_interval", f.WorkAdventure.RoomDiscoveryInterval, &cfg.WorkAdventure.RoomDiscoveryInterval},
		{"workadventure.reconnect_interval", f.WorkAdventure.ReconnectInterval, &cfg.WorkAdventure.ReconnectInterval},
		{"workadventure.initial_sync_timeout", f.WorkAdventure.InitialSyncTimeout, &cfg.WorkAdventure.InitialSyncTimeout},
		{"workadventure.request_timeout", f.WorkAdventure.RequestTimeout, &cfg.WorkAdventure.RequestTimeout},
		{"status.debounce", f.Status.Debounce, &cfg.Status.Debounce},
		{"status.shutdown_timeout", f.Status.ShutdownTimeout, &cfg.Status.ShutdownTimeout},
		{"destination.slack.request_timeout", f.Destination.Slack.RequestTimeout, &cfg.Slack.RequestTimeout},
	} {
		*duration.target, err = parseDuration(duration.name, duration.value, *duration.target)
		if err != nil {
			return Config{}, err
		}
	}
	if cfg.WorkAdventure.Address == "" {
		return Config{}, errors.New("workadventure.address is required")
	}
	if cfg.Status.StateFile == "" {
		return Config{}, errors.New("status.state_file is required")
	}
	if cfg.Slack.ChannelID == "" {
		return Config{}, errors.New("destination.slack.channel_id is required")
	}
	if cfg.Slack.Message == "" {
		return Config{}, errors.New("destination.slack.message is required")
	}
	if cfg.Slack.UserSeparator == "" {
		return Config{}, errors.New("destination.slack.user_separator is required")
	}
	return cfg, nil
}

func parseDuration(name, value string, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}
	return duration, nil
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
