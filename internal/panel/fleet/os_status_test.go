package fleet

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"github.com/mistgate/mistgate/internal/panel/store"
	"google.golang.org/protobuf/proto"
)

func TestOsStatusSettingNeedsCapabilityAndPreservesOldSignature(t *testing.T) {
	n := store.NodeRow{DialTimeoutS: 30, CountryCode: "DE"}
	old := nodeSettings(n, nil)
	if got := settingsSig(old); got != "10 20 45 30 []DE" {
		t.Fatalf("legacy settings signature = %q", got)
	}
	without := nodeSettingsFor(n, []string{capOSStatus}, false)
	if without.GetOsStatus() || settingsSig(without) != settingsSig(old) {
		t.Fatalf("old panel settings changed: %+v, signature %q", without, settingsSig(without))
	}
	with := nodeSettingsFor(n, []string{capOSStatus}, true)
	if !with.GetOsStatus() || settingsSig(with) == settingsSig(old) {
		t.Fatalf("capable and requested settings = %+v, signature %q", with, settingsSig(with))
	}
	uncapable := nodeSettingsFor(n, nil, true)
	if uncapable.GetOsStatus() || settingsSig(uncapable) != settingsSig(old) {
		t.Fatalf("status was sent without capability: %+v", uncapable)
	}
}

func TestOsStatusValidationBoundsUntrustedFieldsAndRejectsAddresses(t *testing.T) {
	now := time.Unix(1794000000, 0)
	base := &agentv1.OsStatus{MeasuredUnix: now.Unix(), DistroId: "ubuntu", DistroVersion: "24.04", Pending: 2, Security: 1,
		Notable: []string{"openssl 3.0.0 3.0.1 s"}, KernelRunning: "6.8.0-1-generic", KernelNewest: "6.8.0-2-generic",
		Apt: "ok", AutoUpdates: "on", BootIdHash: "0123456789abcdef"}
	if !validOsStatus(base, now) {
		t.Fatal("valid fixture snapshot was rejected")
	}
	for _, tc := range []struct {
		name   string
		change func(*agentv1.OsStatus)
	}{
		{"IPv4 distro", func(s *agentv1.OsStatus) { s.DistroId = "203.0.113.77" }},
		{"IPv6 version", func(s *agentv1.OsStatus) { s.DistroVersion = "release-2001:db8::77" }},
		{"IPv4 kernel", func(s *agentv1.OsStatus) { s.KernelRunning = netip.MustParseAddr("198.51.100.7").String() }},
		{"address package", func(s *agentv1.OsStatus) { s.Held = []string{"203.0.113.77"} }},
		{"invalid apt state", func(s *agentv1.OsStatus) { s.Apt = "apt says 203.0.113.77" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := proto.Clone(base).(*agentv1.OsStatus)
			tc.change(candidate)
			if validOsStatus(candidate, now) {
				t.Fatalf("invalid status was accepted: %+v", candidate)
			}
		})
	}
}

// Ubuntu 22.04 kernel meta-packages are versioned 5.15.0.<abi>.<upload>, a dotted quad that is a legal Debian version.
// One such entry must never cost the node its whole snapshot.
func TestOsStatusKeepsDottedQuadPackageVersions(t *testing.T) {
	now := time.Unix(1794000000, 0)
	for _, notable := range []string{
		"linux-image-generic 5.15.0.125.123 5.15.0.126.125 s",
		"linux-generic 5.15.0.72.70 5.15.0.186.166 s",
		"linux-image-virtual 5.15.0.125.123 5.15.0.126.125 s",
		"linux-image-kvm 5.15.0.1070.66 5.15.0.1071.67 s",
		"openssl 203.0.113.77 3.0.1 -",
	} {
		s := osStatusFixture(now)
		s.Notable = []string{"libssl3 3.0.2-0ubuntu1.18 3.0.2-0ubuntu1.19 s", notable}
		if !validOsStatus(s, now) || len(s.Notable) != 2 || s.Notable[1] != notable {
			t.Errorf("entry %q: snapshot rejected or entry dropped (notable %q)", notable, s.Notable)
		}
	}
}

// A malformed entry is dropped on its own; the rest of the snapshot, the other entries included, is stored.
func TestOsStatusDropsOneBadNotableEntry(t *testing.T) {
	now := time.Unix(1794000000, 0)
	good := "libc6 2.35-0ubuntu3.8 2.35-0ubuntu3.9 s"
	for name, bad := range map[string]string{
		"newline":          "libc6\n2.35\t2.36   s",
		"unicode spaces":   "libc6\u20282.35\u00a02.36\u3000s",
		"padded":           " libc6 2.35 2.36 s\r\n",
		"double space":     "libc6  2.35 2.36 s",
		"three fields":     "libc6 2.35 s",
		"upper case":       "LIBC6 2.35 2.36 s",
		"address package":  "203.0.113.77 2.35 2.36 s",
		"bad version char": "libc6 2.35;rm 2.36 s",
		"bad flag":         "libc6 2.35 2.36 x",
	} {
		t.Run(name, func(t *testing.T) {
			s := osStatusFixture(now)
			s.Notable = []string{bad, good}
			if !validOsStatus(s, now) {
				t.Fatal("one bad notable entry rejected the whole snapshot")
			}
			if len(s.Notable) != 1 || s.Notable[0] != good {
				t.Fatalf("notable = %q, want only the good entry", s.Notable)
			}
		})
	}
}

// Debian testing and sid have no VERSION_ID, and a failed os-release read leaves the distribution empty. Both are stored.
func TestOsStatusAcceptsEmptyDistroFields(t *testing.T) {
	now := time.Unix(1794000000, 0)
	s := osStatusFixture(now)
	s.DistroId, s.DistroVersion = "debian", ""
	if !validOsStatus(s, now) {
		t.Fatal("a snapshot without VERSION_ID was rejected")
	}
	failed := &agentv1.OsStatus{MeasuredUnix: now.Unix(), Apt: "unknown", AutoUpdates: "unknown", Error: "read_failed"}
	if !validOsStatus(failed, now) {
		t.Fatal("a snapshot of a failed os-release read was rejected")
	}
}

func osStatusFixture(now time.Time) *agentv1.OsStatus {
	return &agentv1.OsStatus{MeasuredUnix: now.Unix(), DistroId: "ubuntu", DistroVersion: "22.04", Pending: 4, Security: 4,
		KernelRunning: "5.15.0-125-generic", KernelNewest: "5.15.0-126-generic", Apt: "ok", AutoUpdates: "on", BootIdHash: "0123456789abcdef"}
}

// The job of S2 is part of the snapshot. A good one is stored; one that does not check out is dropped on its own, and the
// snapshot around it is kept.
func TestOsStatusJobIsValidatedAndDroppedAlone(t *testing.T) {
	now := time.Unix(1794000000, 0)
	good := func() *agentv1.OsJob {
		return &agentv1.OsJob{JobId: "osj_ab12", Action: "upgrade", Mode: "security", State: "failed", Phase: "rebooting",
			StartedUnix: now.Unix() - 600, UpdatedUnix: now.Unix(), FinishedUnix: now.Unix(), Code: "awg_module_missing",
			Reason: "reboot refused: the AmneziaWG module is not built for kernel 7.0.0-38-generic and AmneziaWG runs on the kernel module",
			Reboot: "held", HeldReason: "awg_module_missing", Upgraded: 12, KernelFrom: "7.0.0-37-generic", KernelTo: "7.0.0-38-generic",
			SourcesFailed: []string{"ppa.example.org", "a source"}}
	}
	s := osStatusFixture(now)
	s.Job = good()
	if !validOsStatus(s, now) || s.Job == nil {
		t.Fatalf("a valid job was dropped or rejected: %+v", s.Job)
	}
	for name, change := range map[string]func(*agentv1.OsJob){
		"job id":            func(j *agentv1.OsJob) { j.JobId = "osj_a;b" },
		"empty job id":      func(j *agentv1.OsJob) { j.JobId = "" },
		"action":            func(j *agentv1.OsJob) { j.Action = "format" },
		"mode":              func(j *agentv1.OsJob) { j.Mode = "dist-upgrade" },
		"state":             func(j *agentv1.OsJob) { j.State = "paused" },
		"phase":             func(j *agentv1.OsJob) { j.Phase = "sleeping" },
		"code":              func(j *agentv1.OsJob) { j.Code = "boom" },
		"held reason":       func(j *agentv1.OsJob) { j.HeldReason = "boom" },
		"reboot":            func(j *agentv1.OsJob) { j.Reboot = "yes" },
		"address in reason": func(j *agentv1.OsJob) { j.Reason = "refused by 203.0.113.9" },
		"newline in reason": func(j *agentv1.OsJob) { j.Reason = "one" + string(rune(10)) + "two" },
		"long reason":       func(j *agentv1.OsJob) { j.Reason = string(make([]byte, 301)) },
		"future time":       func(j *agentv1.OsJob) { j.UpdatedUnix = now.Unix() + 3600 },
		"negative time":     func(j *agentv1.OsJob) { j.StartedUnix = -1 },
		"many sources":      func(j *agentv1.OsJob) { j.SourcesFailed = make([]string, 9) },
		"source address":    func(j *agentv1.OsJob) { j.SourcesFailed = []string{"203.0.113.9"} },
		"kernel":            func(j *agentv1.OsJob) { j.KernelTo = "7 0;rm" },
	} {
		t.Run(name, func(t *testing.T) {
			s := osStatusFixture(now)
			j := good()
			change(j)
			s.Job = j
			if !validOsStatus(s, now) {
				t.Fatal("a bad job rejected the whole snapshot")
			}
			if s.Job != nil {
				t.Fatalf("the bad job was kept: %+v", s.Job)
			}
		})
	}
}

// UpdateOs is a request like the others: the session core knows it, and the panel sends it only to a stream that listed
// os-update/1 (the read-only os-status/1 is not enough). Nothing in the panel sends it yet.
func TestUpdateOsFrameNeedsTheWriteCapability(t *testing.T) {
	frame := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateOs{UpdateOs: &agentv1.UpdateOs{RequestId: "r"}}}
	if capability, message := requestCapability(frame, false); capability != "os-update/1" || message != "agent too old" {
		t.Fatalf("capability = %q %q", capability, message)
	}
	if capability, _ := requestCapability(frame, false); capability == capOSStatus {
		t.Fatal("the read-only capability must not allow the write command")
	}
	if requestKind(frame) != PendingCommand {
		t.Fatalf("request kind = %v", requestKind(frame))
	}
}

// Every code and phase the node can write is accepted by the panel (the lists come from the node's own package), so a new code
// of a job never costs a node its job in the snapshot.
func TestOsStatusJobAcceptsEveryCodeAndPhaseOfTheNode(t *testing.T) {
	now := time.Unix(1794000000, 0)
	for _, code := range osupdate.Codes {
		j := &agentv1.OsJob{JobId: "osj_x", Action: "upgrade", Mode: "all", State: "failed", Code: code, HeldReason: code, Reboot: "held", Reason: "a sentence"}
		if !validOsJob(j, now) {
			t.Errorf("code %q is not accepted", code)
		}
	}
	for _, phase := range osupdate.Phases {
		j := &agentv1.OsJob{JobId: "osj_x", Action: "upgrade", Mode: "all", State: "running", Phase: phase}
		if !validOsJob(j, now) {
			t.Errorf("phase %q is not accepted", phase)
		}
	}
}

func TestOsStatusKernelTableBounds(t *testing.T) {
	now := time.Unix(1794000000, 0)
	row := func(v string) *agentv1.OsKernel {
		return &agentv1.OsKernel{Version: v, Image: true, Initrd: true, GrubEntry: true, Headers: "present", Module: "built", Packages: "ok"}
	}
	good := func() *agentv1.OsStatus {
		s := osStatusFixture(now)
		a, b := row("6.8.0-2-generic"), row("6.8.0-1-generic")
		a.NextBoot, b.Running = true, true
		s.Kernels, s.RebootBlockers, s.AwgSource = []*agentv1.OsKernel{a, b}, []string{"initramfs_missing", "boot_entry_missing"}, "unknown:0123456789ab"
		return s
	}
	if s := good(); !validOsStatus(s, now) || len(s.Kernels) != 2 || len(s.RebootBlockers) != 2 || s.AwgSource == "" {
		t.Fatalf("a good table was dropped: %+v", s)
	}
	nine := func(s *agentv1.OsStatus) {
		s.Kernels = nil
		for i := 0; i < 9; i++ {
			s.Kernels = append(s.Kernels, row("6.8.0-"+string(rune('1'+i))+"-generic"))
		}
	}
	for name, change := range map[string]func(*agentv1.OsStatus){
		"nine kernels":            nine,
		"version with a space":    func(s *agentv1.OsStatus) { s.Kernels[0].Version = "6.8.0 x" },
		"version that is an IP":   func(s *agentv1.OsStatus) { s.Kernels[0].Version = "203.0.113.77" },
		"same kernel twice":       func(s *agentv1.OsStatus) { s.Kernels[1].Version = s.Kernels[0].Version },
		"unknown headers word":    func(s *agentv1.OsStatus) { s.Kernels[0].Headers = "maybe" },
		"unknown module word":     func(s *agentv1.OsStatus) { s.Kernels[0].Module = "ready" },
		"unknown packages word":   func(s *agentv1.OsStatus) { s.Kernels[0].Packages = "" },
		"two running kernels":     func(s *agentv1.OsStatus) { s.Kernels[0].Running = true },
		"two next boots":          func(s *agentv1.OsStatus) { s.Kernels[1].NextBoot = true },
		"blocker not in the list": func(s *agentv1.OsStatus) { s.RebootBlockers = []string{"too_soon"} },
		"blocker with free text":  func(s *agentv1.OsStatus) { s.RebootBlockers = []string{"call 203.0.113.77"} },
		"a blocker twice":         func(s *agentv1.OsStatus) { s.RebootBlockers = []string{"kernel_older", "kernel_older"} },
		"too many blockers": func(s *agentv1.OsStatus) {
			s.RebootBlockers = append(slices.Clone(osupdate.RebootBlockers), "kernel_older")
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := good()
			change(s)
			if !validOsStatus(s, now) {
				t.Fatal("a bad table cost the node its whole snapshot")
			}
			if len(s.Kernels) != 0 || len(s.RebootBlockers) != 0 {
				t.Fatalf("a bad table was kept: %v %v", s.Kernels, s.RebootBlockers)
			}
		})
	}
	s := good()
	s.AwgSource = "needs a patch"
	if !validOsStatus(s, now) || s.AwgSource != "" || len(s.Kernels) != 2 {
		t.Fatalf("a bad awg_source must clear only itself: %+v", s)
	}
	for _, ok := range []string{"", "ok", "patched", "needs_patch", "unknown:0123456789ab"} {
		if !osAwgSource.MatchString(ok) {
			t.Errorf("awg_source %q refused", ok)
		}
	}
	for _, code := range osupdate.RebootBlockers {
		if !slices.Contains(osupdate.Codes, code) {
			t.Errorf("blocker %q is not a job code", code)
		}
	}
}
