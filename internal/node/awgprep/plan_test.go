package awgprep

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func osr(id, like string) map[string]string {
	m := map[string]string{"ID": id, "ID_LIKE": like, "PRETTY_NAME": id + " test"}
	if id == "ubuntu" {
		m["VERSION_ID"] = "24.04"
		m["VERSION_CODENAME"] = "noble"
	}
	return m
}

func cmds(steps []Step) []string {
	var out []string
	for _, s := range steps {
		if len(s.Cmd) > 0 {
			out = append(out, strings.Join(s.Cmd, " "))
		}
	}
	return out
}

const aptOpts = "apt-get -o DPkg::Lock::Timeout=1800 install -y --no-install-recommends "

func TestPlanUbuntu(t *testing.T) {
	for _, tc := range []struct {
		name, version, codename, sourcePath, sourceBody string
		wantRemove                                      string
	}{
		{
			name: "noble", version: "24.04", codename: "noble",
			sourcePath: "/etc/apt/sources.list.d/amnezia-ubuntu-ppa-noble.sources",
			sourceBody: "Types: deb\nURIs: https://ppa.launchpadcontent.net/amnezia/ppa/ubuntu/\nSuites: noble\nComponents: main\nSigned-By: /etc/apt/keyrings/amnezia-ppa.asc\n",
			wantRemove: "/etc/apt/sources.list.d/amnezia-ubuntu-ppa-noble.list",
		},
		{
			name: "jammy", version: "22.04", codename: "jammy",
			sourcePath: "/etc/apt/sources.list.d/amnezia-ubuntu-ppa-jammy.list",
			sourceBody: "deb [signed-by=/etc/apt/keyrings/amnezia-ppa.asc] https://ppa.launchpadcontent.net/amnezia/ppa/ubuntu/ jammy main\n",
		},
		{
			name: "focal", version: "20.04", codename: "focal",
			sourcePath: "/etc/apt/sources.list.d/amnezia-ubuntu-ppa-focal.list",
			sourceBody: "deb [signed-by=/etc/apt/keyrings/amnezia-ppa.asc] https://ppa.launchpadcontent.net/amnezia/ppa/ubuntu/ focal main\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := Env{OSRelease: osr("ubuntu", "debian"), Kernel: "6.8.0-142-generic", CPUs: 2}
			env.OSRelease["VERSION_ID"] = tc.version
			env.OSRelease["VERSION_CODENAME"] = tc.codename
			steps, notes, err := Plan(env)
			if err != nil {
				t.Fatal(err)
			}
			if len(steps) != 6 {
				t.Fatalf("got %d steps, want 6: %+v", len(steps), steps)
			}
			wantDesc := []string{
				"refresh the package index",
				"install the headers of the running kernel",
				"add the Amnezia PPA",
				"refresh the package index from the Amnezia PPA",
				"install amneziawg",
				"load the module",
			}
			for i, want := range wantDesc {
				if steps[i].Desc != want {
					t.Errorf("step %d description = %q, want %q", i+1, steps[i].Desc, want)
				}
			}
			got := strings.Join(cmds(steps), "\n")
			for _, want := range []string{
				"apt-get -o DPkg::Lock::Timeout=1800 update",
				aptOpts + "linux-headers-6.8.0-142-generic",
				aptOpts + "amneziawg",
				"modprobe amneziawg",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("plan lacks %q:\n%s", want, got)
				}
			}
			for _, unwanted := range []string{"add-apt-repository", "software-properties-common", "python3-launchpadlib", "gnupg2"} {
				if strings.Contains(got, unwanted) {
					t.Errorf("plan still uses %q:\n%s", unwanted, got)
				}
			}
			if got := cmds(steps); len(got) != 5 || got[0] != got[2] || !strings.HasPrefix(got[1], aptOpts+"linux-headers-") {
				t.Errorf("Ubuntu command sequence = %v", got)
			}
			if last := cmds(steps)[len(cmds(steps))-1]; last != "modprobe amneziawg" {
				t.Errorf("the plan must end with the load, not %q", last)
			}
			files := steps[2].Files
			if len(files) == 0 {
				t.Fatal("the PPA step has no file operations")
			}
			if files[0].Path != "/etc/apt/keyrings/amnezia-ppa.asc" || files[0].Body != string(amneziaPPAKey) || files[0].Remove {
				t.Errorf("key operation = %+v", files[0])
			}
			var source, removed *FileOp
			for i := range files {
				f := &files[i]
				if f.Path == tc.sourcePath && !f.Remove {
					source = f
				}
				if f.Path == tc.wantRemove && f.Remove {
					removed = f
				}
			}
			if source == nil || source.Body != tc.sourceBody {
				t.Errorf("source operation = %+v, want %s with %q", source, tc.sourcePath, tc.sourceBody)
			}
			if tc.wantRemove != "" && removed == nil {
				t.Errorf("legacy source %s is not removed: %+v", tc.wantRemove, files)
			}
			if tc.wantRemove == "" && len(files) != 2 {
				t.Errorf("Jammy operations = %+v, want key and .list writes", files)
			}
			if len(notes) == 0 || !strings.Contains(strings.Join(notes, " "), "DKMS") {
				t.Errorf("notes = %v", notes)
			}
		})
	}
}

func TestPlanUbuntuMissingCodenameIsUnsupported(t *testing.T) {
	osRelease := osr("ubuntu", "debian")
	delete(osRelease, "VERSION_CODENAME")
	delete(osRelease, "UBUNTU_CODENAME")
	_, _, err := Plan(Env{OSRelease: osRelease, Kernel: "6.8.0"})
	unsupported(t, err, CodeDistro)
	if err == nil || !strings.Contains(err.Error(), "VERSION_CODENAME") || !strings.Contains(err.Error(), "UBUNTU_CODENAME") {
		t.Errorf("missing codename error = %v", err)
	}
}

func TestPlanUbuntuRejectsInvalidCodename(t *testing.T) {
	osRelease := osr("ubuntu", "debian")
	osRelease["VERSION_CODENAME"] = "noble/../../tmp"
	_, _, err := Plan(Env{OSRelease: osRelease, Kernel: "6.8.0"})
	unsupported(t, err, CodeDistro)
	if err == nil || !strings.Contains(err.Error(), "valid") {
		t.Errorf("invalid codename error = %v", err)
	}
}

func TestPlanUbuntuUsesUbuntuCodenameFallback(t *testing.T) {
	osRelease := osr("ubuntu", "debian")
	delete(osRelease, "VERSION_CODENAME")
	osRelease["UBUNTU_CODENAME"] = "jammy"
	osRelease["VERSION_ID"] = "22.04"
	steps, _, err := Plan(Env{OSRelease: osRelease, Kernel: "6.8.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) <= 2 || len(steps[2].Files) == 0 {
		t.Fatalf("plan has no PPA step: %+v", steps)
	}
	if got := steps[2].Files[len(steps[2].Files)-1].Path; got != "/etc/apt/sources.list.d/amnezia-ubuntu-ppa-jammy.list" {
		t.Errorf("source path = %q", got)
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
