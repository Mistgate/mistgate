package main

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/mistgate/mistgate/internal/node/update"
)

const (
	unitName    = "mistgate-node.service"
	defaultBin  = "/usr/local/bin/mistgate-node"
	minGoMemMiB = 64
	// unitGeneration is written into the unit as MISTGATE_UNIT_GEN. Generation 2 adds the
	// crash-loop guard and makes the binary's directory writable so the agent can update itself; generation 1 units
	// (nodes installed before self-update) cannot, and show up in the panel as "update by hand once".
	// Generation 3 is the L3 protocols: /dev/net/tun is reachable (PrivateDevices=no with exactly that
	// device allowed), which the userspace AmneziaWG and WARP backends need, and ExecStopPost removes what a crashed
	// agent leaves behind (the mgawg* and mgwarp interfaces, the tunnel and WARP nft tables, the WARP routes and rules).
	// The agent never rewrites its own unit: a node on generation 2 keeps working (the kernel backends need nothing new),
	// lists "unit/3" only from generation 3, and its awg_backend doctor check says "unit_outdated" when the userspace
	// backend has no /dev/net/tun; the owner runs `mistgate-node install` once.
	unitGeneration = 3
)

// safePath keeps anything that could break out of a unit-file line (spaces, quotes, newlines, %, $) out
// of the paths we interpolate.
var safePath = regexp.MustCompile(`^/[A-Za-z0-9_./-]*$`)

// renderUnit returns the hardened systemd unit. ramBytes sizes the memory limits (the memory budget:
// GOMEMLIMIT about 60% of RAM, MemoryHigh a bit above it); 0 means unknown and assumes 1 GiB.
//
// The agent runs as root but with a capability bounding set of exactly what it needs: CAP_NET_ADMIN
// (nftables, sysctl) and CAP_NET_BIND_SERVICE (TCP 443 for the masquerade and ACME). Root is deliberate:
// the host baseline lives in root-owned /etc/sysctl.d and /etc/systemd/journald.conf.d, and a non-root
// user could not write those or ask systemd to restart journald. ProtectSystem=strict makes everything
// else read-only; only the state directory and those two drop-in directories are writable.
// ProtectKernelTunables stays off on purpose: the fq + bbr baseline is applied through /proc/sys.
//
// Self-update (generation 2): the directory of the binary is writable (the agent puts <bin>.new and <bin>.prev next to
// it and renames over itself), and ExecStartPre runs update.GuardScript, which restores <bin>.prev when a freshly
// updated build crashes three times in a row. Nothing else loosens.
func renderUnit(bin, stateDir string, ramBytes uint64) (string, error) {
	for _, p := range []string{bin, stateDir} {
		if !safePath.MatchString(p) || strings.Contains(p, "..") {
			return "", fmt.Errorf("unsafe path %q (absolute, letters, digits and _ . / - only)", p)
		}
	}
	if stateDir == "/" {
		return "", errors.New("state dir cannot be /")
	}
	binDir := path.Dir(bin)
	if binDir == "/" {
		return "", errors.New("binary cannot sit directly in /: its directory becomes writable for the agent")
	}
	if ramBytes == 0 {
		ramBytes = 1 << 30
	}
	mib := ramBytes >> 20
	goMem := max(mib*60/100, minGoMemMiB)
	high := max(mib*70/100, goMem+16)
	maxMem := max(mib*85/100, high+16)

	return fmt.Sprintf(`[Unit]
Description=Mistgate node agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%[1]s run --state-dir %[2]s
%[6]s
# Whatever the stop was (a crash included) the interfaces and the WARP routes of the agent go: an orphaned
# "unreachable default" would black-hole every socket bound to the WARP device. The "-" ignores a failure.
ExecStopPost=-%[1]s cleanup-net
Environment=MISTGATE_UNIT_GEN=%[7]d
Restart=on-failure
RestartSec=5
# 78 = not enrolled (or retired): restarting cannot help.
RestartPreventExitStatus=78
TimeoutStopSec=30

# Memory: the Go runtime is told the soft limit, systemd throttles a little above it and kills
# only well above both.
Environment=GOMEMLIMIT=%[3]dMiB
MemoryHigh=%[4]dM
MemoryMax=%[5]dM
LimitNOFILE=65536

# Hardening.
NoNewPrivileges=yes
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
ProtectSystem=strict
ReadWritePaths=%[2]s %[8]s /etc/sysctl.d /etc/systemd/journald.conf.d
ProtectHome=yes
PrivateTmp=yes
# /dev/net/tun for the userspace AmneziaWG and WARP backends: the real /dev, but the device policy lets the service open
# nothing except the standard pseudo devices and the tun clone device.
PrivateDevices=no
DevicePolicy=closed
DeviceAllow=/dev/net/tun rw
ProtectClock=yes
ProtectControlGroups=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectHostname=yes
LockPersonality=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
RestrictNamespaces=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
SystemCallArchitectures=native
SystemCallFilter=@system-service
UMask=0077

[Install]
WantedBy=multi-user.target
`, bin, stateDir, goMem, high, maxMem, update.UnitGuardLine(bin, stateDir), unitGeneration, binDir), nil
}
