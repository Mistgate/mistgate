package fleet

import (
	"net/netip"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
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
		{"S1 job", func(s *agentv1.OsStatus) { s.Job = &agentv1.OsJob{Reason: "future job"} }},
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
