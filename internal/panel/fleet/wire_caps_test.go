package fleet

import (
	"context"
	"slices"
	"testing"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// The capability strings the panel asks of an agent are on the wire: they meet the agent's Hello only in these strings, and
// an agent in the field keeps the value it was built with. Every value here is a literal on purpose.
func TestWireCapabilitiesThePanelRequires(t *testing.T) {
	t.Run("requiredCap", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			spec plugin.InboundSpec
			want string
		}{
			{"awg", plugin.InboundSpec{Protocol: "awg"}, "awg/1"},
			{"any tunnel", plugin.InboundSpec{Tunnel: plugin.Tunnel{MTU: 1280}}, "awg/1"},
			{"mtproto", plugin.InboundSpec{Protocol: "mtproto"}, "mtproto/1"},
			{"trusttunnel", plugin.InboundSpec{Protocol: "trusttunnel"}, "trusttunnel/1"},
			{"trusttunnel h3", plugin.InboundSpec{Protocol: "trusttunnel", Settings: []byte(`{"h3":true}`)}, "trusttunnel-h3/1"},
			{"vless", plugin.InboundSpec{Protocol: "vless"}, "vless/1"},
			{"vless, another site", plugin.InboundSpec{Protocol: "vless", Settings: []byte(`{"reality":{"target":"www.example.org:443"}}`)}, "vless/1"},
			{"vless, own site", plugin.InboundSpec{Protocol: "vless", Settings: []byte(`{"reality":{"target":"self"}}`)}, "vless-self/1"},
			{"vless, a site of the node", plugin.InboundSpec{Protocol: "vless", TLS: plugin.TLS{ServerName: "shop.example.com"}, Settings: []byte(`{"reality":{"target":"shop.example.com:443"}}`)}, "vless-target/1"},
			{"vless tls", plugin.InboundSpec{Protocol: "vless", Settings: []byte(`{"security":"tls"}`)}, "vless-tls/1"},
			{"hysteria2", plugin.InboundSpec{Protocol: "hysteria2"}, ""},
		} {
			if got := requiredCap(tc.spec); got != tc.want {
				t.Errorf("%s: requiredCap = %q, want %q", tc.name, got, tc.want)
			}
		}
	})

	t.Run("requestCapability", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			frame    *agentv1.ConnectResponse
			linkOnly bool
			want     string
			message  string
		}{
			{"doctor", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RunDoctor{RunDoctor: &agentv1.RunDoctor{}}}, false, "doctor/1", "agent too old"},
			{"fix", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_ApplyFix{ApplyFix: &agentv1.ApplyFix{}}}, false, "doctor/1", "agent too old"},
			{"update", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateAgent{UpdateAgent: &agentv1.UpdateAgent{}}}, false, "update/1", "node cannot update itself"},
			{"update, link-only", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateAgent{UpdateAgent: &agentv1.UpdateAgent{}}}, true, "update-link/1", "node cannot update itself"},
			{"rollback", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RollbackAgent{RollbackAgent: &agentv1.RollbackAgent{}}}, false, "update/1", "node cannot update itself"},
			{"bandwidth", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_MeasureBandwidth{MeasureBandwidth: &agentv1.MeasureBandwidth{}}}, false, "bandwidth/1", "agent too old"},
			{"awg prepare", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_PrepareAwgKernel{PrepareAwgKernel: &agentv1.PrepareAwgKernel{}}}, false, "awg-prepare/1", "agent too old"},
			{"os update", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateOs{UpdateOs: &agentv1.UpdateOs{}}}, false, "os-update/1", "agent too old"},
			{"udp count", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UdpCount{UdpCount: &agentv1.UdpCount{}}}, false, "udpcheck/1", "agent too old"},
			{"udp send", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UdpSend{UdpSend: &agentv1.UdpSend{}}}, false, "udpcheck/1", "agent too old"},
			{"http mark", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_HttpMark{HttpMark: &agentv1.HttpMark{}}}, false, "tcpcheck/1", "agent too old"},
			{"tcp probe", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_TcpProbe{TcpProbe: &agentv1.TcpProbe{}}}, false, "tcpcheck/1", "agent too old"},
			{"path trace", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_PathTrace{PathTrace: &agentv1.PathTrace{}}}, false, "probe/1", "agent too old"},
			{"speed", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_SpeedTest{SpeedTest: &agentv1.UdpSend{}}}, false, "speed/1", "agent too old"},
			{"speed, the channel", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_SpeedTest{SpeedTest: &agentv1.UdpSend{Size: 4}}}, false, "speed-channel/1", "agent too old"},
			{"debug", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_DebugCapture{DebugCapture: &agentv1.DebugCapture{}}}, false, "debug/1", "agent too old"},
			{"debug, the kernel", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_DebugCapture{DebugCapture: &agentv1.DebugCapture{Sources: []string{"kernel"}}}}, false, "debug/2",
				"this node's agent is too old for the kernel source: update the node"},
			{"country probe", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_CountryProbe{CountryProbe: &agentv1.CountryProbe{}}}, false, "country-probe/1", "agent too old"},
			{"decoy", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_SetDecoy{SetDecoy: &agentv1.SetDecoy{}}}, false, "decoy/1", "agent too old"},
			{"warp reach", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_WarpReach{WarpReach: &agentv1.WarpReach{}}}, false, "warp-reach/1", "agent too old"},
			{"sni scan", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_SniScan{SniScan: &agentv1.SniScan{}}}, false, "sni-scan/1", "agent too old"},
			{"ping needs none", &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_Ping{Ping: &agentv1.Ping{}}}, false, "", ""},
		} {
			if got, message := requestCapability(tc.frame, tc.linkOnly); got != tc.want || message != tc.message {
				t.Errorf("%s: requestCapability = %q (%q), want %q (%q)", tc.name, got, message, tc.want, tc.message)
			}
		}
	})

	t.Run("sources and the unit refresh of a command", func(t *testing.T) {
		e := newEnv(t)
		update := func() *agentv1.ConnectResponse {
			return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateAgent{UpdateAgent: &agentv1.UpdateAgent{Sources: []string{"https://example.com/a"}}}}
		}
		fix := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_ApplyFix{ApplyFix: &agentv1.ApplyFix{FixId: "refresh_unit"}}}
		ctx := context.Background()
		live := func(caps ...string) *store.NodeLiveRow { return &store.NodeLiveRow{Connected: true, AgentCaps: caps} }

		old, current := update(), update()
		if _, err := e.f.readyForRequest(ctx, "nod_1", old, live("update/1")); err != nil || len(old.GetUpdateAgent().Sources) != 0 {
			t.Errorf("without update-sources/1 the sources stay: %v %v", err, old.GetUpdateAgent().Sources)
		}
		if _, err := e.f.readyForRequest(ctx, "nod_1", current, live("update/1", "update-sources/1")); err != nil || len(current.GetUpdateAgent().Sources) != 1 {
			t.Errorf("with update-sources/1 the sources go: %v %v", err, current.GetUpdateAgent().Sources)
		}
		if _, err := e.f.readyForRequest(ctx, "nod_1", fix, live("doctor/1")); err == nil {
			t.Error("a refresh_unit fix went to an agent without unit-refresh/1")
		}
		if _, err := e.f.readyForRequest(ctx, "nod_1", fix, live("doctor/1", "unit-refresh/1")); err != nil {
			t.Errorf("a refresh_unit fix to an agent with unit-refresh/1: %v", err)
		}
	})

	t.Run("mtproto is derived from the engines", func(t *testing.T) {
		h := &agentv1.Hello{Capabilities: []string{"doctor/1"}, Engines: []*agentv1.EngineInfo{{Protocol: "mtproto"}}}
		if got := capabilities(h); !slices.Equal(got, []string{"doctor/1", "mtproto/1"}) {
			t.Errorf("capabilities = %v", got)
		}
	})

	t.Run("withheld", func(t *testing.T) {
		tcp := func(id, protocol string, port uint16, name, settings string) statehash.Inbound {
			return statehash.Inbound{Spec: plugin.InboundSpec{ID: id, Protocol: protocol, Enabled: true, Listen: plugin.Listen{Network: "tcp", Port: port},
				TLS: plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: name}, Settings: []byte(settings)}}
		}
		awgIn := statehash.Inbound{Spec: plugin.InboundSpec{ID: "inb_awg", Protocol: "awg", Enabled: true, Listen: plugin.Listen{Network: "udp", Port: 51820}, Tunnel: plugin.Tunnel{MTU: 1280}}}
		share := []statehash.Inbound{tcp("inb_a", "trusttunnel", 443, "a.example.com", `{}`), tcp("inb_b", "trusttunnel", 443, "b.example.com", `{}`)}
		node := store.NodeRow{ID: "nod_1", State: "active"}
		build := func(in []statehash.Inbound, caps ...string) *nodeState {
			e := newEnv(t)
			e.f.cfg.Desired = func(context.Context, string) ([]statehash.Inbound, error) { return in, nil }
			st, err := e.f.buildState(context.Background(), node, caps)
			if err != nil {
				t.Fatal(err)
			}
			return st
		}
		if st := build([]statehash.Inbound{awgIn}, "doctor/1"); !slices.Equal(st.withheld, []string{"inb_awg"}) {
			t.Errorf("an awg inbound without awg/1: withheld %v", st.withheld)
		}
		if st := build([]statehash.Inbound{awgIn}, "awg/1"); len(st.withheld) != 0 || len(st.in) != 1 {
			t.Errorf("an awg inbound with awg/1: withheld %v held %v", st.withheld, st.ids())
		}
		if st := build(share, "trusttunnel/1"); !slices.Equal(st.withheldShare, []string{"inb_b"}) {
			t.Errorf("two services of a port without tcp-share/1: shared-withheld %v", st.withheldShare)
		}
		if st := build(share, "trusttunnel/1", "tcp-share/1"); len(st.withheld) != 0 || len(st.in) != 2 {
			t.Errorf("two services of a port with tcp-share/1: withheld %v held %v", st.withheld, st.ids())
		}
		person := tcp("inb_p", "vless", 443, "", `{"security":"reality","reality":{"target":"www.example.org:443","server_names":["www.example.org"]}}`)
		person.Creds = []plugin.UserCred{{CredID: "crd_1", RateLimitBps: 8000, UserRateLimitBps: 3000}}
		if st := build([]statehash.Inbound{person}, "vless/1"); st.in["inb_p"].creds[0].RateLimitBps != 3000 || st.in["inb_p"].creds[0].UserRateLimitBps != 0 {
			t.Errorf("without user-rate-limit/1 the limit is folded: %+v", st.in["inb_p"].creds[0])
		}
		if st := build([]statehash.Inbound{person}, "vless/1", "user-rate-limit/1"); st.in["inb_p"].creds[0].RateLimitBps != 8000 || st.in["inb_p"].creds[0].UserRateLimitBps != 3000 {
			t.Errorf("with user-rate-limit/1 the limits stay: %+v", st.in["inb_p"].creds[0])
		}
	})
}
