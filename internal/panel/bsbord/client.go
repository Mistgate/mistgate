package bsbord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	DefaultBaseURL = "https://bsbord.com"
	userAgent      = "mistgate-panel (+https://github.com/mistgate/mistgate)"
	maxBody        = 2 << 20
)

// apiError keeps only bounded, machine-readable fields from an upstream error. The error message is never retained.
type apiError struct {
	Status     int
	Code       string
	Retryable  bool
	RetryAfter time.Duration
}

func (e *apiError) Error() string { return fmt.Sprintf("bsbord: HTTP %d", e.Status) }

type client struct {
	hc        *http.Client
	base      string
	key       vault.Redacted
	onVersion func(string)
}

func (c *client) do(ctx context.Context, method, path string, body string, key string, out any) (http.Header, error) {
	var rd io.Reader
	if body != "" {
		rd = bytes.NewBufferString(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.base, "/")+path, rd)
	if err != nil {
		return nil, errors.New("bsbord: request failed")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key.Reveal())
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("bsbord: request failed")
	}
	defer resp.Body.Close()
	if c.onVersion != nil {
		c.onVersion(resp.Header.Get("X-API-Version"))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(data) > maxBody {
		return nil, errors.New("bsbord: unreadable answer")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		ae := &apiError{Status: resp.StatusCode}
		var envelope struct {
			Error struct {
				Code    string          `json:"code"`
				Details json.RawMessage `json:"details"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil {
			ae.Code = safeCode(envelope.Error.Code)
			var details struct {
				Retryable  bool            `json:"retryable"`
				RetryAfter json.RawMessage `json:"retry_after"`
			}
			if json.Unmarshal(envelope.Error.Details, &details) == nil {
				ae.Retryable = details.Retryable
				ae.RetryAfter = retryAfter(details.RetryAfter)
			}
		}
		if ae.RetryAfter <= 0 {
			if n, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && n > 0 {
				ae.RetryAfter = time.Duration(n) * time.Second
			}
		}
		return resp.Header, ae
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		return resp.Header, errors.New("bsbord: unreadable answer")
	}
	return resp.Header, nil
}

func safeCode(s string) string {
	s = strings.TrimSpace(s)
	if len(s) == 0 || len(s) > 64 {
		return ""
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return ""
		}
	}
	return s
}

func retryAfter(raw json.RawMessage) time.Duration {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 0
}

type account struct {
	BalanceCredits int64  `json:"balance_credits"`
	BonusCredits   int64  `json:"bonus_credits"`
	BalanceTotal   int64  `json:"balance_total"`
	Tier           string `json:"tier"`
	MinIntervalSec int64  `json:"min_interval_sec"`
}

type operatorUnit struct {
	OpKey        string `json:"op_key"`
	Operator     string `json:"operator"`
	Name         string `json:"name"`
	Region       string `json:"region"`
	RegionCode   string `json:"region_code"`
	DPI          string `json:"dpi"`
	ChannelState string `json:"channel_state"`
	Probeable    bool   `json:"probeable"`
}

type operatorsResponse struct {
	Units      []operatorUnit `json:"units"`
	NUnits     int64          `json:"n_units"`
	NProbeable int64          `json:"n_probeable"`
}

type probePreview struct {
	CostCredits int64 `json:"cost_credits"`
}

type probeRequest struct {
	Targets   []string `json:"targets"`
	Operators []string `json:"operators"`
	DPI       string   `json:"dpi"`
}

type probeCell struct {
	OK bool `json:"ok"`
}

type probeLeg struct {
	OK       bool       `json:"ok"`
	Operator string     `json:"operator"`
	Region   string     `json:"region"`
	DPI      string     `json:"dpi"`
	Error    *string    `json:"error"`
	DNSFail  bool       `json:"dns_fail"`
	TCPIsTLS bool       `json:"tcp_is_tls"`
	ICMP     *probeCell `json:"icmp"`
	TCP      *probeCell `json:"tcp"`
}

type probeTargetResult struct {
	ByOperator map[string]probeLeg `json:"by_operator"`
}

type skippedUnit struct {
	OpKey    string `json:"op_key"`
	Target   string `json:"target"`
	Operator string `json:"operator"`
	Region   string `json:"region"`
	Reason   string `json:"reason"`
}

type probeResponse struct {
	Outcome            string                       `json:"outcome"`
	CostCredits        int64                        `json:"cost_credits"`
	Refunded           int64                        `json:"refunded"`
	ByTarget           map[string]probeTargetResult `json:"by_target"`
	Skipped            []string                     `json:"skipped"`
	SkippedUnavailable []skippedUnit                `json:"skipped_unavailable"`
	SkippedDPIOff      []skippedUnit                `json:"skipped_dpi_off"`
}

func (c *client) getAccount(ctx context.Context) (account, http.Header, error) {
	var out account
	h, err := c.do(ctx, http.MethodGet, "/v1/account", "", "", &out)
	return out, h, err
}

func (c *client) operators(ctx context.Context) (operatorsResponse, http.Header, error) {
	var out operatorsResponse
	h, err := c.do(ctx, http.MethodGet, "/v1/operators?probeable=true", "", "", &out)
	return out, h, err
}

func (c *client) preview(ctx context.Context, body string) (probePreview, http.Header, error) {
	var out probePreview
	h, err := c.do(ctx, http.MethodPost, "/v1/probe/preview", body, "", &out)
	return out, h, err
}

func (c *client) probe(ctx context.Context, body, key string) (probeResponse, http.Header, error) {
	var out probeResponse
	h, err := c.do(ctx, http.MethodPost, "/v1/probe", body, key, &out)
	return out, h, err
}

func (c *client) cancel(ctx context.Context, key string) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/probe/cancel", "", key, nil)
	return err
}
