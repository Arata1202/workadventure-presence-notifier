package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

const SlackWebhookEnv = "SLACK_WEBHOOK_URL"

type Config struct {
	WorkAdventure WorkAdventureConfig
	Notification  NotificationConfig
	Slack         SlackConfig
}

type WorkAdventureConfig struct {
	Address               string
	RoomDiscoveryInterval time.Duration
	ReconnectInterval     time.Duration
	InitialSyncTimeout    time.Duration
	RequestTimeout        time.Duration
}

type NotificationConfig struct {
	Interval time.Duration
}

type SlackConfig struct {
	WebhookURL    string
	Message       string
	EmptyMessage  string
	UserSeparator string
}

type fileConfig struct {
	WorkAdventure fileWorkAdventure `yaml:"workadventure"`
	Notification  fileNotification  `yaml:"notification"`
	Notifiers     fileNotifiers     `yaml:"notifiers"`
}

type fileWorkAdventure struct {
	Address               string `yaml:"address"`
	RoomDiscoveryInterval string `yaml:"room_discovery_interval"`
	ReconnectInterval     string `yaml:"reconnect_interval"`
	InitialSyncTimeout    string `yaml:"initial_sync_timeout"`
	RequestTimeout        string `yaml:"request_timeout"`
}

type fileNotification struct {
	Interval string `yaml:"interval"`
}

type fileNotifiers struct {
	Slack fileSlack `yaml:"slack"`
}

type fileSlack struct {
	Message       string `yaml:"message"`
	EmptyMessage  string `yaml:"empty_message"`
	UserSeparator string `yaml:"user_separator"`
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

	cfg, err := file.toConfig()
	if err != nil {
		return Config{}, err
	}

	cfg.Slack.WebhookURL = os.Getenv(SlackWebhookEnv)
	if cfg.Slack.WebhookURL == "" {
		return Config{}, fmt.Errorf("%s is required", SlackWebhookEnv)
	}

	return cfg, nil
}

func (f fileConfig) toConfig() (Config, error) {
	cfg := Config{
		WorkAdventure: WorkAdventureConfig{
			Address:               defaultString(f.WorkAdventure.Address, "back:50051"),
			RoomDiscoveryInterval: 30 * time.Second,
			ReconnectInterval:     5 * time.Second,
			InitialSyncTimeout:    2 * time.Second,
			RequestTimeout:        10 * time.Second,
		},
		Notification: NotificationConfig{Interval: 30 * time.Minute},
		Slack: SlackConfig{
			Message:       defaultString(f.Notifiers.Slack.Message, "Currently online ({{ .Count }}): {{ .Users }}"),
			EmptyMessage:  defaultString(f.Notifiers.Slack.EmptyMessage, "No users are currently online."),
			UserSeparator: defaultString(f.Notifiers.Slack.UserSeparator, ", "),
		},
	}

	var err error
	if cfg.WorkAdventure.RoomDiscoveryInterval, err = parseDuration("workadventure.room_discovery_interval", f.WorkAdventure.RoomDiscoveryInterval, cfg.WorkAdventure.RoomDiscoveryInterval); err != nil {
		return Config{}, err
	}
	if cfg.WorkAdventure.ReconnectInterval, err = parseDuration("workadventure.reconnect_interval", f.WorkAdventure.ReconnectInterval, cfg.WorkAdventure.ReconnectInterval); err != nil {
		return Config{}, err
	}
	if cfg.WorkAdventure.InitialSyncTimeout, err = parseDuration("workadventure.initial_sync_timeout", f.WorkAdventure.InitialSyncTimeout, cfg.WorkAdventure.InitialSyncTimeout); err != nil {
		return Config{}, err
	}
	if cfg.WorkAdventure.RequestTimeout, err = parseDuration("workadventure.request_timeout", f.WorkAdventure.RequestTimeout, cfg.WorkAdventure.RequestTimeout); err != nil {
		return Config{}, err
	}
	if cfg.Notification.Interval, err = parseDuration("notification.interval", f.Notification.Interval, cfg.Notification.Interval); err != nil {
		return Config{}, err
	}

	if cfg.WorkAdventure.Address == "" {
		return Config{}, errors.New("workadventure.address is required")
	}
	if cfg.Slack.Message == "" {
		return Config{}, errors.New("notifiers.slack.message is required")
	}
	if cfg.Slack.EmptyMessage == "" {
		return Config{}, errors.New("notifiers.slack.empty_message is required")
	}
	if cfg.Slack.UserSeparator == "" {
		return Config{}, errors.New("notifiers.slack.user_separator is required")
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
