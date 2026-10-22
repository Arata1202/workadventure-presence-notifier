package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/Arata1202/workadventure-presence-notifier/internal/notifier"
	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

const (
	defaultAPIBaseURL = "https://slack.com/api/"
	defaultAttempts   = 3
	maxResponseSize   = 1 << 20
	maxMessageRunes   = 4000
	maxNameRunes      = 80
	maxNames          = 100
)

type Config struct {
	BotToken      string
	ChannelID     string
	Message       string
	UserSeparator string
	APIBaseURL    string
	MaxAttempts   int
	MaxRetryWait  time.Duration
}

type Client struct {
	token         string
	channelID     string
	message       *template.Template
	userSeparator string
	baseURL       string
	httpClient    *http.Client
	maxAttempts   int
	maxRetryWait  time.Duration
}

type templateData struct {
	Count     int
	Users     string
	UserNames []string
}

type postPayload struct {
	Channel     string `json:"channel"`
	Text        string `json:"text"`
	Mrkdwn      bool   `json:"mrkdwn"`
	UnfurlLinks bool   `json:"unfurl_links"`
	UnfurlMedia bool   `json:"unfurl_media"`
}

type deletePayload struct {
	Channel   string `json:"channel"`
	Timestamp string `json:"ts"`
}

type apiResponse struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error"`
	Timestamp string `json:"ts"`
}

func New(config Config, client *http.Client) (*Client, error) {
	message, err := template.New("slack-message").Option("missingkey=error").Parse(config.Message)
	if err != nil {
		return nil, fmt.Errorf("parse Slack message template: %w", err)
	}
	if config.BotToken == "" {
		return nil, errors.New("Slack bot token is required")
	}
	if config.ChannelID == "" {
		return nil, errors.New("Slack channel ID is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if config.APIBaseURL == "" {
		config.APIBaseURL = defaultAPIBaseURL
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = defaultAttempts
	}
	if config.MaxAttempts < 1 {
		return nil, errors.New("Slack max attempts must be positive")
	}
	if config.MaxRetryWait == 0 {
		config.MaxRetryWait = 30 * time.Second
	}
	if config.MaxRetryWait < 0 {
		return nil, errors.New("Slack maximum retry wait must not be negative")
	}
	return &Client{
		token: config.BotToken, channelID: config.ChannelID, message: message,
		userSeparator: config.UserSeparator, baseURL: strings.TrimRight(config.APIBaseURL, "/") + "/",
		httpClient: client, maxAttempts: config.MaxAttempts, maxRetryWait: config.MaxRetryWait,
	}, nil
}

func (c *Client) DestinationID() string { return "slack:" + c.channelID }

func (c *Client) Render(snapshot presence.Snapshot) (string, error) {
	names := make([]string, 0, min(snapshot.Count(), maxNames))
	for _, user := range snapshot.Users {
		if len(names) == maxNames {
			break
		}
		name := truncateRunes(user.Name, maxNameRunes)
		if name == "" {
			name = "Unknown user"
		}
		names = append(names, escapeSlackText(name))
	}
	sort.Strings(names)
	joined := strings.Join(names, c.userSeparator)
	if omitted := snapshot.Count() - len(names); omitted > 0 {
		joined += fmt.Sprintf("%s… (+%d more)", c.userSeparator, omitted)
	}
	var output bytes.Buffer
	if err := c.message.Execute(&output, templateData{Count: snapshot.Count(), Users: joined, UserNames: names}); err != nil {
		return "", fmt.Errorf("render Slack message template: %w", err)
	}
	if utf8.RuneCount(output.Bytes()) > maxMessageRunes {
		return "", fmt.Errorf("rendered Slack message exceeds %d characters", maxMessageRunes)
	}
	return output.String(), nil
}

func (c *Client) Post(ctx context.Context, text string) (notifier.MessageRef, error) {
	var response apiResponse
	err := c.call(ctx, "chat.postMessage", postPayload{
		Channel: c.channelID, Text: text, Mrkdwn: false, UnfurlLinks: false, UnfurlMedia: false,
	}, &response, false)
	if err != nil {
		return "", err
	}
	if response.Timestamp == "" {
		return "", errors.New("Slack chat.postMessage returned no timestamp")
	}
	return notifier.MessageRef(response.Timestamp), nil
}

func (c *Client) Delete(ctx context.Context, ref notifier.MessageRef) error {
	if ref == "" {
		return nil
	}
	var response apiResponse
	return c.call(ctx, "chat.delete", deletePayload{Channel: c.channelID, Timestamp: string(ref)}, &response, true)
}

func (c *Client) call(ctx context.Context, method string, payload any, result *apiResponse, retryTransient bool) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Slack %s request: %w", method, err)
	}
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+method, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create Slack %s request", method)
		}
		request.Header.Set("Authorization", "Bearer "+c.token)
		request.Header.Set("Content-Type", "application/json; charset=utf-8")
		response, err := c.httpClient.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("call Slack %s: %w", method, ctx.Err())
			}
			if retryTransient && attempt < c.maxAttempts {
				if err := wait(ctx, time.Duration(attempt)*time.Second); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("call Slack %s: request failed", method)
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
		response.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read Slack %s response", method)
		}
		if len(responseBody) > maxResponseSize {
			return fmt.Errorf("Slack %s response exceeded %d bytes", method, maxResponseSize)
		}
		*result = apiResponse{}
		decodeErr := json.Unmarshal(responseBody, result)
		if response.StatusCode == http.StatusTooManyRequests && attempt < c.maxAttempts {
			delay := retryAfter(response.Header.Get("Retry-After"), c.maxRetryWait)
			if err := wait(ctx, delay); err != nil {
				return err
			}
			continue
		}
		if response.StatusCode >= 500 && retryTransient && attempt < c.maxAttempts {
			if err := wait(ctx, time.Duration(attempt)*time.Second); err != nil {
				return err
			}
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("Slack %s returned HTTP %d", method, response.StatusCode)
		}
		if decodeErr != nil {
			return fmt.Errorf("decode Slack %s response", method)
		}
		if !result.OK {
			if result.Error == "message_not_found" {
				return notifier.ErrMessageNotFound
			}
			return fmt.Errorf("Slack %s failed: %s", method, safeErrorCode(result.Error))
		}
		return nil
	}
	return fmt.Errorf("Slack %s failed", method)
}

func retryAfter(value string, maximum time.Duration) time.Duration {
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 1 {
		return time.Second
	}
	delay := time.Duration(seconds) * time.Second
	if delay > maximum {
		return maximum
	}
	return delay
}

func safeErrorCode(code string) string {
	if code == "" {
		return "unknown_error"
	}
	for _, r := range code {
		if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return "unknown_error"
		}
	}
	return code
}

func truncateRunes(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum-1]) + "…"
}

func escapeSlackText(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	return strings.ReplaceAll(value, ">", "&gt;")
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
