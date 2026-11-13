package awgprep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

const (
	KmodHookPath       = "/etc/apt/apt.conf.d/99mistgate-awg"
	KmodUnitName       = "mistgate-awg-kmod"
	KmodStatusFileName = "awg-kmod.json"
	KmodBootBudget     = 180 * time.Second
	KmodRuntimeMaxSec  = 240
	KmodUnitMaxSec     = 3600
	KmodRunBudget      = 55 * time.Minute
	KmodLockWait       = 30 * time.Minute
	KmodHeartbeat      = 5 * time.Second
	KmodStatusStale    = 30 * time.Second
)

type KmodMode string

const (
	KmodPatchOnly KmodMode = "patch-only"
	KmodBoot      KmodMode = "boot"
	KmodAfterApt  KmodMode = "after-apt"
	KmodRepair    KmodMode = "repair"
)

type KmodRebuild struct {
	Kernel    string `json:"kernel"`
	SourceSHA string `json:"source_sha"`
}

// KmodStatus is shared by the apt hook, the transient unit and the agent. Rebuilt is persisted in the same file so a
// module that failed to load cannot trigger an endless rebuild on every agent start.
type KmodStatus struct {
	Mode    KmodMode      `json:"mode"`
	State   string        `json:"state"`
	Started int64         `json:"started"`
	Updated int64         `json:"updated"`
	Rebuilt []KmodRebuild `json:"rebuilt,omitempty"`
	Failed  []KmodRebuild `json:"failed,omitempty"`
}

func ReadKmodStatus(path string) (KmodStatus, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return KmodStatus{}, false, nil
	}
	if err != nil {
		return KmodStatus{}, false, err
	}
	var st KmodStatus
	if json.Unmarshal(b, &st) != nil || (st.State != StateRunning && st.State != StateDone && st.State != StateFailed) {
		return KmodStatus{}, false, nil
	}
	return st, true, nil
}

func WriteKmodStatus(path string, st KmodStatus) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mistgate-awg-kmod-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// KmodController launches a boot sync and exposes the heartbeat to inbound reconciliation.
type KmodController struct {
	StatusPath string
	NeedBoot   func() bool
	Launch     func(context.Context) error
	Now        func() time.Time
	mu         sync.Mutex
}

func NewKmodController(statusPath string, needBoot func() bool, launch func(context.Context) error) *KmodController {
	return &KmodController{StatusPath: statusPath, NeedBoot: needBoot, Launch: launch, Now: time.Now}
}

func (c *KmodController) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Current returns the last status, whether or not its heartbeat is fresh.
func (c *KmodController) Current() (KmodStatus, bool) {
	if c == nil || c.StatusPath == "" {
		return KmodStatus{}, false
	}
	st, ok, _ := ReadKmodStatus(c.StatusPath)
	return st, ok
}

func (c *KmodController) Running() (KmodStatus, bool) {
	st, ok := c.Current()
	if !ok || st.State != StateRunning {
		return st, false
	}
	age := c.now().Unix() - st.Updated
	return st, age <= int64(KmodStatusStale/time.Second) && age >= -int64(KmodStatusStale/time.Second)
}

// Active reports a fresh unit heartbeat.
func (c *KmodController) Active() bool {
	_, ok := c.Running()
	return ok
}

func (c *KmodController) StartBoot(ctx context.Context) bool {
	if c == nil || c.StatusPath == "" || c.Launch == nil || c.NeedBoot == nil || !c.NeedBoot() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.Running(); ok {
		return false
	}
	st, _, _ := ReadKmodStatus(c.StatusPath)
	now := c.now().Unix()
	st.Mode, st.State, st.Started, st.Updated = KmodBoot, StateRunning, now, now
	if WriteKmodStatus(c.StatusPath, st) != nil {
		return false
	}
	if err := c.Launch(ctx); err != nil {
		st.State, st.Updated = StateFailed, c.now().Unix()
		_ = WriteKmodStatus(c.StatusPath, st)
		return false
	}
	return true
}

type KmodConfig struct {
	Mode       KmodMode
	StatusPath string
	NodePath   string
	StateDir   string
	Out        io.Writer
	Run        Runner
	Sys        Sys
	SecureBoot func(context.Context) bool
	Now        func() time.Time
	Heartbeat  time.Duration
	Budget     time.Duration // boot only; zero = KmodBootBudget
	LockWait   time.Duration // after-apt and repair only; zero = KmodLockWait
}

// SafePath is the existing systemd path rule shared by unit rendering and hook generation.
func SafePath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return false
	}
	for _, r := range p {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("_./-", r)) {
			return false
		}
	}
	return true
}

func KmodHookBody(nodePath, stateDir string) (string, error) {
	if !SafePath(nodePath) || filepath.Dir(nodePath) == "/" || !SafePath(stateDir) || stateDir == "/" {
		return "", errors.New("unsafe node binary or state path for the apt hook")
	}
	status := path.Join(stateDir, KmodStatusFileName)
	body := fmt.Sprintf("DPkg::Pre-Invoke { \"if [ -x %s ]; then timeout -k 5 20 %s awg kmod-sync --patch-only || true; fi\"; };\n"+
		"DPkg::Post-Invoke { \"if [ -x %s ]; then timeout 10 systemd-run --no-block --quiet --collect --unit=%s --property=RuntimeMaxSec=%d %s awg kmod-sync --after-apt --status-file %s || true; fi\"; };\n",
		nodePath, nodePath, nodePath, KmodUnitName, KmodUnitMaxSec, nodePath, status)
	return body, nil
}

func ubuntuKmodHook(e Env) (string, error) {
	nodePath, stateDir := e.NodePath, e.StateDir
	if nodePath == "" {
		nodePath = "/usr/local/bin/mistgate-node"
	}
	if stateDir == "" {
		stateDir = "/var/lib/mistgate-node"
	}
	return KmodHookBody(nodePath, stateDir)
}

// RunKmodSync implements the root-only command. Patch-only never reads package state or writes any file except known compat.h
// files; the other modes use the common lock and the live job heartbeats to avoid overlapping another builder.
func RunKmodSync(ctx context.Context, cfg KmodConfig) error {
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	if cfg.Run == nil {
		cfg.Run = ExecRunner
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = KmodHeartbeat
	}
	if cfg.Mode == KmodPatchOnly {
		rs := FixCompat(cfg.Sys, cfg.Sys.sourceDirs(), false, io.Discard)
		return CompatErr(rs)
	}
	if cfg.Mode != KmodBoot && cfg.Mode != KmodAfterApt && cfg.Mode != KmodRepair {
		return fmt.Errorf("unknown kmod-sync mode %q", cfg.Mode)
	}
	if cfg.StatusPath != "" && !SafePath(cfg.StatusPath) {
		return errors.New("unsafe status file path")
	}
	limit := cfg.Budget
	if cfg.Mode == KmodBoot {
		if limit <= 0 {
			limit = KmodBootBudget
		}
	} else {
		limit = KmodRunBudget
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, limit)
	defer cancel()
	st := KmodStatus{}
	if cfg.StatusPath != "" {
		st, _, _ = ReadKmodStatus(cfg.StatusPath)
	}
	now := cfg.Now().Unix()
	if st.State != StateRunning || now-st.Updated > int64(KmodStatusStale/time.Second) {
		st.Started = now
	}
	st.Mode, st.State, st.Updated = cfg.Mode, StateRunning, now
	var statusMu sync.Mutex
	write := func(change func(*KmodStatus)) {
		if cfg.StatusPath == "" {
			return
		}
		statusMu.Lock()
		defer statusMu.Unlock()
		if change != nil {
			change(&st)
		}
		st.Updated = cfg.Now().Unix()
		_ = WriteKmodStatus(cfg.StatusPath, st)
	}
	write(nil)
	finish := func(err error) error {
		if err != nil {
			write(func(s *KmodStatus) { s.State = StateFailed })
			return err
		}
		write(func(s *KmodStatus) { s.State = StateDone })
		return nil
	}
	stop := make(chan struct{})
	var hb sync.WaitGroup
	hb.Add(1)
	go func() {
		defer hb.Done()
		t := time.NewTicker(cfg.Heartbeat)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				write(nil)
			}
		}
	}()
	defer func() { close(stop); hb.Wait() }()

	stateDir := cfg.StateDir
	if stateDir == "" && cfg.StatusPath != "" {
		stateDir = filepath.Dir(cfg.StatusPath)
	}
	if stateDir == "" {
		stateDir = "/var/lib/mistgate-node"
	}
	if !SafePath(stateDir) {
		return finish(errors.New("unsafe state directory"))
	}
	rs := FixCompat(cfg.Sys, cfg.Sys.sourceDirs(), false, cfg.Out)
	if err := CompatErr(rs); err != nil {
		return finish(err)
	}

	longRun := cfg.Mode == KmodAfterApt || cfg.Mode == KmodRepair
	if longRun {
		wait := cfg.LockWait
		if wait <= 0 {
			wait = KmodLockWait
		}
		if err := WaitAptLock(ctx, cfg.Out, []Step{{Cmd: []string{"apt-get"}}}, cfg.Sys, wait); err != nil {
			return finish(err)
		}
	}

	var release func()
	var acquired bool
	var err error
	if longRun {
		release, acquired, err = waitBuilderLock(ctx, cfg, cfg.LockWait)
	} else {
		release, acquired, err = tryBuilderLock(cfg.Sys)
	}
	if err != nil {
		return finish(fmt.Errorf("taking the shared AmneziaWG module lock: %w", err))
	}
	if !acquired {
		return finish(&Failure{Code: CodeBusy, Detail: "the module builder lock wait expired"})
	}
	if release != nil {
		defer release()
	}
	if kmodOtherBuilder(stateDir, cfg.Now()) {
		fmt.Fprintln(cfg.Out, "another OS or AmneziaWG prepare job is running; module sync skipped")
		return finish(nil)
	}
	bootAptBusy := cfg.Mode == KmodBoot && kmodAptBusy(cfg.Sys)
	var workErr error
	if bootAptBusy {
		workErr = kmodLoadBuiltWhileAptBusy(ctx, cfg)
	}
	if longRun && kmodAptBusy(cfg.Sys) {
		return finish(&Failure{Code: CodeAptLock, Detail: "a package manager started while the builder lock was being acquired"})
	}
	if longRun {
		if err := kmodConfigureIfSafe(ctx, cfg); err != nil {
			return finish(err)
		}
	}
	installed, err := KmodDKMSInstalled(ctx, cfg.Run)
	if err != nil {
		return finish(err)
	}
	if !installed {
		if cfg.Sys.RemoveFile != nil {
			if err := cfg.Sys.RemoveFile(KmodHookPath); err != nil {
				return finish(err)
			}
		}
		fmt.Fprintln(cfg.Out, "plain build: run prepare-kernel")
		return finish(workErr)
	}
	if cfg.NodePath == "" {
		return finish(errors.New("node binary path is required to write the apt hook"))
	}
	if cfg.Mode == KmodBoot && !bootAptBusy {
		workErr = kmodBoot(ctx, cfg, &st, write)
	} else if cfg.Mode != KmodBoot {
		workErr = kmodBuildAll(ctx, cfg, &st, write)
		kernel := ""
		if cfg.Sys.KernelRelease != nil {
			kernel = strings.TrimSpace(cfg.Sys.KernelRelease())
		}
		if kmodModuleFile(ctx, cfg, kernel) && !kmodModuleLoaded(cfg.Sys) {
			if loadErr := kmodLoadRunning(ctx, cfg, &st, write); loadErr != nil {
				workErr = errors.Join(workErr, loadErr)
			}
		}
	}
	body, err := KmodHookBody(cfg.NodePath, stateDir)
	if err == nil {
		var current []byte
		current, err = readSysFile(cfg.Sys, KmodHookPath)
		if err != nil || string(current) != body {
			if cfg.Sys.WriteFile == nil {
				err = errors.New("writing the apt hook is unavailable")
			} else if writeErr := cfg.Sys.WriteFile(KmodHookPath, body); writeErr != nil {
				err = fmt.Errorf("writing the apt hook: %w", writeErr)
			} else {
				err = nil
			}
		}
	}
	if workErr != nil {
		return finish(workErr)
	}
	return finish(err)
}

func kmodAptBusy(sys Sys) bool { return sys.AptBusy != nil && sys.AptBusy() }

func waitBuilderLock(ctx context.Context, cfg KmodConfig, maxWait time.Duration) (func(), bool, error) {
	if maxWait <= 0 {
		maxWait = KmodLockWait
	}
	for waited := time.Duration(0); ; waited += time.Second {
		release, acquired, err := tryBuilderLock(cfg.Sys)
		if err != nil || acquired {
			return release, acquired, err
		}
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		if waited >= maxWait {
			return nil, false, nil
		}
		cfg.Sys.sleep(ctx, time.Second)
	}
}

func kmodModuleLoaded(sys Sys) bool { return sys.Exists != nil && sys.Exists("/sys/module/amneziawg") }

func kmodLoadBuiltWhileAptBusy(ctx context.Context, cfg KmodConfig) error {
	kernel := ""
	if cfg.Sys.KernelRelease != nil {
		kernel = strings.TrimSpace(cfg.Sys.KernelRelease())
	}
	if !kmodModuleFile(ctx, cfg, kernel) || kmodModuleLoaded(cfg.Sys) {
		return nil
	}
	_, err := cfg.Run(ctx, cfg.Out, "", "modprobe", "amneziawg")
	return err
}

func kmodOtherBuilder(stateDir string, now time.Time) bool {
	return kmodHeartbeat(filepath.Join(stateDir, "os-update.json"), now, true) ||
		kmodHeartbeat(filepath.Join(stateDir, "awg-prepare.json"), now, false)
}

func readSysFile(sys Sys, path string) ([]byte, error) {
	if sys.ReadFile == nil {
		return nil, errors.New("reading files is unavailable")
	}
	return sys.ReadFile(path)
}

func tryBuilderLock(sys Sys) (func(), bool, error) {
	if sys.TryBuilderLock == nil {
		return nil, true, nil
	}
	return sys.TryBuilderLock()
}

func kmodHeartbeat(path string, now time.Time, ignoreRebooting bool) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var st struct {
		State   string `json:"state"`
		Phase   string `json:"phase"`
		Updated int64  `json:"updated"`
	}
	if json.Unmarshal(b, &st) != nil || st.State != StateRunning || ignoreRebooting && st.Phase == "rebooting" {
		return false
	}
	age := now.Unix() - st.Updated
	return age <= int64(Stale/time.Second) && age >= -int64(Stale/time.Second)
}

// KmodDKMSInstalled accepts held and half-configured installed packages by their dpkg state letter.
func KmodDKMSInstalled(ctx context.Context, run Runner) (bool, error) {
	out, err := run(ctx, io.Discard, "", "dpkg-query", "-W", "-f=${db:Status-Abbrev}", "amneziawg-dkms")
	status := strings.TrimSpace(out)
	if dpkgPresent(status) {
		return true, nil // installed or half-configured; after-apt must be able to finish dpkg first
	}
	if err != nil && !strings.Contains(strings.ToLower(out), "no packages found matching amneziawg-dkms") {
		return false, fmt.Errorf("checking amneziawg-dkms: %w", err)
	}
	return false, nil
}

func dpkgPresent(status string) bool {
	return len(status) > 1 && strings.ContainsRune("iUFWt", rune(status[1]))
}

func kmodConfigureIfSafe(ctx context.Context, cfg KmodConfig) error {
	out, err := cfg.Run(ctx, io.Discard, "", "dpkg-query", "-W", "-f=${db:Status-Abbrev}\t${Package}\n")
	if err != nil && out == "" {
		return fmt.Errorf("reading the dpkg package states: %w", err)
	}
	var half []string
	hasHalfInstalled := false
	for _, line := range strings.Split(out, "\n") {
		status, pkg, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok || len(status) < 2 {
			continue
		}
		if status[1] == 'H' {
			hasHalfInstalled = true
			continue
		}
		if status[1] != 'U' && status[1] != 'F' {
			continue // triggered/awaited packages in W/t do not block configuration
		}
		pkg = strings.TrimSpace(pkg)
		if pkg != "" {
			half = append(half, pkg)
		}
	}
	if hasHalfInstalled {
		fmt.Fprintln(cfg.Out, "not running dpkg --configure -a: a package is half-installed")
		return nil
	}
	if len(half) == 0 {
		return nil
	}
	for _, pkg := range half {
		if !strings.HasPrefix(pkg, "linux-") && !strings.HasPrefix(pkg, "amneziawg") {
			fmt.Fprintf(cfg.Out, "not running dpkg --configure -a: unrelated half-configured package %s\n", pkg)
			return nil
		}
	}
	fmt.Fprintf(cfg.Out, "finishing package configuration for %s\n", strings.Join(half, ", "))
	args := append([]string(nil), configureAll...)
	// dpkg configuration is a transaction and must outlive the caller's context once it starts.
	_, err = cfg.Run(context.WithoutCancel(ctx), cfg.Out, "", args...)
	if err != nil {
		return fmt.Errorf("dpkg --configure -a failed: %w", err)
	}
	return nil
}

var safeKernelRelease = regexp.MustCompile(`^[0-9][A-Za-z0-9._+-]{0,127}$`)

func kmodInstalledKernels(ctx context.Context, run Runner) ([]string, error) {
	out, err := run(ctx, io.Discard, "", "dpkg-query", "-W", "-f=${db:Status-Abbrev}\t${Package}\n")
	if err != nil && out == "" {
		return nil, err
	}
	set := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		status, pkg, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok || !dpkgPresent(status) {
			continue
		}
		pkg = strings.TrimSpace(pkg)
		for _, prefix := range []string{"linux-image-unsigned-", "linux-image-"} {
			if strings.HasPrefix(pkg, prefix) {
				k := strings.TrimPrefix(pkg, prefix)
				if safeKernelRelease.MatchString(k) {
					set[k] = true
				}
				break
			}
		}
	}
	kernels := make([]string, 0, len(set))
	for k := range set {
		kernels = append(kernels, k)
	}
	sort.Strings(kernels)
	return kernels, nil
}

func kmodHasHeaders(sys Sys, kernel string) bool {
	if !safeKernelRelease.MatchString(kernel) || sys.Exists == nil {
		return false
	}
	return sys.Exists("/usr/src/linux-headers-" + kernel)
}

func kmodModuleFile(ctx context.Context, cfg KmodConfig, kernel string) bool {
	if !safeKernelRelease.MatchString(kernel) {
		return false
	}
	out, err := cfg.Run(ctx, io.Discard, "", "modinfo", "-k", kernel, "-F", "filename", "amneziawg")
	return err == nil && strings.TrimSpace(out) != ""
}

func kmodSourceSHA(sys Sys) string {
	if sys.ReadFile == nil {
		return ""
	}
	dirs := append([]string(nil), sys.sourceDirs()...)
	sort.Strings(dirs)
	h := sha256.New()
	found := false
	for _, dir := range dirs {
		var files []string
		walkErr := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type().IsRegular() {
				files = append(files, path)
			}
			return nil
		})
		sort.Strings(files)
		if walkErr != nil {
			return ""
		}
		for _, path := range files {
			b, err := sys.ReadFile(path)
			if err != nil {
				return ""
			}
			found = true
			_, _ = h.Write([]byte(strings.TrimPrefix(path, dir)))
			_, _ = h.Write([]byte{0})
			_, _ = h.Write(b)
			_, _ = h.Write([]byte{0})
		}
	}
	if !found {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func kmodVersion(sys Sys) (string, error) {
	var versions []string
	for _, dir := range sys.sourceDirs() {
		v := strings.TrimPrefix(filepath.Base(dir), "amneziawg-")
		if v != filepath.Base(dir) && v != "" && regexp.MustCompile(`^[A-Za-z0-9.+_-]+$`).MatchString(v) {
			versions = append(versions, v)
		}
	}
	if len(versions) != 0 {
		sort.Slice(versions, func(i, j int) bool {
			a, b := "v"+versions[i], "v"+versions[j]
			if semver.IsValid(a) && semver.IsValid(b) {
				return semver.Compare(a, b) < 0
			}
			return versions[i] < versions[j]
		})
		return versions[len(versions)-1], nil
	}
	return "", errors.New("cannot determine the AmneziaWG DKMS source version")
}

func kmodBuildKernel(ctx context.Context, cfg KmodConfig, kernel string, st *KmodStatus, write func(func(*KmodStatus))) error {
	if !safeKernelRelease.MatchString(kernel) || !kmodHasHeaders(cfg.Sys, kernel) || kmodModuleFile(ctx, cfg, kernel) {
		return nil
	}
	sha := kmodSourceSHA(cfg.Sys)
	if cfg.Mode == KmodBoot && sha != "" {
		for _, record := range st.Failed {
			if record.Kernel == kernel && record.SourceSHA == sha {
				fmt.Fprintf(cfg.Out, "skipping failed AmneziaWG build for kernel %s and source %s\n", kernel, shortSHA(sha))
				return nil
			}
		}
	}
	if kmodAptBusy(cfg.Sys) {
		if cfg.Mode == KmodBoot {
			return nil
		}
		return &Failure{Code: CodeAptLock, Detail: "a package manager started before the DKMS build"}
	}
	fmt.Fprintf(cfg.Out, "building AmneziaWG for kernel %s\n", kernel)
	out, err := cfg.Run(ctx, cfg.Out, "", "dkms", "autoinstall", "-k", kernel)
	if err != nil {
		if cfg.Mode == KmodBoot && sha != "" {
			write(func(s *KmodStatus) { s.Failed = append(s.Failed, KmodRebuild{Kernel: kernel, SourceSHA: sha}) })
		}
		return fmt.Errorf("dkms autoinstall for kernel %s failed: %s: %w", kernel, clip(strings.TrimSpace(out), 200), err)
	}
	return nil
}

func kmodBuildAll(ctx context.Context, cfg KmodConfig, st *KmodStatus, write func(func(*KmodStatus))) error {
	kernels, err := kmodInstalledKernels(ctx, cfg.Run)
	if err != nil {
		return fmt.Errorf("listing installed kernels: %w", err)
	}
	var failures []string
	for _, kernel := range kernels {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := kmodBuildKernel(ctx, cfg, kernel, st, write); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failures = append(failures, err.Error())
		}
	}
	if len(failures) != 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func kmodBoot(ctx context.Context, cfg KmodConfig, st *KmodStatus, write func(func(*KmodStatus))) error {
	kernel := ""
	if cfg.Sys.KernelRelease != nil {
		kernel = strings.TrimSpace(cfg.Sys.KernelRelease())
	}
	if !safeKernelRelease.MatchString(kernel) {
		return errors.New("cannot read a safe running kernel release")
	}
	var failures []string
	if err := kmodBuildKernel(ctx, cfg, kernel, st, write); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		failures = append(failures, err.Error())
	}
	if err := kmodLoadRunning(ctx, cfg, st, write); err != nil {
		failures = append(failures, err.Error())
	}
	kernels, err := kmodInstalledKernels(ctx, cfg.Run)
	if err != nil {
		failures = append(failures, fmt.Sprintf("listing installed kernels: %v", err))
	} else {
		for _, other := range kernels {
			if other == kernel {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := kmodBuildKernel(ctx, cfg, other, st, write); err != nil {
				failures = append(failures, err.Error())
			}
		}
	}
	if len(failures) != 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func kmodLoadRunning(ctx context.Context, cfg KmodConfig, st *KmodStatus, write func(func(*KmodStatus))) error {
	kernel := ""
	if cfg.Sys.KernelRelease != nil {
		kernel = strings.TrimSpace(cfg.Sys.KernelRelease())
	}
	if !safeKernelRelease.MatchString(kernel) {
		return errors.New("cannot read a safe running kernel release")
	}
	out, err := cfg.Run(ctx, cfg.Out, "", "modprobe", "amneziawg")
	if err == nil {
		return nil
	}
	if kmodAptBusy(cfg.Sys) {
		return errors.New("a package manager started before the DKMS repair")
	}
	if kmodSecureBoot(cfg, ctx) || strings.Contains(strings.ToLower(out+" "+err.Error()), "key was rejected") {
		fmt.Fprintf(cfg.Out, "Secure Boot rejected the AmneziaWG module for kernel %s; it was not rebuilt\n", kernel)
		return nil
	}
	if !kmodModuleFile(ctx, cfg, kernel) {
		return fmt.Errorf("modprobe amneziawg for kernel %s failed: %s: %w", kernel, clip(strings.TrimSpace(out), 200), err)
	}
	sha := kmodSourceSHA(cfg.Sys)
	if sha == "" {
		return errors.New("cannot hash the AmneziaWG DKMS source; refusing an unrecorded rebuild")
	}
	for _, record := range st.Rebuilt {
		if record.Kernel == kernel && record.SourceSHA == sha {
			return fmt.Errorf("modprobe amneziawg for kernel %s failed; one rebuild for source %s was already attempted", kernel, shortSHA(sha))
		}
	}
	version, err := kmodVersion(cfg.Sys)
	if err != nil {
		return err
	}
	if kmodAptBusy(cfg.Sys) {
		return errors.New("a package manager started before the DKMS repair")
	}
	write(func(s *KmodStatus) {
		s.Rebuilt = append(s.Rebuilt, KmodRebuild{Kernel: kernel, SourceSHA: sha})
	}) // persist before touching DKMS: a failed or interrupted repair must not loop on the next boot
	if _, err := cfg.Run(ctx, cfg.Out, "", "dkms", "remove", "-m", "amneziawg", "-v", version, "-k", kernel); err != nil {
		return fmt.Errorf("dkms remove for kernel %s failed: %w", kernel, err)
	}
	if kmodAptBusy(cfg.Sys) {
		return errors.New("a package manager started before the DKMS install")
	}
	if _, err := cfg.Run(ctx, cfg.Out, "", "dkms", "install", "-m", "amneziawg", "-v", version, "-k", kernel); err != nil {
		return fmt.Errorf("dkms install for kernel %s failed: %w", kernel, err)
	}
	out, err = cfg.Run(ctx, cfg.Out, "", "modprobe", "amneziawg")
	if err != nil {
		if kmodSecureBoot(cfg, ctx) || strings.Contains(strings.ToLower(out+" "+err.Error()), "key was rejected") {
			fmt.Fprintf(cfg.Out, "Secure Boot rejected the AmneziaWG module for kernel %s after rebuilding\n", kernel)
			return nil
		}
		return fmt.Errorf("modprobe amneziawg for kernel %s failed after one rebuild: %s: %w", kernel, clip(strings.TrimSpace(out), 200), err)
	}
	return nil
}

func kmodSecureBoot(cfg KmodConfig, ctx context.Context) bool {
	return cfg.SecureBoot != nil && cfg.SecureBoot(ctx)
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
