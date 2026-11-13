package osupdate

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// kernelsToSort is what "linux-version sort" is given: the installed kernels and, when it is not among them, the running one, so
// that the order shows whether the next boot would go down to an older kernel.
func kernelsToSort(installed []string, running string) []string {
	all := append([]string(nil), installed...)
	if safeKernelVersion(running) && !contains(installed, running) {
		all = append(all, running)
	}
	return all
}

// kernelCandidates reads the output of "linux-version sort --reverse" (highest first, the running kernel included) and tells the
// kernel the next boot starts: the highest installed one (Newest), whether the running kernel is higher than all of them (Older),
// and the installed kernels the boot menu can end up on (Candidates: Newest and every lower one down to the running kernel).
// It is the one place the job and the reader take this from. Newest is empty when no installed kernel is in the list.
func kernelCandidates(sorted, installed []string, running string) kernelInfo {
	newIdx, runIdx := -1, -1
	for i, v := range sorted {
		if newIdx < 0 && contains(installed, v) {
			newIdx = i
		}
		if runIdx < 0 && v == running {
			runIdx = i
		}
	}
	if newIdx < 0 {
		return kernelInfo{}
	}
	ki := kernelInfo{Newest: sorted[newIdx], Older: runIdx >= 0 && runIdx < newIdx}
	for i, v := range sorted {
		if contains(installed, v) && (runIdx < 0 || i <= runIdx) {
			ki.Candidates = append(ki.Candidates, v)
		}
	}
	return ki
}

// grubNames says whether the text of grub.cfg names the kernel image vmlinuz-<k> (not a longer version that starts with k).
func grubNames(cfg, k string) bool {
	needle := "vmlinuz-" + k
	for from := 0; from < len(cfg); {
		at := strings.Index(cfg[from:], needle)
		if at < 0 {
			return false
		}
		end := from + at + len(needle)
		if end == len(cfg) || !versionByte(cfg[end]) {
			return true
		}
		from += at + 1
	}
	return false
}

func versionByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '.' || b == '+' || b == '_' || b == '-'
}

// The kernel table (OsStatus.kernels) and the reboot preview (OsStatus.reboot_blockers). The reader only reads files in the agent's
// own sandbox; the job, which runs as root, looks at the same things and decides.
const (
	maxKernelRows  = 8  // rows sent to the panel
	maxKernelsRead = 32 // kernels looked at; the blockers cover all of them
)

// kernelTable fills the table and the blockers of s. sorted holds the installed kernels, highest first; ki is kernelCandidates of them.
func (r *Reader) kernelTable(ctx context.Context, s *agentv1.OsStatus, rows string, sorted []string, ki kernelInfo) {
	if len(sorted) == 0 || ki.Newest == "" {
		return
	}
	if len(sorted) > maxKernelsRead {
		sorted = sorted[:maxKernelsRead]
	}
	half := halfPackages(rows)
	moduleLoaded := r.exists("/sys/module/amneziawg")
	dkms := dkmsInstalled(rows)
	inUse := moduleLoaded || dkms
	// grub.cfg is world-readable on Debian and Ubuntu. When the agent still cannot read it, the entry is not reported missing.
	grubCfg, grubErr := r.readFile("/boot/grub/grub.cfg")
	grubKnown := grubErr == nil || errors.Is(grubErr, fs.ErrNotExist)

	var table []*agentv1.OsKernel
	byVersion := map[string]*agentv1.OsKernel{}
	for _, v := range sorted {
		k := &agentv1.OsKernel{
			Version:   v,
			Running:   v == s.KernelRunning,
			NextBoot:  v == ki.Newest,
			Candidate: contains(ki.Candidates, v),
			Image:     r.size("/boot/vmlinuz-"+v) > 0,
			Initrd:    r.size("/boot/initrd.img-"+v) >= minInitramfsBytes,
			GrubEntry: !grubKnown || grubNames(string(grubCfg), v),
			Headers:   "missing",
			Packages:  "ok",
		}
		if info, err := r.stat("/usr/src/linux-headers-" + v); err == nil && info.IsDir() {
			k.Headers = "present"
		}
		if inUse {
			k.Module = r.kernelModule(v, dkms)
		}
		for _, p := range half {
			if kernelPackage(p, v) || (k.NextBoot && strings.HasPrefix(p, "amneziawg")) {
				k.Packages = "half_configured"
			}
		}
		table = append(table, k)
		byVersion[v] = k
	}

	// The preview of rebootChecks, in its order. Unreadable things never become a blocker here: "module unknown" is not "missing".
	var blockers []string
	if len(half) > 0 {
		blockers = append(blockers, CodeDpkgInterrupted)
	}
	noModule, noBoot := false, false
	for _, c := range ki.Candidates {
		if k := byVersion[c]; k != nil {
			noModule = noModule || k.Module == "missing"
			noBoot = noBoot || !k.Image || !k.Initrd
		}
	}
	if noModule {
		blockers = append(blockers, CodeAwgModuleMissing)
	}
	if ki.Older {
		blockers = append(blockers, CodeKernelOlder)
	}
	if noBoot {
		blockers = append(blockers, CodeInitramfsMissing)
	}
	if k := byVersion[ki.Newest]; k != nil && !k.GrubEntry && grubErr == nil { // no grub.cfg at all is boot_default_custom
		blockers = append(blockers, CodeBootEntryMissing)
	}
	if r.grubProblem(ctx) != "" {
		blockers = append(blockers, CodeBootDefaultCustom)
	}
	if len(table) > maxKernelRows {
		table = table[:maxKernelRows]
	}
	s.Kernels, s.RebootBlockers = table, blockers
}

// kernelModule: "built" when a build of the module for kernel k can be seen; "missing" when the places that can be read show none
// (the DKMS tree of an installed DKMS package, or /lib/modules/<k> itself); "unknown" for what the agent's sandbox hides (a plain
// build, which is only in /lib/modules, a directory ProtectKernelModules takes away).
func (r *Reader) kernelModule(k string, dkms bool) string {
	base := "/lib/modules/" + k
	for _, pattern := range []string{
		base + "/updates/dkms/amneziawg.ko*",
		base + "/updates/amneziawg.ko*",
		base + "/extra/amneziawg.ko*",
		"/var/lib/dkms/amneziawg/*/" + k + "/*/module/amneziawg.ko*",
	} {
		if len(r.glob(pattern)) > 0 {
			return "built"
		}
	}
	if _, err := r.readDir(base); err == nil {
		return "missing"
	}
	if _, err := r.readDir("/var/lib/dkms/amneziawg"); dkms && err == nil {
		return "missing"
	}
	return "unknown"
}

// kernelPackage: package p is one of the linux-* packages of kernel k (image, modules, headers, in either distribution's spelling).
func kernelPackage(p, k string) bool {
	return strings.HasPrefix(p, "linux-") && (strings.HasSuffix(p, "-"+k) || strings.HasSuffix(p, "-"+k+"-unsigned"))
}

// halfPackages: the packages whose dpkg state is half-installed, unpacked, half-configured or waiting for triggers (all of them, unbounded).
func halfPackages(rows string) []string {
	var half []string
	for _, line := range strings.Split(rows, "\n") {
		status, name, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok || len(status) < 2 || !strings.ContainsRune("HUFWt", rune(status[1])) {
			continue
		}
		if n, ok := normalizePackageName(strings.TrimSpace(name)); ok {
			half = append(half, n)
		}
	}
	return half
}

// grubProblem is the job's boot default check (grubDefaultProblem) over what the agent can read: the answer behind boot_default_custom.
func (r *Reader) grubProblem(ctx context.Context) string {
	text := func(p string) string { b, _ := r.readFile(p); return strings.TrimSpace(string(b)) }
	return grubDefaultProblem(r.exists, r.glob, text, func() (string, error) {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		out, err := r.Run(cctx, "grub-editenv", "list")
		return string(out), err
	})
}

func (r *Reader) size(p string) int64 {
	if info, err := r.stat(p); err == nil && !info.IsDir() {
		return info.Size()
	}
	return 0
}

func (r *Reader) exists(p string) bool {
	_, err := r.stat(p)
	return err == nil
}

// glob lists the paths that match, without the test root.
func (r *Reader) glob(pattern string) []string {
	matches, _ := filepath.Glob(r.path(pattern))
	if r.Root == "" {
		return matches
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if rel, err := filepath.Rel(r.Root, m); err == nil {
			out = append(out, "/"+filepath.ToSlash(rel))
		}
	}
	return out
}
