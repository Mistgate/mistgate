// Package awguapi renders and parses the UAPI text protocol of amneziawg-go v3.1.x (device.IpcSet / IpcGet).
// It is pure text code and builds everywhere.
package awguapi

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
)

// DeviceSet renders the device part of a set operation: key, port and the obfuscation of version v. Keys of the
// other version are not written (amneziawg-go 0.2.x rejects the 3.x ones). Order does not matter: S/H/HPK are
// validated together when the message ends. IpcSet is NOT atomic: a line that fails leaves the lines before it
// applied (measured), so a failed set is followed by recreating the interface, never by a retry on it.
func DeviceSet(version string, priv [32]byte, port uint16, o awgcfg.Obfuscation) string {
	var b strings.Builder
	w := func(k string, v any) { fmt.Fprintf(&b, "%s=%v\n", k, v) }
	w("private_key", hex.EncodeToString(priv[:]))
	w("listen_port", port)
	w("jc", o.Jc)
	w("jmin", o.Jmin)
	w("jmax", o.Jmax)
	for i, s := range o.S() {
		w("s"+strconv.Itoa(i+1), s)
	}
	for i, h := range o.H() {
		w("h"+strconv.Itoa(i+1), h) // always all four: "1".."4" is off, a zero range would overlap the others
	}
	for i, s := range o.I() {
		if s != "" {
			w("i"+strconv.Itoa(i+1), s)
		}
	}
	if version != awgcfg.Version20 {
		if k, _ := o.HPK(); k != nil {
			w("header_protection_key", hex.EncodeToString(k[:]))
		}
		for i, name := range timerKeys {
			if r := o.Timers()[i]; !r.IsZero() {
				w(name, r)
			}
		}
		w("random_trailers", o.RandomTrailers) // strconv.ParseBool: "true"/"false", NOT "on"/"off"
		w("disable_cookies", o.DisableCookies)
	}
	return b.String()
}

var timerKeys = [6]string{"content_padding_addition", "rekey_after_time", "rekey_timeout", "reject_after_time", "keepalive_timeout", "max_handshake_attempts"}

// PeersSet renders peer operations. A peer starts at its public_key line; device keys must not follow a peer.
func PeersSet(replaceAll bool, peers []awgcfg.Peer) string {
	var b strings.Builder
	if replaceAll {
		b.WriteString("replace_peers=true\n")
	}
	for _, p := range peers {
		fmt.Fprintf(&b, "public_key=%s\n", hex.EncodeToString(p.PublicKey[:]))
		if p.Remove {
			b.WriteString("remove=true\n")
			continue
		}
		if p.UpdateOnly {
			b.WriteString("update_only=true\n")
		}
		if p.PSK != nil {
			fmt.Fprintf(&b, "preshared_key=%s\n", hex.EncodeToString(p.PSK[:]))
		}
		if p.Endpoint != "" {
			fmt.Fprintf(&b, "endpoint=%s\n", p.Endpoint)
		}
		if !p.Keepalive.IsZero() {
			fmt.Fprintf(&b, "persistent_keepalive_interval=%s\n", p.Keepalive)
		}
		if p.ReplaceIPs {
			b.WriteString("replace_allowed_ips=true\n")
		}
		for _, ip := range p.AllowedIPs {
			fmt.Fprintf(&b, "allowed_ip=%s\n", ip)
		}
	}
	return b.String()
}

// ParseStats reads device.IpcGet() output: per peer the public key, counters, last handshake, endpoint and allowed
// ips. Device lines before the first public_key are skipped (they carry the private key: never logged).
func ParseStats(s string) ([]awgcfg.PeerStat, error) {
	var out []awgcfg.PeerStat
	var cur *awgcfg.PeerStat
	var sec, nsec int64
	flush := func() {
		if cur != nil {
			if sec != 0 {
				cur.LastHS = time.Unix(sec, nsec)
			}
			out = append(out, *cur)
		}
		cur, sec, nsec = nil, 0, 0
	}
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		if k == "public_key" {
			flush()
			cur = &awgcfg.PeerStat{}
			b, err := hex.DecodeString(v)
			if err != nil || len(b) != 32 {
				return nil, fmt.Errorf("uapi: bad public_key")
			}
			copy(cur.PublicKey[:], b)
			continue
		}
		if cur == nil {
			continue
		}
		switch k {
		case "endpoint":
			if ap, err := netip.ParseAddrPort(v); err == nil {
				cur.Endpoint = ap
			}
		case "allowed_ip":
			p, err := netip.ParsePrefix(v)
			if err != nil {
				return nil, fmt.Errorf("uapi: bad allowed_ip %q", v)
			}
			cur.AllowedIPs = append(cur.AllowedIPs, p)
		case "tx_bytes", "rx_bytes", "last_handshake_time_sec", "last_handshake_time_nsec":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("uapi: bad %s", k)
			}
			switch k {
			case "tx_bytes":
				cur.TxBytes = uint64(n)
			case "rx_bytes":
				cur.RxBytes = uint64(n)
			case "last_handshake_time_sec":
				sec = n
			default:
				nsec = n
			}
		}
	}
	flush()
	return out, sc.Err()
}
