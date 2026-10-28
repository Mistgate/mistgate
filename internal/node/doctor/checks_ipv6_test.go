package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The ipv6 check names the main uplink: the interface of the ::/0 route and its stable global addresses, never WARP,
// AmneziaWG or other tunnels, and the source address the kernel picks.
func TestIPv6Uplink(t *testing.T) {
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
	src := func(a string) func(*Env) {
		return func(e *Env) {
			e.LocalAddr = func(context.Context, string, string) (string, error) {
				if a == "" {
					return "", errors.New("network is unreachable")
				}
				return a, nil
			}
		}
	}
	const zero = "00000000000000000000000000000000"
	// route is one /proc/net/ipv6_route line: dest plen src plen next_hop metric refcnt use flags iface.
	route := func(metric, flags, iface string) string {
		return zero + " 00 " + zero + " 00 fe800000000000000000000000000001 " + metric + " 00000001 00000000 " + flags + " " + iface + "\n"
	}
	lo := zero + " 00 " + zero + " 00 " + zero + " ffffffff 00000001 00000000 00200200 lo\n" // reject route

	t.Run("uplink among tunnels", func(t *testing.T) {
		f := newFake(t)
		f.put("/proc/net/if_inet6", strings.Join([]string{
			"20010db8000100000000000000000abc 02 40 00 80 eth0",       // the uplink address
			"20010db800010000000000000000d00d 02 40 00 81 eth0",       // temporary
			"20010db800010000000000000000dead 02 40 00 a0 eth0",       // deprecated
			"20010db800010000000000000000f00d 02 40 00 c0 eth0",       // tentative
			"20010db800010000000000000000ba11 02 40 00 88 eth0",       // DAD failed
			"fd000000000000000000000000000001 02 40 00 80 eth0",       // ULA
			"fe800000000000000000000000000001 02 40 20 80 eth0",       // link-local
			"20010db8ffff0000000000000000cafe 03 80 00 80 mgwarp",     // WARP /128
			"fd660000000000010000000000000001 04 40 00 80 mgawg0",     // AWG tunnel
			"20010db8000200000000000000000001 05 40 00 80 tailscale0", // some other tunnel
			"",
		}, "\n"))
		f.put("/proc/net/ipv6_route", lo+
			route("00000100", "00450003", "mgwarp")+ // a lower metric, but a tunnel
			route("00000200", "00450003", "mgawg0")+
			route("00000400", "00450003", "eth0")+
			route("00000800", "00450003", "eth1"))
		r := run(t, f.doctor(dial(true), src("2001:db8:1::abc")), CheckIPv6)
		want(t, r, OK, "")
		code(t, r, CodeIPv6OK, "connect", "ok", "iface", "eth0", "addrs", "2001:db8:1::abc/64", "src", "2001:db8:1::abc")
	})

	t.Run("several addresses are sorted and capped", func(t *testing.T) {
		f := newFake(t)
		var in strings.Builder
		for _, h := range []string{"05", "03", "01", "04", "02"} { // five stable addresses, out of order
			in.WriteString("20010db8000100000000000000000a" + h + " 02 40 00 80 eth0\n")
		}
		f.put("/proc/net/if_inet6", in.String())
		f.put("/proc/net/ipv6_route", route("00000400", "00450003", "eth0"))
		r := run(t, f.doctor(dial(true), src("2001:db8:1::a01")), CheckIPv6)
		code(t, r, CodeIPv6OK, "addrs", "2001:db8:1::a01/64,2001:db8:1::a02/64,2001:db8:1::a03/64,2001:db8:1::a04/64")
	})

	t.Run("a source that is not global is left out", func(t *testing.T) {
		f := newFake(t)
		f.put("/proc/net/if_inet6", "20010db8000100000000000000000abc 02 40 00 80 eth0\n")
		f.put("/proc/net/ipv6_route", route("00000400", "00450003", "eth0"))
		r := run(t, f.doctor(dial(true), src("fd00::1")), CheckIPv6)
		code(t, r, CodeIPv6OK, "iface", "eth0", "src", "")
	})

	t.Run("no IPv6", func(t *testing.T) {
		f := newFake(t)
		f.put("/proc/net/if_inet6", "fe800000000000000000000000000001 02 40 20 80 eth0\n")
		f.put("/proc/net/ipv6_route", lo)
		r := run(t, f.doctor(dial(true), src("2001:db8:1::abc")), CheckIPv6)
		want(t, r, OK, "")
		code(t, r, CodeIPv6None, "ipv6", "no", "iface", "", "addrs", "", "src", "")
	})

	t.Run("an address but no default route", func(t *testing.T) {
		f := newFake(t)
		f.put("/proc/net/if_inet6", "20010db8000100000000000000000abc 02 40 00 80 eth0\n")
		// WARP's default is a tunnel, eth0's is down (no RTF_UP).
		f.put("/proc/net/ipv6_route", lo+route("00000100", "00450003", "mgwarp")+route("00000400", "00450002", "eth0"))
		r := run(t, f.doctor(dial(false), src("")), CheckIPv6)
		want(t, r, Warn, "")
		code(t, r, CodeIPv6Stalls, "connect", "failed", "iface", "", "addrs", "", "src", "")
	})
}
