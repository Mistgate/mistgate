package cheburcheck

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/httpheaders"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	// DefaultBaseURL is cheburcheck's API. Tests override it with a local httptest server.
	DefaultBaseURL = "https://cheburcheck.ru"
	maxBody        = 512 << 10
)

type apiError struct{ Status int }

func (e *apiError) Error() string { return fmt.Sprintf("cheburcheck: HTTP %d", e.Status) }

var errNoID = errors.New("cheburcheck: check answer has no id")

type client struct {
	hc   *http.Client
	base string
}

type siteStatus struct {
	DomainCount uint64 `json:"domain_count"`
	V4Count     uint64 `json:"v4_count"`
	LastUpdate  string `json:"last_update"`
	Version     string `json:"version"`
}

type probeRun struct {
	OnlineProbes int
	HasOnline    bool
	Done         bool
	Results      []probeResult
}

type probeResult struct {
	ID        string
	Region    string
	Provider  string
	ASN       string
	Verdicts  []string
	DPIHop    int
	TargetHop int
}

func (c *client) request(ctx context.Context, method, target, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, errors.New("cheburcheck: request failed")
	}
	req.Header.Set("User-Agent", httpheaders.MistgateUserAgent)
	req.Header.Set("Accept", accept)
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("cheburcheck: request failed")
	}
	return resp, nil
}

func (c *client) status(ctx context.Context) (siteStatus, error) {
	resp, err := c.request(ctx, http.MethodGet, c.base+"/api/v1/status", "application/json")
	if err != nil {
		return siteStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return siteStatus{}, &apiError{Status: resp.StatusCode}
	}
	var out siteStatus
	if err := decodeLimited(resp.Body, &out); err != nil {
		return siteStatus{}, errors.New("cheburcheck: unreadable answer")
	}
	return out, nil
}

func (c *client) check(ctx context.Context, ip string) (string, error) {
	q := url.Values{}
	q.Set("target", ip)
	resp, err := c.request(ctx, http.MethodGet, c.base+"/api/v1/check?"+q.Encode(), "application/json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", &apiError{Status: resp.StatusCode}
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := decodeLimited(resp.Body, &out); err != nil {
		return "", errors.New("cheburcheck: unreadable check answer")
	}
	if out.ID == "" {
		return "", errNoID
	}
	return out.ID, nil
}

func (c *client) probe(ctx context.Context, id string, token vault.Redacted) (probeRun, error) {
	target := c.base + "/api/v1/probe/" + url.PathEscape(id)
	if token != "" {
		q := url.Values{}
		q.Set("token", token.Reveal())
		target += "?" + q.Encode()
	}
	resp, err := c.request(ctx, http.MethodGet, target, "text/event-stream")
	if err != nil {
		return probeRun{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return probeRun{}, &apiError{Status: resp.StatusCode}
	}
	var out probeRun
	err = readSSE(resp.Body, maxBody, func(ev sseEvent) bool {
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(ev.Data), &fields) != nil {
			return true
		}
		if raw, ok := fields["online_probes"]; ok {
			if n, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				out.OnlineProbes, out.HasOnline = n, true
			}
		}
		if status := jsonString(fields["status"]); status == "done" || ev.Name == "done" {
			out.Done = true
			return false
		}
		if ev.Name != "result" && len(fields["probe_id"]) == 0 {
			return true
		}
		var item struct {
			ProbeID   json.RawMessage `json:"probe_id"`
			Region    string          `json:"region"`
			Provider  string          `json:"provider"`
			ASN       json.RawMessage `json:"asn"`
			Verdicts  []string        `json:"verdicts"`
			DPIHop    int             `json:"dpi_hop"`
			TargetHop int             `json:"target_hop"`
		}
		if json.Unmarshal([]byte(ev.Data), &item) != nil {
			return true
		}
		probeID := jsonString(item.ProbeID)
		if probeID == "" {
			return true
		}
		out.Results = append(out.Results, probeResult{ID: clipText(probeID, 64), Region: item.Region, Provider: item.Provider,
			ASN: jsonString(item.ASN), Verdicts: item.Verdicts, DPIHop: item.DPIHop, TargetHop: item.TargetHop})
		return true
	})
	return out, err
}

type sseEvent struct {
	Name string
	Data string
}

// readSSE follows the event-stream framing rules used by the site. It retains the events read before a deadline or a bad
// frame so a caller can still judge the completed probes.
func readSSE(r io.Reader, limit int, consume func(sseEvent) bool) error {
	limited := &io.LimitedReader{R: r, N: int64(limit) + 1}
	scan := bufio.NewScanner(limited)
	scan.Buffer(make([]byte, 4096), limit)
	var name string
	var data []string
	flush := func() bool {
		if len(data) == 0 {
			name = ""
			return true
		}
		keepGoing := consume(sseEvent{Name: name, Data: strings.Join(data, "\n")})
		name, data = "", nil
		return keepGoing
	}
	for scan.Scan() {
		line := strings.TrimSuffix(scan.Text(), "\r")
		if line == "" {
			if !flush() {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, hasValue := strings.Cut(line, ":")
		if !hasValue {
			value = ""
		}
		if strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		switch field {
		case "event":
			name = value
		case "data":
			data = append(data, value)
		}
	}
	if !flush() {
		return nil
	}
	if err := scan.Err(); err != nil {
		return errors.New("cheburcheck: unreadable event stream")
	}
	if limited.N == 0 {
		return errors.New("cheburcheck: event stream too large")
	}
	return nil
}

func decodeLimited(r io.Reader, out any) error {
	b, err := io.ReadAll(io.LimitReader(r, maxBody+1))
	if err != nil || len(b) > maxBody {
		return errors.New("unreadable")
	}
	return json.Unmarshal(b, out)
}

func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

func (r probeResult) vantage() (countryreach.Vantage, bool) {
	if r.ID == "" {
		return countryreach.Vantage{}, false
	}
	state := countryreach.VantageStateUncertain
	for _, verdict := range r.Verdicts {
		if verdict == "tspu_block" {
			state = countryreach.VantageStateDark
			break
		}
		if verdict == "ok" {
			state = countryreach.VantageStateReach
		}
	}
	return countryreach.Vantage{Key: "probe:" + r.ID, Network: clipText(r.Provider, 64), Region: clipText(r.Region, 64),
		ASN: digitsASN(r.ASN), Kind: countryreach.VantageKindUnknown, State: state, DPIHop: r.DPIHop, TargetHop: r.TargetHop}, true
}

func clipText(value string, maxRunes int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return string(runes)
}

func digitsASN(value string) string {
	var digits strings.Builder
	started := false
	for _, r := range value {
		if r >= '0' && r <= '9' {
			started = true
			digits.WriteRune(r)
		} else if started {
			break
		}
	}
	if digits.Len() == 0 {
		return ""
	}
	n, err := strconv.ParseUint(digits.String(), 10, 32)
	if err != nil {
		return ""
	}
	return strconv.FormatUint(n, 10)
}
