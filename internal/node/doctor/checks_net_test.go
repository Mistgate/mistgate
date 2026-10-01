package doctor

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/hostctl"
)

func TestResolverTargets(t *testing.T) {
	tests := []struct {
		s    Settings
		want string
	}{
		{Settings{}, "1.1.1.1,8.8.8.8"},
		{Settings{Country: "RU"}, "77.88.8.8,77.88.8.1"},
		{Settings{Country: "ru", Resolvers: []string{"9.9.9.9", "dns.example.com", " 10.0.0.1:5353 "}}, "9.9.9.9,10.0.0.1:5353"},
		{Settings{Resolvers: []string{"not a resolver"}}, "1.1.1.1,8.8.8.8"},
	}
	for _, tc := range tests {
		if got := strings.Join(ResolverTargets(tc.s), ","); got != tc.want {
			t.Errorf("%+v: got %s, want %s", tc.s, got, tc.want)
		}
	}
}

// lookups builds Lookup/LookupVia fakes from the sets of names that FAIL.
func lookups(hostFail, viaFail []string) func(*Env) {
	has := func(l []string, h string) bool {
		for _, x := range l {
			if x == h {
				return true
			}
		}
		return false
	}
	return func(e *Env) {
		e.Lookup = func(_ context.Context, h string) error {
			if has(hostFail, h) {
				return errors.New("SERVFAIL")
			}
			return nil
		}
		e.LookupVia = func(_ context.Context, _, h string) error {
			if has(viaFail, h) {
				return errors.New("SERVFAIL")
			}
			return nil
		}
	}
}

func TestResolver(t *testing.T) {
	f := newFake(t)
	ru := func(e *Env) { e.Settings = func() Settings { return Settings{Country: "RU"} } }

	r := run(t, f.doctor(lookups(nil, nil)), CheckResolver)
	want(t, r, OK, "")

	r = run(t, f.doctor(lookups([]string{"www.google.com"}, nil)), CheckResolver)
	want(t, r, Warn, FixSetResolver)
	param(t, r, "failed", "www.google.com")
	param(t, r, "resolver", "1.1.1.1")

	r = run(t, f.doctor(lookups([]string{"www.google.com", "www.gstatic.com"}, nil)), CheckResolver)
	want(t, r, Fail, FixSetResolver)

	// Everything fails on the host resolver and on ours too: no fix to offer, it is not the resolver config.
	all := []string{"www.cloudflare.com", "www.gstatic.com", "www.google.com", "gosuslugi.ru"}
	r = run(t, f.doctor(lookups(all, all)), CheckResolver)
	want(t, r, Fail, "")

	// The RU node: gosuslugi.ru alone failing is a FAIL (a public resolver makes it die), and the recommended
	// resolvers must be the Russian ones for the fix to be offered.
	r = run(t, f.doctor(lookups([]string{"gosuslugi.ru"}, nil), ru), CheckResolver)
	want(t, r, Fail, FixSetResolver)
	r = run(t, f.doctor(lookups([]string{"gosuslugi.ru"}, []string{"gosuslugi.ru"}), ru), CheckResolver)
	want(t, r, Fail, "") // even the recommended resolver cannot resolve it: the fix would not help
	// gosuslugi is not asked on a non-RU node.
	asked := false
	want(t, run(t, f.doctor(func(e *Env) {
		e.Lookup = func(_ context.Context, h string) error {
			if h == "gosuslugi.ru" {
				asked = true
			}
			return nil
		}
		e.LookupVia = func(context.Context, string, string) error { return nil }
	}), CheckResolver), OK, "")
	if asked {
		t.Error("gosuslugi.ru looked up on a non-RU node")
	}

	// One retry rescues a single timeout.
	calls := 0
	flaky := func(e *Env) {
		e.Lookup = func(_ context.Context, h string) error {
			if h == "www.google.com" {
				if calls++; calls == 1 {
					return errors.New("timeout")
				}
			}
			return nil
		}
		e.LookupVia = func(context.Context, string, string) error { return nil }
	}
	want(t, run(t, f.doctor(flaky), CheckResolver), OK, "")
}

func TestResolverSlow(t *testing.T) {
	f := newFake(t)
	d := f.doctor(func(e *Env) {
		e.Now = time.Now // the latency is measured, so this one test uses the real clock
		e.Lookup = func(context.Context, string) error { time.Sleep(600 * time.Millisecond); return nil }
		e.LookupVia = func(context.Context, string, string) error { return nil }
	})
	want(t, run(t, d, CheckResolver), Warn, FixSetResolver) // a median over 500 ms; the recommended resolvers answer
}

func TestIPv6(t *testing.T) {
	f := newFake(t)
	global := "2a010db800000000000000000000abcd 02 40 00 80 eth0\nfe800000000000000000000000000001 02 40 20 80 eth0\n"
	ula := "fd000000000000000000000000000001 02 40 00 80 eth0\n"
	tentative := "2a010db800000000000000000000abcd 02 40 00 c0 eth0\n"
	dial := func(ok bool) func(*Env) {
		return func(e *Env) {
			e.Dial = func(context.Context, string, string) error {
				if ok {
					return nil
				}
				return errors.New("network unreachable")
			}
		}
	}
	warp := func(e *Env) {
		e.Inbounds = func() []Inbound {
			return []Inbound{{ID: "inb_w", Enabled: true, Egress: "warp"}, {ID: "inb_d", Enabled: true, Egress: "direct"}}
		}
	}

	// No IPv6: fine unless a WARP inbound needs it.
	f.put("/proc/net/if_inet6", ula+tentative)
	want(t, run(t, f.doctor(), CheckIPv6), OK, "")
	r := run(t, f.doctor(warp), CheckIPv6)
	want(t, r, Warn, "")
	param(t, r, "warp_inbounds", "inb_w")

	// IPv6 and the connect works.
	f.put("/proc/net/if_inet6", global)
	want(t, run(t, f.doctor(dial(true)), CheckIPv6), OK, "")
	// IPv6 but the connect fails: clients that get AAAA will stall.
	r = run(t, f.doctor(dial(false)), CheckIPv6)
	want(t, r, Warn, "")
	param(t, r, "connect", "failed")

	// No table at all (IPv6 disabled in the kernel) is "no IPv6".
	f.remove("/proc/net/if_inet6")
	want(t, run(t, f.doctor(), CheckIPv6), OK, "")
}

func TestForeignVPN(t *testing.T) {
	ib := Inbound{ID: "inb_1", Enabled: true, Network: "udp", Port: 443}
	withInbound := func(e *Env) { e.Inbounds = func() []Inbound { return []Inbound{ib} } }
	units := "ssh.service loaded active running OpenBSD\nx-ui.service loaded failed failed x-ui\nwg-quick@wg0.service loaded inactive dead WG\n"
	files := "ssh.service enabled enabled\nxray.service disabled enabled\nwg-quick@.service disabled enabled\nhysteria-server@.service disabled enabled\n"

	t.Run("clean host", func(t *testing.T) {
		f := newFake(t)
		f.bin("systemctl")
		f.cmd("systemctl list-units --all --type=service --no-legend --no-pager --plain", "ssh.service loaded active running x\n", nil)
		f.cmd("systemctl list-unit-files --type=service --no-legend --no-pager --plain", "ssh.service enabled enabled\nwg-quick@.service disabled enabled\n", nil)
		f.put("/sys/class/net/eth0/address", "")
		f.put("/proc/10/comm", "sshd\n")
		r := run(t, f.doctor(), CheckForeignVPN)
		want(t, r, OK, "")
		code(t, r, CodeVPNNone)
	})
	t.Run("leftovers everywhere", func(t *testing.T) {
		f := newFake(t)
		f.bin("systemctl")
		f.cmd("systemctl list-units --all --type=service --no-legend --no-pager --plain", units, nil)
		f.cmd("systemctl list-unit-files --type=service --no-legend --no-pager --plain", files, nil)
		f.put("/sys/class/net/eth0/address", "")
		f.put("/sys/class/net/wg0/address", "")
		f.put("/proc/77/comm", "xray\n")
		r := run(t, f.doctor(func(e *Env) {
			e.Docker = func(context.Context) ([]Container, error) {
				return []Container{{Names: []string{"/amnezia-awg"}, Image: "amneziavpn/amnezia-wg"}, {Names: []string{"/db"}, Image: "postgres"}}, nil
			}
		}), CheckForeignVPN)
		want(t, r, Warn, "")
		code(t, r, CodeVPNFound, "count", "6")
		for _, n := range []string{"unit:x-ui", "unit:xray", "unit:wg-quick@wg0", "docker:amnezia-awg", "iface:wg0", "proc:xray"} {
			if !strings.Contains(r.Params["names"], n) {
				t.Errorf("%s missing from %q", n, r.Params["names"])
			}
		}
		if strings.Contains(r.Params["names"], "postgres") || strings.Contains(r.Params["names"], "wg-quick@,") {
			t.Errorf("false positive in %q", r.Params["names"])
		}
	})
	t.Run("a foreign process on our port is a FAIL", func(t *testing.T) {
		f := newFake(t)
		f.bin("systemctl")
		f.cmd("systemctl list-units --all --type=service --no-legend --no-pager --plain", "", nil)
		f.cmd("systemctl list-unit-files --type=service --no-legend --no-pager --plain", "", nil)
		f.put("/proc/77/comm", "xray\n")
		f.put("/proc/net/udp", udpTable("  1: 00000000:01BB 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 9001 2 0\n"))
		f.links["/proc/77/fd/5"] = "socket:[9001]"
		f.put("/proc/77/fd/5", "")
		r := run(t, f.doctor(withInbound), CheckForeignVPN)
		want(t, r, Fail, "")
		param(t, r, "on_our_ports", "xray:udp/443")
		code(t, r, CodeVPNClash)
	})
	t.Run("a docker port on our port is a FAIL", func(t *testing.T) {
		f := newFake(t)
		r := run(t, f.doctor(withInbound, func(e *Env) {
			e.Docker = func(context.Context) ([]Container, error) {
				return []Container{{Names: []string{"/xray"}, Image: "x", Ports: []int{443}}}, nil
			}
		}), CheckForeignVPN)
		want(t, r, Fail, "")
	})
	t.Run("our own interfaces are not foreign", func(t *testing.T) {
		f := newFake(t)
		f.put("/sys/class/net/awg0/address", "")
		want(t, run(t, f.doctor(func(e *Env) { e.OwnIface = func(n string) bool { return n == "awg0" } }), CheckForeignVPN), OK, "")
	})
}

func udpTable(rows string) string {
	return "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops\n" + rows
}

func tcpTable(rows string) string {
	return "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" + rows
}

const nftOurs = `{"table":{"family":"inet","name":"mistgate_node","handle":1}},
{"chain":{"family":"inet","table":"mistgate_node","name":"hop","handle":1,"type":"nat","hook":"prerouting","prio":-100,"policy":"accept"}},
{"rule":{"family":"inet","table":"mistgate_node","chain":"hop","handle":2,"expr":[{"match":{"op":"==","left":{"payload":{"protocol":"udp","field":"dport"}},"right":{"range":[20000,29999]}}},{"redirect":{"port":443}}]}}`

func nftDocument(parts ...string) string {
	return `{"nftables":[{"metainfo":{"version":"1.0.6"}},` + strings.Join(parts, ",") + `]}`
}

func TestForeignNft(t *testing.T) {
	ibs := []Inbound{{ID: "inb_1", Enabled: true, Network: "udp", Port: 443, HopFrom: 20000, HopTo: 29999}}
	withInbound := func(e *Env) { e.Inbounds = func() []Inbound { return ibs } }
	cmd := "nft -j list ruleset"
	f2b := `{"table":{"family":"inet","name":"f2b-table","handle":5}},
{"chain":{"family":"inet","table":"f2b-table","name":"f2b-chain","handle":1,"type":"filter","hook":"input","prio":-1,"policy":"accept"}},
{"rule":{"family":"inet","table":"f2b-table","chain":"f2b-chain","handle":3,"expr":[{"match":{"op":"==","left":{"payload":{"protocol":"tcp","field":"dport"}},"right":{"set":[22]}}},{"drop":null}]}}`
	docker := `{"table":{"family":"ip","name":"nat","handle":2}},
{"chain":{"family":"ip","table":"nat","name":"PREROUTING","handle":1,"type":"nat","hook":"prerouting","prio":-100,"policy":"accept"}}`
	hy := func(match string) string {
		return `{"table":{"family":"ip","name":"old_port_hop","handle":9}},
{"chain":{"family":"ip","table":"old_port_hop","name":"pre","handle":1,"type":"nat","hook":"prerouting","prio":-100,"policy":"accept"}},
{"rule":{"family":"ip","table":"old_port_hop","chain":"pre","handle":4,"expr":[` + match + `,{"redirect":{"port":8443}}]}}`
	}
	udp := func(right string) string {
		return `{"match":{"op":"==","left":{"payload":{"protocol":"udp","field":"dport"}},"right":` + right + `}}`
	}

	tests := []struct {
		name string
		out  string
		want Status
		code string
	}{
		{"only ours", nftDocument(nftOurs), OK, CodeNftClean},
		{"a filter table that does not touch our ports", nftDocument(nftOurs, f2b), OK, CodeNftFound},
		{"a nat table without rules is only a note", nftDocument(nftOurs, docker), Warn, CodeNftNat},
		{"forgotten redirect inside our hop range", nftDocument(nftOurs, hy(udp(`{"range":[25000,26000]}`))), Fail, CodeNftHits},
		{"forgotten redirect of our own port", nftDocument(nftOurs, hy(udp(`443`))), Fail, CodeNftHits},
		{"a set containing our port", nftDocument(nftOurs, hy(udp(`{"set":[80,{"range":[400,500]}]}`))), Fail, CodeNftHits},
		{"same port, other protocol", nftDocument(nftOurs, hy(`{"match":{"op":"==","left":{"payload":{"protocol":"tcp","field":"dport"}},"right":443}}`)), Warn, CodeNftNat},
		{"a range beside ours", nftDocument(nftOurs, hy(udp(`{"range":[30000,31000]}`))), Warn, CodeNftNat},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.bin("nft")
			f.cmd(cmd, tc.out, nil)
			r := run(t, f.doctor(withInbound), CheckForeignNft)
			want(t, r, tc.want, "")
			code(t, r, tc.code)
		})
	}

	f := newFake(t)
	code(t, run(t, f.doctor(), CheckForeignNft), CodeNftNone) // no nft binary
	f.bin("nft")
	f.cmd(cmd, "", errors.New("Operation not permitted"))
	code(t, run(t, f.doctor(), CheckForeignNft), CodeNftError)
	f.cmd(cmd, `{"nftables":[{"table":`, nil)
	want(t, run(t, f.doctor(), CheckForeignNft), Skip, "") // truncated JSON is a SKIP, never an OK
	// Disabled inbounds are not ours to protect.
	f.cmd(cmd, nftDocument(nftOurs, hy(udp(`443`))), nil)
	r := run(t, f.doctor(func(e *Env) {
		e.Inbounds = func() []Inbound { return []Inbound{{ID: "x", Enabled: false, Network: "udp", Port: 443}} }
	}), CheckForeignNft)
	want(t, r, Warn, "")
}

func TestPortConflicts(t *testing.T) {
	ib := Inbound{ID: "inb_1", Enabled: true, Network: "udp", Port: 443, HopFrom: 20000, HopTo: 20100, State: "running"}
	row := func(port, inode int) string {
		return fmt.Sprintf("  1: 00000000:%04X 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 %d 2 0\n", port, inode)
	}
	setup := func(f *fake, inbound Inbound, holders ...struct {
		pid   int
		comm  string
		port  int
		inode int
	}) func(*Env) {
		table := ""
		for _, h := range holders {
			table += row(h.port, h.inode)
			f.put(fmt.Sprintf("/proc/%d/comm", h.pid), h.comm+"\n")
			f.put(fmt.Sprintf("/proc/%d/fd/3", h.pid), "")
			f.links[fmt.Sprintf("/proc/%d/fd/3", h.pid)] = fmt.Sprintf("socket:[%d]", h.inode)
		}
		f.put("/proc/net/udp", udpTable(table))
		f.put("/proc/net/tcp", tcpTable(""))
		return func(e *Env) { e.Inbounds = func() []Inbound { return []Inbound{inbound} } }
	}
	type h = struct {
		pid   int
		comm  string
		port  int
		inode int
	}

	t.Run("we hold our own port", func(t *testing.T) {
		f := newFake(t)
		tw := setup(f, ib, h{100, "mistgate-node", 443, 1001})
		r := run(t, f.doctor(tw), CheckPortConflicts)
		want(t, r, OK, "")
		code(t, r, CodePortsOK, "checked", "1")
	})
	t.Run("caddy holds UDP 443", func(t *testing.T) {
		f := newFake(t)
		tw := setup(f, ib, h{812, "caddy", 443, 1002})
		r := run(t, f.doctor(tw), CheckPortConflicts)
		want(t, r, Fail, "")
		param(t, r, "process", "caddy(812)")
		param(t, r, "port", "443")
		param(t, r, "inbound_id", "inb_1")
		code(t, r, CodePortsHeld, "network", "udp")
	})
	t.Run("foreign listener inside the hop range", func(t *testing.T) {
		f := newFake(t)
		tw := setup(f, ib, h{100, "mistgate-node", 443, 1001}, h{900, "ntpd", 20050, 1003})
		r := run(t, f.doctor(tw), CheckPortConflicts)
		want(t, r, Warn, "")
		param(t, r, "hop_holders", "ntpd:20050")
		code(t, r, CodePortsHop, "hop_from", "20000", "hop_to", "20100", "inbound_id", "inb_1")
	})
	t.Run("tcp inbound needs LISTEN state", func(t *testing.T) {
		f := newFake(t)
		tcp := ib
		tcp.Network, tcp.HopFrom, tcp.HopTo = "tcp", 0, 0
		f.put("/proc/net/udp", udpTable(""))
		// One LISTEN (0A) and one ESTABLISHED (01) socket on 443: only the first counts.
		f.put("/proc/net/tcp", tcpTable(
			"  1: 00000000:01BB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2001 1 0\n"+
				"  2: 0100007F:01BB 0100007F:9C40 01 00000000:00000000 00:00000000 00000000     0        0 2002 1 0\n"))
		f.put("/proc/50/comm", "nginx\n")
		f.links["/proc/50/fd/4"] = "socket:[2001]"
		f.put("/proc/50/fd/4", "")
		r := run(t, f.doctor(func(e *Env) { e.Inbounds = func() []Inbound { return []Inbound{tcp} } }), CheckPortConflicts)
		want(t, r, Fail, "")
		param(t, r, "process", "nginx(50)")
		code(t, r, CodePortsHeld, "network", "tcp")
	})
	t.Run("connected UDP sockets are clients", func(t *testing.T) {
		f := newFake(t)
		f.put("/proc/net/udp", udpTable("  1: 0100007F:01BB 08080808:0035 01 00000000:00000000 00:00000000 00000000     0        0 3001 2 0\n"))
		f.put("/proc/net/tcp", tcpTable(""))
		want(t, run(t, f.doctor(func(e *Env) { e.Inbounds = func() []Inbound { return []Inbound{ib} } }), CheckPortConflicts), OK, "")
	})
	t.Run("failed to bind and the port is free now", func(t *testing.T) {
		f := newFake(t)
		failed := ib
		failed.State, failed.Error = "failed", "listen udp 443: bind: address already in use"
		tw := setup(f, failed)
		r := run(t, f.doctor(tw), CheckPortConflicts)
		want(t, r, Warn, FixRestartInbound)
		param(t, r, "inbound_id", "inb_1")
		code(t, r, CodePortsFailedBnd, "port", "443", "network", "udp")
	})
	t.Run("failed for another reason is not ours to restart", func(t *testing.T) {
		f := newFake(t)
		failed := ib
		failed.State, failed.Error = "failed", "obfs: bad password"
		tw := setup(f, failed)
		want(t, run(t, f.doctor(tw), CheckPortConflicts), OK, "")
	})
	t.Run("failed and still held: no fix", func(t *testing.T) {
		f := newFake(t)
		failed := ib
		failed.State, failed.Error = "failed", "listen udp 443: bind: address already in use"
		tw := setup(f, failed, h{812, "caddy", 443, 1002})
		want(t, run(t, f.doctor(tw), CheckPortConflicts), Fail, "")
	})
	t.Run("nothing to check", func(t *testing.T) {
		f := newFake(t)
		code(t, run(t, f.doctor(), CheckPortConflicts), CodePortsNone)
		off := ib
		off.Enabled = false
		want(t, run(t, f.doctor(func(e *Env) { e.Inbounds = func() []Inbound { return []Inbound{off} } }), CheckPortConflicts), OK, "")
	})
	t.Run("no socket table is a SKIP", func(t *testing.T) {
		f := newFake(t)
		r := run(t, f.doctor(func(e *Env) { e.Inbounds = func() []Inbound { return []Inbound{ib} } }), CheckPortConflicts)
		want(t, r, Skip, "")
		code(t, r, CodePortsNoTable)
	})
}

func TestNetBaseline(t *testing.T) {
	good := func(f *fake) {
		f.put("/proc/sys/net/core/default_qdisc", "fq\n")
		f.put("/proc/sys/net/ipv4/tcp_congestion_control", "bbr\n")
		f.put("/proc/sys/net/ipv4/tcp_available_congestion_control", "reno cubic bbr\n")
		f.put(hostctl.SysctlFilePath, hostctl.SysctlFileBody)
		f.put(hostctl.JournaldFilePath, hostctl.JournaldFileBody)
		f.put("/run/systemd/system/.keep", "")
	}
	f := newFake(t)
	good(f)
	r := run(t, f.doctor(), CheckNetBaseline)
	want(t, r, OK, "")
	code(t, r, CodeBaselineOK)

	f.put("/proc/sys/net/core/default_qdisc", "fq_codel\n")
	r = run(t, f.doctor(), CheckNetBaseline)
	want(t, r, Warn, FixApplyBaseline)
	param(t, r, "differs", "default_qdisc")
	code(t, r, CodeBaselineDiff)

	good(f)
	f.put("/proc/sys/net/ipv4/tcp_congestion_control", "cubic\n")
	want(t, run(t, f.doctor(), CheckNetBaseline), Warn, FixApplyBaseline)

	// BBR missing from the kernel: ApplyBaseline cannot load it, so no fix for that alone.
	f.put("/proc/sys/net/ipv4/tcp_available_congestion_control", "reno cubic\n")
	r = run(t, f.doctor(), CheckNetBaseline)
	want(t, r, Warn, "")
	code(t, r, CodeBaselineDiff, "notes", "no_bbr")

	good(f)
	f.put(hostctl.JournaldFilePath, "[Journal]\nSystemMaxUse=2G\n") // someone edited our drop-in
	r = run(t, f.doctor(), CheckNetBaseline)
	want(t, r, Warn, FixApplyBaseline)
	param(t, r, "differs", "journald_file")

	good(f)
	f.remove(hostctl.SysctlFilePath)
	want(t, run(t, f.doctor(), CheckNetBaseline), Warn, FixApplyBaseline)

	// A container cannot set qdisc/cc: only the journald drop-in is judged.
	good(f)
	f.put("/proc/sys/net/core/default_qdisc", "pfifo_fast\n")
	f.remove(hostctl.SysctlFilePath)
	r = run(t, f.doctor(func(e *Env) { e.Virt = "openvz" }), CheckNetBaseline)
	want(t, r, OK, "")
	code(t, r, CodeBaselineNotes, "notes", "container")
}

func TestCertExpiry(t *testing.T) {
	day := 24 * time.Hour
	served := func(c map[int]time.Time, sanNames ...string) func(*Env) {
		return func(e *Env) {
			e.ServedCert = func(_ context.Context, addr, sni string) (*x509.Certificate, error) {
				var port int
				_, _ = fmt.Sscanf(addr, "127.0.0.1:%d", &port)
				na, ok := c[port]
				if !ok {
					return nil, errors.New("connection refused")
				}
				names := sanNames
				if len(names) == 0 {
					names = []string{sni}
				}
				return mkCert(t, na, names...), nil
			}
		}
	}
	ib := func(mode string, tlsPort int) Inbound {
		return Inbound{ID: "inb_1", Enabled: true, Network: "udp", Port: 443, TLSMode: mode, ServerName: "example.com", State: "running", TLSPort: tlsPort}
	}
	with := func(ibs ...Inbound) func(*Env) { return func(e *Env) { e.Inbounds = func() []Inbound { return ibs } } }
	f := newFake(t)

	tests := []struct {
		name string
		left time.Duration
		want Status
	}{
		{"a month left", 30 * day, OK},
		{"15 days", 15 * day, OK},
		{"just under 14 days", 14*day - time.Hour, Warn},
		{"just over 3 days", 3*day + time.Hour, Warn},
		{"just under 3 days", 3*day - time.Hour, Fail},
		{"expired", -time.Hour, Fail},
	}
	for _, tc := range tests {
		t.Run("acme "+tc.name, func(t *testing.T) {
			r := run(t, f.doctor(with(ib("acme_domain", 8443)), served(map[int]time.Time{8443: t0.Add(tc.left)})), CheckCertExpiry)
			want(t, r, tc.want, "") // ACME renews by itself: never a fix
		})
	}

	// Self-signed gets restart_inbound (a restart regenerates a stale certificate).
	r := run(t, f.doctor(with(ib("self_signed", 8443)), served(map[int]time.Time{8443: t0.Add(2 * day)})), CheckCertExpiry)
	want(t, r, Fail, FixRestartInbound)
	param(t, r, "inbound_id", "inb_1")
	code(t, r, CodeCertInbound, "reason", "expiring", "days_left", "2", "server_name", "example.com")

	// The name the client asks for is not covered by the certificate.
	r = run(t, f.doctor(with(ib("acme_domain", 8443)), served(map[int]time.Time{8443: t0.Add(60 * day)}, "other.example.org")), CheckCertExpiry)
	want(t, r, Fail, "")
	param(t, r, "reason", "san_mismatch")

	// An IP certificate (acme_ip) is matched against the IP.
	ipIn := ib("acme_ip", 8443)
	ipIn.ServerName = "203.0.113.10"
	r = run(t, f.doctor(with(ipIn), served(map[int]time.Time{8443: t0.Add(60 * day)})), CheckCertExpiry)
	want(t, r, OK, "")
	code(t, r, CodeCertOK, "checked", "1")

	// No listener to read: the apply-time expiry is trusted for self-signed only.
	ss := ib("self_signed", 0)
	ss.CertNotAfter = t0.Add(2 * day)
	want(t, run(t, f.doctor(with(ss)), CheckCertExpiry), Fail, FixRestartInbound)
	acme := ib("acme_domain", 0)
	acme.CertNotAfter = t0.Add(2 * day) // stale by construction: never judged
	code(t, run(t, f.doctor(with(acme)), CheckCertExpiry), CodeCertUnreadable, "inbounds", "inb_1")

	// Worst inbound wins.
	a, b := ib("acme_domain", 8443), ib("acme_domain", 8444)
	b.ID = "inb_2"
	r = run(t, f.doctor(with(a, b), served(map[int]time.Time{8443: t0.Add(30 * day), 8444: t0.Add(5 * day)})), CheckCertExpiry)
	want(t, r, Warn, "")
	param(t, r, "inbound_id", "inb_2")

	// Failed and disabled inbounds serve nothing.
	bad := ib("self_signed", 8443)
	bad.State = "failed"
	code(t, run(t, f.doctor(with(bad)), CheckCertExpiry), CodeCertNone)

	// The agent's own mTLS certificate.
	agentLeft := func(d time.Duration) func(*Env) {
		return func(e *Env) { e.AgentCertNotAfter = func() time.Time { return t0.Add(d) } }
	}
	want(t, run(t, f.doctor(agentLeft(6*day)), CheckCertExpiry), OK, "")
	want(t, run(t, f.doctor(agentLeft(4*day)), CheckCertExpiry), Warn, "")
	r = run(t, f.doctor(agentLeft(12*time.Hour)), CheckCertExpiry)
	want(t, r, Fail, "")
	param(t, r, "subject", "agent")
	code(t, r, CodeCertAgent, "reason", "expiring", "days_left", "0")
}
