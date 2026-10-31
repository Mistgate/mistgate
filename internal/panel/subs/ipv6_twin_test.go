package subs_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// The IPv6 entry in every format (IPv6 plan, step 7), next to a relay: de1 has the entry 2001:db8::5 and its server is also passed
// on by fi1's relay, so its person gets the server, its IPv6 twin and then the relay's twin, never a relay twin for IPv6. The
// files in testdata are what each format answers; the credential of the device is the one random value and is masked.

func TestIPv6TwinAndRelayTwinInEveryFormat(t *testing.T) {
	m := newRelayRig(t)
	if _, err := m.st.W.Exec(`UPDATE node SET ipv6_entry = '2001:db8::5' WHERE id = 'nod_1'`); err != nil {
		t.Fatal(err)
	}
	tok := m.token("alice")

	list := fetch(m.h, "/"+tok, happUA)
	body := decode(t, list.Body.String())
	secret := secretOf.FindStringSubmatch(body)
	if list.Code != 200 || secret == nil {
		t.Fatalf("list: %d %q", list.Code, body)
	}
	masked := func(s string) string { return strings.ReplaceAll(s, secret[1], "<secret>") }
	lines := strings.Split(body, "\n")
	if len(lines) != 3 {
		t.Fatalf("the list has %d lines, want the server, its IPv6 twin and the relay's:\n%s", len(lines), body)
	}
	var names []string
	for _, l := range lines {
		_, frag, _ := strings.Cut(l, "#")
		n, err := url.PathUnescape(frag)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	// the names Happ shows (30 units at most)
	if names[0] != "\U0001F1E9\U0001F1EA DE · Hysteria2 premium" || names[1] != "\U0001F1E9\U0001F1EA DE · HY2 premium · IPv6" || names[2] != "\U0001F1E9\U0001F1EA DE · HY2 premium · via FI" {
		t.Errorf("names: %q", names)
	}
	// the IPv6 twin is the server's line with the node's IPv6 address in brackets and the same port; the relay's has the entry's
	main, _, _ := strings.Cut(lines[0], "#")
	v6, _, _ := strings.Cut(lines[1], "#")
	relay, _, _ := strings.Cut(lines[2], "#")
	if want := strings.Replace(main, "@de1.example.com:4443/", "@[2001:db8::5]:4443/", 1); v6 != want || v6 == main {
		t.Errorf("IPv6 twin\n%s\nwant\n%s", v6, want)
	}
	if want := strings.Replace(main, "@de1.example.com:4443/", "@fi1.example.com:8443/", 1); relay != want || strings.Contains(relay, "2001:db8") {
		t.Errorf("relay twin\n%s\nwant\n%s", relay, want)
	}
	goldenFile(t, "ipv6_list.golden", masked(strings.Join(lines, "\n"))+"\n")

	for _, c := range []struct{ file, path, ua string }{
		{"ipv6_mihomo.yaml", "/" + tok, mihomoUA},
		{"ipv6_xray.json", "/" + tok + "?format=xray-json", happUA},
		{"ipv6_singbox.json", "/" + tok, sfaUA},
	} {
		rec := fetch(m.h, c.path, c.ua)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.file, rec.Code, rec.Body.String())
		}
		got := masked(rec.Body.String())
		if strings.Contains(got, "[2001:db8::5]") {
			t.Errorf("%s: a bracketed address outside a URI", c.file)
		}
		if !strings.Contains(got, "2001:db8::5") {
			t.Errorf("%s: no IPv6 twin", c.file)
		}
		goldenFile(t, c.file, got)
	}
}

func TestMihomoAmneziaWGProxyGetsIPv6Twin(t *testing.T) {
	m := newM3Rig(t)
	if _, err := m.st.W.Exec(`UPDATE node SET ipv6_entry = '2001:db8::5' WHERE id = 'nod_1'`); err != nil {
		t.Fatal(err)
	}
	h, _ := m.handler(nil)
	_, tok := m.newUser("alice", nil)
	rec := fetch(h, "/"+tok, mihomoUA)
	if rec.Code != 200 {
		t.Fatalf("Mihomo: %d %s", rec.Code, rec.Body.String())
	}
	p := parseYAML(t, rec.Body.Bytes())
	var awg []map[string]any
	for _, proxy := range p.Proxies {
		if proxy["type"] == "wireguard" {
			awg = append(awg, proxy)
		}
	}
	if len(awg) != 2 || awg[0]["server"] != "de1.example.com" || awg[1]["server"] != "2001:db8::5" || !strings.Contains(awg[1]["name"].(string), "IPv6") {
		t.Fatalf("AmneziaWG proxies = %+v, want the main server and its IPv6 twin", awg)
	}
	if awg[0]["private-key"] != awg[1]["private-key"] || awg[0]["ip"] != awg[1]["ip"] {
		t.Errorf("the IPv6 twin changed the device key or tunnel address: %v / %v", awg[0], awg[1])
	}
	got := maskMihomoGenerated(maskMihomoCredentials(rec.Body.String(), p.Proxies))
	goldenFile(t, "ipv6_awg_mihomo.yaml", got)
}

func maskMihomoCredentials(body string, values any) string {
	switch v := values.(type) {
	case map[string]any:
		for key, value := range v {
			if strings.Contains(key, "password") || key == "ip" || strings.Contains(key, "key") {
				if secret, ok := value.(string); ok && secret != "" {
					body = strings.ReplaceAll(body, secret, "<secret>")
					continue
				}
			}
			body = maskMihomoCredentials(body, value)
		}
	case []any:
		for _, value := range v {
			body = maskMihomoCredentials(body, value)
		}
	case []map[string]any:
		for _, value := range v {
			body = maskMihomoCredentials(body, value)
		}
	}
	return body
}

func maskMihomoGenerated(body string) string {
	lines := strings.Split(body, "\n")
	inWireguard, inOptions := false, false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- name:") {
			inWireguard, inOptions = false, false
		}
		if inWireguard && (strings.HasPrefix(trimmed, "port:") || strings.HasPrefix(trimmed, "persistent-keepalive:")) {
			key, _, _ := strings.Cut(trimmed, ":")
			lines[i] = strings.Repeat(" ", len(line)-len(strings.TrimLeft(line, " "))) + key + ": 0"
		}
		if inWireguard && strings.HasPrefix(trimmed, "amnezia-wg-option:") {
			inOptions = true
			continue
		}
		if inOptions {
			indent := len(line) - len(strings.TrimLeft(line, " "))
			if indent <= 4 {
				inOptions = false
			} else if key, _, ok := strings.Cut(trimmed, ":"); ok {
				var value string
				switch key {
				case "jc", "jmin", "jmax", "s1", "s2", "s3", "s4":
					value = "0"
				case "i1", "i2", "i3", "i4", "i5":
					value = `"<generated>"`
				case "content-padding-addition", "rekey-after-time", "rekey-timeout", "reject-after-time", "keepalive-timeout", "max-handshake-attempts":
					value = `"<generated>"`
				}
				if value != "" {
					lines[i] = fmt.Sprintf("%s%s: %s", strings.Repeat(" ", indent), key, value)
				}
			}
		}
		if trimmed == "type: wireguard" {
			inWireguard = true
		}
	}
	return strings.Join(lines, "\n")
}

// Without the entry nothing changes for anybody: the golden of the relay's twin (relay_*.golden) is what answers.
func TestNoIPv6TwinWhileTheEntryIsOff(t *testing.T) {
	m := newRelayRig(t)
	body := decode(t, fetch(m.h, "/"+m.token("alice"), happUA).Body.String())
	if strings.Contains(body, "2001:db8") || strings.Contains(body, "IPv6") || len(strings.Split(body, "\n")) != 2 {
		t.Errorf("the list with the entry off:\n%s", body)
	}
}
