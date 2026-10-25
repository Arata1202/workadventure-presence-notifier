package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestInteractionReceiverLifecycle(t *testing.T) {
	const secret = "test-only-secret"
	server, failures, done, err := startInteractions("127.0.0.1:0", secret)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	body := url.Values{"payload": {`{"type":"block_actions","actions":[{"type":"button","action_id":"open_workadventure"}]}`}}.Encode()
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + stamp + ":" + body))
	req, err := http.NewRequest("POST", "http://"+server.Addr+"/slack/interactions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", stamp)
	req.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	received, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || len(received) != 0 {
		t.Fatalf("ACK: status=%d body=%q error=%v", response.StatusCode, received, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("receiver goroutine did not stop")
	}
	select {
	case err := <-failures:
		t.Fatalf("unexpected shutdown error: %v", err)
	default:
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		t.Fatal("listener not released:", err)
	}
	listener.Close()
}

func TestInteractionReceiverBindFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, _, _, err := startInteractions(listener.Addr().String(), "test-only-secret"); err == nil {
		t.Fatal("occupied bind accepted")
	}
	if _, _, _, err := startInteractions("127.0.0.1:0", ""); err == nil {
		t.Fatal("missing secret accepted")
	}
}
