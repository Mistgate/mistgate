package hostctl

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeV6 is the kernel of one machine: an uplink de1 with a hoster address, and a log of what was asked.
type fakeV6 struct {
	iface  string
	addrs  []v6Addr
	calls  []string
	addErr error
	// replaceErr fails an add that turns an address that is there into a deprecated one; delErr fails every removal.
	replaceErr, delErr error
	fileDir            string // when set, "add" asserts that the owned file already names the address
	t                  *testing.T
}

func (f *fakeV6) DefaultIface() (string, error) { return f.iface, nil }
func (f *fakeV6) Addrs(iface string) ([]v6Addr, error) {
	if iface != f.iface {
		return nil, errors.New("no such interface")
	}
	return append([]v6Addr(nil), f.addrs...), nil
}
func (f *fakeV6) Add(iface string, a netip.Addr, replace bool) error {
	f.calls = append(f.calls, "add "+a.String())
	if replace && f.replaceErr != nil {
		return f.replaceErr
	}
	if f.fileDir != "" {
		b, _ := os.ReadFile(filepath.Join(f.fileDir, OwnedIPv6File))
		var of ownedFile
		_ = json.Unmarshal(b, &of)
		found := false
		for _, e := range of.Addrs {
			found = found || e.Addr == a.String()
		}
		if !found {
			f.t.Errorf("add %s before the owned file names it (file: %s)", a, b)
		}
	}
	if f.addErr != nil {
		return f.addErr
	}
	for i := range f.addrs {
		if f.addrs[i].Addr == a {
			f.addrs[i].Flags |= ifaDeprecated | ifaNoPrefixRoute
			return nil
		}
	}
	f.addrs = append(f.addrs, v6Addr{Addr: a, Bits: 128, Flags: ifaDeprecated | ifaNoPrefixRoute | ifaTentative})
	return nil
}
func (f *fakeV6) Del(iface string, a netip.Addr) error {
	f.calls = append(f.calls, "del "+a.String())
	if f.delErr != nil {
		return f.delErr
	}
	for i := range f.addrs {
		if f.addrs[i].Addr == a {
			f.addrs = append(f.addrs[:i], f.addrs[i+1:]...)
			break
		}
	}
	return nil
}
func (f *fakeV6) has(a string) bool {
	for _, x := range f.addrs {
		if x.Addr == netip.MustParseAddr(a) {
			return true
		}
	}
	return false
}
func (f *fakeV6) count(prefix string) (n int) {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return
}

const (
	hoster = "2001:db8:1::10" // the hoster's own address, /64
	other  = "2001:db8:1::beef"
	third  = "2001:db8:1::cafe"
	far    = "2001:db8:99::1" // another /64
)

func newV6Test(t *testing.T) (*ipv6Owner, *fakeV6, string) {
	t.Helper()
	dir := t.TempDir()
	f := &fakeV6{iface: "de1", fileDir: dir, t: t, addrs: []v6Addr{
		{Addr: netip.MustParseAddr(hoster), Bits: 64},
		{Addr: netip.MustParseAddr("fe80::1"), Bits: 64},
	}}
	return newIPv6Owner(f, dir), f, dir
}

func addrs(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, a := range s {
		out = append(out, netip.MustParseAddr(a))
	}
	return out
}

func stateOf(r OwnedIPv6Result, a string) string {
	for _, s := range r.States {
		if s.Addr == netip.MustParseAddr(a) {
			return s.State
		}
	}
	return "absent"
}

func (o *ipv6Owner) ownedCount() int { st, _ := o.load(); return len(st.addrs) }

func TestOwnedIPv6AddsDeprecated128AndIsIdempotent(t *testing.T) {
	o, f, dir := newV6Test(t)
	r, err := o.Sync(context.Background(), addrs(other))
	if err != nil {
		t.Fatal(err)
	}
	if stateOf(r, other) != IPv6AddrTentative || f.count("add") != 1 {
		t.Fatalf("first sync: %+v, calls %v", r.States, f.calls)
	}
	got := f.addrs[len(f.addrs)-1]
	if got.Bits != 128 || got.Flags&ifaNoPrefixRoute == 0 || !got.deprecated() {
		t.Fatalf("added %+v: want /128, noprefixroute, deprecated", got)
	}
	if _, err := os.Stat(filepath.Join(dir, OwnedIPv6File)); err != nil {
		t.Fatal(err)
	}
	// DAD ends; the same sync again changes nothing and reports ok.
	f.addrs[len(f.addrs)-1].Flags &^= ifaTentative
	f.calls = nil
	r, err = o.Sync(context.Background(), addrs(other))
	if err != nil || stateOf(r, other) != IPv6AddrOK || len(f.calls) != 0 || len(r.Readded) != 0 {
		t.Fatalf("second sync: %+v, err %v, calls %v", r, err, f.calls)
	}
}

func TestOwnedIPv6NeverTouchesTheHostersAddresses(t *testing.T) {
	o, f, _ := newV6Test(t)
	if _, err := o.Sync(context.Background(), addrs(other, hoster)); err != nil { // the hoster's own address is wanted too
		t.Fatal(err)
	}
	r, _ := o.Sync(context.Background(), addrs(hoster))
	if stateOf(r, hoster) != IPv6AddrOK {
		t.Fatalf("an address the host had is not ok: %+v", r.States)
	}
	// Nothing wanted any more: only the added one goes.
	if _, err := o.Sync(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !f.has(hoster) || !f.has("fe80::1") || f.has(other) || f.count("del") != 1 || f.calls[len(f.calls)-1] != "del "+other {
		t.Fatalf("addresses %+v, calls %v", f.addrs, f.calls)
	}
	if o.ownedCount() != 0 {
		t.Fatal("the file still names an address")
	}
}

func TestOwnedIPv6RemovesOnlyWhatTheFileNames(t *testing.T) {
	o, f, dir := newV6Test(t)
	// An address of the same kind that is on the host and not in the file (a deprecated /128 of the hoster).
	f.addrs = append(f.addrs, v6Addr{Addr: netip.MustParseAddr(third), Bits: 128, Flags: ifaDeprecated})
	if _, err := o.Sync(context.Background(), addrs(other, third)); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Sync(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !f.has(third) || f.has(other) {
		t.Fatalf("addresses %+v", f.addrs)
	}
	// A sync with an empty file and nothing wanted never asks the kernel.
	f.calls = nil
	if _, err := o.Sync(context.Background(), nil); err != nil || len(f.calls) != 0 {
		t.Fatalf("idle sync: %v %v", err, f.calls)
	}
	if _, err := os.Stat(filepath.Join(dir, OwnedIPv6File)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file left behind: %v", err)
	}
}

func TestOwnedIPv6Policy(t *testing.T) {
	o, f, _ := newV6Test(t)
	bad := []string{far, "fd00::1", "fe80::2", "::1", "ff02::1", "::ffff:10.0.0.1"}
	r, err := o.Sync(context.Background(), addrs(bad...))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range bad {
		if got := stateOf(r, netip.MustParseAddr(a).Unmap().String()); got != IPv6AddrDenied {
			t.Errorf("%s = %s, want denied", a, got)
		}
	}
	if f.count("add") != 0 {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestOwnedIPv6AnchorNeedsAStableAddress(t *testing.T) {
	o, f, _ := newV6Test(t)
	// The hoster's address is gone; only a temporary and a deprecated one remain in the /64: neither is an anchor.
	f.addrs = []v6Addr{
		{Addr: netip.MustParseAddr("2001:db8:1::7"), Bits: 64, Flags: ifaTemporary},
		{Addr: netip.MustParseAddr("2001:db8:1::8"), Bits: 64, Flags: ifaDeprecated},
	}
	r, _ := o.Sync(context.Background(), addrs(other))
	if stateOf(r, other) != IPv6AddrDenied || f.count("add") != 0 {
		t.Fatalf("%+v %v", r.States, f.calls)
	}
}

func TestOwnedIPv6AtMostFour(t *testing.T) {
	o, f, _ := newV6Test(t)
	var want []string
	for _, s := range []string{"a1", "a2", "a3", "a4", "a5"} {
		want = append(want, "2001:db8:1::"+s)
	}
	r, _ := o.Sync(context.Background(), addrs(want...))
	if f.count("add") != MaxOwnedIPv6 || stateOf(r, want[4]) != IPv6AddrDenied {
		t.Fatalf("%+v calls %v", r.States, f.calls)
	}
}

func TestOwnedIPv6PermissionIsDenied(t *testing.T) {
	o, f, dir := newV6Test(t)
	f.addErr = syscall.EPERM
	r, err := o.Sync(context.Background(), addrs(other))
	if err != nil || stateOf(r, other) != IPv6AddrDenied {
		t.Fatalf("%+v %v", r.States, err)
	}
	if _, err := os.Stat(filepath.Join(dir, OwnedIPv6File)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the file names an address that was never added")
	}
}

func TestOwnedIPv6DADFailedIsRemovedAndNotRetried(t *testing.T) {
	o, f, _ := newV6Test(t)
	ctx := context.Background()
	if _, err := o.Sync(ctx, addrs(other)); err != nil {
		t.Fatal(err)
	}
	f.addrs[len(f.addrs)-1].Flags |= ifaDADFailed
	r, err := o.Sync(ctx, addrs(other))
	if err != nil || stateOf(r, other) != IPv6AddrDADFailed || f.has(other) || o.ownedCount() != 0 {
		t.Fatalf("%+v %v has=%v", r.States, err, f.has(other))
	}
	f.calls = nil
	r, _ = o.Sync(ctx, addrs(other))
	if stateOf(r, other) != IPv6AddrDADFailed || f.count("add") != 0 {
		t.Fatalf("retried: %+v %v", r.States, f.calls)
	}
	// Not asked for any more, then asked again: another try.
	_, _ = o.Sync(ctx, nil)
	r, _ = o.Sync(ctx, addrs(other))
	if stateOf(r, other) != IPv6AddrTentative {
		t.Fatalf("%+v", r.States)
	}
}

func TestOwnedIPv6ReaddedAfterAManagerRemovedIt(t *testing.T) {
	o, f, _ := newV6Test(t)
	ctx := context.Background()
	_, _ = o.Sync(ctx, addrs(other))
	f.addrs = f.addrs[:2] // netplan apply
	r, err := o.Sync(ctx, addrs(other))
	if err != nil || len(r.Readded) != 1 || r.Readded[0] != netip.MustParseAddr(other) || !f.has(other) {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestOwnedIPv6ReplacesAnAddressThatLostItsDeprecation(t *testing.T) {
	o, f, _ := newV6Test(t)
	ctx := context.Background()
	_, _ = o.Sync(ctx, addrs(other))
	f.addrs[len(f.addrs)-1].Flags = 0
	f.calls = nil
	if _, err := o.Sync(ctx, addrs(other)); err != nil || f.count("add") != 1 || !f.addrs[len(f.addrs)-1].deprecated() {
		t.Fatalf("%v %v", err, f.calls)
	}
}

func TestOwnedIPv6NoDefaultRouteKeepsWhatIsThere(t *testing.T) {
	o, f, _ := newV6Test(t)
	ctx := context.Background()
	_, _ = o.Sync(ctx, addrs(other))
	f.iface = ""
	f.calls = nil
	r, _ := o.Sync(ctx, addrs(other))
	if stateOf(r, other) != IPv6AddrMissing || len(f.calls) != 0 || !f.has(other) {
		t.Fatalf("%+v %v", r.States, f.calls)
	}
}

func TestOwnedIPv6CleanupRemovesOnlyOurs(t *testing.T) {
	o, f, _ := newV6Test(t)
	ctx := context.Background()
	_, _ = o.Sync(ctx, addrs(other, third))
	if err := o.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if f.has(other) || f.has(third) || !f.has(hoster) || o.ownedCount() != 0 {
		t.Fatalf("%+v", f.addrs)
	}
}

func TestOwnedIPv6BrokenFileChangesNothing(t *testing.T) {
	o, f, dir := newV6Test(t)
	if err := os.WriteFile(filepath.Join(dir, OwnedIPv6File), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Sync(context.Background(), addrs(other)); err == nil || len(f.calls) != 0 {
		t.Fatalf("err %v calls %v", err, f.calls)
	}
}

// A panel that says nothing about the addresses (an older one) neither adds nor removes: the agent only puts back what the
// file names, and a later managed sync with an empty list removes it.
func TestOwnedIPv6KeepNeitherAddsNorRemoves(t *testing.T) {
	o, f, _ := newV6Test(t)
	ctx := context.Background()
	_, _ = o.Sync(ctx, addrs(other, third))
	f.calls = nil
	r, err := o.Keep(ctx)
	if err != nil || len(f.calls) != 0 || stateOf(r, other) != IPv6AddrTentative && stateOf(r, other) != IPv6AddrOK {
		t.Fatalf("keep with everything there: %+v %v %v", r.States, err, f.calls)
	}
	f.addrs = f.addrs[:2] // a manager took both
	r, err = o.Keep(ctx)
	if err != nil || f.count("del") != 0 || f.count("add") != 2 || len(r.Readded) != 2 || !f.has(other) || !f.has(third) {
		t.Fatalf("keep after a removal: %+v %v %v", r, err, f.calls)
	}
	if o.ownedCount() != 2 {
		t.Fatal("keep changed what the file names")
	}
	// Nothing owned: keep never touches the kernel, and never asks for an address of its own accord.
	o2, f2, _ := newV6Test(t)
	if r, err := o2.Keep(ctx); err != nil || len(r.States) != 0 || len(f2.calls) != 0 {
		t.Fatalf("keep with an empty file: %+v %v %v", r, err, f2.calls)
	}
	if _, err := o.Sync(ctx, nil); err != nil || f.has(other) || f.has(third) {
		t.Fatalf("a managed empty list: %v %+v", err, f.addrs)
	}
}

// The address stays on the host but is no longer deprecated: that is never reported as ok.
func TestOwnedIPv6ReplaceFailureIsNotOK(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{{syscall.EPERM, IPv6AddrDenied}, {errors.New("netlink: busy"), IPv6AddrMissing}} {
		o, f, _ := newV6Test(t)
		ctx := context.Background()
		_, _ = o.Sync(ctx, addrs(other))
		f.addrs[len(f.addrs)-1].Flags = 0
		f.replaceErr = c.err
		r, err := o.Sync(ctx, addrs(other))
		if err == nil || stateOf(r, other) != c.want || !f.has(other) || o.ownedCount() != 1 {
			t.Fatalf("%v: state %s want %s, err %v", c.err, stateOf(r, other), c.want, err)
		}
	}
}

// A removal that failed at retire leaves the file with what is still on the host, and a later cleanup (what
// cleanup-net --ipv6-addresses runs, on a fresh owner) finishes the job and removes the file.
func TestOwnedIPv6CleanupKeepsTheFileUntilTheHostIsClean(t *testing.T) {
	o, f, dir := newV6Test(t)
	ctx := context.Background()
	_, _ = o.Sync(ctx, addrs(other, third))
	f.delErr = errors.New("netlink: busy")
	if err := o.Cleanup(ctx); err == nil {
		t.Fatal("a failed removal was not reported")
	}
	if _, err := os.Stat(filepath.Join(dir, OwnedIPv6File)); err != nil || o.ownedCount() != 2 || !f.has(other) || !f.has(third) {
		t.Fatalf("file after a failed cleanup: %v, owned %d", err, o.ownedCount())
	}
	f.delErr = nil
	if err := newIPv6Owner(f, dir).Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, OwnedIPv6File)); !errors.Is(err, os.ErrNotExist) || f.has(other) || f.has(third) || !f.has(hoster) {
		t.Fatalf("after the second cleanup: %v %+v", err, f.addrs)
	}
}

// A DAD failure outlives the agent: after a restart (a new owner on the same state directory) the address is not tried again,
// and it is forgotten once the panel stops asking for it.
func TestOwnedIPv6DADFailureSurvivesARestart(t *testing.T) {
	o, f, dir := newV6Test(t)
	ctx := context.Background()
	_, _ = o.Sync(ctx, addrs(other))
	f.addrs[len(f.addrs)-1].Flags |= ifaDADFailed
	_, _ = o.Sync(ctx, addrs(other))
	f.calls = nil
	o2 := newIPv6Owner(f, dir) // the agent restarted
	r, _ := o2.Sync(ctx, addrs(other))
	if stateOf(r, other) != IPv6AddrDADFailed || f.count("add") != 0 {
		t.Fatalf("tried again after a restart: %+v %v", r.States, f.calls)
	}
	if r, _ := o2.Keep(ctx); len(r.States) != 0 || f.count("add") != 0 {
		t.Fatalf("keep tried it: %+v %v", r.States, f.calls)
	}
	_, _ = o2.Sync(ctx, nil)
	if _, err := os.Stat(filepath.Join(dir, OwnedIPv6File)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the failure is remembered for an address nobody asks for: %v", err)
	}
	r, _ = newIPv6Owner(f, dir).Sync(ctx, addrs(other))
	if stateOf(r, other) != IPv6AddrTentative {
		t.Fatalf("asking again was not a new try: %+v", r.States)
	}
}

// The journal hears of a re-added address at most once an hour, also across restarts: the time is in the file.
func TestOwnedIPv6ReaddedEventLimitSurvivesARestart(t *testing.T) {
	o, f, dir := newV6Test(t)
	ctx := context.Background()
	clock := time.Unix(1_700_000_000, 0)
	o.now = func() time.Time { return clock }
	_, _ = o.Sync(ctx, addrs(other))
	lose := func(o *ipv6Owner) OwnedIPv6Result {
		f.addrs = f.addrs[:2] // a manager takes the address
		r, err := o.Sync(ctx, addrs(other))
		if err != nil || len(r.Readded) != 1 {
			t.Fatalf("%+v %v", r, err)
		}
		return r
	}
	if !lose(o).ReaddedEvent {
		t.Fatal("the first re-add is silent")
	}
	for i, d := range []time.Duration{time.Minute, 30 * time.Minute} {
		clock = clock.Add(d)
		o = newIPv6Owner(f, dir) // restarted
		o.now = func() time.Time { return clock }
		if lose(o).ReaddedEvent {
			t.Fatalf("round %d: the event came again within the hour", i)
		}
	}
	clock = clock.Add(31 * time.Minute)
	o = newIPv6Owner(f, dir)
	o.now = func() time.Time { return clock }
	if !lose(o).ReaddedEvent {
		t.Fatal("no event an hour after the last one")
	}
	// With the address gone for good, the file still holds the time for the hour, then goes.
	_, _ = o.Sync(ctx, nil)
	if _, err := os.Stat(filepath.Join(dir, OwnedIPv6File)); err != nil {
		t.Fatalf("the time of the last event was dropped at once: %v", err)
	}
	clock = clock.Add(2 * time.Hour)
	_, _ = o.Sync(ctx, addrs(other))
	_, _ = o.Sync(ctx, nil)
	if _, err := os.Stat(filepath.Join(dir, OwnedIPv6File)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an old time keeps the file: %v", err)
	}
}
