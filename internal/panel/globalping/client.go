package globalping

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

	"github.com/mistgate/mistgate/internal/panel/httpheaders"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// A small client of the Globalping API (https://api.globalping.io/v1/spec.yaml). It speaks to POST /v1/measurements, GET
// /v1/measurements/{id}, GET /v1/limits and GET /v1/probes. The token, when there is one, only ever goes into the
// Authorization header of those calls; it is a vault.Redacted so that no log line or error can print it.

const (
	// DefaultBaseURL is Globalping's API.
	DefaultBaseURL = "https://api.globalping.io"
	maxBody        = 4 << 20 // a measurement of a few probes is a few KB; this is a ceiling
	// maxProbesBody caps the response size of the full probe list.
	maxProbesBody = 16 << 20
)

// apiError is an answer that was not a success. Only the error type of Globalping's body is kept, never its text.
type apiError struct {
	Status int
	Type   string
	// Reset is how long Globalping asks to wait (X-RateLimit-Reset on a create, Retry-After on a read); 0 = not said.
	Reset time.Duration
}

func (e *apiError) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("globalping: HTTP %d (%s)", e.Status, e.Type)
	}
	return fmt.Sprintf("globalping: HTTP %d", e.Status)
}

type client struct {
	hc    *http.Client
	base  string
	token vault.Redacted
}

// do sends one request and decodes a 2xx JSON body into out (nil = ignore it). Any other status is an *apiError.
func (c *client) do(ctx context.Context, method, path string, body, out any) (http.Header, error) {
	return c.doLimit(ctx, method, path, body, out, maxBody)
}

func (c *client) doLimit(ctx context.Context, method, path string, body, out any, limit int64) (http.Header, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpheaders.MistgateUserAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token.Reveal())
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil { // the caller's deadline or cancel, kept so that it can be told apart
			return nil, fmt.Errorf("globalping: %w", cerr)
		}
		return nil, errors.New("globalping: request failed") // net/http puts the URL into its error: say nothing of it
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, errors.New("globalping: unreadable answer")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("globalping: unreadable answer")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		ae := &apiError{Status: resp.StatusCode}
		var e struct {
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil {
			ae.Type = e.Error.Type
		}
		for _, h := range []string{"X-RateLimit-Reset", "Retry-After"} {
			if n, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get(h))); err == nil && n > 0 {
				ae.Reset = time.Duration(n) * time.Second
				break
			}
		}
		return resp.Header, ae
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.Header, errors.New("globalping: unreadable answer")
		}
	}
	return resp.Header, nil
}

// ---------------------------------------------------------------------------------------------------
// Wire types

type location struct {
	Country string   `json:"country,omitempty"`
	ASN     uint32   `json:"asn,omitempty"`
	City    string   `json:"city,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Limit   int      `json:"limit,omitempty"`
}

type pingOptions struct {
	Protocol string `json:"protocol,omitempty"`
	Port     uint16 `json:"port,omitempty"`
	Packets  int    `json:"packets,omitempty"`
	// IPVersion (4 or 6) is only allowed when the target is a hostname: it picks the address family of the lookup, and with 6
	// only the probes that have IPv6. With an IP literal as the target the API refuses it and takes the literal's family.
	IPVersion int `json:"ipVersion,omitempty"`
}

// measurementRequest: Locations is either []location or the id (string) of an earlier measurement whose probes are reused.
type measurementRequest struct {
	Type               string `json:"type"`
	Target             string `json:"target"`
	Locations          any    `json:"locations"`
	MeasurementOptions any    `json:"measurementOptions,omitempty"`
}

type created struct {
	ID          string `json:"id"`
	ProbesCount int    `json:"probesCount"`
}

type probeInfo struct {
	City    string   `json:"city"`
	ASN     uint32   `json:"asn"`
	Network string   `json:"network"`
	Tags    []string `json:"tags"`
}

type listedProbe struct {
	Location struct {
		Country string `json:"country"`
		City    string `json:"city"`
		ASN     uint32 `json:"asn"`
	} `json:"location"`
	Tags []string `json:"tags"`
}

type testResult struct {
	Status        string `json:"status"`
	FailureSource string `json:"failureSource"`
	Stats         struct {
		Avg *float64 `json:"avg"`
		Rcv int      `json:"rcv"`
	} `json:"stats"`
}

type resultItem struct {
	Probe  probeInfo  `json:"probe"`
	Result testResult `json:"result"`
}

type measurement struct {
	ID          string       `json:"id"`
	Status      string       `json:"status"`
	ProbesCount int          `json:"probesCount"`
	Results     []resultItem `json:"results"`
}

func (c *client) create(ctx context.Context, r measurementRequest) (created, error) {
	var out created
	_, err := c.do(ctx, http.MethodPost, "/v1/measurements", r, &out)
	if err == nil && out.ID == "" {
		err = errors.New("globalping: an answer without a measurement id")
	}
	return out, err
}

func (c *client) get(ctx context.Context, id string) (measurement, error) {
	var out measurement
	_, err := c.do(ctx, http.MethodGet, "/v1/measurements/"+id, nil, &out)
	return out, err
}

func (c *client) probes(ctx context.Context) ([]listedProbe, error) {
	var out []listedProbe
	_, err := c.doLimit(ctx, http.MethodGet, "/v1/probes", nil, &out, maxProbesBody)
	return out, err
}

type limits struct {
	RateLimit struct {
		Measurements struct {
			Create struct {
				Limit     uint32 `json:"limit"`
				Remaining uint32 `json:"remaining"`
				Reset     uint32 `json:"reset"`
			} `json:"create"`
		} `json:"measurements"`
	} `json:"rateLimit"`
	Credits struct {
		Remaining uint32 `json:"remaining"`
	} `json:"credits"`
}

func (c *client) limits(ctx context.Context) (limits, error) {
	var out limits
	_, err := c.do(ctx, http.MethodGet, "/v1/limits", nil, &out)
	return out, err
}
