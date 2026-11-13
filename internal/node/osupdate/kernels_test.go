package osupdate

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

func TestKernelCandidates(t *testing.T) {
	cases := []struct {
		name      string
		sorted    []string
		installed []string
		running   string
		newest    string
		older     bool
		cands     []string
	}{
		{"new kernel waiting", []string{"7.0.0-39", "7.0.0-38", "7.0.0-37"}, []string{"7.0.0-37", "7.0.0-38", "7.0.0-39"}, "7.0.0-37",
			"7.0.0-39", false, []string{"7.0.0-39", "7.0.0-38", "7.0.0-37"}},
		{"running is the newest", []string{"7.0.0-37", "7.0.0-36"}, []string{"7.0.0-36", "7.0.0-37"}, "7.0.0-37", "7.0.0-37", false, []string{"7.0.0-37"}},
		{"running kernel was removed and is higher", []string{"7.0.0-40", "7.0.0-38"}, []string{"7.0.0-38"}, "7.0.0-40", "7.0.0-38", true, nil},
		{"running kernel was removed and is lower", []string{"7.0.0-39", "7.0.0-30"}, []string{"7.0.0-39"}, "7.0.0-30", "7.0.0-39", false, []string{"7.0.0-39"}},
		{"running kernel unknown", []string{"7.0.0-39", "7.0.0-38"}, []string{"7.0.0-38", "7.0.0-39"}, "", "7.0.0-39", false, []string{"7.0.0-39", "7.0.0-38"}},
		{"nothing installed in the list", []string{"7.0.0-37"}, nil, "7.0.0-37", "", false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ki := kernelCandidates(c.sorted, c.installed, c.running)
			if ki.Newest != c.newest || ki.Older != c.older || !slices.Equal(ki.Candidates, c.cands) {
				t.Fatalf("kernelCandidates = %+v, want newest %q older %v candidates %v", ki, c.newest, c.older, c.cands)
			}
		})
	}
	if got := kernelsToSort([]string{"7.0.0-38"}, "7.0.0-40"); !slices.Equal(got, []string{"7.0.0-38", "7.0.0-40"}) {
		t.Fatalf("kernelsToSort = %v", got)
	}
	if got := kernelsToSort([]string{"7.0.0-38"}, "7.0.0-38"); !slices.Equal(got, []string{"7.0.0-38"}) {
		t.Fatalf("kernelsToSort with the running kernel installed = %v", got)
	}
}

func TestGrubNamesNeedsTheWholeVersion(t *testing.T) {
	cfg := "menuentry 'a' {\n\tlinux\t/boot/vmlinuz-6.8.0-57-generic root=/dev/sda1\n}\nmenuentry 'b' {\n\tlinux /vmlinuz-6.8.0-5-generic ro\n}\n"
	for k, want := range map[string]bool{"6.8.0-57-generic": true, "6.8.0-5-generic": true, "6.8.0-5": false, "6.8.0-57": false, "6.8.0-58-generic": false, "": false} {
		if got := grubNames(cfg, k); got != want && k != "" {
			t.Errorf("grubNames(%q) = %v, want %v", k, got, want)
		}
	}
	if !grubNames("linux /vmlinuz-7.0.0-38-generic", "7.0.0-38-generic") {
		t.Error("a name at the end of the file is not found")
	}
}

// ------------------------------------------------------------------------------------------------ the reader's kernel table

const (
	kRun = "7.0.0-37-generic"
	kNew = "7.0.0-38-generic"
	kOld = "7.0.0-36-generic"
)

// tableRoot is a host the reader looks at: kernels kOld, kRun (running) and kNew installed, and the module in use. Each kernel's
// boot files, headers, menu entry and DKMS build exist unless a test takes them away.
type tableRoot struct {
	t    *testing.T
	root string
	rows string // dpkg rows
	run  Runner
}

func newTableRoot(t *testing.T) *tableRoot {
	tr := &tableRoot{t: t, root: t.TempDir(), rows: "ii \tlibc6\nii \tamneziawg-dkms\n"}
	w := func(p, body string) { writeRoot(t, tr.root, p, body) }
	w("/etc/os-release", "ID=ubuntu\nVERSION_ID=\"26.04\"\n")
	w("/proc/sys/kernel/osrelease", kRun+"\n")
	w("/proc/locks", "")
	w("/var/lib/dpkg/status", "x")
	w("/var/lib/apt/lists/x_InRelease", "x")
	w("/sys/module/amneziawg/x", "")
	w("/etc/default/grub", "GRUB_DEFAULT=0\n")
	w("/boot/grub/grubenv", "# GRUB Environment Block\n")
	cfg := ""
	for _, k := range []string{kOld, kRun, kNew} {
		tr.bootFiles(k)
		cfg += "menuentry 'Ubuntu, with Linux " + k + "' {\n\tlinux\t/boot/vmlinuz-" + k + " root=/dev/sda1 ro\n}\n"
		w("/usr/src/linux-headers-"+k+"/Makefile", "x")
		w("/var/lib/dkms/amneziawg/3.1/"+k+"/x86_64/module/amneziawg.ko.zst", "x")
	}
	w("/boot/grub/grub.cfg", cfg)
	tr.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "dpkg-query":
			if len(args) > 2 && args[2] == "unattended-upgrades" {
				return nil, errors.New("not installed")
			}
			return []byte(tr.rows), nil
		case "linux-version":
			if args[0] == "list" {
				return []byte(kOld + "\n" + kRun + "\n" + kNew + "\n"), nil
			}
			sorted := slices.DeleteFunc(slices.Clone(args[1:]), func(v string) bool { return v == "--reverse" })
			sort.Slice(sorted, func(i, j int) bool { return kernelLess(sorted[j], sorted[i]) })
			return []byte(strings.Join(sorted, "\n")), nil
		case "apt-get":
			return nil, nil
		}
		return nil, nil
	}
	return tr
}

func (tr *tableRoot) path(p string) string {
	return filepath.Join(tr.root, filepath.FromSlash(strings.TrimPrefix(p, "/")))
}

// bootFiles puts the image (a small file) and the initramfs (a sparse file of 70 MiB) of kernel k in /boot.
func (tr *tableRoot) bootFiles(k string) {
	writeRoot(tr.t, tr.root, "/boot/vmlinuz-"+k, "image")
	tr.initrd(k, 70<<20)
}

func (tr *tableRoot) initrd(k string, size int64) {
	p := tr.path("/boot/initrd.img-" + k)
	writeRoot(tr.t, tr.root, "/boot/initrd.img-"+k, "")
	if err := os.Truncate(p, size); err != nil {
		tr.t.Fatal(err)
	}
}

func (tr *tableRoot) remove(p string) {
	if err := os.RemoveAll(tr.path(p)); err != nil {
		tr.t.Fatal(err)
	}
}

func (tr *tableRoot) read() *agentv1.OsStatus {
	tr.t.Helper()
	r := &Reader{Root: tr.root, Run: tr.run, Now: func() time.Time { return time.Unix(1794000000, 0) }}
	s, err := r.Read(context.Background())
	if err != nil {
		tr.t.Fatal(err)
	}
	return s
}

func kernelRow(t *testing.T, s *agentv1.OsStatus, v string) *agentv1.OsKernel {
	t.Helper()
	for _, k := range s.GetKernels() {
		if k.GetVersion() == v {
			return k
		}
	}
	t.Fatalf("no row for %s in %v", v, s.GetKernels())
	return nil
}

func TestKernelTableOfACleanHostHasNoBlockers(t *testing.T) {
	s := newTableRoot(t).read()
	if len(s.GetKernels()) != 3 || s.GetKernels()[0].GetVersion() != kNew || s.GetKernels()[2].GetVersion() != kOld {
		t.Fatalf("kernels = %v, want 3 rows, newest first", s.GetKernels())
	}
	if len(s.GetRebootBlockers()) != 0 {
		t.Fatalf("blockers = %v", s.GetRebootBlockers())
	}
	n := kernelRow(t, s, kNew)
	if !n.GetNextBoot() || n.GetRunning() || !n.GetCandidate() || !n.GetImage() || !n.GetInitrd() || !n.GetGrubEntry() ||
		n.GetHeaders() != "present" || n.GetModule() != "built" || n.GetPackages() != "ok" {
		t.Fatalf("newest kernel row = %v", n)
	}
	if r := kernelRow(t, s, kRun); !r.GetRunning() || r.GetNextBoot() || !r.GetCandidate() {
		t.Fatalf("running kernel row = %v", r)
	}
	if o := kernelRow(t, s, kOld); o.GetCandidate() {
		t.Fatalf("a kernel below the running one is not on the fallback path: %v", o)
	}
	if s.GetKernelNewest() != kNew {
		t.Fatalf("kernel_newest = %q", s.GetKernelNewest())
	}
}

// The shape of the incident: the newest kernel's packages half configured, no initramfs, no menu entry, and no module build.
func TestKernelTableOfTheIncidentGivesThreeBlockers(t *testing.T) {
	tr := newTableRoot(t)
	tr.rows += "iF \tlinux-headers-" + kNew + "\niF \tlinux-image-" + kNew + "\niU \tamneziawg-dkms\n"
	tr.remove("/boot/initrd.img-" + kNew)
	tr.remove("/usr/src/linux-headers-" + kNew)
	tr.remove("/var/lib/dkms/amneziawg/3.1/" + kNew)
	writeRoot(t, tr.root, "/boot/grub/grub.cfg", "menuentry 'a' {\n\tlinux /boot/vmlinuz-"+kRun+" ro\n}\nmenuentry 'b' {\n\tlinux /boot/vmlinuz-"+kOld+" ro\n}\n")
	s := tr.read()
	want := []string{CodeDpkgInterrupted, CodeAwgModuleMissing, CodeInitramfsMissing, CodeBootEntryMissing}
	if !slices.Equal(s.GetRebootBlockers(), want) {
		t.Fatalf("blockers = %v, want %v", s.GetRebootBlockers(), want)
	}
	n := kernelRow(t, s, kNew)
	if !n.GetImage() || n.GetInitrd() || n.GetGrubEntry() || n.GetHeaders() != "missing" || n.GetModule() != "missing" || n.GetPackages() != "half_configured" {
		t.Fatalf("newest kernel row = %v", n)
	}
	if r := kernelRow(t, s, kRun); !r.GetInitrd() || !r.GetGrubEntry() {
		t.Fatalf("running kernel row = %v", r)
	}
	for _, b := range s.GetRebootBlockers() {
		if !slices.Contains(RebootBlockers, b) {
			t.Errorf("blocker %q is not in RebootBlockers", b)
		}
	}
}

func TestKernelTableBlockerRules(t *testing.T) {
	cases := map[string]struct {
		set  func(*tableRoot)
		want []string
	}{
		"truncated initramfs of the running kernel":                {func(tr *tableRoot) { tr.initrd(kRun, 4096) }, []string{CodeInitramfsMissing}},
		"initramfs of an older kernel is not on the fallback path": {func(tr *tableRoot) { tr.remove("/boot/initrd.img-" + kOld) }, nil},
		"module missing for the fallback kernel":                   {func(tr *tableRoot) { tr.remove("/var/lib/dkms/amneziawg/3.1/" + kRun) }, []string{CodeAwgModuleMissing}},
		"module missing for a kernel below the running one":        {func(tr *tableRoot) { tr.remove("/var/lib/dkms/amneziawg/3.1/" + kOld) }, nil},
		"no module in use": {func(tr *tableRoot) {
			tr.remove("/sys/module/amneziawg")
			tr.rows = "ii \tlibc6\n"
			tr.remove("/var/lib/dkms/amneziawg/3.1/" + kNew)
		}, nil},
		"GRUB_DEFAULT=saved":                        {func(tr *tableRoot) { writeRoot(tr.t, tr.root, "/etc/default/grub", "GRUB_DEFAULT=saved\n") }, []string{CodeBootDefaultCustom}},
		"saved_entry":                               {func(tr *tableRoot) { grubEnv(tr, "saved_entry=1\n") }, []string{CodeBootDefaultCustom}},
		"no grub.cfg at all":                        {func(tr *tableRoot) { tr.remove("/boot/grub/grub.cfg") }, []string{CodeBootDefaultCustom}},
		"half configured package of another kernel": {func(tr *tableRoot) { tr.rows += "iF \tlinux-image-" + kOld + "\n" }, []string{CodeDpkgInterrupted}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tr := newTableRoot(t)
			c.set(tr)
			if got := tr.read().GetRebootBlockers(); !slices.Equal(got, c.want) {
				t.Fatalf("blockers = %v, want %v", got, c.want)
			}
		})
	}
}

func grubEnv(tr *tableRoot, out string) {
	base := tr.run
	tr.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "grub-editenv" {
			return []byte(out), nil
		}
		return base(ctx, name, args...)
	}
}

func TestKernelTableOlderKernelAfterTheRunningOneWasRemoved(t *testing.T) {
	tr := newTableRoot(t)
	writeRoot(t, tr.root, "/proc/sys/kernel/osrelease", "7.0.0-40-generic\n") // not installed, higher than all
	s := tr.read()
	if !slices.Contains(s.GetRebootBlockers(), CodeKernelOlder) {
		t.Fatalf("blockers = %v, want kernel_older", s.GetRebootBlockers())
	}
	if kernelRow(t, s, kNew).GetCandidate() {
		t.Fatal("no kernel is on the fallback path when the next boot goes down")
	}
}

// A plain (not DKMS) build lives only in /lib/modules, which the agent's sandbox hides: the table says "unknown", not "missing",
// and "unknown" never blocks. Where /lib/modules/<k> can be read and holds no module, it is "missing".
func TestKernelTablePlainBuildIsUnknown(t *testing.T) {
	tr := newTableRoot(t)
	tr.rows = "ii \tlibc6\n" // no amneziawg-dkms: Debian's plain build
	tr.remove("/var/lib/dkms")
	s := tr.read()
	for _, k := range s.GetKernels() {
		if k.GetModule() != "unknown" {
			t.Fatalf("%s module = %q, want unknown", k.GetVersion(), k.GetModule())
		}
	}
	if len(s.GetRebootBlockers()) != 0 {
		t.Fatalf("blockers = %v: an unknown module is not a blocker", s.GetRebootBlockers())
	}
	writeRoot(t, tr.root, "/lib/modules/"+kNew+"/extra/amneziawg.ko", "x")
	writeRoot(t, tr.root, "/lib/modules/"+kRun+"/modules.dep", "x")
	s = tr.read()
	if got := kernelRow(t, s, kNew).GetModule(); got != "built" {
		t.Fatalf("newest module = %q, want built", got)
	}
	if got := kernelRow(t, s, kRun).GetModule(); got != "missing" {
		t.Fatalf("running module = %q, want missing", got)
	}
	if !slices.Equal(s.GetRebootBlockers(), []string{CodeAwgModuleMissing}) {
		t.Fatalf("blockers = %v", s.GetRebootBlockers())
	}
}

func TestKernelTableIsBoundedAndSkippedInAContainer(t *testing.T) {
	tr := newTableRoot(t)
	var many []string
	for i := 30; i < 45; i++ {
		many = append(many, "7.0.0-"+string(rune('0'+i/10))+string(rune('0'+i%10))+"-generic")
	}
	base := tr.run
	tr.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "linux-version" {
			if args[0] == "list" {
				return []byte(strings.Join(many, "\n")), nil
			}
			rev := slices.Clone(many)
			slices.Reverse(rev)
			return []byte(strings.Join(rev, "\n")), nil
		}
		return base(ctx, name, args...)
	}
	s := tr.read()
	if len(s.GetKernels()) != maxKernelRows || s.GetKernels()[0].GetVersion() != many[len(many)-1] {
		t.Fatalf("%d rows, first %q", len(s.GetKernels()), s.GetKernels()[0].GetVersion())
	}
	writeRoot(t, tr.root, "/run/systemd/container", "lxc")
	if s := tr.read(); len(s.GetKernels()) != 0 || len(s.GetRebootBlockers()) != 0 {
		t.Fatalf("a container has no kernel table: %v %v", s.GetKernels(), s.GetRebootBlockers())
	}
}

// ------------------------------------------------------------------------------------------------ boot_entry_missing, in the job

func TestMissingGrubEntryOfTheNewestKernelRefusesTheReboot(t *testing.T) {
	m := menuMachine(t)
	m.grubOmit = map[string]bool{"7.0.0-39-generic": true} // the newest installed kernel
	p := statusPath(t)
	f := asFailure(t, m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, p).Do(context.Background()))
	if f.Code != CodeBootEntryMissing || !strings.Contains(f.Detail, "7.0.0-39-generic") {
		t.Fatalf("failure = %+v, want boot_entry_missing for 7.0.0-39-generic", f)
	}
	noReboot(t, m)
	if st := mustStatus(t, p); st.Reboot != RebootHeld || st.HeldReason != CodeBootEntryMissing {
		t.Fatalf("status = %+v", st)
	}

	// A menu without the entry of a lower kernel is the business of boot_default_custom and the boot files, not of this check.
	m = menuMachine(t)
	m.grubOmit = map[string]bool{"7.0.0-36-generic": true}
	ctx, cancel := rebootCtx(m)
	defer cancel()
	if err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, "").Do(ctx); err != nil {
		t.Fatalf("the newest kernel has its entry: %v", err)
	}
}

// ------------------------------------------------------------------------------------------------ the only reboot call

// execNames are the calls that run a command: the standard library's, and the job's own wrappers.
var execNames = map[string]bool{"Command": true, "CommandContext": true, "cmd": true, "cmdKeep": true, "query": true, "Run": true, "run": true,
	"Output": true, "CombinedOutput": true, "SystemdRun": true, "Exec": true}

func execCall(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return execNames[f.Name]
	case *ast.SelectorExpr:
		return execNames[f.Sel.Name]
	}
	return false
}

// TestOneRebootCall: the agent reboots the host in one place, osupdate/reboot.go, behind rebootChecks. A new path that passes
// "reboot" (or poweroff, halt, shutdown) to an exec would skip those checks, so this test fails for it.
func TestOneRebootCall(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	fset := token.NewFileSet()
	seen := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "node_modules" || n == ".git" || n == "testdata" || n == "web" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		allowed := filepath.ToSlash(p) == "../../../internal/node/osupdate/reboot.go"
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !execCall(call.Fun) {
				return true
			}
			for _, arg := range call.Args {
				ast.Inspect(arg, func(n ast.Node) bool {
					lit, ok := n.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return true
					}
					switch strings.Trim(lit.Value, "`\"") {
					case "reboot", "poweroff", "halt", "shutdown":
						seen++
						if !allowed {
							t.Errorf("%s: %s is passed to an exec outside osupdate/reboot.go; a reboot must go through rebootChecks", fset.Position(lit.Pos()), lit.Value)
						}
					}
					return true
				})
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("the walk found no reboot call at all: the test is not looking at the repo")
	}
}
