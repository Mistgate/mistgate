package doctor

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/node/hostctl"
)

// Env is everything the checks and fixes use from the outside. Production code gets the machine side from
// DefaultEnv and fills the agent side; a test points Root at a temp directory and replaces the functions.
// Nil function fields get defaults (nothing configured, real clock) in New.
type Env struct {
	// Root is prefixed to every absolute path the checks read (/proc/..., /etc/..., /var/log/...); "" = the
	// real root. Commands and syscalls are not redirected: they go through Run, Statfs, ... below.
	Root string
	// StateDir is the agent state directory; its volume is checked for free space too.
	StateDir string
	// Virt is systemd-detect-virt style ("kvm", "openvz", "lxc", "none", "unknown", "").
	Virt string
	// CPUs is the logical CPU count (0 = runtime.NumCPU).
	CPUs int
	// SelfPID is the agent's pid: sockets it holds are "ours".
	SelfPID int
	// Unsupported, when non-empty, makes every check SKIP and every fix unsupported_host (not a Linux host).
	Unsupported string

	// Machine side.
	Statfs   func(path string) (FSStat, error)
	Run      func(ctx context.Context, name string, args ...string) ([]byte, error) // stdout; error carries stderr
	LookPath func(name string) (string, error)
	Readlink func(path string) (string, error) // absolute path, not rooted; nil = os.Readlink under Root
	Sleep    func(ctx context.Context, d time.Duration)
	Now      func() time.Time
	// Dial is a TCP connect attempt that is closed at once.
	Dial func(ctx context.Context, network, addr string) error
	// Lookup resolves host with the host's own resolver; LookupVia asks one server ("ip:port") directly.
	Lookup    func(ctx context.Context, host string) error
	LookupVia func(ctx context.Context, server, host string) error
	// ServedCert returns the leaf certificate a TLS listener at addr (host:port) serves for sni.
	ServedCert func(ctx context.Context, addr, sni string) (*x509.Certificate, error)
	// Docker lists the containers through the read-only socket API; an error means "no docker here".
	Docker func(ctx context.Context) ([]Container, error)

	// Agent side.
	Settings func() Settings
	Inbounds func() []Inbound
	// Samples returns the stats ring samples of the last window, oldest first.
	Samples func(window time.Duration) []Sample
	// Offset is the panel clock minus the local clock in seconds, measured at Hello.
	Offset            func() int64
	AgentCertNotAfter func() time.Time
	// OwnIface says an interface was created by the agent (nil = none is).
	OwnIface func(name string) bool
	// AwgBackend is the choice of the AmneziaWG backend (awg_backend check); nil = this build has no awg engine.
	AwgBackend func() AwgBackend
	// Warp is the WARP manager's view (warp_path check); nil = this build has no WARP manager.
	Warp func(ctx context.Context) WarpInfo
	// HostPath lists clashes of the host with the tunnel path (a routing table or rule preference another tool uses, a
	// FORWARD drop policy); nil = not checked.
	HostPath func(ctx context.Context) []PathFinding
	// UnitGen is the generation of the systemd unit the agent runs from (MISTGATE_UNIT_GEN), 0 = unknown.
	UnitGen int

	// Fix actions.
	ApplyBaseline  func(ctx context.Context) error
	RestartInbound func(ctx context.Context, inboundID string) (uint32, error) // "" = every FAILED inbound
	Fixer          hostctl.Fixer                                               // nil = journal/resolver fixes unsupported

	// Set by Run: the previous result of a check.
	Prev func(id string) (Result, bool)
	memo *memo
}

// FSStat is what statfs says about a mount.
type FSStat struct {
	BlockSize uint64
	Blocks    uint64
	Bfree     uint64
	Bavail    uint64
	Files     uint64
	Ffree     uint64
}

// Settings is the part of NodeSettings the doctor needs.
type Settings struct {
	Country   string   // ISO 3166-1 alpha-2, "" = unknown
	Resolvers []string // NodeSettings.dns_resolvers
}

// Inbound is one inbound as the doctor sees it.
type Inbound struct {
	ID       string
	Protocol string
	Network  string // "udp" | "tcp"
	Port     int
	HopFrom  int // 0 = no hop range
	HopTo    int
	Enabled  bool
	Egress   string // "direct" | "warp"
	TLSMode  string // "acme_domain" | "acme_ip" | "self_signed" | ""
	// ServerName is the TLS server name (or IP for acme_ip).
	ServerName string
	State      string // "starting" | "running" | "failed" | "stopped"
	Error      string // why it is not running
	// CertNotAfter is what the engine reported when the inbound was last applied; zero = unknown. It can be
	// stale for ACME (renewal happens later), so it is only trusted for self-signed certificates.
	CertNotAfter time.Time
	// TLSPort is the TCP port of the inbound's HTTPS listener (decoy and ACME), 0 = none. The served certificate
	// is read there, but only if the listener is ours (see TLSDown and the socket table).
	TLSPort int
	// TLSDown is why the engine says that listener is not up ("masq_tcp: ... address already in use"), "" = up.
	TLSDown string
}

// Sample is one stats-interval host sample kept by the agent (Ring).
type Sample struct {
	At      time.Time
	CPU     float64 // total busy %
	Softirq float64 // softirq %
	Load1   float64
}

// Container is a docker container as far as foreign_vpn cares.
type Container struct {
	Names []string
	Image string
	Ports []int // published host ports
}

// memo caches per-run results that several checks share.
type memo struct {
	journal once[journalInfo]
	klog    once[klogInfo]
	procs   once[map[int]string]
	listen  once[[]listener]
}

type once[T any] struct {
	o sync.Once
	v T
}

func (x *once[T]) get(f func() T) T {
	x.o.Do(func() { x.v = f() })
	return x.v
}

// defaults fills nil fields so the checks never test for nil.
func (e *Env) defaults() {
	if e.Now == nil {
		e.Now = time.Now
	}
	if e.Sleep == nil {
		e.Sleep = func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		}
	}
	if e.CPUs <= 0 {
		e.CPUs = runtime.NumCPU()
	}
	if e.Statfs == nil {
		e.Statfs = func(string) (FSStat, error) { return FSStat{}, errors.New("statfs not available") }
	}
	if e.Run == nil {
		e.Run = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no command runner") }
	}
	if e.LookPath == nil {
		e.LookPath = func(n string) (string, error) { return "", errors.New("not found: " + n) }
	}
	if e.Dial == nil {
		e.Dial = func(context.Context, string, string) error { return errors.New("no dialer") }
	}
	if e.Lookup == nil {
		e.Lookup = func(context.Context, string) error { return errors.New("no resolver") }
	}
	if e.LookupVia == nil {
		e.LookupVia = func(context.Context, string, string) error { return errors.New("no resolver") }
	}
	if e.ServedCert == nil {
		e.ServedCert = func(context.Context, string, string) (*x509.Certificate, error) {
			return nil, errors.New("no tls dialer")
		}
	}
	if e.Docker == nil {
		e.Docker = func(context.Context) ([]Container, error) { return nil, errors.New("no docker") }
	}
	if e.Settings == nil {
		e.Settings = func() Settings { return Settings{} }
	}
	if e.Inbounds == nil {
		e.Inbounds = func() []Inbound { return nil }
	}
	if e.Samples == nil {
		e.Samples = func(time.Duration) []Sample { return nil }
	}
	if e.Offset == nil {
		e.Offset = func() int64 { return 0 }
	}
	if e.AgentCertNotAfter == nil {
		e.AgentCertNotAfter = func() time.Time { return time.Time{} }
	}
	if e.Prev == nil {
		e.Prev = func(string) (Result, bool) { return Result{}, false }
	}
	if e.memo == nil {
		e.memo = &memo{}
	}
}

func (e *Env) now() time.Time { return e.Now() }

func (e *Env) path(p string) string { return filepath.Join(e.Root, filepath.FromSlash(p)) }

func (e *Env) read(p string) (string, error) {
	b, err := os.ReadFile(e.path(p))
	return string(b), err
}

func (e *Env) readDir(p string) ([]os.DirEntry, error) { return os.ReadDir(e.path(p)) }

func (e *Env) stat(p string) (os.FileInfo, error) { return os.Stat(e.path(p)) }

func (e *Env) readlink(p string) (string, error) {
	if e.Readlink != nil {
		return e.Readlink(p)
	}
	return os.Readlink(e.path(p))
}

// has reports whether a binary is installed.
func (e *Env) has(name string) bool {
	_, err := e.LookPath(name)
	return err == nil
}

func (e *Env) container() bool {
	switch strings.ToLower(e.Virt) {
	case "openvz", "lxc", "lxc-libvirt", "docker", "podman", "systemd-nspawn", "wsl", "container-other":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------------------------------
// Production machine side

// DefaultEnv returns the machine side of an Env for this host: real commands (output capped), sockets,
// resolver and docker socket. The caller fills the agent side (Inbounds, Settings, ...). On a non-Linux
// host every check is SKIP (Unsupported is set).
func DefaultEnv() Env {
	e := Env{
		Statfs:     statfs,
		Run:        execRun,
		LookPath:   exec.LookPath,
		Dial:       dialOnce,
		Lookup:     lookupHost,
		LookupVia:  lookupVia,
		ServedCert: servedCert,
		Docker:     dockerContainers,
		SelfPID:    os.Getpid(),
	}
	if !hostSupported {
		e.Unsupported = "not a Linux host"
	}
	return e
}

const (
	maxOutput   = 4 << 20 // stdout kept from a command
	maxStderr   = 512
	maxDockerJS = 4 << 20
)

// capBuffer keeps the first max bytes and silently drops the rest (the process is not stopped or blocked).
type capBuffer struct {
	b   bytes.Buffer
	max int
	cut bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.b.Len(); room < len(p) {
		c.cut = true
		if room > 0 {
			c.b.Write(p[:room])
		}
	} else {
		c.b.Write(p)
	}
	return len(p), nil
}

// execRun runs a command with a C locale and no pager. Stdout is returned (capped); on failure the error
// carries the tail of stderr.
func execRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "SYSTEMD_COLORS=0", "SYSTEMD_PAGER=", "PAGER=cat")
	cmd.WaitDelay = 2 * time.Second
	out, errb := &capBuffer{max: maxOutput}, &capBuffer{max: maxStderr}
	cmd.Stdout, cmd.Stderr = out, errb
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errb.b.String()); msg != "" {
			err = errors.New(err.Error() + ": " + msg)
		}
		return out.b.Bytes(), err
	}
	return out.b.Bytes(), nil
}

func dialOnce(ctx context.Context, network, addr string) error {
	var d net.Dialer
	c, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return err
	}
	return c.Close()
}

// lookupHost uses the process resolver, which follows the host's resolv.conf (and nsswitch when built with cgo).
func lookupHost(ctx context.Context, host string) error {
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err == nil && len(addrs) == 0 {
		return errors.New("no addresses")
	}
	return err
}

func lookupVia(ctx context.Context, server, host string) error {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp", server)
	}}
	addrs, err := r.LookupHost(ctx, host)
	if err == nil && len(addrs) == 0 {
		return errors.New("no addresses")
	}
	return err
}

// servedCert reads the certificate a local TLS listener serves. Nothing is verified on purpose: this looks at
// the certificate, it does not trust it, and sends no data.
func servedCert(ctx context.Context, addr, sni string) (*x509.Certificate, error) {
	d := tls.Dialer{Config: &tls.Config{ServerName: sni, InsecureSkipVerify: true}} // #nosec G402 -- inspection only
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	certs := c.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("no certificate")
	}
	return certs[0], nil
}

// dockerContainers lists containers with GET /containers/json?all=1 on the docker socket (read-only call).
func dockerContainers(ctx context.Context) ([]Container, error) {
	const sock = "/var/run/docker.sock"
	if _, err := os.Stat(sock); err != nil {
		return nil, err
	}
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		DisableKeepAlives: true,
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/json?all=1", nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("docker: " + resp.Status)
	}
	var raw []struct {
		Names []string
		Image string
		Ports []struct{ PublicPort int }
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxDockerJS)).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(raw))
	for _, r := range raw {
		c := Container{Names: r.Names, Image: r.Image}
		for _, pt := range r.Ports {
			if pt.PublicPort != 0 {
				c.Ports = append(c.Ports, pt.PublicPort)
			}
		}
		out = append(out, c)
	}
	return out, nil
}
