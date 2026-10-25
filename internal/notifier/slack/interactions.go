package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	InteractionsPath   = "/slack/interactions"
	entranceActionID   = "open_workadventure"
	maxInteractionBody = 1 << 20
)

// NewInteractionHandler acknowledges the fixed URL button without taking any
// action or following URLs supplied in the payload. It never logs request data.
func NewInteractionHandler(signingSecret string) (http.Handler, error) {
	if strings.TrimSpace(signingSecret) == "" {
		return nil, errors.New("Slack signing secret is required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != InteractionsPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		timestamp := r.Header.Get("X-Slack-Request-Timestamp")
		seconds, err := strconv.ParseInt(timestamp, 10, 64)
		now := time.Now().Unix()
		if err != nil || seconds < now-300 || seconds > now+300 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInteractionBody))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			} else {
				http.Error(w, "invalid request", http.StatusBadRequest)
			}
			return
		}
		mac := hmac.New(sha256.New, []byte(signingSecret))
		_, _ = mac.Write([]byte("v0:" + timestamp + ":"))
		_, _ = mac.Write(body)
		expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(expected), []byte(r.Header.Get("X-Slack-Signature"))) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/x-www-form-urlencoded" {
			http.Error(w, "unsupported content type", http.StatusUnsupportedMediaType)
			return
		}
		form, err := url.ParseQuery(string(body))
		if err != nil || len(form["payload"]) != 1 {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		var payload struct {
			Type    string `json:"type"`
			Actions []struct {
				Type     string `json:"type"`
				ActionID string `json:"action_id"`
			} `json:"actions"`
		}
		if json.Unmarshal([]byte(form.Get("payload")), &payload) != nil ||
			payload.Type != "block_actions" || len(payload.Actions) != 1 ||
			payload.Actions[0].Type != "button" || payload.Actions[0].ActionID != entranceActionID {
			http.Error(w, "unsupported payload", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}), nil
}
