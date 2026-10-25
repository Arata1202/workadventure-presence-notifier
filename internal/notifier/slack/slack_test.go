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
	"unicode/utf8"

	"github.com/Arata1202/workadventure-presence-notifier/internal/notifier"
	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

func TestRenderEscapesSortsAndBoundsNames(t *testing.T) {
	client := newTestClient(t, "http://example.invalid/")
	longName := strings.Repeat("a", maxNameRunes+10)
	text, err := client.Render(presence.Snapshot{Users: []presence.User{
		{ID: "2", Name: "Bob >"}, {ID: "1", Name: "Alice & Co"}, {ID: "3", Name: longName},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Alice &amp; Co") || !strings.Contains(text, "Bob &gt;") || strings.Contains(text, longName) {
		t.Fatalf("Render() = %q", text)
	}
}

func TestDestinationID(t *testing.T) {
	client := newTestClient(t, "http://example.invalid/")
	if got := client.DestinationID(); got != "slack:C123" {
		t.Fatalf("DestinationID() = %q", got)
	}
}

func TestPostAndDelete(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer xoxb-test" {
			t.Fatal("missing bearer token")
		}
		methods = append(methods, request.URL.Path)
		if request.URL.Path == "/chat.postMessage" {
			var payload postPayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Channel != "C123" || payload.Text != "*hello*" || payload.Mrkdwn || payload.UnfurlLinks || payload.UnfurlMedia || len(payload.Blocks) != 0 {
				t.Fatalf("post payload = %#v", payload)
			}
			_, _ = writer.Write([]byte(`{"ok":true,"ts":"123.456"}`))
			return
		}
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	client := newTestClient(t, server.URL+"/")
	ref, err := client.Post(context.Background(), "*hello*")
	if err != nil || ref != "123.456" {
		t.Fatalf("Post() = %q, %v", ref, err)
	}
	if err := client.Delete(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "/chat.postMessage,/chat.delete" {
		t.Fatalf("methods = %v", methods)
	}
}

func TestDeleteMapsMessageNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"ok":false,"error":"message_not_found"}`))
	}))
	defer server.Close()
	client := newTestClient(t, server.URL+"/")
	if err := client.Delete(context.Background(), "123.456"); !errors.Is(err, notifier.ErrMessageNotFound) {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestPostDoesNotRetryAmbiguousServerFailure(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		http.Error(writer, "failure", http.StatusInternalServerError)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL+"/")
	if _, err := client.Post(context.Background(), "hello"); err == nil {
		t.Fatal("Post() error = nil")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestResponseSizeIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("x", maxResponseSize+1)))
	}))
	defer server.Close()
	client := newTestClient(t, server.URL+"/")
	if _, err := client.Post(context.Background(), "hello"); err == nil {
		t.Fatal("Post() error = nil")
	}
}

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := New(Config{
		BotToken: "xoxb-test", ChannelID: "C123", Message: "Currently online ({{ .Count }}): {{ .Users }}",
		UserSeparator: ", ", APIBaseURL: baseURL, MaxAttempts: 1, MaxRetryWait: time.Millisecond,
	}, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestPostEntrance(t *testing.T) {
	for _, label := range []string{"", "Enter office"} {
		t.Run(label, func(t *testing.T) {
			var got postPayload
			var raw map[string]json.RawMessage
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/chat.postMessage" {
					t.Errorf("path = %s", request.URL.Path)
				}
				if err := json.NewDecoder(request.Body).Decode(&raw); err != nil {
					t.Error(err)
				}
				body, _ := json.Marshal(raw)
				if err := json.Unmarshal(body, &got); err != nil {
					t.Error(err)
				}
				_, _ = writer.Write([]byte(`{"ok":true,"ts":"123.456"}`))
			}))
			defer server.Close()
			client, err := New(Config{BotToken: "xoxb-test", ChannelID: "C123", Message: "{{ .Users }}", EntranceURL: "https://office.example.invalid/entrance", EntranceLabel: label, APIBaseURL: server.URL}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			text := "*hello* &amp; &lt;@everyone&gt; :wave:"
			if _, err := client.Post(context.Background(), text); err != nil {
				t.Fatal(err)
			}
			if got.Text != text || got.Mrkdwn || got.UnfurlLinks || got.UnfurlMedia {
				t.Fatalf("fallback = %#v", got)
			}
			if len(got.Blocks) != 2 {
				t.Fatalf("blocks = %#v", got.Blocks)
			}
			body, actions := got.Blocks[0], got.Blocks[1]
			if body.Type != "section" || body.Text == nil || body.Text.Type != "plain_text" || body.Text.Text != text || body.Text.Emoji {
				t.Fatalf("body = %#v", body)
			}
			if actions.Type != "actions" || len(actions.Elements) != 1 {
				t.Fatalf("actions = %#v", actions)
			}
			button := actions.Elements[0]
			if label == "" {
				label = "Open WorkAdventure"
			}
			if button.Type != "button" || button.URL != "https://office.example.invalid/entrance" || button.ActionID != "open_workadventure" || button.Text.Type != "plain_text" || button.Text.Text != label {
				t.Fatalf("button = %#v", button)
			}
		})
	}
}

func TestPostEntrancePreservesLongUnicodeMessage(t *testing.T) {
	var got postPayload
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_, _ = writer.Write([]byte(`{"ok":true,"ts":"123.456"}`))
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	client.entranceURL = "https://office.example.invalid/"
	text := strings.Repeat("人", 2998) + "&amp;" + strings.Repeat("人", 900)
	if _, err := client.Post(context.Background(), text); err != nil {
		t.Fatal(err)
	}
	if len(got.Blocks) != 3 {
		t.Fatalf("blocks = %d", len(got.Blocks))
	}
	var joined string
	for _, block := range got.Blocks[:2] {
		if block.Type != "section" || block.Text == nil || utf8.RuneCountInString(block.Text.Text) > maxSectionRunes || !utf8.ValidString(block.Text.Text) {
			t.Fatalf("invalid section = %#v", block)
		}
		joined += block.Text.Text
	}
	if joined != text || got.Text != text {
		t.Fatal("message changed when adding entrance")
	}
	if got.Blocks[1].Text.Text[:5] != "&amp;" {
		t.Fatal("escaped entity split across sections")
	}
}
