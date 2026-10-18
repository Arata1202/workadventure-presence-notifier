package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

func TestNotify(t *testing.T) {
	var payload webhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s", request.Method)
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier, err := New(Config{
		WebhookURL:    server.URL,
		Message:       "Currently online ({{ .Count }}): {{ .Users }}",
		EmptyMessage:  "No users are currently online.",
		UserSeparator: ", ",
	}, server.Client())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = notifier.Notify(context.Background(), presence.Snapshot{Users: []presence.User{
		{ID: "2", Name: "Bob >"},
		{ID: "1", Name: "Alice & Co"},
	}})
	if err != nil {
		t.Fatalf("Notify() error = %v", err)
	}

	want := "Currently online (2): Alice &amp; Co, Bob &gt;"
	if payload.Text != want {
		t.Fatalf("payload.Text = %q, want %q", payload.Text, want)
	}
}

func TestNotifyEmptySnapshot(t *testing.T) {
	var payload webhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier, err := New(Config{
		WebhookURL:    server.URL,
		Message:       "Currently online ({{ .Count }}): {{ .Users }}",
		EmptyMessage:  "No users are currently online.",
		UserSeparator: ", ",
	}, server.Client())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := notifier.Notify(context.Background(), presence.Snapshot{}); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if payload.Text != "No users are currently online." {
		t.Fatalf("payload.Text = %q", payload.Text)
	}
}

func TestNotifyReturnsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "invalid_payload", http.StatusBadRequest)
	}))
	defer server.Close()

	notifier, err := New(Config{
		WebhookURL:    server.URL,
		Message:       "{{ .Users }}",
		EmptyMessage:  "empty",
		UserSeparator: ", ",
	}, server.Client())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := notifier.Notify(context.Background(), presence.Snapshot{}); err == nil {
		t.Fatal("Notify() error = nil")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestNotifyDoesNotExposeWebhookURLOnTransportError(t *testing.T) {
	const webhookURL = "https://hooks.slack.com/services/secret/path/value"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: request.Method, URL: request.URL.String(), Err: errors.New("connection failed")}
	})}

	notifier, err := New(Config{
		WebhookURL:    webhookURL,
		Message:       "{{ .Users }}",
		EmptyMessage:  "empty",
		UserSeparator: ", ",
	}, client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = notifier.Notify(context.Background(), presence.Snapshot{})
	if err == nil {
		t.Fatal("Notify() error = nil")
	}
	if strings.Contains(err.Error(), webhookURL) || strings.Contains(err.Error(), "secret/path/value") {
		t.Fatalf("Notify() error exposed webhook URL: %v", err)
	}
}
