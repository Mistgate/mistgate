// Package warp is the panel side of the WARP egress: one
// Cloudflare WARP account per node, either registered by the panel at the owner's click (an anonymous device
// registration, the same call the 1.1.1.1 app makes, with the Cloudflare terms accepted on the owner's behalf)
// or imported from wgcf files, stored encrypted, turned into the node's WarpSpec, and shown (without secrets) in
// the admin WarpService.
//
// The panel talks to Cloudflare only when the owner clicks: register, refresh, delete, or (off by default) the
// auto_reregister setting. Nothing here runs by itself. Secrets (private key, access token, device id, license)
// never reach a log line, an audit row or a response.
package warp

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	// APIURL is the Cloudflare client API. Tests point the Client elsewhere.
	APIURL = "https://api.cloudflareclient.com"
	// TOSURL is the terms the UI links next to the register button and that the owner accepts for the account.
	TOSURL = "https://www.cloudflare.com/application/terms/"

	// SettingKey is the setting that holds the owner's Params overrides (JSON).
	SettingKey = "warp.api"
)

// Params are the constants the registration request is built from. They changed twice in 2026,
// so they are data: the built-in values plus an owner override in the setting warp.api. An empty string
// field falls back to the built-in value.
type Params struct {
	APIVersion      string `json:"api_version"`       // the path segment of the URL, "v0a5641"
	UserAgent       string `json:"user_agent"`        // what the Android app sends
	CFClientVersion string `json:"cf_client_version"` // CF-Client-Version header
	TLSSpecID       string `json:"tls_spec_id"`       // which uTLS ClientHello to imitate, see tlsSpecs
	AutoReregister  bool   `json:"auto_reregister"`   // off by default; see AutoReregister in service.go
}

// Defaults are the built-in values: wgcf v2.3.0 (2026-09-18), the release that fixed the HTTP 429 on /reg.
func Defaults() Params {
	return Params{
		APIVersion:      "v0a5641",
		UserAgent:       "1.1.1.1/6.38.9-5641 (Android 16.0.0)",
		CFClientVersion: "a-6.38.9-5641",
		TLSSpecID:       tlsSpecWgcf230,
	}
}

// fill replaces empty fields by the built-in values.
func (p Params) fill() Params {
	d := Defaults()
	if p.APIVersion == "" {
		p.APIVersion = d.APIVersion
	}
	if p.UserAgent == "" {
		p.UserAgent = d.UserAgent
	}
	if p.CFClientVersion == "" {
		p.CFClientVersion = d.CFClientVersion
	}
	if p.TLSSpecID == "" {
		p.TLSSpecID = d.TLSSpecID
	}
	return p
}

// Customised is true when a request constant differs from the built-in value (auto_reregister is a separate switch).
func (p Params) Customised() bool {
	d := Defaults()
	p = p.fill()
	return p.APIVersion != d.APIVersion || p.UserAgent != d.UserAgent || p.CFClientVersion != d.CFClientVersion || p.TLSSpecID != d.TLSSpecID
}

var apiVersionRE = regexp.MustCompile(`^v[0-9a-z]{2,20}$`)

// Validate refuses what cannot be a header value or a path segment, and an unknown ClientHello id.
func (p Params) Validate() error {
	p = p.fill()
	if !apiVersionRE.MatchString(p.APIVersion) {
		return fmt.Errorf("api_version: must look like v0a5641")
	}
	for _, f := range []struct{ name, v string }{{"user_agent", p.UserAgent}, {"cf_client_version", p.CFClientVersion}} {
		if len(f.v) > 200 || strings.ContainsFunc(f.v, func(r rune) bool { return r < 0x20 || r > 0x7e }) {
			return fmt.Errorf("%s: printable ASCII, at most 200 characters", f.name)
		}
	}
	if _, ok := tlsSpecs[p.TLSSpecID]; !ok {
		return fmt.Errorf("tls_spec_id: unknown (known: %s)", strings.Join(tlsSpecIDs(), ", "))
	}
	return nil
}

// parseParams reads the stored setting; garbage or "" means "no override".
func parseParams(raw string) Params {
	var p Params
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &p)
	}
	return p.fill()
}
