package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/panel/vault"
)

// The Telegram Bot API over HTTPS, as little of it as the panel needs: getMe (is the token good, who is the bot),
// deleteWebhook (long polling and a webhook exclude each other), getUpdates (hear "/start <code>") and sendMessage. Every
// call is a JSON POST to <base>/bot<token>/<method>. The token is part of that URL, so an error from net/http (which prints
// the URL) is never passed on: a transport failure becomes an apiError that says only that it was one.

const (
	defaultAPIBase = "https://api.telegram.org"
	maxRetryAfter  = time.Hour
)

var botTokenInText = regexp.MustCompile(`bot[0-9]+:[A-Za-z0-9_-]+`)

func scrubBotDescription(desc, token string) string {
	if token != "" {
		desc = strings.ReplaceAll(desc, token, "[REDACTED]")
	}
	return botTokenInText.ReplaceAllString(desc, "[REDACTED]")
}

func retryAfterDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	if seconds > int(maxRetryAfter/time.Second) {
		return maxRetryAfter
	}
	return time.Duration(seconds) * time.Second
}

func retryAfterWait(d time.Duration) time.Duration {
	return min(max(d, time.Second), maxRetryAfter)
}

// apiError is what a call returns when it did not succeed. It never carries the token.
type apiError struct {
	Method     string
	Status     int           // HTTP status; 0 for a transport failure
	Desc       string        // Telegram's own description ("Forbidden: bot was blocked by the user")
	RetryAfter time.Duration // 429: how long Telegram asks to wait
	Transport  bool          // no usable answer: the network, a timeout, or a body that was not JSON
}

func (e *apiError) Error() string {
	if e.Transport {
		return "telegram " + e.Method + ": no answer"
	}
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Status, e.Desc)
}

// code is the short word the admin screen shows for a failure ("" for none).
func (e *apiError) code() string {
	switch {
	case e == nil:
		return ""
	case e.Status == http.StatusUnauthorized || e.Status == http.StatusNotFound:
		return "unauthorized" // Telegram answers 401 for a revoked token and 404 for a malformed one
	case e.Status == http.StatusConflict:
		return "conflict"
	case e.Status == http.StatusForbidden:
		return "blocked" // the user blocked the bot, or removed the chat
	}
	return "unreachable"
}

// errCode is code() for any error from this package.
func errCode(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.code()
	}
	if err == nil {
		return ""
	}
	return "unreachable"
}

type apiClient struct {
	base string
	hc   *http.Client
}

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (c *apiClient) call(ctx context.Context, token vault.Redacted, method string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+token.Reveal()+"/"+method, bytes.NewReader(body))
	if err != nil {
		return &apiError{Method: method, Transport: true}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return &apiError{Method: method, Transport: true} // *url.Error would print the URL, and the token with it
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return &apiError{Method: method, Transport: true}
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return &apiError{Method: method, Status: resp.StatusCode, Transport: true}
	}
	if !env.OK {
		return &apiError{Method: method, Status: resp.StatusCode,
			Desc:       strings.TrimSpace(scrubBotDescription(env.Description, token.Reveal())),
			RetryAfter: retryAfterDuration(env.Parameters.RetryAfter)}
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return &apiError{Method: method, Status: resp.StatusCode, Transport: true}
		}
	}
	return nil
}

type tgUser struct {
	ID       int64  `json:"id"`
	IsBot    bool   `json:"is_bot"`
	Username string `json:"username"`
}

type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type tgMessage struct {
	Chat tgChat `json:"chat"`
	Text string `json:"text"`
}

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

func (c *apiClient) getMe(ctx context.Context, token vault.Redacted) (tgUser, error) {
	var u tgUser
	err := c.call(ctx, token, "getMe", struct{}{}, &u)
	return u, err
}

func (c *apiClient) deleteWebhook(ctx context.Context, token vault.Redacted) error {
	return c.call(ctx, token, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
}

// getUpdates is a long poll: Telegram holds the request up to timeout seconds. Only messages are asked for.
func (c *apiClient) getUpdates(ctx context.Context, token vault.Redacted, offset int64, timeout int) ([]tgUpdate, error) {
	var ups []tgUpdate
	err := c.call(ctx, token, "getUpdates", map[string]any{"offset": offset, "timeout": timeout, "limit": 20, "allowed_updates": []string{"message"}}, &ups)
	return ups, err
}

// sendMessage sends HTML text without a link preview.
func (c *apiClient) sendMessage(ctx context.Context, token vault.Redacted, chatID int64, text string) error {
	return c.call(ctx, token, "sendMessage", map[string]any{
		"chat_id": chatID, "text": text, "parse_mode": "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}, nil)
}
