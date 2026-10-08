//go:build linux

package hostctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	torrentlinux "github.com/mistgate/mistgate/internal/node/torrentguard/linux"
)

// runner runs an external command with optional stdin and returns its combined output.
type runner func(ctx context.Context, stdin, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, stdin, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	// After a timeout kill, a child that still holds the output pipe (systemd-run --pipe hands it to its transient unit)
	// must not keep the caller waiting.
	cmd.WaitDelay = 5 * time.Second
	return cmd.CombinedOutput()
}

type linuxHost struct {
	log *slog.Logger
	run runner

	// Overridable for tests; New fills in the real paths.
	sysctlFile   string // /etc/sysctl.d/90-mistgate.conf
	journaldFile string // /etc/systemd/journald.conf.d/90-mistgate.conf
	procSys      string // /proc/sys
	procRoot     string // /proc
	sshdConfDir  string // /etc/ssh

	// Resolver fix (fixes_linux.go). Empty = the fix is unavailable (tests that do not exercise it).
	resolvedFile   string // /etc/systemd/resolved.conf.d/90-mistgate.conf
	resolvConfFile string // /etc/resolv.conf

	// fw serializes Mistgate-owned firewall changes: the nft table and exact tagged UFW inbound rules.
	fw       sync.Mutex
	hops     []Hop
	sshPorts []uint16
	// UFW inbound sync (firewall_linux.go): ufw.conf ("" = UFW is never run), and the last sync's key, result and time.
	ufwConf   string
	udpSynced bool
	udpKey    string
	udpErr    error
	udpAt     time.Time

	// The tunnel table of the L3 protocols (tunnel_linux.go) and the deletion of our links; nil links = none (tests).
	tun   tunnelState
	links func() error

	// Torrent guard owns a separate nft table and one fail-open NFQUEUE runtime.
	torrentMu     sync.Mutex
	torrent       *torrentlinux.Runtime
	torrentIfaces []string

	mu      sync.Mutex
	prevCPU cpuSample
	prevNet struct {
		iface  string
		rx, tx uint64
		at     time.Time
	}
}

// New returns the real Linux host owner.
//
// nftables is driven through the `nft` binary, not github.com/google/nftables: the ruleset is one short
// text script applied with `nft -f -` (a single atomic transaction, replace-or-create, easy to read in an
// incident and to assert in a test by rendering it), while the library would add a netlink dependency and a
// hand-built expression tree for the same three lines. The price is that `nft` must be installed (it is on
// Debian 12 and Ubuntu 22.04+); SetPortHops reports a clear error if it is not.
func New(log *slog.Logger) Host {
	return &linuxHost{
		log:          log,
		run:          execRunner,
		sysctlFile:   SysctlFilePath,
		journaldFile: JournaldFilePath,
		procSys:      "/proc/sys",
		procRoot:     "/proc",
		sshdConfDir:  "/etc/ssh",
		ufwConf:      defaultUFWConf,
		links:        deleteOwnLinks,

		resolvedFile:   defaultResolvedFile,
		resolvConfFile: defaultResolvConfFile,
	}
}

func (h *linuxHost) read(rel string) string {
	b, _ := os.ReadFile(filepath.Join(h.procRoot, rel))
	return string(b)
}

func (h *linuxHost) Facts(ctx context.Context) Facts {
	f := Facts{Arch: runtime.GOARCH, CPUCount: runtime.NumCPU(), HasIPv6: hasGlobalIPv6(), Virt: "unknown", OS: "linux"}
	f.Hostname, _ = os.Hostname()
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		if n := osPrettyName(string(b)); n != "" {
			f.OS = n
		}
	}
	f.Kernel = strings.TrimSpace(h.read("sys/kernel/osrelease"))
	f.RAMTotal, _ = parseMeminfo(h.read("meminfo"))
	f.DiskTotal, _ = diskUsage("/")
	if bt := parseBtime(h.read("stat")); bt > 0 {
		f.Boot = time.Unix(bt, 0).UTC()
	}
	// systemd-detect-virt exits 1 with "none" on bare metal, so the output counts even on error.
	if out, _ := h.run(ctx, "", "systemd-detect-virt"); len(bytes.TrimSpace(out)) > 0 && len(out) < 32 {
		f.Virt = strings.TrimSpace(string(out))
	} else if _, err := os.Stat("/proc/vz"); err == nil {
		f.Virt = "openvz"
	}
	return f
}

func diskUsage(path string) (total, used uint64) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return 0, 0
	}
	total = st.Blocks * uint64(st.Bsize)
	used = (st.Blocks - st.Bfree) * uint64(st.Bsize)
	return
}

func (h *linuxHost) Metrics() Metrics {
	h.mu.Lock()
	defer h.mu.Unlock()
	var m Metrics
	if cur, ok := parseCPUStat(h.read("stat")); ok {
		if h.prevCPU.total != 0 {
			m.CPUPct, m.SoftirqPct = cpuPct(h.prevCPU, cur)
		}
		h.prevCPU = cur
	}
	m.Load1 = parseFirstFloat(h.read("loadavg"))
	m.UptimeS = uint64(parseFirstFloat(h.read("uptime")))
	total, avail := parseMeminfo(h.read("meminfo"))
	m.RAMTotal = total
	if total >= avail {
		m.RAMUsed = total - avail
	}
	m.DiskTotal, m.DiskUsed = diskUsage("/")

	// Main NIC = the default route's interface (IPv4, else IPv6). Rates are averaged since the previous sample.
	if iface, rx, tx, ok := NICCountersAt(h.procRoot); ok {
		now := time.Now()
		p := &h.prevNet
		if p.iface == iface && rx >= p.rx && tx >= p.tx {
			if dt := now.Sub(p.at).Seconds(); dt > 0 {
				m.NetRxBps = uint64(float64(rx-p.rx) * 8 / dt)
				m.NetTxBps = uint64(float64(tx-p.tx) * 8 / dt)
			}
		}
		p.iface, p.rx, p.tx, p.at = iface, rx, tx, now
	}
	return m
}

func baselineUDPBufferValue(procSys, key string) uint64 {
	b, err := os.ReadFile(filepath.Join(procSys, key))
	if err != nil {
		return UDPBufferMinBytes
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || value < UDPBufferMinBytes {
		return UDPBufferMinBytes
	}
	return value
}

func (h *linuxHost) ApplyBaseline(ctx context.Context) error {
	var errs []error
	rmemMax := baselineUDPBufferValue(h.procSys, "net/core/rmem_max")
	wmemMax := baselineUDPBufferValue(h.procSys, "net/core/wmem_max")
	if _, err := writeIfChanged(h.sysctlFile, sysctlFileBodyWithBuffers(rmemMax, wmemMax), 0o644); err != nil {
		errs = append(errs, err)
	}
	// Apply now without the sysctl binary. On OpenVZ/LXC these files are read-only or absent.
	for _, kv := range [][2]string{
		{"net/core/default_qdisc", "fq"},
		{"net/ipv4/tcp_congestion_control", "bbr"},
		{"net/core/rmem_max", strconv.FormatUint(rmemMax, 10)},
		{"net/core/wmem_max", strconv.FormatUint(wmemMax, 10)},
	} {
		p := filepath.Join(h.procSys, kv[0])
		if cur, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(cur)) == kv[1] {
			continue
		}
		if err := os.WriteFile(p, []byte(kv[1]), 0o644); err != nil {
			errs = append(errs, fmt.Errorf("sysctl %s=%s: %w", strings.ReplaceAll(kv[0], "/", "."), kv[1], err))
		}
	}
	changed, err := writeIfChanged(h.journaldFile, journaldFileBody, 0o644)
	if err != nil {
		errs = append(errs, err)
	} else if changed {
		// journald only reads drop-ins on start; a restart is brief and only happens when the file changed.
		if out, err := h.run(ctx, "", "systemctl", "restart", "systemd-journald"); err != nil {
			errs = append(errs, fmt.Errorf("restart journald: %w: %s", err, bytes.TrimSpace(out)))
		}
	}
	// The SSH guard goes in last: its failure (no nft, no kernel support) must not skip the steps above.
	h.fw.Lock()
	h.sshPorts = h.detectSSHPorts(ctx)
	err = h.applyFirewall(ctx)
	h.fw.Unlock()
	if err != nil {
		errs = append(errs, fmt.Errorf("ssh guard: %w", err))
	}
	return errors.Join(errs...)
}

func (h *linuxHost) SSHPorts() []uint16 {
	h.fw.Lock()
	defer h.fw.Unlock()
	return append([]uint16(nil), h.sshPorts...)
}

func (h *linuxHost) SetPortHops(ctx context.Context, hops []Hop) error {
	h.fw.Lock()
	defer h.fw.Unlock()
	if _, err := RenderRuleset(hops, h.sshPorts); err != nil { // reject before anything is sent to nft
		return err
	}
	prev := h.hops
	h.hops = append([]Hop(nil), hops...)
	if err := h.applyFirewall(ctx); err != nil {
		h.hops = prev
		return err
	}
	return nil
}

// applyFirewall replaces the agent's table with the current hops and SSH guard. Caller holds h.fw.
func (h *linuxHost) applyFirewall(ctx context.Context) error {
	script, err := RenderRuleset(h.hops, h.sshPorts)
	if err != nil {
		return err
	}
	return h.nft(ctx, script, len(h.hops) == 0 && len(h.sshPorts) == 0)
}

// detectSSHPorts finds where sshd really listens: `sshd -T` (the effective config, Include files and
// Match-less defaults resolved) or, when that cannot run, the Port lines of /etc/ssh/sshd_config and
// sshd_config.d; plus the ListenStream ports of a socket-activated ssh (Ubuntu 22.10+ ignores Port
// there). 22 when nothing is found: a guard on the default port beats no guard.
func (h *linuxHost) detectSSHPorts(ctx context.Context) []uint16 {
	var ports []uint16
	for _, bin := range []string{"sshd", "/usr/sbin/sshd"} {
		out, err := h.run(ctx, "", bin, "-T")
		if err == nil {
			ports = portsFrom(sshdTPortRe, string(out))
			break
		}
		var ee *exec.Error
		if !errors.As(err, &ee) { // it ran and refused: do not try another path, read the files
			break
		}
	}
	if len(ports) == 0 {
		files, _ := filepath.Glob(filepath.Join(h.sshdConfDir, "sshd_config.d", "*.conf"))
		for _, f := range append([]string{filepath.Join(h.sshdConfDir, "sshd_config")}, files...) {
			if b, err := os.ReadFile(f); err == nil {
				ports = append(ports, portsFrom(sshdConfPortRe, string(b))...)
			}
		}
	}
	if out, err := h.run(ctx, "", "systemctl", "show", "-p", "Listen", "ssh.socket", "sshd.socket"); err == nil {
		ports = append(ports, portsFrom(socketListenRe, string(out))...)
	}
	if ports = normalizePorts(ports); len(ports) == 0 {
		return []uint16{22}
	}
	return ports
}

// nft applies a script. When quiet (cleanup) a missing nft binary is fine: nothing could have been installed.
func (h *linuxHost) nft(ctx context.Context, script string, quiet bool) error {
	out, err := h.run(ctx, script, "nft", "-f", "-")
	if err != nil {
		var ee *exec.Error // the binary itself is missing
		if errors.As(err, &ee) {
			if quiet {
				return nil
			}
			return fmt.Errorf("nft is not installed: %w", err)
		}
		return fmt.Errorf("nft: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func (h *linuxHost) Cleanup(ctx context.Context) error {
	errs := []error{h.SetTorrentGuard(ctx, nil, nil), h.cleanupTunnels(ctx)} // torrent table, tunnel table and our links
	errs = append(errs, h.SyncInboundUDPPorts(ctx, nil))                     // only UFW rules with Mistgate's exact ownership tag
	errs = append(errs, h.removeProvisionUFWRules(ctx))                      // and the 80/443 rules the SSH install tagged
	script, _ := RenderRuleset(nil, nil)
	h.fw.Lock()
	h.hops, h.sshPorts = nil, nil
	errs = append(errs, h.nft(ctx, script, true))
	h.fw.Unlock()
	if err := os.Remove(h.sysctlFile); err != nil && !os.IsNotExist(err) {
		errs = append(errs, err)
	}
	// journald only reads drop-ins on start (ApplyBaseline): restart it when its drop-in really went, or the cap stays.
	switch err := os.Remove(h.journaldFile); {
	case err == nil:
		if out, err := h.run(ctx, "", "systemctl", "restart", "systemd-journald"); err != nil {
			errs = append(errs, fmt.Errorf("restart journald: %w: %s", err, bytes.TrimSpace(out)))
		}
	case !os.IsNotExist(err):
		errs = append(errs, err)
	}
	errs = append(errs, h.undoResolver(ctx)) // the resolver fix, if one was ever applied
	return errors.Join(errs...)
}

// writeIfChanged writes body atomically unless the file already holds it.
func writeIfChanged(path, body string, mode os.FileMode) (bool, error) {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == body {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), mode); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp, mode); err != nil { // the unit runs with umask 0077; these files are meant to be 0644
		os.Remove(tmp)
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, nil
}
