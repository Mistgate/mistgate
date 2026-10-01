package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/node/update"
)

func TestRenderUnit(t *testing.T) {
	u, err := renderUnit("/usr/local/bin/mistgate-node", "/var/lib/mistgate-node", 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ExecStart=/usr/local/bin/mistgate-node run --state-dir /var/lib/mistgate-node\n",
		"CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE\n",
		"NoNewPrivileges=yes\n", "ProtectSystem=strict\n", "PrivateTmp=yes\n",
		"ReadWritePaths=/var/lib/mistgate-node /usr/local/bin /etc/sysctl.d /etc/systemd/journald.conf.d\n",
		"RestartPreventExitStatus=78\n", "Restart=on-failure\n", "RestartSec=5\n",
		// Unit generation 2: the three lines that let a node update itself.
		// Generation 3: /dev/net/tun for the userspace backends, ExecStopPost cleanup.
		"Environment=MISTGATE_UNIT_GEN=3\n",
		"PrivateDevices=no\n", "DevicePolicy=closed\n", "DeviceAllow=/dev/net/tun rw\n",
		"ExecStopPost=-/usr/local/bin/mistgate-node cleanup-net\n",
		"ExecStartPre=-/bin/sh -c '" + strings.ReplaceAll(update.GuardScript, "$", "$$") + "' mistgate-guard /usr/local/bin/mistgate-node /var/lib/mistgate-node\n",
		"Environment=GOMEMLIMIT=614MiB\n", // 60% of 1024 MiB
		"MemoryHigh=716M\n", "MemoryMax=870M\n",
		"WantedBy=multi-user.target\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q", want)
		}
	}
	// ProtectKernelTunables would break the fq + bbr baseline, which is applied through /proc/sys.
	if strings.Contains(u, "ProtectKernelTunables=yes") {
		t.Error("ProtectKernelTunables must stay off")
	}
}

// A $ left single in the unit would be expanded by systemd and break the guard; a newline would end the directive.
func TestUnitGuardSurvivesSystemdParsing(t *testing.T) {
	u, err := renderUnit("/usr/local/bin/mistgate-node", "/var/lib/mistgate-node", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(u, "\n") {
		if !strings.HasPrefix(line, "ExecStartPre=") {
			continue
		}
		rest := strings.ReplaceAll(line, "$$", "")
		if strings.Contains(rest, "$") || strings.ContainsAny(line, "\\%") {
			t.Errorf("guard line has a raw $, backslash or percent sign: %s", line)
		}
		if strings.Count(line, "'") != 2 {
			t.Errorf("guard script must be one single-quoted word: %s", line)
		}
		return
	}
	t.Fatal("no ExecStartPre line")
}

func TestRenderUnitRefusesBinaryInRoot(t *testing.T) {
	if _, err := renderUnit("/mistgate-node", "/var/lib/mistgate-node", 0); err == nil {
		t.Fatal("ReadWritePaths=/ would open the whole filesystem")
	}
}

func TestRenderUnitMemoryScaling(t *testing.T) {
	u, _ := renderUnit("/opt/b", "/s", 512<<20)
	if !strings.Contains(u, "GOMEMLIMIT=307MiB") {
		t.Errorf("512 MiB host: %s", u)
	}
	u, _ = renderUnit("/opt/b", "/s", 64<<20) // tiny host: never below the floor, limits keep their order
	if !strings.Contains(u, "GOMEMLIMIT=64MiB") || !strings.Contains(u, "MemoryHigh=80M") || !strings.Contains(u, "MemoryMax=96M") {
		t.Errorf("64 MiB host: %s", u)
	}
	u, _ = renderUnit("/opt/b", "/s", 0) // unknown RAM assumes 1 GiB
	if !strings.Contains(u, "GOMEMLIMIT=614MiB") {
		t.Errorf("unknown RAM: %s", u)
	}
}

func TestRenderUnitRejectsUnsafePaths(t *testing.T) {
	for _, bad := range []string{"", "relative/path", "/a b", "/a\nExecStartPre=/bin/evil", "/a;b", "/a%h", "/a$b", "/a/../b", "/a\"b", "/"} {
		if _, err := renderUnit("/usr/local/bin/mistgate-node", bad, 0); err == nil {
			t.Errorf("state dir %q accepted", bad)
		}
		if bad != "/" {
			if _, err := renderUnit(bad, "/var/lib/x", 0); err == nil {
				t.Errorf("bin %q accepted", bad)
			}
		}
	}
}

func TestDispatch(t *testing.T) {
	if got := dispatch(nil); got != 2 {
		t.Errorf("no args: %d", got)
	}
	if got := dispatch([]string{"frobnicate"}); got != 2 {
		t.Errorf("unknown command: %d", got)
	}
	if got := dispatch([]string{"version"}); got != 0 {
		t.Errorf("version: %d", got)
	}
	if got := dispatch([]string{"enroll"}); got != 2 {
		t.Errorf("enroll without flags: %d", got)
	}
	if got := dispatch([]string{"enroll", "--panel", "p:1", "--sni", "x.invalid", "--ca-sha256", "zz", "--token", "t", "--state-dir", filepath.Join(t.TempDir(), "s")}); got != 1 {
		t.Errorf("enroll with a bad pin: %d", got)
	}
}

func TestRunWithoutEnrollmentExitsWithConfigError(t *testing.T) {
	if got := dispatch([]string{"run", "--state-dir", t.TempDir()}); got != exitNotEnrolled {
		t.Errorf("run on an empty state dir: %d", got)
	}
	if got := dispatch([]string{"run", "--state-dir", t.TempDir(), "--log-level", "loud"}); got != 2 {
		t.Errorf("bad log level: %d", got)
	}
}

func TestTokenCanComeFromTheEnvironment(t *testing.T) {
	t.Setenv("MISTGATE_ENROLL_TOKEN", "from-env")
	// Fails later (bad pin), but not with the "flags required" exit code 2: the token was found.
	got := dispatch([]string{"enroll", "--panel", "p:1", "--sni", "x.invalid", "--ca-sha256", "zz", "--state-dir", filepath.Join(t.TempDir(), "s")})
	if got != 1 {
		t.Errorf("exit %d", got)
	}
}
