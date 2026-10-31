package hostctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The IPv6 addresses the agent holds on its uplink for the panel (agent.proto "IPV6 ADDRESSES"). The rules live here, free of
// any operating system, and talk to the kernel through v6Netlink: Linux plugs in rtnetlink (ipv6addr_linux.go), the tests a fake.
//
// Hard rule: only an address that is in ipv6-owned.json is ever removed. The file is written BEFORE the address is added, so a
// crash between the two leaves a name without an address (harmless), never an address without a name.

// OwnedIPv6File is the file in the state dir that names the addresses the agent put on the host.
const OwnedIPv6File = "ipv6-owned.json"

// MaxOwnedIPv6 is how many addresses the agent holds at once.
const MaxOwnedIPv6 = 4

// States of one wanted address (the doctor's owned param).
const (
	IPv6AddrOK        = "ok"
	IPv6AddrMissing   = "missing"
	IPv6AddrTentative = "tentative"
	IPv6AddrDADFailed = "dad_failed"
	IPv6AddrDenied    = "denied"
)

// ifa_flags of the kernel that matter here (linux/if_addr.h); spelled out so that this file builds everywhere.
const (
	ifaTemporary     = 0x01
	ifaDADFailed     = 0x08
	ifaDeprecated    = 0x20
	ifaTentative     = 0x40
	ifaNoPrefixRoute = 0x200
)

// OwnedIPv6State is what one wanted address came to.
type OwnedIPv6State struct {
	Addr   netip.Addr
	State  string
	Reason string // why it is denied or missing; for the log, never sent
}

// OwnedIPv6Result is one SyncOwnedIPv6.
type OwnedIPv6Result struct {
	States []OwnedIPv6State
	// Readded are owned addresses that a network manager had removed and the sync put back.
	Readded []netip.Addr
	// ReaddedEvent says that the journal should hear of it: Readded is not empty and the last time it said so, kept in
	// ipv6-owned.json, is over an hour ago (a manager that removes the address at every reconfigure would fill the journal).
	ReaddedEvent bool
}

// OwnedIPv6Host is implemented by the Linux host owner. A caller type-asserts it (`h, ok := host.(hostctl.OwnedIPv6Host)`);
// the stub does not implement it, so an agent on another OS does not list "ipv6-addr/1".
type OwnedIPv6Host interface {
	// SyncOwnedIPv6 makes the agent's addresses on the uplink match wanted exactly: it adds what is missing (after the
	// policy allows it), keeps what is there and removes what the agent added earlier and is no longer wanted. A nil list
	// with nothing owned does not touch the kernel. Idempotent.
	SyncOwnedIPv6(ctx context.Context, wanted []netip.Addr) (OwnedIPv6Result, error)
	// KeepOwnedIPv6 is the sync for a panel that says nothing about the addresses: it adds none and removes none, and only
	// puts back what ipv6-owned.json names.
	KeepOwnedIPv6(ctx context.Context) (OwnedIPv6Result, error)
	// CleanupOwnedIPv6 removes every address of ipv6-owned.json (retire, uninstall). The file goes only when all of them
	// are gone; while one is not, it stays, so that a later cleanup can finish.
	CleanupOwnedIPv6(ctx context.Context) error
}

// v6Addr is one IPv6 address of an interface as the kernel lists it.
type v6Addr struct {
	Addr  netip.Addr
	Bits  int
	Flags int
}

func (a v6Addr) deprecated() bool { return a.Flags&ifaDeprecated != 0 }

// v6Netlink is the kernel side.
type v6Netlink interface {
	// DefaultIface is the interface of the ::/0 route of the main table, "" = none.
	DefaultIface() (string, error)
	// Addrs lists the IPv6 addresses of the interface; a missing interface is an error.
	Addrs(iface string) ([]v6Addr, error)
	// Add adds addr/128 with IFA_F_NOPREFIXROUTE, preferred_lft 0 and valid_lft forever; replace turns an address that is there
	// into that.
	Add(iface string, addr netip.Addr, replace bool) error
	// Del removes addr/128; an address that is not there is not an error.
	Del(iface string, addr netip.Addr) error
}

// uplinkNameBlock lists what is never the uplink: the agent's own links and other tunnel, bridge and container devices.
// The doctor picks the interface of the ::/0 route with the same list.
var uplinkNameBlock = []string{"lo", "mg", "wg", "awg", "tun", "tap", "ppp", "sit", "ip6tnl", "ip6gre", "gre", "tailscale", "zt", "docker", "br-", "veth", "virbr"}

// NotUplink says whether an interface name is one that never carries the node's uplink.
func NotUplink(name string) bool {
	for _, p := range uplinkNameBlock {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// readdedEvery is how seldom a re-added address is worth an event.
const readdedEvery = time.Hour

type ownedEntry struct {
	Addr  string `json:"addr"`
	Iface string `json:"iface"`
}

type ownedFile struct {
	Addrs []ownedEntry `json:"addrs"`
	// DADFailed are addresses whose DAD failed: not tried again until the panel stops asking for them, restart or not.
	DADFailed []string `json:"dad_failed,omitempty"`
	// ReaddedAt is when the last ipv6_readded was due (unix seconds).
	ReaddedAt int64 `json:"readded_at,omitempty"`
}

// ownedState is the file in memory.
type ownedState struct {
	addrs     map[netip.Addr]ownedEntry
	failed    map[netip.Addr]bool
	readdedAt int64
}

type ipv6Owner struct {
	mu  sync.Mutex
	nl  v6Netlink
	dir string // state dir; "" = nowhere to keep the file, so nothing is added
	now func() time.Time
}

func newIPv6Owner(nl v6Netlink, dir string) *ipv6Owner {
	return &ipv6Owner{nl: nl, dir: dir, now: time.Now}
}

func (o *ipv6Owner) path() string { return filepath.Join(o.dir, OwnedIPv6File) }

func (o *ipv6Owner) load() (*ownedState, error) {
	st := &ownedState{addrs: map[netip.Addr]ownedEntry{}, failed: map[netip.Addr]bool{}}
	b, err := os.ReadFile(o.path())
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	} else if err != nil {
		return nil, err
	}
	var f ownedFile
	// ponytail: a file that does not parse stops every change until someone removes it; nothing is guessed about ownership.
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", OwnedIPv6File, err)
	}
	for _, e := range f.Addrs {
		if a, err := netip.ParseAddr(e.Addr); err == nil {
			st.addrs[a] = e
		}
	}
	for _, s := range f.DADFailed {
		if a, err := netip.ParseAddr(s); err == nil {
			st.failed[a] = true
		}
	}
	st.readdedAt = f.ReaddedAt
	return st, nil
}

// empty: nothing is worth a file. The time of the last re-add counts for an hour, so that a restart cannot reset the limit.
func (o *ipv6Owner) empty(st *ownedState) bool {
	recent := st.readdedAt != 0 && o.now().Sub(time.Unix(st.readdedAt, 0)) < readdedEvery
	return len(st.addrs) == 0 && len(st.failed) == 0 && !recent
}

// save writes the file (durably, atomically); a state with nothing in it removes the file.
func (o *ipv6Owner) save(st *ownedState) error {
	if o.empty(st) {
		if err := os.Remove(o.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	f := ownedFile{ReaddedAt: st.readdedAt}
	for _, e := range st.addrs {
		f.Addrs = append(f.Addrs, e)
	}
	for a := range st.failed {
		f.DADFailed = append(f.DADFailed, a.String())
	}
	sort.Slice(f.Addrs, func(i, j int) bool { return f.Addrs[i].Addr < f.Addrs[j].Addr }) // the same bytes for the same content
	sort.Strings(f.DADFailed)
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(o.dir, 0o700); err != nil {
		return err
	}
	tmp := o.path() + ".tmp"
	t, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, werr := t.Write(b)
	if serr := t.Sync(); werr == nil {
		werr = serr
	}
	if cerr := t.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmp)
		return werr
	}
	return os.Rename(tmp, o.path())
}

// isPermission: the process may not change addresses (a container without CAP_NET_ADMIN, a locked-down host).
func isPermission(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EOPNOTSUPP)
}

// policy says why an address may not be added, "" = it may: global unicast, not ULA, inside the /64 of a stable address of
// the interface that is not one of ours.
func policy(addr netip.Addr, anchors []netip.Addr) string {
	if !addr.Is6() || addr.Is4In6() || addr.Zone() != "" || !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return "not a global unicast address"
	}
	p := netip.PrefixFrom(addr, 64)
	for _, a := range anchors {
		if p.Contains(a) {
			return ""
		}
	}
	return "outside the /64 of a stable address of the uplink"
}

// Sync is SyncOwnedIPv6 (see OwnedIPv6Host).
func (o *ipv6Owner) Sync(ctx context.Context, wanted []netip.Addr) (OwnedIPv6Result, error) {
	return o.sync(ctx, wanted, true)
}

// Keep is KeepOwnedIPv6.
func (o *ipv6Owner) Keep(ctx context.Context) (OwnedIPv6Result, error) {
	return o.sync(ctx, nil, false)
}

// sync: managed = the wanted list is the truth (what is not in it goes); otherwise the wanted list is what the file names
// and nothing is added or removed beyond that.
func (o *ipv6Owner) sync(ctx context.Context, wanted []netip.Addr, managed bool) (OwnedIPv6Result, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var res OwnedIPv6Result
	if o.dir == "" {
		return res, errors.New("no state directory for " + OwnedIPv6File)
	}
	st, err := o.load()
	if err != nil {
		return res, err
	}
	if !managed {
		for a := range st.addrs {
			wanted = append(wanted, a)
		}
		sort.Slice(wanted, func(i, j int) bool { return wanted[i].Less(wanted[j]) })
	}
	want := make([]netip.Addr, 0, len(wanted))
	isWanted := map[netip.Addr]bool{}
	for _, a := range wanted {
		if a = a.Unmap(); a.IsValid() && !isWanted[a] {
			isWanted[a] = true
			want = append(want, a)
		}
	}
	if len(want) == 0 && len(st.addrs) == 0 && len(st.failed) == 0 {
		return res, nil
	}
	var errs []error

	if managed { // a failure the panel no longer asks about is forgotten, so that asking again is a new try
		forgot := false
		for a := range st.failed {
			if !isWanted[a] {
				delete(st.failed, a)
				forgot = true
			}
		}
		if forgot {
			if err := o.save(st); err != nil {
				errs = append(errs, err)
			}
		}
	}

	iface, err := o.nl.DefaultIface()
	if err != nil {
		errs = append(errs, fmt.Errorf("uplink: %w", err))
	}
	if managed {
		// What is no longer wanted (or sits on another interface than the uplink now) goes first: only what the file names.
		for a, e := range st.addrs {
			if isWanted[a] && (iface == "" || e.Iface == iface) { // no default route right now (a reconfigure): leave it
				continue
			}
			if err := o.nl.Del(e.Iface, a); err != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", a, err))
				continue
			}
			delete(st.addrs, a)
			if err := o.save(st); err != nil {
				errs = append(errs, err)
			}
		}
	} else { // an address of the file on another interface is left alone: moving it would be a removal
		kept := want[:0]
		for _, a := range want {
			if e := st.addrs[a]; iface == "" || e.Iface == iface {
				kept = append(kept, a)
			}
		}
		want = kept
	}
	if iface == "" || err != nil {
		for _, a := range want {
			res.States = append(res.States, OwnedIPv6State{Addr: a, State: IPv6AddrMissing, Reason: "no IPv6 default route"})
		}
		return res, errors.Join(errs...)
	}

	have, err := o.nl.Addrs(iface)
	if err != nil {
		for _, a := range want {
			res.States = append(res.States, OwnedIPv6State{Addr: a, State: IPv6AddrMissing, Reason: err.Error()})
		}
		return res, errors.Join(append(errs, fmt.Errorf("list %s: %w", iface, err))...)
	}
	present := map[netip.Addr]v6Addr{}
	var anchors []netip.Addr
	for _, h := range have {
		present[h.Addr] = h
		if _, ours := st.addrs[h.Addr]; !ours && h.Flags&(ifaTemporary|ifaDeprecated|ifaTentative|ifaDADFailed) == 0 {
			anchors = append(anchors, h.Addr)
		}
	}

	for _, a := range want {
		out := OwnedIPv6State{Addr: a}
		_, ours := st.addrs[a]
		if h, ok := present[a]; ok {
			switch {
			case h.Flags&ifaDADFailed != 0:
				out.State = IPv6AddrDADFailed
				if ours { // an address of ours that never became usable is taken away, and not tried again
					if err := o.nl.Del(iface, a); err != nil {
						errs = append(errs, fmt.Errorf("remove %s: %w", a, err))
					} else {
						delete(st.addrs, a)
						st.failed[a] = true
						if err := o.save(st); err != nil {
							errs = append(errs, err)
						}
					}
				}
			case h.Flags&ifaTentative != 0:
				out.State = IPv6AddrTentative
			default:
				out.State = IPv6AddrOK
			}
			if ours && !h.deprecated() && out.State != IPv6AddrDADFailed { // somebody re-added it as a normal address
				if err := o.nl.Add(iface, a, true); err != nil {
					// Still there but not deprecated: it may be picked as a source. Never reported as ok.
					out.State, out.Reason = IPv6AddrMissing, "could not deprecate it: "+err.Error()
					if isPermission(err) {
						out.State = IPv6AddrDenied
					}
					errs = append(errs, fmt.Errorf("deprecate %s: %w", a, err))
				}
			}
			res.States = append(res.States, out)
			continue
		}
		if st.failed[a] {
			out.State = IPv6AddrDADFailed
			res.States = append(res.States, out)
			continue
		}
		if why := policy(a, anchors); why != "" {
			out.State, out.Reason = IPv6AddrDenied, why
			res.States = append(res.States, out)
			continue
		}
		if len(st.addrs) >= MaxOwnedIPv6 && !ours {
			out.State, out.Reason = IPv6AddrDenied, fmt.Sprintf("more than %d own addresses", MaxOwnedIPv6)
			res.States = append(res.States, out)
			continue
		}
		wasOwned := ours
		st.addrs[a] = ownedEntry{Addr: a.String(), Iface: iface}
		if err := o.save(st); err != nil { // no name on disk, no address on the host
			delete(st.addrs, a)
			out.State, out.Reason = IPv6AddrMissing, err.Error()
			res.States = append(res.States, out)
			errs = append(errs, err)
			continue
		}
		switch err := o.nl.Add(iface, a, false); {
		case err == nil:
			out.State = IPv6AddrTentative // DAD has only started; the next sweep tells
			if wasOwned {
				res.Readded = append(res.Readded, a)
			}
		case isPermission(err):
			out.State, out.Reason = IPv6AddrDenied, "not allowed to change addresses: "+err.Error()
			if derr := o.drop(st, "", a); derr != nil {
				errs = append(errs, derr)
			}
		case errors.Is(err, syscall.EEXIST): // it appeared since the listing: not ours
			out.State, out.Reason = IPv6AddrOK, "appeared on its own"
			if derr := o.drop(st, "", a); derr != nil {
				errs = append(errs, derr)
			}
		default:
			out.State, out.Reason = IPv6AddrMissing, err.Error()
			errs = append(errs, fmt.Errorf("add %s: %w", a, err))
		}
		res.States = append(res.States, out)
	}
	if len(res.Readded) > 0 {
		if now := o.now(); st.readdedAt == 0 || now.Sub(time.Unix(st.readdedAt, 0)) >= readdedEvery {
			st.readdedAt = now.Unix()
			res.ReaddedEvent = true
			if err := o.save(st); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return res, errors.Join(errs...)
}

// drop removes an address of ours from the host (iface "" = it was never added) and then from the file.
func (o *ipv6Owner) drop(st *ownedState, iface string, a netip.Addr) error {
	if iface != "" {
		if err := o.nl.Del(iface, a); err != nil {
			return fmt.Errorf("remove %s: %w", a, err)
		}
	}
	delete(st.addrs, a)
	return o.save(st)
}

// Cleanup is CleanupOwnedIPv6: every address of the file goes; the file goes with the last one, and stays while one is on
// the host, so that a later cleanup (cleanup-net --ipv6-addresses) can finish.
func (o *ipv6Owner) Cleanup(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.dir == "" {
		return nil
	}
	st, err := o.load()
	if err != nil {
		return err
	}
	if len(st.addrs) == 0 && len(st.failed) == 0 && st.readdedAt == 0 {
		return nil
	}
	var errs []error
	for a, e := range st.addrs {
		if err := o.nl.Del(e.Iface, a); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", a, err))
			continue
		}
		delete(st.addrs, a)
	}
	if len(errs) == 0 {
		if err := os.Remove(o.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
	st.failed, st.readdedAt = map[netip.Addr]bool{}, 0
	errs = append(errs, o.save(st)) // what is left on the host stays named
	return errors.Join(errs...)
}
