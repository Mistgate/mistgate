package doctor

import (
	"fmt"
	"strconv"
	"testing"
	"time"
)

// statOf builds an FSStat of totalMiB MiB (1 MiB blocks) with freeMiB free.
func statOf(totalMiB, freeMiB uint64, files, ffree uint64) FSStat {
	return FSStat{BlockSize: 1 << 20, Blocks: totalMiB, Bfree: freeMiB, Bavail: freeMiB, Files: files, Ffree: ffree}
}

func TestDiskSpaceThresholds(t *testing.T) {
	tests := []struct {
		name string
		st   FSStat
		want Status
	}{
		{"79 percent used, plenty free", statOf(100000, 21000, 1000, 900), OK},
		{"80 percent used", statOf(100000, 20000, 1000, 900), Warn},
		{"91 percent used", statOf(100000, 9000, 1000, 900), Warn},
		{"92 percent used", statOf(100000, 8000, 1000, 900), Fail},
		{"free just above 1 GiB", statOf(4000, 1025, 1000, 900), OK},
		{"free below 1 GiB", statOf(4000, 1023, 1000, 900), Warn},
		{"free just above 300 MiB", statOf(400, 301, 1000, 900), Warn},
		{"free below 300 MiB", statOf(400, 299, 1000, 900), Fail},
		{"inodes 94 percent", statOf(100000, 50000, 1000, 60), OK},
		{"inodes 95 percent", statOf(100000, 50000, 1000, 50), Fail},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.stat["/"] = tc.st
			r := run(t, f.doctor(), CheckDiskSpace)
			want(t, r, tc.want, "")
		})
	}
}

func TestDiskSpaceWorstMountAndJournalFix(t *testing.T) {
	f := newFake(t)
	f.stat["/"] = statOf(100000, 50000, 1000, 900)
	f.stat["/var/lib/mistgate"] = statOf(1000, 50, 1000, 900) // the state volume is nearly full
	f.putSized("/var/log/journal/abc/system.journal", 400<<20)
	f.putSized("/var/log/btmp", 1100<<20)
	f.bin("journalctl")
	r := run(t, f.doctor(func(e *Env) { e.StateDir = "/var/lib/mistgate" }), CheckDiskSpace)
	want(t, r, Fail, FixJournaldVacuum)
	param(t, r, "mount", "/var/lib/mistgate")
	param(t, r, "journal_mb", "400")
	param(t, r, "btmp_mb", "1100")

	// No fix when the journal is small: the space is somewhere else (btmp is only named).
	f.remove("/var/log/journal")
	f.putSized("/var/log/journal/abc/system.journal", 10<<20)
	r = run(t, f.doctor(func(e *Env) { e.StateDir = "/var/lib/mistgate" }), CheckDiskSpace)
	want(t, r, Fail, "")

	// The state dir on the same volume is not counted twice.
	f.stat["/var/lib/mistgate"] = f.stat["/"]
	r = run(t, f.doctor(func(e *Env) { e.StateDir = "/var/lib/mistgate" }), CheckDiskSpace)
	want(t, r, OK, "")

	// statfs failing everywhere is a SKIP, not an OK.
	delete(f.stat, "/")
	delete(f.stat, "/var/lib/mistgate")
	want(t, run(t, f.doctor(), CheckDiskSpace), Skip, "")
}

func TestJournaldSizeThresholds(t *testing.T) {
	const mib = 1 << 20
	tests := []struct {
		size int64
		want Status
		fix  string
	}{
		{200 * mib, OK, ""},
		{300 * mib, OK, ""},
		{300*mib + 1, Warn, FixJournaldVacuum},
		{1 << 30, Warn, FixJournaldVacuum},
		{1<<30 + 1, Fail, FixJournaldVacuum},
	}
	for _, tc := range tests {
		t.Run(strconv.FormatInt(tc.size, 10), func(t *testing.T) {
			f := newFake(t)
			f.bin("journalctl")
			f.putSized("/var/log/journal/m/a.journal", tc.size/2)
			f.putSized("/run/log/journal/m/b.journal", tc.size-tc.size/2) // persistent + volatile add up
			want(t, run(t, f.doctor(), CheckJournaldSize), tc.want, tc.fix)
		})
	}
	f := newFake(t)
	want(t, run(t, f.doctor(), CheckJournaldSize), Skip, "") // no journal directory at all
	f.putSized("/var/log/journal/m/a.journal", 2<<30)
	want(t, run(t, f.doctor(), CheckJournaldSize), Fail, "") // no journalctl: nothing to offer
}

// procStatLine builds a /proc/<pid>/stat line; start is the start time in ticks.
func procStatLine(pid int, comm string, state byte, ppid int, flags uint64, start uint64) string {
	return fmt.Sprintf("%d (%s) %c %d %d %d 0 -1 %d 0 0 0 0 0 0 0 0 20 0 1 0 %d 0 0\n", pid, comm, state, ppid, pid, pid, flags, start)
}

func TestParseProcStat(t *testing.T) {
	ps, ok := parseProcStat(procStatLine(812, "a (b) c", 'D', 1, 4194560, 123456))
	if !ok || ps.pid != 812 || ps.comm != "a (b) c" || ps.state != 'D' || ps.ppid != 1 || ps.starts != 123456 || ps.flags != 4194560 {
		t.Fatalf("parsed %+v ok=%v", ps, ok)
	}
	if ps.kernelThread() {
		t.Error("a normal task is not a kernel thread")
	}
	k, _ := parseProcStat(procStatLine(9, "kworker/0:1", 'D', 2, pfKthread, 5))
	if !k.kernelThread() {
		t.Error("kernel thread not recognised")
	}
	for _, bad := range []string{"", "garbage", "1 (x) D 1", "x (y) D 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 5 0 0"} {
		if _, ok := parseProcStat(bad); ok {
			t.Errorf("parsed %q", bad)
		}
	}
}

func TestDstateTasks(t *testing.T) {
	setup := func(f *fake) {
		f.put("/proc/1/stat", procStatLine(1, "systemd", 'S', 0, 0, 1))
		f.put("/proc/812/stat", procStatLine(812, "apt-get", 'D', 1, 0, 9000))
		f.put("/proc/13/stat", procStatLine(13, "kworker/u2:0", 'D', 2, pfKthread, 50)) // kernel threads do not count
	}
	t.Run("stuck in every sample", func(t *testing.T) {
		f := newFake(t)
		setup(f)
		r := run(t, f.doctor(), CheckDstateTasks)
		want(t, r, Warn, "")
		param(t, r, "stuck", "1")
		param(t, r, "tasks", "apt-get(812)")
	})
	t.Run("leaves D between samples", func(t *testing.T) {
		f := newFake(t)
		setup(f)
		n := 0
		f.sleep = func() {
			if n++; n == 1 {
				f.put("/proc/812/stat", procStatLine(812, "apt-get", 'S', 1, 0, 9000))
			}
		}
		want(t, run(t, f.doctor(), CheckDstateTasks), OK, "")
	})
	t.Run("same pid but a new task is not the same task", func(t *testing.T) {
		f := newFake(t)
		setup(f)
		n := 0
		f.sleep = func() {
			if n++; n == 1 {
				f.put("/proc/812/stat", procStatLine(812, "apt-get", 'D', 1, 0, 9999)) // pid reuse
			}
		}
		want(t, run(t, f.doctor(), CheckDstateTasks), OK, "")
	})
	t.Run("nothing in D never sleeps", func(t *testing.T) {
		f := newFake(t)
		f.put("/proc/1/stat", procStatLine(1, "systemd", 'S', 0, 0, 1))
		slept := 0
		f.sleep = func() { slept++ }
		want(t, run(t, f.doctor(), CheckDstateTasks), OK, "")
		if slept != 0 {
			t.Errorf("slept %d times with nothing to watch", slept)
		}
	})
	t.Run("second consecutive run is a FAIL", func(t *testing.T) {
		f := newFake(t)
		setup(f)
		d := f.doctor()
		want(t, run(t, d, CheckDstateTasks), Warn, "")
		want(t, run(t, d, CheckDstateTasks), Fail, "")
	})
	t.Run("TTM or QXL in the kernel log", func(t *testing.T) {
		f := newFake(t)
		setup(f)
		f.bin("journalctl")
		f.cmd("journalctl -k --no-pager -q -p warning -o cat --since -24h -n 5000",
			"usb 1-1: new device\nqxl 0000:00:02.0: [TTM] timeout waiting for fence\n", nil)
		r := run(t, f.doctor(), CheckDstateTasks)
		want(t, r, Fail, "")
		param(t, r, "hint", "qxl_ttm")
	})
	t.Run("idle CPU with a load of one per core for ten minutes", func(t *testing.T) {
		f := newFake(t)
		f.put("/proc/1/stat", procStatLine(1, "systemd", 'S', 0, 0, 1))
		mk := func(load, cpu float64, n int) func(time.Duration) []Sample {
			return func(time.Duration) []Sample {
				var s []Sample
				for i := n - 1; i >= 0; i-- {
					s = append(s, Sample{At: t0.Add(-time.Duration(i) * 10 * time.Second), CPU: cpu, Load1: load})
				}
				return s
			}
		}
		withSamples := func(fn func(time.Duration) []Sample) func(*Env) { return func(e *Env) { e.Samples = fn } }
		r := run(t, f.doctor(withSamples(mk(3.8, 5, 60))), CheckDstateTasks)
		want(t, r, Fail, "")
		param(t, r, "load_idle", "1")
		want(t, run(t, f.doctor(withSamples(mk(3.4, 5, 60))), CheckDstateTasks), OK, "")  // 0.9 x 4 CPUs = 3.6
		want(t, run(t, f.doctor(withSamples(mk(3.8, 20, 60))), CheckDstateTasks), OK, "") // busy, not stuck
		want(t, run(t, f.doctor(withSamples(mk(3.8, 5, 20))), CheckDstateTasks), OK, "")  // not enough history
	})
	t.Run("no procfs", func(t *testing.T) {
		want(t, run(t, newFake(t).doctor(), CheckDstateTasks), Skip, "")
	})
}

func TestTimeSync(t *testing.T) {
	tests := []struct {
		name   string
		offset int64
		synced string
		virt   string
		want   Status
	}{
		{"in sync", 0, "yes", "kvm", OK},
		{"2 s is fine", 2, "yes", "kvm", OK},
		{"3 s warns", 3, "yes", "kvm", Warn},
		{"negative offset counts", -3, "yes", "kvm", Warn},
		{"30 s warns", 30, "yes", "kvm", Warn},
		{"31 s fails", 31, "yes", "kvm", Fail},
		{"not synchronized", 0, "no", "kvm", Warn},
		{"a container's clock is the host's", 0, "no", "lxc", OK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.bin("timedatectl")
			f.cmd("timedatectl show -p NTPSynchronized --value", tc.synced+"\n", nil)
			r := run(t, f.doctor(func(e *Env) {
				e.Virt = tc.virt
				e.Offset = func() int64 { return tc.offset }
			}), CheckTimeSync)
			want(t, r, tc.want, "")
		})
	}
	// Without timedatectl the offset alone decides.
	f := newFake(t)
	r := run(t, f.doctor(), CheckTimeSync)
	want(t, r, OK, "")
	param(t, r, "ntp_synced", "unknown")
}

func TestMemoryPressure(t *testing.T) {
	meminfoWith := func(totalMB, availMB, swapTotalMB, swapFreeMB int) string {
		return fmt.Sprintf("MemTotal: %d kB\nMemFree: 1 kB\nMemAvailable: %d kB\nSwapTotal: %d kB\nSwapFree: %d kB\n",
			totalMB*1024, availMB*1024, swapTotalMB*1024, swapFreeMB*1024)
	}
	psi := func(some, full float64) string {
		return fmt.Sprintf("some avg10=0.00 avg60=%.2f avg300=0.00 total=0\nfull avg10=0.00 avg60=%.2f avg300=0.00 total=0\n", some, full)
	}
	tests := []struct {
		name string
		mem  string
		psi  string
		klog string
		want Status
	}{
		{"healthy", meminfoWith(1000, 500, 0, 0), psi(0, 0), "", OK},
		{"12 percent available is fine", meminfoWith(1000, 120, 0, 0), psi(0, 0), "", OK},
		{"11 percent warns", meminfoWith(1000, 119, 0, 0), psi(0, 0), "", Warn},
		{"exactly 5 percent still only warns", meminfoWith(1000, 50, 0, 0), psi(0, 0), "", Warn},
		{"below 5 percent fails", meminfoWith(1000, 49, 0, 0), psi(0, 0), "", Fail},
		{"swap over half with pressure", meminfoWith(1000, 500, 1000, 400), psi(10, 0), "", Warn},
		{"swap over half without pressure", meminfoWith(1000, 500, 1000, 400), psi(9.9, 0), "", OK},
		{"full stall fails", meminfoWith(1000, 500, 0, 0), psi(0, 5), "", Fail},
		{"OOM kill of something else", meminfoWith(1000, 500, 0, 0), psi(0, 0), "Out of memory: Killed process 4242 (php-fpm) total-vm:1kB\n", Warn},
		{"OOM kill of the agent", meminfoWith(1000, 500, 0, 0), psi(0, 0), "Out of memory: Killed process 77 (mistgate-node) total-vm:1kB\n", Fail},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.put("/proc/meminfo", tc.mem)
			f.put("/proc/pressure/memory", tc.psi)
			f.bin("journalctl")
			f.cmd("journalctl -k --no-pager -q -p warning -o cat --since -24h -n 5000", tc.klog, nil)
			want(t, run(t, f.doctor(), CheckMemoryPressure), tc.want, "")
		})
	}
	t.Run("no PSI and an unreadable kernel log still judge RAM", func(t *testing.T) {
		f := newFake(t)
		f.put("/proc/meminfo", meminfoWith(1000, 500, 1000, 400)) // swap 60% but no PSI and 50% available
		r := run(t, f.doctor(), CheckMemoryPressure)
		want(t, r, OK, "")
		param(t, r, "oom_kills", "unknown")
	})
	t.Run("no meminfo", func(t *testing.T) {
		want(t, run(t, newFake(t).doctor(), CheckMemoryPressure), Skip, "")
	})
	t.Run("dmesg fallback keeps only the last day", func(t *testing.T) {
		f := newFake(t)
		f.put("/proc/meminfo", meminfoWith(1000, 500, 0, 0))
		f.bin("dmesg")
		old := t0.Add(-48 * time.Hour).Format("2006-01-02T15:04:05,000000-07:00")
		cur := t0.Add(-time.Hour).Format("2006-01-02T15:04:05,000000-07:00")
		f.cmd("dmesg --time-format=iso --level=emerg,alert,crit,err,warn",
			old+" Out of memory: Killed process 1 (old) x\n"+cur+" Out of memory: Killed process 2 (php-fpm) x\n", nil)
		r := run(t, f.doctor(), CheckMemoryPressure)
		want(t, r, Warn, "")
		param(t, r, "oom_kills", "1")
		param(t, r, "oom_victims", "php-fpm")
	})
}

func TestCPUSoftirq(t *testing.T) {
	mk := func(soft, cpu float64, n int) func(*Env) {
		return func(e *Env) {
			e.Samples = func(time.Duration) []Sample {
				s := make([]Sample, n)
				for i := range s {
					s[i] = Sample{At: t0.Add(-time.Duration(n-i) * 10 * time.Second), CPU: cpu, Softirq: soft}
				}
				return s
			}
		}
	}
	f := newFake(t)
	want(t, run(t, f.doctor(mk(80, 90, 29)), CheckCPUSoftirq), Skip, "") // not enough samples yet
	want(t, run(t, f.doctor(mk(10, 30, 30)), CheckCPUSoftirq), OK, "")
	want(t, run(t, f.doctor(mk(49.9, 60, 60)), CheckCPUSoftirq), OK, "")
	want(t, run(t, f.doctor(mk(50, 60, 60)), CheckCPUSoftirq), Warn, "")
	want(t, run(t, f.doctor(mk(89, 60, 60)), CheckCPUSoftirq), Warn, "")
	want(t, run(t, f.doctor(mk(90, 95, 60)), CheckCPUSoftirq), Fail, "")
	want(t, run(t, f.doctor(mk(10, 97, 60)), CheckCPUSoftirq), Fail, "") // CPU pegged for ten minutes
}

func TestKernelHeaders(t *testing.T) {
	f := newFake(t)
	f.put("/proc/sys/kernel/osrelease", "6.1.0-18-amd64\n")
	f.bin("dkms", "make", "gcc")
	// Without an AmneziaWG inbound the check has nothing to say, whatever is missing.
	want(t, run(t, f.doctor(), CheckKernelHeaders), Skip, "")
	hy := func(e *Env) {
		e.Inbounds = func() []Inbound { return []Inbound{{ID: "h", Protocol: "hysteria2", Enabled: true}} }
	}
	want(t, run(t, f.doctor(hy), CheckKernelHeaders), Skip, "")

	awg := func(e *Env) {
		e.Inbounds = func() []Inbound { return []Inbound{{ID: "a", Protocol: "awg", Enabled: true}} }
	}
	// Kernel mode asked for and the module is not running: the missing pieces are a real WARN.
	kernelMode := func(e *Env) {
		awg(e)
		e.AwgBackend = func() AwgBackend {
			return AwgBackend{Mode: "kernel", Reason: "the amneziawg kernel module is not loaded"}
		}
	}
	r := run(t, f.doctor(kernelMode), CheckKernelHeaders)
	want(t, r, Warn, "")
	param(t, r, "missing", "headers")
	param(t, r, "mode", "kernel")

	f.put("/lib/modules/6.1.0-18-amd64/build/Makefile", "")
	r = run(t, f.doctor(kernelMode), CheckKernelHeaders)
	want(t, r, OK, "")
	if r.Code != CodeHeadersReady {
		t.Errorf("code = %s", r.Code)
	}

	delete(f.bins, "dkms")
	r = run(t, f.doctor(kernelMode), CheckKernelHeaders)
	want(t, r, Warn, "")
	param(t, r, "missing", "dkms")

	// cc is as good as gcc.
	f.bin("dkms")
	delete(f.bins, "gcc")
	f.bin("cc")
	want(t, run(t, f.doctor(kernelMode), CheckKernelHeaders), OK, "")

	// Containers build no kernel modules.
	want(t, run(t, f.doctor(awg, func(e *Env) { e.Virt = "openvz" }), CheckKernelHeaders), Skip, "")
}

// The severity of kernel_headers (the owner's complaint: a WARN on a node whose AmneziaWG runs in userspace by design).
func TestKernelHeadersSeverityMatrix(t *testing.T) {
	userspace := AwgBackend{Mode: "auto", Name: "userspace", Version: "v1", Available: true}
	tests := []struct {
		name    string
		backend *AwgBackend // nil = this build has no awg engine
		virt    string
		noAwg   bool
		missing bool // build tools and headers are absent
		want    Status
		code    string
	}{
		{"auto, userspace runs, tools missing", &userspace, "kvm", false, true, OK, CodeHeadersUserspace},
		{"userspace asked, userspace runs, tools missing", &AwgBackend{Mode: "userspace", Name: "userspace", Available: true}, "kvm", false, true, OK, CodeHeadersUserspace},
		{"empty mode counts as auto", &AwgBackend{Name: "userspace", Available: true}, "kvm", false, true, OK, CodeHeadersUserspace},
		{"auto, no backend at all, tools missing", &AwgBackend{Mode: "auto", Reason: "no tun"}, "kvm", false, true, OK, CodeHeadersOptional},
		{"no engine in this build", nil, "kvm", false, true, OK, CodeHeadersOptional},
		{"auto, kernel module runs, tools missing", &AwgBackend{Mode: "auto", Name: "kernel", Available: true}, "kvm", false, true, OK, CodeHeadersOptional},
		{"kernel asked, module loaded, tools missing", &AwgBackend{Mode: "kernel", Name: "kernel", Available: true}, "kvm", false, true, OK, CodeHeadersOptional},
		{"kernel asked, module missing, tools missing", &AwgBackend{Mode: "kernel", Reason: "not loaded"}, "kvm", false, true, Warn, CodeHeadersMissing},
		{"auto, userspace runs, tools present", &userspace, "kvm", false, false, OK, CodeHeadersReady},
		{"kernel asked, tools present", &AwgBackend{Mode: "kernel", Reason: "not loaded"}, "kvm", false, false, OK, CodeHeadersReady},
		{"no awg inbound, whatever is missing", &userspace, "kvm", true, true, Skip, CodeHeadersNoAwg},
		{"container, kernel asked", &AwgBackend{Mode: "kernel", Reason: "x"}, "lxc", false, true, Skip, CodeHeadersContainer},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.put("/proc/sys/kernel/osrelease", "6.8.0-142-generic\n")
			if !tc.missing {
				f.put("/lib/modules/6.8.0-142-generic/build/Makefile", "")
				f.bin("dkms", "make", "gcc")
			}
			r := run(t, f.doctor(func(e *Env) {
				e.Virt = tc.virt
				e.Inbounds = func() []Inbound { return []Inbound{{ID: "a", Protocol: "awg", Enabled: !tc.noAwg}} }
				if tc.backend != nil {
					b := *tc.backend
					e.AwgBackend = func() AwgBackend { return b }
				}
			}), CheckKernelHeaders)
			want(t, r, tc.want, "")
			if r.Code != tc.code {
				t.Errorf("code = %s, want %s (%s)", r.Code, tc.code, r.Detail)
			}
			if tc.missing && tc.want != Skip {
				param(t, r, "missing", "headers,dkms,make,gcc")
				param(t, r, "kernel", "6.8.0-142-generic")
			}
		})
	}
}

func TestPSIAndMeminfoParsers(t *testing.T) {
	if v := psiAvg60("some avg10=1 avg60=2.5 avg300=3 total=9\nfull avg10=0 avg60=0.5 avg300=0 total=1\n", "full"); v != 0.5 {
		t.Errorf("full avg60 = %v", v)
	}
	if psiAvg60("", "some") != -1 {
		t.Error("missing PSI must be -1")
	}
	m := meminfo("MemTotal:  1024 kB\nHugePages_Total: 3\n")
	if m["MemTotal"] != 1024*1024 || m["HugePages_Total"] != 3 {
		t.Errorf("meminfo = %v", m)
	}
}
