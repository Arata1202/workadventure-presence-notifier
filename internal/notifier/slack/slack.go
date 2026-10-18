package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

type Config struct {
	WebhookURL    string
	Message       string
	EmptyMessage  string
	UserSeparator string
}

type Notifier struct {
	webhookURL    string
	message       *template.Template
	emptyMessage  string
	userSeparator string
	client        *http.Client
}

type templateData struct {
	Count     int
	Users     string
	UserNames []string
}

type webhookPayload struct {
	Text string `json:"text"`
}

func New(config Config, client *http.Client) (*Notifier, error) {
	message, err := template.New("slack-message").Option("missingkey=error").Parse(config.Message)
	if err != nil {
		return nil, fmt.Errorf("parse Slack message template: %w", err)
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Notifier{
		webhookURL:    config.WebhookURL,
		message:       message,
		emptyMessage:  config.EmptyMessage,
		userSeparator: config.UserSeparator,
		client:        client,
	}, nil
}

func (n *Notifier) Notify(ctx context.Context, snapshot presence.Snapshot) error {
	text, err := n.render(snapshot)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(webhookPayload{Text: text})
	if err != nil {
		return fmt.Errorf("encode Slack webhook payload: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create Slack webhook request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := n.client.Do(request)
	if err != nil {
		return fmt.Errorf("send Slack webhook: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("Slack webhook returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (n *Notifier) render(snapshot presence.Snapshot) (string, error) {
	if snapshot.Count() == 0 {
		return n.emptyMessage, nil
	}

	names := make([]string, 0, snapshot.Count())
	for _, user := range snapshot.Users {
		names = append(names, escapeSlackText(user.Name))
	}
	sort.Strings(names)

	var output bytes.Buffer
	if err := n.message.Execute(&output, templateData{
		Count:     len(names),
		Users:     strings.Join(names, n.userSeparator),
		UserNames: names,
	}); err != nil {
		return "", fmt.Errorf("render Slack message template: %w", err)
	}
	return output.String(), nil
}

func escapeSlackText(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	return strings.ReplaceAll(value, ">", "&gt;")
}
