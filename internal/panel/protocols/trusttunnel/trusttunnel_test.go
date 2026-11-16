package trusttunnel

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

const (
	testUsername = "ualice2345672345"
	testPassword = "aB234567cD234567eF234567gH234567"
	testPin      = "abababababababababababababababababababababababababababababababab"
)

func testSettings(t *testing.T, s Settings) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSpace(b)
}

func TestProtocolBasics(t *testing.T) {
	p := New()
	if p.ID() != "trusttunnel" || p.DisplayName() != "TrustTunnel" || !p.Experimental() || !protocols.IsExperimental(p) {
		t.Fatalf("protocol metadata: id=%q name=%q experimental=%v", p.ID(), p.DisplayName(), p.Experimental())
	}
	defaults, err := p.DefaultSettings()
	if err != nil {
		t.Fatal(err)
	}
	var s Settings
	if err := json.Unmarshal(defaults, &s); err != nil {
		t.Fatal(err)
	}
	if s != (Settings{Port: 443, TLSMode: "acme_domain", Egress: "direct"}) {
		t.Fatalf("defaults = %+v", s)
	}
	if got := p.Summary(defaults); !strings.Contains(got, "TCP 443") || !strings.Contains(got, "HTTP/2") || !strings.Contains(got, "Let's Encrypt") {
		t.Fatalf("summary = %q", got)
	}
	s.H3 = true
	if got := p.Summary(testSettings(t, s)); !strings.Contains(got, "HTTP/3") {
		t.Fatalf("HTTP/3 summary = %q", got)
	}
}

func TestValidate(t *testing.T) {
	base := Settings{Port: 443, TLSMode: "acme_domain", Egress: "direct"}
	for _, tc := range []struct {
		name string
		mut  func(*Settings)
		ptr  string
	}{
		{name: "valid", mut: func(*Settings) {}},
		{name: "bad port", mut: func(s *Settings) { s.Port = 0 }, ptr: "/port"},
		{name: "bad TLS mode", mut: func(s *Settings) { s.TLSMode = "unknown" }, ptr: "/tls_mode"},
		{name: "bad ACME IP SNI", mut: func(s *Settings) { s.SNI = "192.0.2.4" }, ptr: "/sni"},
		{name: "self-signed IP SNI", mut: func(s *Settings) { s.TLSMode, s.SNI = "self_signed", "192.0.2.4" }},
		{name: "bad hostname", mut: func(s *Settings) { s.SNI = "bad host" }, ptr: "/sni"},
		{name: "bad egress", mut: func(s *Settings) { s.Egress = "unknown" }, ptr: "/egress"},
		{name: "long SNI", mut: func(s *Settings) { s.SNI = strings.Repeat("a", 254) }, ptr: "/sni"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.mut(&s)
			errs := New().Validate(testSettings(t, s))
			if tc.ptr == "" && len(errs) != 0 {
				t.Fatalf("Validate() = %+v, want valid", errs)
			}
			if tc.ptr != "" {
				for _, got := range errs {
					if got.Pointer == tc.ptr {
						return
					}
				}
				t.Fatalf("Validate() = %+v, want an error at %s", errs, tc.ptr)
			}
		})
	}
	if got := New().Validate(json.RawMessage(`{"port":"443"}`)); len(got) == 0 || got[0].Pointer != "/port" {
		t.Fatalf("invalid port type = %+v", got)
	}
}

func TestBuildInboundNeedsDomainAndUsesSNIOverride(t *testing.T) {
	p := New()
	base := protocols.InboundInput{
		Profile: protocols.ProfileView{ID: "prf_tt", Version: 1, Settings: testSettings(t, Settings{Port: 443, TLSMode: "acme_domain", Egress: "direct"})},
		Node:    protocols.NodeView{ID: "nod_de1", Name: "de1", Address: "192.0.2.4"},
	}
	_, err := p.BuildInbound(base)
	var needsDomain *protocols.NeedsDomainError
	if !errors.As(err, &needsDomain) || needsDomain.Address != "192.0.2.4" {
		t.Fatalf("BuildInbound without a domain = %v, want acme NeedsDomainError", err)
	}

	s := Settings{Port: 443, TLSMode: "acme_domain", Egress: "direct", SNI: "de1.example.com"}
	base.Profile.Settings = testSettings(t, s)
	got, err := p.BuildInbound(base)
	if err != nil {
		t.Fatal(err)
	}
	if got.TLS.ServerName != s.SNI || got.Listen != (plugin.Listen{Network: "tcp", Port: 443}) || string(got.Settings) != string(golden(t, "node_settings_h2.json")) {
		t.Fatalf("BuildInbound = %+v settings=%s", got, got.Settings)
	}
	base.TLSServerNameOverride = "edge.example.com"
	got, err = p.BuildInbound(base)
	if err != nil || got.TLS.ServerName != base.TLSServerNameOverride {
		t.Fatalf("BuildInbound with SNI override = %+v, %v", got, err)
	}
}

func TestIssueCredentialFormatAndNodeDataGolden(t *testing.T) {
	issued, err := New().IssueCredential(protocols.IssueInput{UserID: "usr_alice", DeviceID: "dev_1"})
	if err != nil {
		t.Fatal(err)
	}
	username, password, ok := strings.Cut(issued.Secret, ":")
	if !ok || !regexp.MustCompile(`^u[a-z2-7]{15}$`).MatchString(username) || !regexp.MustCompile(`^[a-zA-Z0-9]{32}$`).MatchString(password) {
		t.Fatalf("credential format invalid: %q", issued.Secret)
	}
	var data NodeCredential
	if err := json.Unmarshal(issued.NodeData, &data); err != nil {
		t.Fatal(err)
	}
	if data.User != username || strings.Contains(string(issued.NodeData), password) || bytes.Contains(issued.NodeData, []byte("password")) {
		t.Fatalf("NodeData contains an unexpected secret: %s", issued.NodeData)
	}
	if got, want := string(issued.NodeData), string(golden(t, "node_credential.json")); got == want {
		t.Fatal("random credential unexpectedly matched the deterministic golden")
	}
	fixed, err := encodeNodeCredential(testUsername, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fixed, golden(t, "node_credential.json")) {
		t.Fatalf("NodeData JSON = %s, want %s", fixed, golden(t, "node_credential.json"))
	}
}

func TestNodeSettingsGoldens(t *testing.T) {
	// These exact bytes are the panel contract that internal/node/trusttunnel must accept.
	for _, tc := range []struct {
		name string
		h3   bool
		want string
	}{
		{name: "h2", want: "node_settings_h2.json"},
		{name: "h3", h3: true, want: "node_settings_h3.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(NodeSettings{H3: tc.h3})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, golden(t, tc.want)) {
				t.Fatalf("Settings JSON = %s, want %s", got, golden(t, tc.want))
			}
		})
	}
}

func TestMistgatePathGoldensAndCapabilityGate(t *testing.T) {
	base := protocols.RenderInput{
		Format: plugin.FormatMistgate, Capabilities: []string{"trusttunnel=1"},
		Settings: testSettings(t, Settings{Port: 443, H3: true, TLSMode: "acme_domain", Egress: "direct"}),
		Inbound:  protocols.InboundView{Node: protocols.NodeView{Address: "192.0.2.10"}, Port: 443, TLSServerName: "de1.example.com", TLSMode: plugin.TLSAcmeDomain},
		Secret:   testUsername + ":" + testPassword,
	}
	checks := []struct {
		name   string
		input  protocols.RenderInput
		golden string
	}{
		{name: "direct auto", input: base, golden: "mistgate_direct_auto.json"},
		{name: "relay twin h2", input: func() protocols.RenderInput {
			in := base
			in.Inbound.Node.Address = "192.0.2.22"
			in.Inbound.TCPOnly = true
			return in
		}(), golden: "mistgate_relay_h2.json"},
		{name: "self-signed pin", input: func() protocols.RenderInput {
			in := base
			in.Inbound.TLSMode = plugin.TLSSelfSigned
			in.Inbound.CertPinSHA256 = testPin
			return in
		}(), golden: "mistgate_pin.json"},
		{name: "masked secrets", input: func() protocols.RenderInput { in := base; in.MaskSecrets = true; return in }(), golden: "mistgate_masked.json"},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			frag, ok := New().Render(tc.input)
			if !ok {
				t.Fatal("Render() omitted a Mistgate path")
			}
			if !bytes.Equal(frag.Data, golden(t, tc.golden)) {
				t.Fatalf("path JSON = %s, want %s", frag.Data, golden(t, tc.golden))
			}
			if strings.Contains(string(frag.Data), "tt://") {
				t.Fatal("TrustTunnel URI links belong to T6")
			}
		})
	}
	noCaps := base
	noCaps.Capabilities = nil
	if _, ok := New().Render(noCaps); ok {
		t.Fatal("Render() emitted a path without trusttunnel=1")
	}
	missingSNI := base
	missingSNI.Inbound.TLSServerName = ""
	if _, ok := New().Render(missingSNI); ok {
		t.Fatal("Render() emitted a path without the required SNI")
	}
	otherFormat := base
	otherFormat.Format = plugin.FormatURIList
	if _, ok := New().Render(otherFormat); ok {
		t.Fatal("Render() emitted a path for another client format")
	}
}
