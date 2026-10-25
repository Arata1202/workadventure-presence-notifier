package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestInteractions(t *testing.T) {
	const secret = "test-only-signing-secret"
	payload := `{"type":"block_actions","actions":[{"type":"button","action_id":"open_workadventure"}],"response_url":"https://ignored.example.invalid/"}`
	body := url.Values{"payload": {payload}}.Encode()
	handler, err := NewInteractionHandler(secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, body, timestamp, signature, contentType string
		want                                                        int
	}{
		{name: "valid", want: 200},
		{name: "wrong method", method: "GET", want: 405},
		{name: "wrong path", path: "/slack/events", want: 404},
		{name: "trailing slash", path: InteractionsPath + "/", want: 404},
		{name: "missing timestamp", timestamp: "missing", want: 401},
		{name: "invalid timestamp", timestamp: "nope", want: 401},
		{name: "overflow timestamp", timestamp: "9223372036854775807", want: 401},
		{name: "old", timestamp: strconv.FormatInt(time.Now().Unix()-600, 10), want: 401},
		{name: "future", timestamp: strconv.FormatInt(time.Now().Unix()+600, 10), want: 401},
		{name: "missing signature", signature: "missing", want: 401},
		{name: "wrong signature", signature: "v0=" + strings.Repeat("0", 64), want: 401},
		{name: "unsupported signature", signature: "v1=abc", want: 401},
		{name: "wrong content type", contentType: "application/json", want: 415},
		{name: "invalid form", body: "payload=%zz", want: 400},
		{name: "missing payload", body: "other=value", want: 400},
		{name: "duplicate payload", body: body + "&" + body, want: 400},
		{name: "invalid json", body: "payload=%7B", want: 400},
		{name: "wrong type", body: url.Values{"payload": {strings.Replace(payload, "block_actions", "url_verification", 1)}}.Encode(), want: 400},
		{name: "wrong action", body: url.Values{"payload": {strings.Replace(payload, entranceActionID, "other", 1)}}.Encode(), want: 400},
		{name: "wrong element", body: url.Values{"payload": {strings.Replace(payload, "button", "static_select", 1)}}.Encode(), want: 400},
		{name: "no actions", body: `payload=%7B%22type%22%3A%22block_actions%22%7D`, want: 400},
		{name: "oversized", body: strings.Repeat("x", maxInteractionBody+1), want: 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method, path, b, stamp, kind := tc.method, tc.path, tc.body, tc.timestamp, tc.contentType
			if method == "" {
				method = "POST"
			}
			if path == "" {
				path = InteractionsPath
			}
			if b == "" {
				b = body
			}
			if stamp == "" {
				stamp = strconv.FormatInt(time.Now().Unix(), 10)
			}
			if stamp == "missing" {
				stamp = ""
			}
			if kind == "" {
				kind = "application/x-www-form-urlencoded; charset=utf-8"
			}
			req := httptest.NewRequest(method, path, strings.NewReader(b))
			req.Header.Set("Content-Type", kind)
			req.Header.Set("X-Slack-Request-Timestamp", stamp)
			mac := hmac.New(sha256.New, []byte(secret))
			_, _ = mac.Write([]byte("v0:" + stamp + ":" + b))
			sig := "v0=" + hex.EncodeToString(mac.Sum(nil))
			if tc.signature != "" {
				sig = tc.signature
			}
			if sig == "missing" {
				sig = ""
			}
			req.Header.Set("X-Slack-Signature", sig)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.want {
				t.Fatalf("status=%d want=%d", response.Code, tc.want)
			}
			if tc.want == 200 && response.Body.Len() != 0 {
				t.Fatal("ACK must be empty")
			}
			if strings.Contains(response.Body.String(), secret) || strings.Contains(response.Body.String(), "ignored.example.invalid") {
				t.Fatal("request data leaked")
			}
		})
	}
}

func TestInteractionsVerifyRawBody(t *testing.T) {
	handler, _ := NewInteractionHandler("test-only-secret")
	// Signing a decoded or re-encoded form must not authenticate a different body.
	raw := `payload=%7b%22type%22%3a%22block_actions%22%7d`
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("test-only-secret"))
	_, _ = mac.Write([]byte("v0:" + stamp + ":" + strings.ToUpper(raw)))
	req := httptest.NewRequest("POST", InteractionsPath, strings.NewReader(raw))
	req.Header.Set("X-Slack-Request-Timestamp", stamp)
	req.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatal("altered raw body authenticated")
	}
}

func TestInteractionHandlerRequiresSecret(t *testing.T) {
	if _, err := NewInteractionHandler(""); err == nil {
		t.Fatal("empty secret accepted")
	}
}
