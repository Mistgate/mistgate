package osupdate

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func TestParseDpkgTable(t *testing.T) {
	out := "ii \tlibc6:amd64\t2.40-1\niF \tlinux-generic\t7.0.0.38\niH \tlinux-headers-7.0.0-38-generic\t7.0.0-38.38\n" +
		"iU \tfoo\t1\ntt \tbar\t1\niW \tbaz\t1\nrc \told\t1\nhi \theld-pkg\t1\nii \tBad Name\t1\nnonsense\n"
	versions, half := parseDpkgTable(out)
	if want := []string{"bar", "baz", "foo", "linux-generic", "linux-headers-7.0.0-38-generic"}; !slices.Equal(half, want) {
		t.Fatalf("half = %v, want %v", half, want)
	}
	if versions["libc6"] != "2.40-1" || versions["held-pkg"] != "1" || versions["old"] != "" || versions["linux-generic"] != "" {
		t.Fatalf("versions = %v", versions)
	}
}

func TestClassifyUpdate(t *testing.T) {
	out := "Hit:1 http://archive.example.net/ubuntu resolute InRelease\n" +
		"Err:2 http://security.example.net/ubuntu resolute-security InRelease\n  Could not connect\n" +
		"Err:3 https://deb.example.org/debian bookworm-security InRelease\n" +
		"Err:4 https://ppa.example.org/amnezia resolute InRelease\n" +
		"Err:5 http://203.0.113.9/repo stable InRelease\n" +
		"Err:6 http://[2001:db8::1]/repo stable Release\n" +
		"Err:7\nErr: x\n"
	sec, other := classifyUpdate(out)
	if !slices.Equal(sec, []string{"deb.example.org", "security.example.net"}) {
		t.Fatalf("security = %v", sec)
	}
	if !slices.Equal(other, []string{"a source", "ppa.example.org"}) {
		t.Fatalf("other = %v", other)
	}
}

func TestFailedPackage(t *testing.T) {
	for out, want := range map[string]string{
		"dpkg: error processing package foo-bar (--configure):": "foo-bar",
		"dpkg: error processing package libx:amd64 (--install)": "libx",
		"dpkg: error processing package ;rm -rf (--configure):": "",
		"nothing": "",
	} {
		if got := failedPackage(out); got != want {
			t.Errorf("%q -> %q, want %q", out, got, want)
		}
	}
}

func TestDkmsStatusInstalled(t *testing.T) {
	const k = "7.0.0-38-generic"
	if !dkmsStatusInstalled("amneziawg/3.1, "+k+", x86_64: installed\n", k) {
		t.Error("installed was not seen")
	}
	for _, out := range []string{"", "amneziawg/3.1, " + k + ", x86_64: built\n", "amneziawg/3.1, 7.0.0-37-generic, x86_64: installed\n",
		"amneziawg/3.1, " + k + ", x86_64: installed (WARNING! Diff between built and installed module!)\n"} {
		if dkmsStatusInstalled(out, k) {
			t.Errorf("%q must not count as installed", out)
		}
	}
}

func FuzzParseDpkgTable(f *testing.F) {
	f.Add("ii \tlibc6\t1\niF \tx\t2\n")
	f.Add("\t\t\n\x00\xff\n")
	f.Fuzz(func(t *testing.T, in string) {
		versions, half := parseDpkgTable(in)
		if len(half) > maxPackages {
			t.Fatalf("half has %d entries", len(half))
		}
		for _, n := range append(half, keys(versions)...) {
			if !validPackageName(n) {
				t.Fatalf("invalid package name %q", n)
			}
		}
	})
}

func FuzzClassifyUpdate(f *testing.F) {
	f.Add("Err:2 http://security.example.net/ubuntu resolute-security InRelease\n")
	f.Add("Err:1 http://203.0.113.9/x y z\nErr:2 http://[::1]/ a b\n")
	f.Fuzz(func(t *testing.T, in string) {
		sec, other := classifyUpdate(in)
		if len(sec) > 8 || len(other) > 8 {
			t.Fatal("too many hosts")
		}
		for _, h := range append(sec, other...) {
			if _, err := netip.ParseAddr(h); err == nil {
				t.Fatalf("an address was reported: %q", h)
			}
			if h != "a source" && !hostName.MatchString(h) {
				t.Fatalf("invalid host %q", h)
			}
			if h != "a source" && strings.ContainsAny(h, " \n\t:/") {
				t.Fatalf("host %q carries more than a name", h)
			}
		}
	})
}

func FuzzFailedPackage(f *testing.F) {
	f.Add("dpkg: error processing package foo (--configure):")
	f.Fuzz(func(t *testing.T, in string) {
		if p := failedPackage(in); p != "" && !validPackageName(p) {
			t.Fatalf("invalid package %q", p)
		}
	})
}

func FuzzParseSpec(f *testing.F) {
	f.Add("upgrade", "security", "if-required", "osj_a")
	f.Add("reboot", "", "always", "manual-1")
	f.Fuzz(func(t *testing.T, action, mode, reboot, id string) {
		spec, ref := ParseSpec(action, mode, reboot, id)
		if ref != nil {
			if ref.Code != CodeBadRequest {
				t.Fatalf("refusal code %q", ref.Code)
			}
			return
		}
		switch spec.Action {
		case ActionRefresh, ActionUpgrade, ActionReboot:
		default:
			t.Fatalf("accepted action %q", spec.Action)
		}
		if spec.Mode != "" && spec.Mode != ModeSecurity && spec.Mode != ModeAll {
			t.Fatalf("accepted mode %q", spec.Mode)
		}
		if spec.Reboot != RebootNever && spec.Reboot != RebootIfRequired && spec.Reboot != RebootAlways {
			t.Fatalf("accepted reboot %q", spec.Reboot)
		}
		if spec.JobID != "" && !ValidJobID(spec.JobID) {
			t.Fatalf("accepted job id %q", spec.JobID)
		}
	})
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
