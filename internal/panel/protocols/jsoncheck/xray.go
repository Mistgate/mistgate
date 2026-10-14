//go:build !js

// Package jsoncheck lets the tests ask the real cores whether a client config they render is valid: Xray in
// process (xray-core's own config builder) and sing-box through its binary. Only tests import it.
package jsoncheck

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/transport/internet"
	"google.golang.org/protobuf/proto"
)

// XrayConfig wraps one outbound fragment (no tag) into the smallest config v2rayNG-style clients accept: a loopback
// socks inbound, the outbound tagged "proxy" and a routing section.
func XrayConfig(t testing.TB, outbound []byte) []byte {
	t.Helper()
	var o map[string]json.RawMessage
	if err := json.Unmarshal(outbound, &o); err != nil {
		t.Fatalf("outbound is not a JSON object: %v\n%s", err, outbound)
	}
	o["tag"] = json.RawMessage(`"proxy"`)
	cfg, err := json.Marshal(map[string]any{
		"inbounds": []any{map[string]any{
			"tag": "socks", "protocol": "socks", "listen": "127.0.0.1", "port": 10808,
			"settings": map[string]any{"auth": "noauth", "udp": true},
		}},
		"outbounds": []any{o},
		"routing":   map[string]any{"rules": []any{map[string]any{"network": "tcp,udp", "outboundTag": "proxy"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// XrayBuild unmarshals a full config into xray-core's conf.Config and builds it, as `xray run` does before it starts
// anything. The core ignores unknown keys, so the outbound objects are also decoded with DisallowUnknownFields
// (stream settings, and the protocol settings object) to catch a misspelt key.
func XrayBuild(t testing.TB, config []byte) {
	t.Helper()
	var c conf.Config
	if err := json.Unmarshal(config, &c); err != nil {
		t.Fatalf("xray conf.Config: %v\n%s", err, config)
	}
	if _, err := c.Build(); err != nil {
		t.Fatalf("xray conf.Config.Build: %v\n%s", err, config)
	}
	var strict struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal(config, &strict); err != nil {
		t.Fatal(err)
	}
	for _, raw := range strict.Outbounds {
		var o conf.OutboundDetourConfig
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&o); err != nil {
			t.Fatalf("xray outbound has a key the core does not know: %v\n%s", err, raw)
		}
		if o.Settings == nil {
			continue
		}
		var target any
		switch o.Protocol {
		case "vless":
			target = &conf.VLessOutboundConfig{}
		case "hysteria":
			target = &conf.HysteriaClientConfig{}
		default:
			continue
		}
		dec = json.NewDecoder(bytes.NewReader(*o.Settings))
		dec.DisallowUnknownFields()
		if err := dec.Decode(target); err != nil {
			t.Fatalf("xray %s settings have a key the core does not know: %v\n%s", o.Protocol, err, *o.Settings)
		}
	}
}

// XrayStreams builds a full config and returns the stream settings of its outbounds, so a test can look at what
// reached the core (a key the core ignores shows up here as a zero value).
func XrayStreams(t testing.TB, config []byte) []*internet.StreamConfig {
	t.Helper()
	var c conf.Config
	if err := json.Unmarshal(config, &c); err != nil {
		t.Fatalf("xray conf.Config: %v", err)
	}
	built, err := c.Build()
	if err != nil {
		t.Fatalf("xray conf.Config.Build: %v", err)
	}
	var out []*internet.StreamConfig
	for _, o := range built.Outbound {
		msg, err := o.SenderSettings.GetInstance()
		if err != nil {
			t.Fatal(err)
		}
		sender, ok := msg.(*proxyman.SenderConfig)
		if !ok {
			t.Fatalf("sender settings are %T", msg)
		}
		out = append(out, sender.StreamSettings)
	}
	return out
}

// XrayTransport returns the typed settings of the named transport ("splithttp", "websocket", ...) or fails.
func XrayTransport(t testing.TB, s *internet.StreamConfig, name string) proto.Message {
	t.Helper()
	for _, tc := range s.TransportSettings {
		if tc.ProtocolName == name {
			msg, err := tc.Settings.GetInstance()
			if err != nil {
				t.Fatal(err)
			}
			return msg
		}
	}
	t.Fatalf("no %s transport in %v", name, s.TransportSettings)
	return nil
}

// XraySecurity returns the typed security settings (tls or reality config) or fails.
func XraySecurity(t testing.TB, s *internet.StreamConfig) proto.Message {
	t.Helper()
	if len(s.SecuritySettings) != 1 {
		t.Fatalf("security settings = %v", s.SecuritySettings)
	}
	msg, err := s.SecuritySettings[0].GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

// XrayBuildArray checks a whole Xray JSON subscription: a non-empty array in which every element is a complete config
// the core builds. `remarks` is the app's own key (the core does not know it), so it is dropped; every other key of the
// config, and every key of the outbounds, has to be one the core knows. It returns the elements.
func XrayBuildArray(t testing.TB, doc []byte) []map[string]json.RawMessage {
	t.Helper()
	var elems []map[string]json.RawMessage
	if err := json.Unmarshal(doc, &elems); err != nil || len(elems) == 0 {
		t.Fatalf("not a non-empty array of configs: %v\n%s", err, doc)
	}
	for i, e := range elems {
		if _, ok := e["remarks"]; !ok {
			t.Errorf("config %d has no remarks", i)
		}
		for _, key := range []string{"inbounds", "outbounds", "routing"} { // v2rayNG takes a config only with all three
			if _, ok := e[key]; !ok {
				t.Errorf("config %d has no %s", i, key)
			}
		}
		rest := make(map[string]json.RawMessage, len(e))
		for k, v := range e {
			if k != "remarks" {
				rest[k] = v
			}
		}
		raw, err := json.Marshal(rest)
		if err != nil {
			t.Fatal(err)
		}
		var strict conf.Config
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&strict); err != nil {
			t.Fatalf("config %d has a key the core does not know: %v\n%s", i, err, raw)
		}
		XrayBuild(t, raw)
	}
	return elems
}
