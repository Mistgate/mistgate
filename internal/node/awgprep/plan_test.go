package awgprep

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func osr(id, like string) map[string]string {
	return map[string]string{"ID": id, "ID_LIKE": like, "PRETTY_NAME": id + " test"}
}

func cmds(steps []Step) []string {
	var out []string
	for _, s := range steps {
		out = append(out, strings.Join(s.Cmd, " "))
	}
	return out
}

const aptOpts = "apt-get -o DPkg::Lock::Timeout=1800 install -y --no-install-recommends "

func TestPlanUbuntu(t *testing.T) {
	steps, notes, err := Plan(Env{OSRelease: osr("ubuntu", "debian"), Kernel: "6.8.0-142-generic", CPUs: 2})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(cmds(steps), "\n")
	for _, want := range []string{
		"apt-get -o DPkg::Lock::Timeout=1800 update",
		aptOpts + "software-properties-common python3-launchpadlib gnupg2 linux-headers-6.8.0-142-generic",
		"add-apt-repository -y ppa:amnezia/ppa",
		aptOpts + "amneziawg",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan lacks %q:\n%s", want, got)
		}
	}
	if last := cmds(steps)[len(steps)-1]; last != "modprobe amneziawg" {
		t.Errorf("the plan must end with the load, not %q", last)
	}
	if len(notes) == 0 || !strings.Contains(strings.Join(notes, " "), "DKMS") {
		t.Errorf("notes = %v", notes)
	}
}

func TestPlanDebianIsAPinnedSourceBuild(t *testing.T) {
	steps, notes, err := Plan(Env{OSRelease: osr("debian", ""), Kernel: "6.1.0-18-amd64", CPUs: 4})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(cmds(steps), "\n")
	for _, want := range []string{
		aptOpts + "git make gcc linux-headers-6.1.0-18-amd64",
		"git clone --depth 1 --branch " + pinnedModuleTag + " " + moduleRepo + " {src}",
		"make -C {src}/src KERNELDIR=/lib/modules/6.1.0-18-amd64/build -j4",
		"install -D -m 0644 {src}/src/amneziawg.ko /lib/modules/6.1.0-18-amd64/updates/amneziawg.ko",
		"depmod -a 6.1.0-18-amd64",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(strings.Join(notes, " "), "run this command again") {
		t.Errorf("a build without DKMS must say it needs repeating after a kernel upgrade: %v", notes)
	}
	// A derivative that says debian in ID_LIKE takes the same recipe; an unknown one has none.
	if _, _, err := Plan(Env{OSRelease: osr("raspbian", "debian"), Kernel: "6.1.0"}); err != nil {
		t.Errorf("debian derivative: %v", err)
	}
}

// Every apt command of a plan that installs is non-interactive-friendly: it waits for the dpkg lock and pulls no recommends.
func TestPlanInstallsNeverPullRecommends(t *testing.T) {
	for _, id := range []string{"ubuntu", "debian"} {
		steps, _, _ := Plan(Env{OSRelease: osr(id, ""), Kernel: "6.8.0", CPUs: 1})
		for _, c := range cmds(steps) {
			if strings.HasPrefix(c, "apt-get") && (!strings.Contains(c, "DPkg::Lock::Timeout=1800") || (strings.Contains(c, " install ") && !strings.Contains(c, "--no-install-recommends"))) {
				t.Errorf("%s: %q", id, c)
			}
		}
	}
}

func TestPackageManagerWaitIsBoundedAndSharedWithApt(t *testing.T) {
	if LockWait != 30*time.Minute || JobTimeout < LockWait+15*time.Minute {
		t.Fatalf("lock wait/job timeout = %s/%s, want a 30-minute lock wait plus build time", LockWait, JobTimeout)
	}
	if aptLockTimeoutSeconds != int(LockWait/time.Second) {
		t.Fatalf("apt lock timeout = %d seconds, want %d", aptLockTimeoutSeconds, int(LockWait/time.Second))
	}
}

func unsupported(t *testing.T, err error, code string) {
	t.Helper()
	var u *Unsupported
	if !errors.As(err, &u) || u.Code != code {
		t.Errorf("err = %v, want unsupported %q", err, code)
	}
}

func TestPlanRefusals(t *testing.T) {
	_, _, err := Plan(Env{OSRelease: osr("ubuntu", ""), Kernel: "6.8.0", Container: "lxc"})
	unsupported(t, err, CodeContainer)
	if err == nil || !strings.Contains(err.Error(), "container") {
		t.Errorf("a container: %v", err)
	}
	_, _, err = Plan(Env{OSRelease: osr("alpine", ""), Kernel: "6.6.0"})
	unsupported(t, err, CodeDistro)
	_, _, err = Plan(Env{OSRelease: osr("ubuntu", ""), Kernel: "6.8.0'; rm -rf /; '"})
	unsupported(t, err, CodeUnknownKernel) // an unsafe kernel string never reaches a package name
	_, _, err = Plan(Env{OSRelease: osr("ubuntu", "")})
	unsupported(t, err, CodeUnknownKernel)
	_, notes, _ := Plan(Env{OSRelease: osr("ubuntu", ""), Kernel: "6.8.0", SecureBoot: true})
	if !strings.Contains(strings.Join(notes, " "), "Secure Boot") {
		t.Error("Secure Boot must be said before anything is installed")
	}
}

// The run the panel asks for refuses, before anything is installed, what only a person can fix halfway through.
func TestPlanAutoRefusesWhatCannotFinish(t *testing.T) {
	ok := Env{OSRelease: osr("ubuntu", "debian"), Kernel: "6.8.0", CPUs: 2, Systemd: true}
	if _, _, err := PlanAuto(ok); err != nil {
		t.Fatalf("a plain Ubuntu: %v", err)
	}
	sb := ok
	sb.SecureBoot = true
	_, _, err := PlanAuto(sb)
	unsupported(t, err, CodeSecureBoot)
	if _, _, err := Plan(sb); err != nil { // by hand the owner may know better (MOK enrolled)
		t.Errorf("by hand: %v", err)
	}
	nosd := ok
	nosd.Systemd = false
	_, _, err = PlanAuto(nosd)
	unsupported(t, err, CodeNoSystemd)
	ct := ok
	ct.Container = "docker"
	_, _, err = PlanAuto(ct)
	unsupported(t, err, CodeContainer) // a container is named as a container, not as a missing systemd
	alp := ok
	alp.OSRelease = osr("alpine", "")
	_, _, err = PlanAuto(alp)
	unsupported(t, err, CodeDistro)
}

func TestParseOSRelease(t *testing.T) {
	m := ParseOSRelease(strings.NewReader("# comment\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nID=ubuntu\nID_LIKE=debian\nVERSION_CODENAME=noble\n\nnot a pair\n"))
	if m["ID"] != "ubuntu" || m["ID_LIKE"] != "debian" || m["PRETTY_NAME"] != "Ubuntu 24.04.1 LTS" || m["VERSION_CODENAME"] != "noble" {
		t.Errorf("os-release = %v", m)
	}
}
