package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFullRunListsEveryCheckInOrder(t *testing.T) {
	f := newFake(t)
	rep, err := f.doctor().Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := CheckIDs()
	if len(ids) != 16 || rep.Partial || len(rep.Results) != len(ids) {
		t.Fatalf("partial=%v results=%d ids=%d", rep.Partial, len(rep.Results), len(ids))
	}
	for i, r := range rep.Results {
		if r.ID != ids[i] {
			t.Errorf("result %d is %s, want %s", i, r.ID, ids[i])
		}
		if !r.Measured.Equal(t0) || r.Status < OK || r.Status > Skip {
			t.Errorf("%s: measured=%v status=%v", r.ID, r.Measured, r.Status)
		}
		if r.FixID != "" && r.Status != Warn && r.Status != Fail {
			t.Errorf("%s offers a fix with status %s", r.ID, r.Status)
		}
		if r.TitleKey() != "doctor."+r.ID+".title" {
			t.Errorf("title key %q", r.TitleKey())
		}
	}
	// The id list is the contract in agent.proto: pin it.
	want := "disk_space,journald_size,dstate_tasks,time_sync,resolver,ipv6,foreign_vpn,foreign_nft,port_conflicts,net_baseline,cert_expiry,memory_pressure,cpu_softirq,kernel_headers,awg_backend,warp_path"
	if got := strings.Join(ids, ","); got != want {
		t.Errorf("check ids changed:\n got %s\nwant %s", got, want)
	}
	if got := strings.Join(FixIDs(), ","); got != "journald_vacuum,apply_baseline,restart_inbound,set_resolver,reconnect_warp" {
		t.Errorf("fix ids = %s", got)
	}
}

func TestPartialRunAndUnknownID(t *testing.T) {
	f := newFake(t)
	rep, err := f.doctor().Run(context.Background(), []string{CheckTimeSync, "nope", CheckDiskSpace, CheckTimeSync})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Partial || len(rep.Results) != 3 {
		t.Fatalf("partial=%v %+v", rep.Partial, rep.Results)
	}
	if rep.Results[0].ID != CheckTimeSync || rep.Results[1].ID != "nope" || rep.Results[2].ID != CheckDiskSpace {
		t.Errorf("order: %+v", rep.Results)
	}
	if r := rep.Results[1]; r.Status != Skip || r.Detail != "unknown check" {
		t.Errorf("unknown id answered %+v", r)
	}
}

func TestOneRunAtATime(t *testing.T) {
	f := newFake(t)
	in, release := make(chan struct{}), make(chan struct{})
	d := f.doctor(func(e *Env) {
		e.Statfs = func(string) (FSStat, error) {
			close(in)
			<-release
			return statOf(100, 50, 10, 5), nil
		}
	})
	done := make(chan *Report)
	go func() { r, _ := d.Run(context.Background(), []string{CheckDiskSpace}); done <- r }()
	<-in
	if _, err := d.Run(context.Background(), nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("second run: %v, want ErrBusy", err)
	}
	close(release)
	if r := <-done; r == nil || len(r.Results) != 1 {
		t.Fatalf("first run: %+v", r)
	}
	if _, err := d.Run(context.Background(), []string{CheckTimeSync}); err != nil {
		t.Fatalf("run after the first finished: %v", err)
	}
}

func TestStuckAndPanickingChecksBecomeSkips(t *testing.T) {
	oldChecks, oldBudget := checks, checkBudget
	t.Cleanup(func() { checks, checkBudget = oldChecks, oldBudget })
	checkBudget = 50 * time.Millisecond
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	checks = []check{
		{"stuck", func(context.Context, *Env) Result { <-block; return Result{Status: OK} }}, // ignores its context
		{"boom", func(context.Context, *Env) Result { panic("bad /proc") }},
		{"fine", func(context.Context, *Env) Result { return Result{Status: OK, Detail: strings.Repeat("é", 300)} }},
	}
	f := newFake(t)
	start := time.Now()
	rep, err := f.doctor().Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the run waited for the stuck check")
	}
	got := map[string]Result{}
	for _, r := range rep.Results {
		got[r.ID] = r
	}
	if r := got["stuck"]; r.Status != Skip || r.Detail != "timed out" {
		t.Errorf("stuck: %+v", r)
	}
	if r := got["boom"]; r.Status != Skip || !strings.Contains(r.Detail, "internal error") {
		t.Errorf("boom: %+v", r)
	}
	if r := got["fine"]; r.Status != OK || len(r.Detail) > 200 || !utf8.ValidString(r.Detail) {
		t.Errorf("fine: %d bytes valid=%v", len(r.Detail), utf8.ValidString(r.Detail))
	}
}

func TestUnsupportedHostSkipsEverything(t *testing.T) {
	f := newFake(t)
	rep, _ := f.doctor(func(e *Env) { e.Unsupported = "not a Linux host" }).Run(context.Background(), nil)
	for _, r := range rep.Results {
		if r.Status != Skip || r.Detail != "not a Linux host" {
			t.Errorf("%s: %+v", r.ID, r)
		}
	}
}

// A run never changes anything: no fix action is called and only read-only commands are executed.
func TestRunIsReadOnly(t *testing.T) {
	f := newFake(t)
	f.bin("systemctl", "nft", "timedatectl", "journalctl", "dmesg", "dkms", "make", "gcc")
	for _, c := range []string{
		"systemctl list-units --all --type=service --no-legend --no-pager --plain",
		"systemctl list-unit-files --type=service --no-legend --no-pager --plain",
		"journalctl -k --no-pager -q -p warning -o cat --since -24h -n 5000",
	} {
		f.cmd(c, "", nil)
	}
	f.cmd("nft -j list ruleset", `{"nftables":[]}`, nil)
	f.cmd("timedatectl show -p NTPSynchronized --value", "no\n", nil)
	fx := &fakeFixer{}
	var acts int
	d := f.doctor(func(e *Env) {
		e.Fixer = fx
		e.ApplyBaseline = func(context.Context) error { acts++; return nil }
		e.RestartInbound = func(context.Context, string) (uint32, error) { acts++; return 1, nil }
		e.Inbounds = func() []Inbound {
			return []Inbound{{ID: "inb_1", Enabled: true, Network: "udp", Port: 443, State: "failed", Error: "address already in use"}}
		}
	})
	if _, err := d.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if acts != 0 || fx.vacuum != 0 || fx.set != 0 {
		t.Fatalf("a run called a fix: acts=%d vacuum=%d set=%d", acts, fx.vacuum, fx.set)
	}
	for _, c := range f.calls {
		ok := false
		for _, allowed := range []string{"systemctl list-", "nft -j list", "timedatectl show", "journalctl -k", "dmesg "} {
			ok = ok || strings.HasPrefix(c, allowed)
		}
		if !ok {
			t.Errorf("a check ran %q", c)
		}
	}
}

func TestRingKeepsOnlyRecentSamplesInFixedSpace(t *testing.T) {
	var r Ring
	for i := 0; i < 3*ringSize; i++ {
		r.Add(Sample{At: t0.Add(time.Duration(i) * 10 * time.Second), CPU: float64(i)})
	}
	now := t0.Add(time.Duration(3*ringSize-1) * 10 * time.Second)
	all := r.Since(now, 24*time.Hour)
	if len(all) != ringSize {
		t.Fatalf("kept %d, want %d", len(all), ringSize)
	}
	if all[len(all)-1].CPU != float64(3*ringSize-1) || all[0].CPU != float64(2*ringSize) {
		t.Errorf("order: first %v last %v", all[0].CPU, all[len(all)-1].CPU)
	}
	ten := r.Since(now, 10*time.Minute)
	if len(ten) != 61 { // the newest sample and 60 before it, both ends inclusive
		t.Errorf("10 minutes hold %d samples", len(ten))
	}
	var empty Ring
	if len(empty.Since(now, time.Hour)) != 0 {
		t.Error("empty ring returned samples")
	}
}

func TestClip(t *testing.T) {
	if got := clip("abc", 200); got != "abc" {
		t.Error(got)
	}
	s := clip(strings.Repeat("é", 150), 201) // 300 bytes cut in the middle of a rune
	if len(s) > 201 || !utf8.ValidString(s) {
		t.Errorf("clip produced %d bytes, valid=%v", len(s), utf8.ValidString(s))
	}
}

func TestParsersOfProcNetAndKmsg(t *testing.T) {
	tcp := tcpTable(
		"   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 11 1 0\n" +
			"   1: 0100007F:0050 0100007F:C350 01 00000000:00000000 00:00000000 00000000     0        0 12 1 0\n" +
			"   2: 00000000:0000 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 13 1 0\n")
	got := parseNetSockets(tcp, "tcp")
	if len(got) != 1 || got[0].port != 80 || got[0].inode != 11 {
		t.Errorf("tcp listen sockets = %+v", got)
	}
	udp6 := udpTable("  1: 00000000000000000000000000000000:1F90 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 21 2 0\n" +
		"  2: 00000000000000000000000000000000:1F91 00000000000000000000000001000000:0035 01 00000000:00000000 00:00000000 00000000     0        0 22 2 0\n")
	got = parseNetSockets(udp6, "udp")
	if len(got) != 1 || got[0].port != 8080 || got[0].inode != 21 {
		t.Errorf("udp6 bound sockets = %+v", got)
	}
	since := t0.Add(-time.Hour)
	lines := recentDmesg([]string{
		t0.Add(-2*time.Hour).UTC().Format("2006-01-02T15:04:05,000000+00:00") + " old",
		t0.Add(-time.Minute).UTC().Format("2006-01-02T15:04:05,000000+00:00") + " new",
		"no timestamp at all",
	}, since)
	if len(lines) != 2 || !strings.HasSuffix(lines[0], " new") {
		t.Errorf("recentDmesg = %q", lines)
	}
}
