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

	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

var ErrMessageNotFound = errors.New("Slack message not found")

const (
	StatusEventType     = "workadventure_presence_status"
	DefaultAttempts     = 3
	MaximumRetryWait    = 30 * time.Second
	HistoryPageSize     = 50
	MaximumResponseSize = 1 << 20
)

type Message struct {
	Timestamp string
}

type Config struct {
	BotToken      string
	ChannelID     string
	Message       string
	UserSeparator string
	APIBaseURL    string
	MaxAttempts   int
	MaxRetryWait  time.Duration
	RetryWait     func(context.Context, time.Duration) error
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
	retryWait     func(context.Context, time.Duration) error
}

type templateData struct {
	Count     int
	Users     string
	UserNames []string
}

type postPayload struct {
	Channel     string          `json:"channel"`
	Text        string          `json:"text"`
	Metadata    messageMetadata `json:"metadata"`
	Mrkdwn      bool            `json:"mrkdwn"`
	UnfurlLinks bool            `json:"unfurl_links"`
	UnfurlMedia bool            `json:"unfurl_media"`
}

type updatePayload struct {
	Channel     string          `json:"channel"`
	Timestamp   string          `json:"ts"`
	Text        string          `json:"text"`
	Metadata    messageMetadata `json:"metadata"`
	Mrkdwn      bool            `json:"mrkdwn"`
	UnfurlLinks bool            `json:"unfurl_links"`
	UnfurlMedia bool            `json:"unfurl_media"`
}

type messageMetadata struct {
	EventType    string               `json:"event_type"`
	EventPayload metadataEventPayload `json:"event_payload"`
}

type metadataEventPayload struct {
	OperationID string `json:"operation_id"`
}

type historyPayload struct {
	Channel            string `json:"channel"`
	IncludeAllMetadata bool   `json:"include_all_metadata"`
	Oldest             string `json:"oldest,omitempty"`
	Cursor             string `json:"cursor,omitempty"`
	Limit              int    `json:"limit"`
}

type deletePayload struct {
	Channel   string `json:"channel"`
	Timestamp string `json:"ts"`
}

type apiResponse struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error"`
	Timestamp string `json:"ts"`
	Messages  []struct {
		Timestamp string          `json:"ts"`
		Metadata  messageMetadata `json:"metadata"`
	} `json:"messages"`
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
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
		config.APIBaseURL = "https://slack.com/api/"
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = DefaultAttempts
	}
	if config.MaxAttempts < 1 {
		return nil, errors.New("Slack max attempts must be positive")
	}
	if config.RetryWait == nil {
		config.RetryWait = sleep
	}
	if config.MaxRetryWait == 0 {
		config.MaxRetryWait = MaximumRetryWait
	}
	if config.MaxRetryWait < 0 {
		return nil, errors.New("Slack maximum retry wait must not be negative")
	}
	return &Client{
		token:         config.BotToken,
		channelID:     config.ChannelID,
		message:       message,
		userSeparator: config.UserSeparator,
		baseURL:       strings.TrimRight(config.APIBaseURL, "/") + "/",
		httpClient:    client,
		maxAttempts:   config.MaxAttempts,
		maxRetryWait:  config.MaxRetryWait,
		retryWait:     config.RetryWait,
	}, nil
}

func (c *Client) ChannelID() string { return c.channelID }

func (c *Client) Render(snapshot presence.Snapshot) (string, error) {
	names := make([]string, 0, snapshot.Count())
	for _, user := range snapshot.Users {
		names = append(names, escapeSlackText(user.Name))
	}
	sort.Strings(names)
	var output bytes.Buffer
	if err := c.message.Execute(&output, templateData{
		Count: len(names), Users: strings.Join(names, c.userSeparator), UserNames: names,
	}); err != nil {
		return "", fmt.Errorf("render Slack message template: %w", err)
	}
	return output.String(), nil
}

func (c *Client) Post(ctx context.Context, text, operationID string) (string, error) {
	var response apiResponse
	err := c.call(ctx, "chat.postMessage", postPayload{
		Channel: c.channelID, Text: text, Metadata: statusMetadata(operationID),
		Mrkdwn: true, UnfurlLinks: false, UnfurlMedia: false,
	}, &response, false)
	if err != nil {
		return "", err
	}
	if response.Timestamp == "" {
		return "", errors.New("Slack chat.postMessage returned no timestamp")
	}
	return response.Timestamp, nil
}

func (c *Client) Update(ctx context.Context, timestamp, text, operationID string) error {
	var response apiResponse
	return c.call(ctx, "chat.update", updatePayload{
		Channel: c.channelID, Timestamp: timestamp, Text: text, Metadata: statusMetadata(operationID),
		Mrkdwn: true, UnfurlLinks: false, UnfurlMedia: false,
	}, &response, true)
}

func (c *Client) Delete(ctx context.Context, timestamp string) error {
	if timestamp == "" {
		return nil
	}
	var response apiResponse
	return c.call(ctx, "chat.delete", deletePayload{Channel: c.channelID, Timestamp: timestamp}, &response, true)
}

func (c *Client) FindByOperation(ctx context.Context, operationID, oldest string) ([]Message, error) {
	var matches []Message
	cursor := ""
	for {
		var response apiResponse
		err := c.call(ctx, "conversations.history", historyPayload{
			Channel: c.channelID, IncludeAllMetadata: true, Oldest: oldest, Cursor: cursor, Limit: HistoryPageSize,
		}, &response, true)
		if err != nil {
			return nil, err
		}
		for _, message := range response.Messages {
			if message.Metadata.EventType == StatusEventType && message.Metadata.EventPayload.OperationID == operationID {
				matches = append(matches, Message{Timestamp: message.Timestamp})
			}
		}
		cursor = response.ResponseMetadata.NextCursor
		if cursor == "" {
			return matches, nil
		}
	}
}

func (c *Client) call(ctx context.Context, method string, payload any, result *apiResponse, retryAmbiguous bool) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Slack %s request: %w", method, err)
	}
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+method, bytes.NewReader(body))
		if requestErr != nil {
			return fmt.Errorf("create Slack %s request", method)
		}
		request.Header.Set("Authorization", "Bearer "+c.token)
		request.Header.Set("Content-Type", "application/json; charset=utf-8")
		response, requestErr := c.httpClient.Do(request)
		if requestErr != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("call Slack %s: %w", method, ctx.Err())
			}
			if retryAmbiguous && attempt < c.maxAttempts {
				if err := c.retryWait(ctx, retryDelay(attempt)); err != nil {
					return fmt.Errorf("retry Slack %s: %w", method, err)
				}
				continue
			}
			return fmt.Errorf("call Slack %s: request failed", method)
		}
		*result = apiResponse{}
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, MaximumResponseSize+1))
		response.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read Slack %s response", method)
		}
		if len(responseBody) > MaximumResponseSize {
			return fmt.Errorf("Slack %s response exceeded %d bytes", method, MaximumResponseSize)
		}
		decodeErr := json.Unmarshal(responseBody, result)
		if response.StatusCode == http.StatusTooManyRequests {
			if attempt < c.maxAttempts {
				delay := retryAfter(response.Header.Get("Retry-After"), c.maxRetryWait)
				if err := c.retryWait(ctx, delay); err != nil {
					return fmt.Errorf("retry Slack %s: %w", method, err)
				}
				continue
			}
			return fmt.Errorf("Slack %s rate limit exceeded", method)
		}
		if response.StatusCode >= 500 {
			if retryAmbiguous && attempt < c.maxAttempts {
				if err := c.retryWait(ctx, retryDelay(attempt)); err != nil {
					return fmt.Errorf("retry Slack %s: %w", method, err)
				}
				continue
			}
			return fmt.Errorf("Slack %s returned HTTP %d", method, response.StatusCode)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("Slack %s returned HTTP %d", method, response.StatusCode)
		}
		if decodeErr != nil {
			return fmt.Errorf("decode Slack %s response", method)
		}
		if !result.OK {
			if result.Error == "message_not_found" {
				return ErrMessageNotFound
			}
			if retryAmbiguous && isTransientAPIError(result.Error) && attempt < c.maxAttempts {
				if err := c.retryWait(ctx, retryDelay(attempt)); err != nil {
					return fmt.Errorf("retry Slack %s: %w", method, err)
				}
				continue
			}
			return fmt.Errorf("Slack %s failed: %s", method, safeErrorCode(result.Error))
		}
		return nil
	}
	return fmt.Errorf("Slack %s failed", method)
}

func statusMetadata(operationID string) messageMetadata {
	return messageMetadata{EventType: StatusEventType, EventPayload: metadataEventPayload{OperationID: operationID}}
}

func retryDelay(attempt int) time.Duration { return time.Duration(attempt) * time.Second }
func retryAfter(value string, maximum time.Duration) time.Duration {
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 1 {
		return time.Second
	}
	return minDuration(time.Duration(seconds)*time.Second, maximum)
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
func isTransientAPIError(code string) bool {
	return code == "ratelimited" || code == "internal_error" || code == "fatal_error"
}
func safeErrorCode(code string) string {
	for _, r := range code {
		if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return "unknown_error"
		}
	}
	if code == "" {
		return "unknown_error"
	}
	return code
}
func escapeSlackText(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	return strings.ReplaceAll(value, ">", "&gt;")
}
func sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
