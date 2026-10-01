// Command awgclient runs a real amneziawg-go process from an AmneziaVPN / AmneziaWG ".conf" file, for
// scripts/e2e-wsl.sh. The daemon is configured over its UAPI socket (it reads no .conf itself; the AmneziaWG
// tools that would convert one are not installed), the TUN gets the addresses of the file and every packet goes
// into the tunnel by default routes. Linux only, root, run it inside the namespace of the client:
//
//	nsenter -t <pid> -n awgclient -conf device.conf -bin ~/.cache/mistgate-tests/awgc-3.1/amneziawg-go \
//	    -iface mg3e1234 -endpoint 198.18.50.1:51842
//
// It prints "ready <iface> <uapi socket>" once the interface is up and then runs until SIGTERM/SIGINT or until
// the daemon dies. The handshake is not awaited: read `get=1` from the socket (`nc -U`) or ping through the
// tunnel. Nothing here is a product feature, it is test tooling.
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/node/awg/awguapi"
)

func main() {
	conf := flag.String("conf", "", "the .conf file")
	bin := flag.String("bin", "", "amneziawg-go binary")
	iface := flag.String("iface", "", "TUN name")
	endpoint := flag.String("endpoint", "", "host:port that replaces the Endpoint of the file (the panel writes the node's own address)")
	flavor := flag.String("flavor", "3.1", "3.1 | 2.0: the generation of the daemon")
	flag.Parse()
	if *conf == "" || *bin == "" || *iface == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*conf, *bin, *iface, *endpoint, *flavor); err != nil {
		fmt.Fprintln(os.Stderr, "awgclient:", err)
		os.Exit(1)
	}
}

type parsed struct {
	priv      [32]byte
	addrs     []string
	mtu       int
	obf       awgcfg.Obfuscation
	peer      awgcfg.Peer
	allowedV6 bool
}

func key(s string) (k [32]byte, err error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != 32 {
		return k, fmt.Errorf("not a 32-byte base64 key")
	}
	copy(k[:], b)
	return k, nil
}

func isOn(v string) bool {
	switch strings.ToLower(v) {
	case "on", "true", "yes", "1":
		return true
	}
	return false
}

func parse(path string) (p parsed, err error) {
	f, err := os.Open(path)
	if err != nil {
		return p, err
	}
	defer f.Close()
	p.mtu = 1280
	section := ""
	rng := func(v string) awgcfg.Range { r, _ := awgcfg.ParseRange(v); return r }
	num := func(v string) int { n, _ := strconv.Atoi(v); return n }
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] == '[' {
			section = strings.ToLower(strings.Trim(line, "[] "))
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		o := &p.obf
		switch section {
		case "interface":
			switch k {
			case "privatekey":
				if p.priv, err = key(v); err != nil {
					return p, fmt.Errorf("PrivateKey: %w", err)
				}
			case "address":
				for _, a := range strings.Split(v, ",") {
					p.addrs = append(p.addrs, strings.TrimSpace(a))
				}
			case "mtu":
				p.mtu = num(v)
			case "jc":
				o.Jc = num(v)
			case "jmin":
				o.Jmin = num(v)
			case "jmax":
				o.Jmax = num(v)
			case "s1":
				o.S1 = num(v)
			case "s2":
				o.S2 = num(v)
			case "s3":
				o.S3 = num(v)
			case "s4":
				o.S4 = num(v)
			case "h1":
				o.H1 = rng(v)
			case "h2":
				o.H2 = rng(v)
			case "h3":
				o.H3 = rng(v)
			case "h4":
				o.H4 = rng(v)
			case "i1":
				o.I1 = v
			case "i2":
				o.I2 = v
			case "i3":
				o.I3 = v
			case "i4":
				o.I4 = v
			case "i5":
				o.I5 = v
			case "headerprotectionkey":
				o.HeaderProtectionKey = v
			case "contentpaddingaddition":
				o.ContentPaddingAddition = rng(v)
			case "rekeyaftertime":
				o.RekeyAfterTime = rng(v)
			case "rekeytimeout":
				o.RekeyTimeout = rng(v)
			case "rejectaftertime":
				o.RejectAfterTime = rng(v)
			case "keepalivetimeout":
				o.KeepaliveTimeout = rng(v)
			case "maxhandshakeattempts":
				o.MaxHandshakeAttempts = rng(v)
			case "randomtrailers":
				o.RandomTrailers = isOn(v)
			case "disablecookies":
				o.DisableCookies = isOn(v)
			}
		case "peer":
			switch k {
			case "publickey":
				if p.peer.PublicKey, err = key(v); err != nil {
					return p, fmt.Errorf("PublicKey: %w", err)
				}
			case "presharedkey":
				psk, err := key(v)
				if err != nil {
					return p, fmt.Errorf("PresharedKey: %w", err)
				}
				p.peer.PSK = &psk
			case "allowedips":
				for _, a := range strings.Split(v, ",") {
					pf, err := netip.ParsePrefix(strings.TrimSpace(a))
					if err != nil {
						return p, fmt.Errorf("AllowedIPs: %w", err)
					}
					p.peer.AllowedIPs = append(p.peer.AllowedIPs, pf)
					p.allowedV6 = p.allowedV6 || pf.Addr().Is6()
				}
			case "endpoint":
				p.peer.Endpoint = v
			case "persistentkeepalive":
				p.peer.Keepalive = rng(v)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return p, err
	}
	if len(p.addrs) == 0 || len(p.peer.AllowedIPs) == 0 || p.peer.Endpoint == "" {
		return p, fmt.Errorf("the file has no Address, AllowedIPs or Endpoint")
	}
	return p, nil
}

func ip(args ...string) error {
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func uapi(sock, req string) error {
	c, err := net.DialTimeout("unix", sock, 3*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write([]byte(req + "\n")); err != nil {
		return err
	}
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		switch l := sc.Text(); {
		case l == "errno=0":
			return nil
		case strings.HasPrefix(l, "errno="):
			return fmt.Errorf("uapi %s", l)
		}
	}
	return fmt.Errorf("uapi: no answer (%v)", sc.Err())
}

func run(conf, bin, iface, endpoint, flavor string) error {
	p, err := parse(conf)
	if err != nil {
		return err
	}
	if endpoint != "" {
		p.peer.Endpoint = endpoint
	}
	version := awgcfg.Version31
	if flavor == "2.0" {
		version = awgcfg.Version20
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cmd := exec.Command(bin, iface)
	cmd.Env = append(os.Environ(), "WG_PROCESS_FOREGROUND=1", "LOG_LEVEL=error")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	kill := func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	}

	sock := ""
	for deadline := time.Now().Add(5 * time.Second); sock == "" && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		for _, d := range []string{"/var/run/amneziawg", "/var/run/wireguard"} {
			if s := filepath.Join(d, iface+".sock"); fileExists(s) {
				sock = s
			}
		}
	}
	if sock == "" {
		kill()
		return fmt.Errorf("amneziawg-go opened no UAPI socket")
	}
	text := awguapi.DeviceSet(version, p.priv, 0, p.obf)
	if flavor == "3.0" { // the 3.0 daemon knows neither key
		var keep []string
		for _, l := range strings.Split(text, "\n") {
			if !strings.HasPrefix(l, "random_trailers=") && !strings.HasPrefix(l, "disable_cookies=") {
				keep = append(keep, l)
			}
		}
		text = strings.Join(keep, "\n")
	}
	text += awguapi.PeersSet(false, []awgcfg.Peer{p.peer})
	if err := uapi(sock, "set=1\n"+text); err != nil {
		kill()
		return fmt.Errorf("configure the daemon: %w", err)
	}
	steps := [][]string{{"link", "set", iface, "mtu", strconv.Itoa(p.mtu)}}
	for _, a := range p.addrs {
		s := []string{"addr", "add", a, "dev", iface}
		if strings.Contains(a, ":") {
			s = append(s, "nodad")
		}
		steps = append(steps, s)
	}
	steps = append(steps, []string{"link", "set", iface, "up"}, []string{"route", "replace", "default", "dev", iface})
	if p.allowedV6 {
		steps = append(steps, []string{"-6", "route", "replace", "default", "dev", iface})
	}
	for _, s := range steps {
		if err := ip(s...); err != nil {
			kill()
			return err
		}
	}
	fmt.Println("ready", iface, sock)
	select {
	case <-ctx.Done():
		kill()
		return nil
	case err := <-exited:
		return fmt.Errorf("amneziawg-go exited: %v", err)
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
