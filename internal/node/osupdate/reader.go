// Package osupdate reads the package and kernel state already present on an apt host.
// It never refreshes package lists and its only apt invocation is a simulation.
package osupdate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/awgprep"
	"github.com/mistgate/mistgate/internal/node/secupdates"
)

const (
	FingerprintInterval = 5 * time.Minute
	FullReadInterval    = 6 * time.Hour
	aptReadTimeout      = 30 * time.Second
	maxPackages         = 16
	maxSecurityPending  = 32
	securityStuckAfter  = 48 * time.Hour
	maxOutput           = 4 << 20
)

// listsStampPath is touched by every successful "apt-get update" of the daily timer, also when no list file changed.
const listsStampPath = "/var/lib/apt/periodic/update-success-stamp"

// aptSimulationArgs is the one apt call of the reader. The agent runs as root under ProtectSystem=strict, so every
// file apt could write must be switched off here rather than allowed: both caches, and the planner log that apt
// would otherwise append to /var/log/apt/eipp.log.xz on each simulation.
var aptSimulationArgs = []string{"-s", "-q", "-o", "Debug::NoLocking=1", "-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=",
	"-o", "Dir::Log::Planner=", "--with-new-pkgs", "upgrade"}

// Runner executes one local command and returns its combined output. Tests replace it with fixture-backed responses.
type Runner func(context.Context, string, ...string) ([]byte, error)

// Reader inspects the host. Root is a test-only filesystem root; commands should be replaced when it is set.
type Reader struct {
	Root     string
	StateDir string
	Run      Runner
	Now      func() time.Time
}

func NewReader() *Reader { return &Reader{Run: runCommand, Now: time.Now} }

func (r *Reader) defaults() {
	if r.Run == nil {
		r.Run = runCommand
	}
	if r.Now == nil {
		r.Now = time.Now
	}
}

func (r *Reader) path(p string) string {
	if r.Root == "" {
		return p
	}
	return filepath.Join(r.Root, filepath.FromSlash(strings.TrimPrefix(p, "/")))
}

func (r *Reader) readFile(p string) ([]byte, error)       { return os.ReadFile(r.path(p)) }
func (r *Reader) stat(p string) (os.FileInfo, error)      { return os.Stat(r.path(p)) }
func (r *Reader) readDir(p string) ([]os.DirEntry, error) { return os.ReadDir(r.path(p)) }

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_MODE=l", "NEEDRESTART_SUSPEND=1")
	var out limitedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	return out.Bytes(), err
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len() >= maxOutput {
		return len(p), nil
	}
	if n := maxOutput - b.Len(); len(p) > n {
		_, _ = b.Buffer.Write(p[:n])
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// Read creates a fresh snapshot. Package lists are only read; no apt-get update or other network command is run.
func (r *Reader) Read(ctx context.Context) (*agentv1.OsStatus, error) {
	r.defaults()
	s := &agentv1.OsStatus{MeasuredUnix: r.Now().Unix(), Apt: "ok", AutoUpdates: "unknown"}
	osRelease, err := r.readFile("/etc/os-release")
	if err != nil {
		s.Error = "read_failed"
		return s, err
	}
	values := parseOSRelease(string(osRelease))
	s.DistroId, s.DistroVersion = strings.ToLower(values["ID"]), values["VERSION_ID"]
	s.BootIdHash = r.bootIDHash()

	if !r.commandExists("apt-get") || !r.commandExists("dpkg-query") {
		s.Error, s.Apt = "not_apt", "unknown"
		return s, nil
	}

	statusRows, statusErr := r.packageStatuses(ctx)
	if statusErr != nil {
		s.Apt = "unknown"
	} else {
		s.Held, s.HalfConfigured = parsePackageStatuses(statusRows)
	}

	locked, lockHolder, lockSince := r.aptLockStatus()
	s.AptLockHolder, s.AptLockSinceUnix = lockHolder, lockSince
	if locked {
		s.Apt = "locked"
	} else if len(s.HalfConfigured) > 0 {
		s.Apt = "half_configured"
	}

	if b, err := r.readFile("/proc/sys/kernel/osrelease"); err == nil {
		s.KernelRunning = strings.TrimSpace(string(b))
	}
	s.Container = r.containerKind(ctx)
	if s.Container == "" {
		if ki, sorted, err := r.installedKernels(ctx, s.KernelRunning); err == nil {
			s.KernelNewest = ki.Newest
			r.kernelTable(ctx, s, statusRows, sorted, ki)
		}
	}

	if stamp, ok := r.listsTime(); ok {
		s.ListsUnix = stamp.Unix()
	} else if s.Apt == "ok" || s.Apt == "half_configured" {
		s.Apt = "no_lists"
	}
	if info, err := r.stat("/var/lib/dpkg/status"); err == nil {
		s.DpkgUnix = info.ModTime().Unix()
	}

	s.RebootRequired, s.RebootReasons = r.rebootRequired(s)
	var simErr error
	var securityPending []string
	var securityFirstSeen map[string]int64
	if !locked && s.Apt != "no_lists" && s.Apt != "unknown" {
		out, err := r.simulate(ctx)
		if err == nil {
			s.Pending, s.Security, s.Notable, securityPending = parseSimulationDetails(string(out))
			s.SecurityPending = firstPackageNames(securityPending, maxSecurityPending)
			securityFirstSeen = r.rememberSecurityPending(securityPending, s.MeasuredUnix)
		} else {
			// The status still goes out, with the counts left empty, so the panel shows that the read failed instead of
			// an old state. The returned error carries the reason for the agent's log.
			simErr = fmt.Errorf("apt simulation failed: %w: %s", err, firstLine(out, 200))
			if s.Error == "" {
				s.Error = "read_failed"
			}
		}
	}
	r.autoUpdates(ctx, s)
	if s.AutoUpdates == "on" {
		for _, name := range securityPending {
			if first := securityFirstSeen[name]; first > 0 && s.MeasuredUnix-first > int64(securityStuckAfter/time.Second) {
				s.SecurityStuck = append(s.SecurityStuck, name)
			}
		}
		s.SecurityStuck = firstPackageNames(s.SecurityStuck, maxSecurityPending)
	}
	r.awgModule(s, statusRows)
	s.AwgSource = r.awgSource()
	return s, simErr
}

// firstLine is the first non-empty line of command output, cut to max bytes, for an error message.
func firstLine(out []byte, max int) string {
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if len(line) > max {
				line = line[:max]
			}
			return line
		}
	}
	return "no output"
}

func (r *Reader) commandExists(name string) bool {
	if r.Root != "" {
		return true
	}
	_, err := exec.LookPath(name)
	return err == nil
}

func (r *Reader) bootIDHash() string {
	b, err := r.readFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return BootIDHash(string(b))
}

// BootIDHash is the first 16 hex characters of the SHA-256 of a boot id (the contents of /proc/sys/kernel/random/boot_id): what
// OsStatus.boot_id_hash carries, so that the panel can tell its own server from another one without a reading of its own.
func BootIDHash(bootID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(bootID)))
	return hex.EncodeToString(sum[:])[:16]
}

// simulate runs the apt simulation in the agent's own sandbox. There is deliberately no retry through systemd-run: that
// would run apt-get as unsandboxed root on any error at all, on a production server. If the sandbox denies the
// simulation, the read fails with the reason and the panel shows it.
func (r *Reader) simulate(ctx context.Context) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, aptReadTimeout)
	defer cancel()
	return r.Run(cctx, "apt-get", aptSimulationArgs...)
}

func (r *Reader) packageStatuses(ctx context.Context) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := r.Run(commandCtx, "dpkg-query", "-W", "-f=${db:Status-Abbrev}\t${Package}\n")
	return string(out), err
}

// installedKernels lists the installed kernels, highest first, and tells the one the next boot starts (kernelCandidates, as the job
// does). The list is empty when linux-version lists none.
func (r *Reader) installedKernels(ctx context.Context, running string) (kernelInfo, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	list, err := r.Run(ctx, "linux-version", "list")
	if err != nil {
		return kernelInfo{}, nil, err
	}
	var installed []string
	for _, v := range strings.Fields(string(list)) {
		if safeKernelVersion(v) {
			installed = append(installed, v)
		}
	}
	if len(installed) == 0 {
		return kernelInfo{}, nil, nil
	}
	args := append([]string{"sort", "--reverse"}, kernelsToSort(installed, running)...)
	out, err := r.Run(ctx, "linux-version", args...)
	if err != nil {
		return kernelInfo{}, nil, err
	}
	sorted := strings.Fields(string(out))
	ki := kernelCandidates(sorted, installed, running)
	if ki.Newest == "" {
		return kernelInfo{}, nil, errors.New("linux-version returned no kernel version")
	}
	var highestFirst []string
	for _, v := range sorted {
		if contains(installed, v) {
			highestFirst = append(highestFirst, v)
		}
	}
	return ki, highestFirst, nil
}

func safeKernelVersion(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".+_-", r)) {
			return false
		}
	}
	return true
}

// listsTime is when the package lists were last checked. The daily timer touches the update-success stamp on every
// successful "apt-get update", also when no list changed (and so no list file got a new mtime), so the stamp is the
// better answer. Without it the newest InRelease file is used. The bool is false when there are no lists at all.
func (r *Reader) listsTime() (time.Time, bool) {
	entries, err := r.readDir("/var/lib/apt/lists")
	if err != nil {
		return time.Time{}, false
	}
	var newest time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "InRelease") {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		return time.Time{}, false
	}
	if info, err := r.stat(listsStampPath); err == nil && !info.ModTime().IsZero() {
		return info.ModTime(), true
	}
	return newest, true
}

func (r *Reader) rebootRequired(s *agentv1.OsStatus) (bool, []string) {
	var reasons []string
	marker := false
	if _, err := r.stat("/run/reboot-required"); err == nil {
		marker = true
	}
	if body, err := r.readFile("/run/reboot-required.pkgs"); err == nil {
		for _, line := range strings.Split(string(body), "\n") {
			name := strings.TrimSpace(line)
			if name, ok := normalizePackageName(name); ok {
				reasons = append(reasons, name)
			}
		}
	}
	sort.Strings(reasons)
	reasons = uniqueStrings(reasons)
	// "kernel" goes first and the cut to maxPackages comes after it, so a host with many pending packages still
	// shows that the kernel is the reason.
	if s.Container == "" && s.KernelRunning != "" && s.KernelNewest != "" && s.KernelRunning != s.KernelNewest {
		reasons = append([]string{"kernel"}, reasons...)
	}
	if len(reasons) > maxPackages {
		reasons = reasons[:maxPackages]
	}
	return marker || len(reasons) > 0, reasons
}

func (r *Reader) containerKind(ctx context.Context) string {
	if b, err := r.readFile("/run/systemd/container"); err == nil {
		return safeValue(strings.TrimSpace(string(b)), 48)
	}
	if r.Root != "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, _ := r.Run(ctx, "systemd-detect-virt", "--container")
	v := strings.TrimSpace(string(out))
	if v == "none" {
		return ""
	}
	return safeValue(v, 48)
}

func (r *Reader) aptLocked() bool {
	locked, _, _ := r.aptLockStatus()
	return locked
}

// aptLockStatus matches the apt lock inode first, then reads the process named by that exact /proc/locks row. A lock with
// an unknown or invalid owner still counts as locked; the optional metadata is simply left empty.
func (r *Reader) aptLockStatus() (bool, string, int64) {
	locks, err := r.readFile("/proc/locks")
	if err != nil {
		return false, "", 0
	}
	wanted := map[string]bool{}
	for _, p := range []string{"/var/lib/dpkg/lock-frontend", "/var/lib/dpkg/lock", "/var/lib/apt/lists/lock"} {
		if info, err := r.stat(p); err == nil {
			if key, ok := deviceInode(info); ok {
				wanted[key] = true
			}
		}
	}
	matched := false
	for _, line := range strings.Split(string(locks), "\n") {
		fields := strings.Fields(line)
		inodeAt := -1
		for i, field := range fields {
			if wanted[field] {
				inodeAt = i
				break
			}
		}
		if inodeAt < 0 {
			continue
		}
		matched = true
		// A leading "->" marks a blocked waiter, not the process that owns the lock.
		if len(fields) < 2 || fields[1] == "->" || inodeAt == 0 {
			continue
		}
		pid, err := strconv.Atoi(fields[inodeAt-1])
		if err != nil || pid <= 0 {
			continue
		}
		comm, err := r.readFile(fmt.Sprintf("/proc/%d/comm", pid))
		if err != nil {
			continue
		}
		holder := strings.TrimSpace(string(comm))
		if !validAptLockHolder(holder) {
			continue
		}
		stat, statErr := r.readFile(fmt.Sprintf("/proc/%d/stat", pid))
		procStat, procStatErr := r.readFile("/proc/stat")
		if statErr != nil || procStatErr != nil {
			return true, holder, 0
		}
		return true, holder, processStartUnix(string(stat), string(procStat))
	}
	return matched, "", 0
}

func validAptLockHolder(s string) bool {
	if len(s) == 0 || len(s) > 15 {
		return false
	}
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
			return false
		}
	}
	return true
}

// processStartUnix converts /proc/<pid>/stat's starttime (field 22, in USER_HZ ticks) to Unix seconds using /proc/stat's
// boot time. Linux exposes USER_HZ as 100 for this proc field on supported Debian and Ubuntu hosts.
func processStartUnix(stat, procStat string) int64 {
	end := strings.LastIndexByte(stat, ')') // comm may itself contain spaces or ')' characters
	if end < 0 || end+1 >= len(stat) {
		return 0
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) <= 19 {
		return 0
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil || ticks < 0 {
		return 0
	}
	var boot int64
	for _, line := range strings.Split(procStat, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "btime" {
			boot, err = strconv.ParseInt(fields[1], 10, 64)
			if err == nil && boot > 0 {
				return boot + ticks/100
			}
			return 0
		}
	}
	return 0
}

// deviceInode uses reflection because syscall.Stat_t has different definitions across the build targets.
func deviceInode(info os.FileInfo) (string, bool) {
	v := reflect.ValueOf(info.Sys())
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return "", false
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return "", false
	}
	dev, ok1 := integerField(v, "Dev")
	ino, ok2 := integerField(v, "Ino")
	if !ok1 || !ok2 {
		return "", false
	}
	major, minor := linuxMajorMinor(dev)
	// /proc/locks prints the device as "%02x:%02x:inode" (lock_get_status in the kernel's fs/locks.c).
	return fmt.Sprintf("%02x:%02x:%d", major, minor, ino), true
}

func integerField(v reflect.Value, name string) (uint64, bool) {
	field := v.FieldByName(name)
	if !field.IsValid() {
		return 0, false
	}
	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return field.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if field.Int() < 0 {
			return 0, false
		}
		return uint64(field.Int()), true
	default:
		return 0, false
	}
}

func linuxMajorMinor(dev uint64) (uint64, uint64) {
	major := (dev >> 8) & 0xfff
	major |= (dev >> 32) &^ 0xfff
	minor := dev & 0xff
	minor |= (dev >> 12) &^ 0xff
	return major, minor
}

func (r *Reader) autoUpdates(ctx context.Context, s *agentv1.OsStatus) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	installedOut, installedErr := r.Run(ctx, "dpkg-query", "-W", "-f=${Status}", "unattended-upgrades")
	if installedErr != nil || strings.TrimSpace(string(installedOut)) != "install ok installed" {
		s.AutoUpdates = "not_installed"
		return
	}
	config, configErr := r.Run(ctx, "apt-config", "shell", "E", "APT::Periodic::Enable", "L", "APT::Periodic::Update-Package-Lists",
		"U", "APT::Periodic::Unattended-Upgrade", "R", "Unattended-Upgrade::Automatic-Reboot/b")
	enabled, _ := r.Run(ctx, "systemctl", "is-enabled", "apt-daily.timer", "apt-daily-upgrade.timer")
	active, _ := r.Run(ctx, "systemctl", "is-active", "apt-daily.timer", "apt-daily-upgrade.timer")
	if configErr != nil {
		s.AutoUpdates = "unknown"
		return
	}
	s.AutoReboot = periodicTrue(parseAssignments(string(config))["R"])
	timersDisabled, timersKnown := timersOff(string(enabled), string(active))
	if !timersKnown {
		s.AutoUpdates = "unknown"
		return
	}
	// The doctor's parser decides what "disabled" means: an unset APT::Periodic::Enable is on, as on a stock host.
	if len(secupdates.ParsePeriodicShell(string(config)).DisabledKeys()) > 0 || timersDisabled {
		s.AutoUpdates = "off"
	} else {
		s.AutoUpdates = "on"
	}
}

// awgModule says whether the AmneziaWG kernel module is built for the kernel the next boot will use. It only runs when
// the module matters (loaded now, or the DKMS package is installed) and a different kernel is waiting.
//
// The agent unit has ProtectKernelModules=yes, which hides /usr/lib/modules from it, so the module file itself is
// usually out of sight. DKMS keeps a copy of every build under /var/lib/dkms, which stays readable, and is checked as
// well. When neither place can be read, the answer is "" (unknown) and never "missing": a wrong "missing" would tell the
// owner to rebuild a module that is there.
func (r *Reader) awgModule(s *agentv1.OsStatus, rows string) {
	if s.KernelNewest == "" || s.KernelRunning == s.KernelNewest {
		return
	}
	_, moduleErr := r.stat("/sys/module/amneziawg")
	moduleLoaded := moduleErr == nil
	if !moduleLoaded && !dkmsInstalled(rows) {
		return
	}
	base := "/lib/modules/" + s.KernelNewest
	for _, pattern := range []string{
		base + "/updates/dkms/amneziawg.ko*",
		base + "/updates/amneziawg.ko",
		"/var/lib/dkms/amneziawg/*/" + s.KernelNewest + "/*/module/amneziawg.ko*",
	} {
		matches, _ := filepath.Glob(r.path(pattern))
		if len(matches) > 0 {
			s.AwgModuleNext = "ready"
			return
		}
	}
	_, modulesErr := r.readDir(base)
	_, dkmsErr := r.readDir("/var/lib/dkms/amneziawg")
	if modulesErr == nil || dkmsErr == nil {
		s.AwgModuleNext = "missing"
	}
}

// dkmsInstalled reports whether the dpkg rows (abbreviated status, a tab, the package) hold amneziawg-dkms as
// installed. A row in state "rc" is a removed package whose configuration is left behind, not an installed one.
func dkmsInstalled(rows string) bool {
	for _, line := range strings.Split(rows, "\n") {
		status, name, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if ok && strings.TrimSpace(name) == "amneziawg-dkms" && (strings.HasPrefix(status, "ii") || strings.HasPrefix(status, "hi")) {
			return true
		}
	}
	return false
}

func parseOSRelease(s string) map[string]string {
	out := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(s))
	for scanner.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !ok || k == "" {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && ((v[0] == '\'' && v[len(v)-1] == '\'') || (v[0] == '"' && v[len(v)-1] == '"')) {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

func parsePackageStatuses(out string) (held, half []string) {
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name, ok := normalizePackageName(parts[1])
		if !ok {
			continue
		}
		status := parts[0]
		if len(status) > 0 && status[0] == 'h' {
			held = append(held, name)
		}
		if len(status) > 1 && strings.ContainsRune("HUFWt", rune(status[1])) {
			half = append(half, name)
		}
	}
	return boundedSorted(held), boundedSorted(half)
}

func parseSimulation(out string) (pending, security uint32, notable []string) {
	pending, security, notable, _ = parseSimulationDetails(out)
	return pending, security, notable
}

func parseSimulationDetails(out string) (pending, security uint32, notable, securityPending []string) {
	securityNames := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Inst ") {
			continue
		}
		pkg, old, next, origin, ok := parseInst(line)
		if !ok {
			continue
		}
		securityPackage := pkg
		pkg, ok = normalizePackageName(pkg)
		if !ok {
			continue
		}
		pending++
		if isSecurityOrigin(origin) {
			security++
			if validSecurityPackageName(securityPackage) {
				securityNames[securityPackage] = true
			}
		}
		if notablePackage(pkg) && len(notable) < maxPackages {
			suffix := "-"
			if isSecurityOrigin(origin) {
				suffix = "s"
			}
			notable = append(notable, strings.Join([]string{pkg, safeVersion(old), safeVersion(next), suffix}, " "))
		}
	}
	for name := range securityNames {
		securityPending = append(securityPending, name)
	}
	sort.Strings(securityPending)
	return pending, security, notable, securityPending
}

func parseSecurityPending(out string) []string {
	_, _, _, names := parseSimulationDetails(out)
	return names
}

func validSecurityPackageName(s string) bool {
	name, arch, hasArch := strings.Cut(s, ":")
	if len(name) < 2 || len(name) > 63 || !((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= '0' && name[0] <= '9')) {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("+.-", c)) {
			return false
		}
	}
	if !hasArch {
		return true
	}
	if len(arch) == 0 || len(arch) > 32 {
		return false
	}
	for _, c := range arch {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

type osPendingState struct {
	FirstSeen map[string]int64 `json:"first_seen_unix"`
}

func (r *Reader) rememberSecurityPending(names []string, now int64) map[string]int64 {
	firstSeen := map[string]int64{}
	if r.StateDir == "" {
		return firstSeen
	}
	path := filepath.Join(r.StateDir, "os-pending.json")
	if data, err := os.ReadFile(path); err == nil {
		var state osPendingState
		if json.Unmarshal(data, &state) == nil {
			for name, first := range state.FirstSeen {
				if validSecurityPackageName(name) && first > 0 && first <= now {
					firstSeen[name] = first
				}
			}
		}
	}
	current := make(map[string]int64, len(names))
	for _, name := range names {
		if !validSecurityPackageName(name) {
			continue
		}
		first := firstSeen[name]
		if first == 0 {
			first = now
		}
		current[name] = first
	}
	_ = writeJSONFile(path, osPendingState{FirstSeen: current})
	return current
}

func firstPackageNames(names []string, limit int) []string {
	if len(names) == 0 {
		return []string{}
	}
	out := append([]string(nil), names...)
	sort.Strings(out)
	unique := out[:0]
	for _, name := range out {
		if len(unique) == 0 || unique[len(unique)-1] != name {
			unique = append(unique, name)
		}
	}
	if len(unique) > limit {
		unique = unique[:limit]
	}
	return unique
}

func parseInst(line string) (pkg, old, next, origin string, ok bool) {
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Inst "))
	nameEnd := strings.IndexAny(line, " [(")
	if nameEnd < 1 {
		return "", "", "", "", false
	}
	pkg, line = line[:nameEnd], strings.TrimSpace(line[nameEnd:])
	if strings.HasPrefix(line, "[") {
		end := strings.IndexByte(line, ']')
		if end < 0 {
			return "", "", "", "", false
		}
		old, line = line[1:end], strings.TrimSpace(line[end+1:])
	}
	// With broken packages in the simulation, apt adds " [pkg:arch ]" (or just " []") after the closing parenthesis.
	closing := strings.LastIndexByte(line, ')')
	if !strings.HasPrefix(line, "(") || closing < 0 {
		return "", "", "", "", false
	}
	if tail := strings.TrimSpace(line[closing+1:]); tail != "" && !(strings.HasPrefix(tail, "[") && strings.HasSuffix(tail, "]")) {
		return "", "", "", "", false
	}
	inside := strings.Fields(line[1:closing])
	if len(inside) == 0 {
		return "", "", "", "", false
	}
	next = inside[0]
	if len(inside) > 1 {
		origin = strings.Join(inside[1:], " ")
	}
	return pkg, old, next, origin, true
}

func isSecurityOrigin(origin string) bool {
	lower := strings.ToLower(origin)
	return strings.Contains(lower, "-security") || strings.Contains(lower, "debian-security")
}

func notablePackage(name string) bool {
	return strings.HasPrefix(name, "linux-image") || strings.HasPrefix(name, "linux-") && (strings.Contains(name, "generic") || strings.Contains(name, "virtual") || strings.Contains(name, "kvm") || strings.Contains(name, "cloud") || strings.Contains(name, "hwe")) ||
		strings.HasPrefix(name, "libssl") || name == "openssl" || strings.HasPrefix(name, "openssh-") || name == "systemd" || name == "libsystemd0" || name == "libc6" || strings.HasPrefix(name, "amneziawg")
}

func safeVersion(s string) string {
	if s == "" {
		return "-"
	}
	if len(s) > 80 {
		return "-"
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".+:~%-", r)) {
			return "-"
		}
	}
	return s
}

func validPackageName(s string) bool {
	if len(s) == 0 || len(s) > 64 || !((s[0] >= 'a' && s[0] <= 'z') || (s[0] >= '0' && s[0] <= '9')) {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune(".+-", r)) {
			return false
		}
	}
	return true
}

func normalizePackageName(s string) (string, bool) {
	if validPackageName(s) {
		return s, true
	}
	name, arch, ok := strings.Cut(s, ":")
	if !ok || !validPackageName(name) || arch == "" || len(arch) > 32 {
		return "", false
	}
	for _, r := range arch {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return "", false
		}
	}
	return name, true
}

func safeValue(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".+_-", r)) {
			return ""
		}
	}
	return s
}

func parseAssignments(out string) map[string]string {
	values := map[string]string{}
	for _, part := range strings.FieldsFunc(out, func(r rune) bool { return r == ';' || r == '\n' }) {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) > 1 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		values[strings.TrimSpace(key)] = value
	}
	return values
}

func periodicTrue(v string) bool {
	v = strings.TrimSpace(v)
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

func timersOff(enabled, active string) (off, known bool) {
	e, a := strings.Fields(enabled), strings.Fields(active)
	if len(e) < 2 || len(a) < 2 {
		return false, false
	}
	for i := 0; i < 2; i++ {
		if e[i] != "enabled" && e[i] != "enabled-runtime" && e[i] != "disabled" && e[i] != "masked" && e[i] != "masked-runtime" && e[i] != "static" {
			return false, false
		}
		if a[i] != "active" && a[i] != "inactive" && a[i] != "failed" {
			return false, false
		}
		if (e[i] != "enabled" && e[i] != "enabled-runtime") || a[i] != "active" {
			return true, true
		}
	}
	return false, true
}

func boundedSorted(in []string) []string {
	sort.Strings(in)
	in = uniqueStrings(in)
	if len(in) > maxPackages {
		in = in[:maxPackages]
	}
	return in
}

func uniqueStrings(in []string) []string {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for _, v := range in[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

// awgSource says what the module's DKMS source looks like to the source fix (awgprep.FixCompat in check mode: it reads and
// changes nothing): "ok", "needs_patch" (a known file that does not build on some kernels; the job or the next package run
// repairs it), "unknown:<12 hex>" (a file Mistgate does not know) or "" (no DKMS source on this node).
func (r *Reader) awgSource() string {
	dirs, _ := filepath.Glob(r.path("/usr/src/amneziawg-*"))
	if len(dirs) == 0 {
		return ""
	}
	out := ""
	for _, c := range awgprep.FixCompat(awgprep.Sys{ReadFile: os.ReadFile}, dirs, true, io.Discard) {
		switch c.State {
		case awgprep.CompatUnknown:
			if c.SHA12 != "" {
				return "unknown:" + c.SHA12
			}
		case awgprep.CompatNeedsPatch:
			out = "needs_patch"
		case awgprep.CompatOK, awgprep.CompatPatched:
			if out == "" {
				out = "ok"
			}
		}
	}
	return out
}
