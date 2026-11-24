package fleet

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestPublicPortsOfSharedListenerAreAllProbed(t *testing.T) {
	own := vlessSpec("a", 19443, `{"security":"reality","reality":{"target":"self","server_names":["node.example.com"]}}`, plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "node.example.com"})
	own.Settings = json.RawMessage(`{"client_port":443}`)
	other := vlessSpec("b", 19443, realityOther, plugin.TLS{})
	other.Settings = json.RawMessage(`{"client_port":8443}`)
	got := tcpTargets([]tcpSpec{own, other}, nil)
	found := false
	for _, x := range got {
		if x.Port == 8443 {
			found = true
		}
	}
	if !found {
		t.Fatalf("public 8443 omitted because listener 19443 is shared; targets=%+v", got)
	}
}
func TestDirectRelayConnectsToPublicExitPort(t *testing.T) {
	x := newRelayEnv(t)
	now := time.Now()
	x.exec(`INSERT INTO profile (id,protocol,name,settings_json,version,created_at,updated_at) VALUES ('prf_public','vless','public','{"port":19443,"client_port":443,"network":"tcp"}',1,?,?)`, now.Unix(), now.Unix())
	x.exec(`INSERT INTO inbound (id,profile_id,node_id,spec_version,state,created_at,updated_at) VALUES ('inb_public','prf_public',?,1,'active',?,?)`, x.exit.nodeID, now.Unix(), now.Unix())
	if err := x.st.CreateRelay(x.ctx, store.Relay{ID: "rly_public", InboundID: "inb_public", ViaNodeID: x.entry.nodeID, Network: "tcp", Port: 2443}, now); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if err := x.st.UpdateRelay(x.ctx, "rly_public", store.RelayPatch{Enabled: &enabled}, now); err != nil {
		t.Fatal(err)
	}
	node, err := x.st.Node(x.ctx, x.entry.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := x.f.relayForwards(x.ctx, node, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ToPort != 443 {
		t.Fatalf("direct relay bypasses the public entry instead of dialing exit:443; forwards=%+v", got)
	}
}

func TestALPNThroughPublicPort(t *testing.T) {
	s := vlessSpec("public", 19443, `{"security":"reality","reality":{"target":"self","server_names":["node.example.com"]}}`, plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "node.example.com"})
	s.Settings = json.RawMessage(`{"client_port":443}`)
	for _, target := range tcpTargets([]tcpSpec{s}, nil) {
		if target.Port == 443 {
			if !target.ALPN {
				t.Fatalf("public 443 TLS-ALPN listener marked incapable: %+v", target)
			}
			return
		}
	}
	t.Fatal("public target missing")
}

func TestReverseBridgeRecognisesPublicOwnSite(t *testing.T) {
	for _, mapped := range []bool{false, true} {
		t.Run(map[bool]string{false: "native443", true: "forwarded443"}[mapped], func(t *testing.T) {
			x := newRelayEnv(t)
			x.entryTLSite()
			if mapped {
				x.exec(`UPDATE profile SET settings_json = '{"port":19443,"client_port":443,"security":"tls"}' WHERE id='prf_entry_tls'`)
			}
			entry, err := x.st.Node(x.ctx, x.entry.nodeID)
			if err != nil {
				t.Fatal(err)
			}
			target, ok := x.f.bridgeTLSName(x.ctx, entry)
			if !ok || target.Port != 443 {
				t.Fatalf("entry own site reachable on public443 rejected by bridge selection: target=%+v,ok=%v", target, ok)
			}
		})
	}
}

func TestReverseBridgeKeepsLocalTargetPort(t *testing.T) {
	x := newRelayEnv(t)
	x.givePath(8443)
	x.reverseRelay("rly_public", relayInbound, 7443, store.RelayModeReverse)
	x.exec(`UPDATE profile SET settings_json = '{"port":19443,"client_port":443,"security":"tls"}' WHERE id='prf_vless_tcp'`)
	connectCaps(x.exit, "exit", agentcap.RelayReverse, agentcap.VLESSTLS)
	connectCaps(x.entry, "entry", agentcap.RelayForward, agentcap.RelayReverse)
	exit, err := x.st.Node(x.ctx, x.exit.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	_, dials, err := x.f.bridgeDesired(x.ctx, exit, exit.AgentCaps)
	if err != nil || len(dials) != 1 || len(dials[0].Targets) != 1 || dials[0].Targets[0].Port != 19443 {
		t.Fatalf("bridge dial target: %+v, err %v", dials, err)
	}
	entry, err := x.st.Node(x.ctx, x.entry.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	forwards, err := x.f.relayForwards(x.ctx, entry, entry.AgentCaps)
	if err != nil || len(forwards) != 1 || forwards[0].ToPort != 443 {
		t.Fatalf("forward public target: %+v, err %v", forwards, err)
	}
}
