package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/notifier"
	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

func TestRenderPreservesTrustedMrkdwnAndEscapesNames(t *testing.T) {
	client, err := New(Config{
		BotToken: "test", ChannelID: "C123",
		Message:       "*Online* <https://example.com|office> <!here> ({{ .Count }}): {{ .Users }} / {{ range .UserNames }}[{{ . }}]{{ end }}",
		UserSeparator: ", ",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text, err := client.Render(presence.Snapshot{Users: []presence.User{
		{ID: "2", Name: "<!channel>"}, {ID: "1", Name: "Alice & <https://evil.test|click>"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := "*Online* <https://example.com|office> <!here> (2): &lt;!channel&gt;, Alice &amp; &lt;https://evil.test|click&gt; / [&lt;!channel&gt;][Alice &amp; &lt;https://evil.test|click&gt;]"
	if text != want {
		t.Fatalf("Render() = %q, want %q", text, want)
	}
}

func TestPostUsesBotAPIAndDisablesUnfurls(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatal("missing bot authorization")
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"ts":"123.456"}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{})
	ts, err := client.Post(context.Background(), "hello", "11111111-2222-4333-8444-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	if ts != "123.456" {
		t.Fatalf("timestamp = %q", ts)
	}
	if payload["mrkdwn"] != true || payload["unfurl_links"] != false || payload["unfurl_media"] != false {
		t.Fatalf("payload = %#v", payload)
	}
	metadata := payload["metadata"].(map[string]any)
	eventPayload := metadata["event_payload"].(map[string]any)
	if metadata["event_type"] != StatusEventType || eventPayload["operation_id"] != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestAPIOkFalseAndMessageNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "chat.delete") {
			_, _ = w.Write([]byte(`{"ok":false,"error":"message_not_found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":false,"error":"not_in_channel"}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{MaxAttempts: 1})
	if _, err := client.Post(context.Background(), "hello", "id"); err == nil || !strings.Contains(err.Error(), "not_in_channel") {
		t.Fatalf("Post() error = %v", err)
	}
	if err := client.Delete(context.Background(), "123"); !errors.Is(err, notifier.ErrMessageNotFound) {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestRetries429UsingRetryAfter(t *testing.T) {
	attempts := 0
	var waited time.Duration
	var operationIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		var payload postPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		operationIDs = append(operationIDs, payload.Metadata.EventPayload.OperationID)
		if attempts == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("secret response"))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"ts":"123"}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{RetryWait: func(_ context.Context, d time.Duration) error { waited = d; return nil }})
	if _, err := client.Post(context.Background(), "hello", "stable-id"); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || waited != 7*time.Second {
		t.Fatalf("attempts = %d, waited = %v", attempts, waited)
	}
	if len(operationIDs) != 2 || operationIDs[0] != "stable-id" || operationIDs[1] != "stable-id" {
		t.Fatalf("operation IDs = %#v", operationIDs)
	}
}

func TestUpdateUsesBotAPIAndDisablesUnfurls(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.update" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"ts":"123.456"}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{})
	if err := client.Update(context.Background(), "123.456", "updated", "operation-id"); err != nil {
		t.Fatal(err)
	}
	if payload["ts"] != "123.456" || payload["text"] != "updated" || payload["mrkdwn"] != true || payload["unfurl_links"] != false || payload["unfurl_media"] != false {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestErrorsDoNotExposeTokenOrResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("private-display-name"))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{BotToken: "secret-token", MaxAttempts: 1})
	_, err := client.Post(context.Background(), "hello", "id")
	if err == nil {
		t.Fatal("Post() error = nil")
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "private-display-name") {
		t.Fatalf("error exposed secret: %v", err)
	}
}

func TestFindByOperationPaginatesAndFiltersMetadata(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload historyPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if !payload.IncludeAllMetadata || payload.Channel != "C123" || payload.Oldest != "100.000000" || payload.Limit != HistoryPageSize {
			t.Fatalf("payload = %#v", payload)
		}
		if requests == 1 {
			_, _ = w.Write([]byte(`{"ok":true,"messages":[{"ts":"101","metadata":{"event_type":"other","event_payload":{"operation_id":"op"}}}],"response_metadata":{"next_cursor":"next"}}`))
			return
		}
		if payload.Cursor != "next" {
			t.Fatalf("cursor = %q", payload.Cursor)
		}
		_, _ = w.Write([]byte(`{"ok":true,"messages":[{"ts":"102","metadata":{"event_type":"workadventure_presence_status","event_payload":{"operation_id":"op"}}}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{})
	matches, err := client.FindByOperation(context.Background(), "op", time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0] != "102" || requests != 2 {
		t.Fatalf("matches = %#v, requests = %d", matches, requests)
	}
}

func TestFindByOperationDecodesHistoryResponseLargerThan64KiB(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload historyPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Limit != HistoryPageSize {
			t.Fatalf("history limit = %d", payload.Limit)
		}
		response := map[string]any{
			"ok": true,
			"messages": []any{
				map[string]any{"ts": "101.000000", "text": strings.Repeat("x", 70*1024)},
				map[string]any{"ts": "102.000000", "metadata": map[string]any{
					"event_type":    StatusEventType,
					"event_payload": map[string]any{"operation_id": "op"},
				}},
			},
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{})
	matches, err := client.FindByOperation(context.Background(), "op", time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0] != "102.000000" {
		t.Fatalf("matches = %#v", matches)
	}
}

func TestSlackResponsesRemainBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", MaximumResponseSize+1)))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{MaxAttempts: 1})
	if _, err := client.FindByOperation(context.Background(), "op", time.Unix(100, 0)); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("FindByOperation() error = %v", err)
	}
}

func TestPostDoesNotRetryAmbiguousFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"http 500", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }},
		{"fatal error", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ok":false,"error":"fatal_error"}`))
		}},
		{"decode", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`not-json`)) }},
		{"missing timestamp", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				test.handler(w, r)
			}))
			defer server.Close()
			client := newTestClient(t, server, Config{MaxAttempts: 3})
			if _, err := client.Post(context.Background(), "hello", "op"); err == nil {
				t.Fatal("Post() error = nil")
			}
			if attempts != 1 {
				t.Fatalf("attempts = %d", attempts)
			}
		})
	}
}

func TestPostDoesNotRetryNetworkFailure(t *testing.T) {
	attempts := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("network unavailable")
	})}
	client, err := New(Config{
		BotToken: "test-token", ChannelID: "C123", Message: "{{ .Users }}",
		UserSeparator: ", ", MaxAttempts: 3, RetryWait: func(context.Context, time.Duration) error { return nil },
	}, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Post(context.Background(), "hello", "op"); err == nil {
		t.Fatal("Post() error = nil")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d", attempts)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func newTestClient(t *testing.T, server *httptest.Server, override Config) *Client {
	t.Helper()
	config := Config{BotToken: "test-token", ChannelID: "C123", Message: "{{ .Users }}", UserSeparator: ", ", APIBaseURL: server.URL, MaxAttempts: 3}
	if override.BotToken != "" {
		config.BotToken = override.BotToken
	}
	if override.MaxAttempts != 0 {
		config.MaxAttempts = override.MaxAttempts
	}
	if override.RetryWait != nil {
		config.RetryWait = override.RetryWait
	} else {
		config.RetryWait = func(context.Context, time.Duration) error { return nil }
	}
	client, err := New(config, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}
