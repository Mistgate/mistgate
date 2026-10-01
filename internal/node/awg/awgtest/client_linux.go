//go:build linux

package awgtest

import (
	"bufio"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/node/awg/awguapi"
)

// NewKey returns an X25519 key pair (the private key is the raw scalar; WireGuard clamps it).
func NewKey(t testing.TB) (priv, pub [32]byte) {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	copy(priv[:], k.Bytes())
	copy(pub[:], k.PublicKey().Bytes())
	return
}

// B64 is the base64 form of a key as it appears in credential data and settings.
func B64(k [32]byte) string { return base64.StdEncoding.EncodeToString(k[:]) }

// Flavor names a client implementation: which AmneziaWG generation it speaks.
const (
	Flavor31 = "3.1" // amneziawg-go v3.1.20260828 (the pin)
	Flavor30 = "3.0" // amneziawg-go v3.0.20260805: no RandomTrailers / DisableCookies
	Flavor20 = "2.0" // amneziawg-go v0.2.17, the last of the 2.0 line
)

var flavors = map[string]struct{ module, version string }{
	Flavor31: {"github.com/amnezia-vpn/amneziawg-go/v3", "v3.1.20260828"},
	Flavor30: {"github.com/amnezia-vpn/amneziawg-go/v3", "v3.0.20260805"},
	Flavor20: {"github.com/amnezia-vpn/amneziawg-go", "v0.2.17"},
}

// CacheDir is where client binaries are kept between runs (MG3_TEST_CACHE, default ~/.cache/mistgate-tests).
func CacheDir() string {
	if d := os.Getenv("MG3_TEST_CACHE"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".cache", "mistgate-tests")
}

// ClientBin returns the path of the amneziawg-go binary of the flavor, building it from the pinned source with
// `go install` on first use (needs the network and a Go toolchain; a binary is never downloaded).
func ClientBin(t testing.TB, flavor string) string {
	t.Helper()
	f, ok := flavors[flavor]
	if !ok {
		t.Fatalf("unknown client flavor %q", flavor)
	}
	dir := filepath.Join(CacheDir(), "awgc-"+flavor)
	bin := filepath.Join(dir, "amneziawg-go")
	if _, err := os.Stat(bin); err == nil {
		return bin
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "install", f.module+"@"+f.version)
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), "GOBIN="+dir, "GOTOOLCHAIN=auto", "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building amneziawg-go %s %s: %v\n%s", f.module, f.version, err, out)
	}
	return bin
}

// ClientCfg is one client interface.
type ClientCfg struct {
	Flavor    string // Flavor31 | Flavor30 | Flavor20
	Addrs     []string
	Routes    []string // destinations sent into the tunnel
	Priv      [32]byte
	ServerPub [32]byte
	PSK       *[32]byte
	Endpoint  string // "203.0.113.10:51842"
	Obf       awgcfg.Obfuscation
	MTU       int    // 0 = 1280
	Keepalive string // "25"; "" = none
}

// Client is a real amneziawg-go process in a namespace.
type Client struct {
	NS    NS
	Iface string
	proc  *Proc
	sock  string
}

var clientSeq atomic.Int32

// StartClient starts amneziawg-go in ns, configures it over its UAPI socket, addresses the TUN and brings it up.
// It does not wait for a handshake.
func StartClient(t testing.TB, ns NS, cfg ClientCfg) *Client {
	t.Helper()
	bin := ClientBin(t, cfg.Flavor)
	iface := fmt.Sprintf("mg3c%d", clientSeq.Add(1)+int32(os.Getpid()%1000)*100)
	c := &Client{NS: ns, Iface: iface}
	c.proc = ns.Start(t, []string{"WG_PROCESS_FOREGROUND=1", "LOG_LEVEL=error"}, bin, iface)
	for _, d := range []string{"/var/run/amneziawg", "/var/run/wireguard"} {
		s := filepath.Join(d, iface+".sock")
		if Wait(3*time.Second, func() bool { _, err := os.Stat(s); return err == nil }) {
			c.sock = s
			break
		}
	}
	if c.sock == "" {
		t.Fatalf("amneziawg-go %s did not open its UAPI socket: %s", cfg.Flavor, c.proc.Output())
	}

	version := awgcfg.Version31
	if cfg.Flavor == Flavor20 {
		version = awgcfg.Version20
	}
	text := awguapi.DeviceSet(version, cfg.Priv, 0, cfg.Obf)
	if cfg.Flavor == Flavor30 { // the 3.0 tools know no RandomTrailers/DisableCookies: "invalid UAPI device key"
		var keep []string
		for _, l := range strings.Split(text, "\n") {
			if !strings.HasPrefix(l, "random_trailers=") && !strings.HasPrefix(l, "disable_cookies=") {
				keep = append(keep, l)
			}
		}
		text = strings.Join(keep, "\n")
	}
	ka, err := awgcfg.ParseRange(cfg.Keepalive)
	if err != nil {
		t.Fatal(err)
	}
	text += awguapi.PeersSet(false, []awgcfg.Peer{{
		PublicKey: cfg.ServerPub, PSK: cfg.PSK, Endpoint: cfg.Endpoint,
		AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")},
		Keepalive:  ka,
	}})
	if err := c.uapi("set=1\n" + text); err != nil {
		t.Fatalf("configure client (%s): %v\n%s", cfg.Flavor, err, c.proc.Output())
	}
	mtu := cfg.MTU
	if mtu == 0 {
		mtu = 1280
	}
	ns.Must(t, "ip", "link", "set", iface, "mtu", fmt.Sprint(mtu))
	for _, a := range cfg.Addrs {
		args := []string{"ip", "addr", "add", a, "dev", iface}
		if strings.Contains(a, ":") {
			args = append(args, "nodad")
		}
		ns.Must(t, args...)
	}
	ns.Must(t, "ip", "link", "set", iface, "up")
	for _, r := range cfg.Routes {
		args := []string{"ip", "route", "replace", r, "dev", iface}
		if strings.Contains(r, ":") {
			args = []string{"ip", "-6", "route", "replace", r, "dev", iface}
		}
		ns.Must(t, args...)
	}
	t.Cleanup(c.Stop)
	return c
}

// uapi sends one request to the client's socket and checks "errno=0".
func (c *Client) uapi(req string) error {
	_, err := c.uapiRaw(req)
	return err
}

func (c *Client) uapiRaw(req string) (string, error) {
	conn, err := net.DialTimeout("unix", c.sock, 3*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte(req + "\n")); err != nil {
		return "", err
	}
	var b strings.Builder
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "errno=") {
			if line != "errno=0" {
				return b.String(), fmt.Errorf("uapi %s", line)
			}
			continue
		}
		b.WriteString(line + "\n")
	}
	return b.String(), sc.Err()
}

// Stats reads the client's view of its peers.
func (c *Client) Stats() ([]awgcfg.PeerStat, error) {
	text, err := c.uapiRaw("get=1\n")
	if err != nil {
		return nil, err
	}
	return awguapi.ParseStats(text)
}

// Handshaken reports whether the client completed a handshake with the server.
func (c *Client) Handshaken() bool {
	st, err := c.Stats()
	return err == nil && len(st) == 1 && !st[0].LastHS.IsZero()
}

// LastHandshake is the time of the client's most recent handshake (zero = never).
func (c *Client) LastHandshake() time.Time {
	st, err := c.Stats()
	if err != nil || len(st) != 1 {
		return time.Time{}
	}
	return st[0].LastHS
}

// Stop ends the client process (its TUN goes with it).
func (c *Client) Stop() {
	if c.proc != nil {
		c.proc.Kill()
	}
	if c.sock != "" {
		_ = os.Remove(c.sock)
	}
}

// Output is what the client process printed (errors only: LOG_LEVEL=error).
func (c *Client) Output() string { return c.proc.Output() }
