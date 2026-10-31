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
}

// OwnedIPv6Host is implemented by the Linux host owner. A caller type-asserts it (`h, ok := host.(hostctl.OwnedIPv6Host)`);
// the stub does not implement it, so an agent on another OS does not list "ipv6-addr/1".
type OwnedIPv6Host interface {
	// SyncOwnedIPv6 makes the agent's addresses on the uplink match wanted exactly: it adds what is missing (after the
	// policy allows it), keeps what is there and removes what the agent added earlier and is no longer wanted. A nil list
	// with nothing owned does not touch the kernel. Idempotent.
	SyncOwnedIPv6(ctx context.Context, wanted []netip.Addr) (OwnedIPv6Result, error)
	// CleanupOwnedIPv6 removes every address of ipv6-owned.json (retire, uninstall).
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

type ownedEntry struct {
	Addr  string `json:"addr"`
	Iface string `json:"iface"`
}

type ownedFile struct {
	Addrs []ownedEntry `json:"addrs"`
}

type ipv6Owner struct {
	mu  sync.Mutex
	nl  v6Netlink
	dir string // state dir; "" = nowhere to keep the file, so nothing is added
	// dadFailed are addresses whose DAD failed: not tried again until the panel stops asking for them.
	dadFailed map[netip.Addr]bool
}

func newIPv6Owner(nl v6Netlink, dir string) *ipv6Owner {
	return &ipv6Owner{nl: nl, dir: dir, dadFailed: map[netip.Addr]bool{}}
}

func (o *ipv6Owner) path() string { return filepath.Join(o.dir, OwnedIPv6File) }

func (o *ipv6Owner) load() (map[netip.Addr]ownedEntry, error) {
	out := map[netip.Addr]ownedEntry{}
	b, err := os.ReadFile(o.path())
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
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
			out[a] = e
		}
	}
	return out, nil
}

// save writes the file (durably, atomically); no entries remove it.
func (o *ipv6Owner) save(owned map[netip.Addr]ownedEntry) error {
	if len(owned) == 0 {
		if err := os.Remove(o.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	f := ownedFile{}
	for _, e := range owned {
		f.Addrs = append(f.Addrs, e)
	}
	sort.Slice(f.Addrs, func(i, j int) bool { return f.Addrs[i].Addr < f.Addrs[j].Addr }) // the same bytes for the same content
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
	o.mu.Lock()
	defer o.mu.Unlock()
	var res OwnedIPv6Result
	if o.dir == "" {
		return res, errors.New("no state directory for " + OwnedIPv6File)
	}
	want := make([]netip.Addr, 0, len(wanted))
	isWanted := map[netip.Addr]bool{}
	for _, a := range wanted {
		if a = a.Unmap(); a.IsValid() && !isWanted[a] {
			isWanted[a] = true
			want = append(want, a)
		}
	}
	for a := range o.dadFailed {
		if !isWanted[a] {
			delete(o.dadFailed, a)
		}
	}
	owned, err := o.load()
	if err != nil {
		return res, err
	}
	if len(want) == 0 && len(owned) == 0 {
		return res, nil
	}
	var errs []error

	// What is no longer wanted (or sits on another interface than the uplink now) goes first: only what the file names.
	iface, err := o.nl.DefaultIface()
	if err != nil {
		errs = append(errs, fmt.Errorf("uplink: %w", err))
	}
	for a, e := range owned {
		if isWanted[a] && (iface == "" || e.Iface == iface) { // no default route right now (a reconfigure): leave it
			continue
		}
		if err := o.nl.Del(e.Iface, a); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", a, err))
			continue
		}
		delete(owned, a)
		if err := o.save(owned); err != nil {
			errs = append(errs, err)
		}
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
		if _, ours := owned[h.Addr]; !ours && h.Flags&(ifaTemporary|ifaDeprecated|ifaTentative|ifaDADFailed) == 0 {
			anchors = append(anchors, h.Addr)
		}
	}

	for _, a := range want {
		st := OwnedIPv6State{Addr: a}
		_, ours := owned[a]
		if h, ok := present[a]; ok {
			switch {
			case h.Flags&ifaDADFailed != 0:
				st.State = IPv6AddrDADFailed
				if ours { // an address of ours that never became usable is taken away, and not tried again
					o.dadFailed[a] = true
					if err := o.drop(owned, iface, a); err != nil {
						errs = append(errs, err)
					}
				}
			case h.Flags&ifaTentative != 0:
				st.State = IPv6AddrTentative
			default:
				st.State = IPv6AddrOK
			}
			if ours && !h.deprecated() && st.State != IPv6AddrDADFailed { // somebody re-added it as a normal address
				if err := o.nl.Add(iface, a, true); err != nil {
					errs = append(errs, fmt.Errorf("deprecate %s: %w", a, err))
				}
			}
			res.States = append(res.States, st)
			continue
		}
		if o.dadFailed[a] {
			st.State = IPv6AddrDADFailed
			res.States = append(res.States, st)
			continue
		}
		if why := policy(a, anchors); why != "" {
			st.State, st.Reason = IPv6AddrDenied, why
			res.States = append(res.States, st)
			continue
		}
		if len(owned) >= MaxOwnedIPv6 && !ours {
			st.State, st.Reason = IPv6AddrDenied, fmt.Sprintf("more than %d own addresses", MaxOwnedIPv6)
			res.States = append(res.States, st)
			continue
		}
		wasOwned := ours
		owned[a] = ownedEntry{Addr: a.String(), Iface: iface}
		if err := o.save(owned); err != nil { // no name on disk, no address on the host
			delete(owned, a)
			st.State, st.Reason = IPv6AddrMissing, err.Error()
			res.States = append(res.States, st)
			errs = append(errs, err)
			continue
		}
		switch err := o.nl.Add(iface, a, false); {
		case err == nil:
			st.State = IPv6AddrTentative // DAD has only started; the next sweep tells
			if wasOwned {
				res.Readded = append(res.Readded, a)
			}
		case isPermission(err):
			st.State, st.Reason = IPv6AddrDenied, "not allowed to change addresses: "+err.Error()
			if derr := o.drop(owned, "", a); derr != nil {
				errs = append(errs, derr)
			}
		case errors.Is(err, syscall.EEXIST): // it appeared since the listing: not ours
			st.State, st.Reason = IPv6AddrOK, "appeared on its own"
			if derr := o.drop(owned, "", a); derr != nil {
				errs = append(errs, derr)
			}
		default:
			st.State, st.Reason = IPv6AddrMissing, err.Error()
			errs = append(errs, fmt.Errorf("add %s: %w", a, err))
		}
		res.States = append(res.States, st)
	}
	return res, errors.Join(errs...)
}

// drop removes an address of ours from the host (iface "" = it was never added) and then from the file.
func (o *ipv6Owner) drop(owned map[netip.Addr]ownedEntry, iface string, a netip.Addr) error {
	if iface != "" {
		if err := o.nl.Del(iface, a); err != nil {
			return fmt.Errorf("remove %s: %w", a, err)
		}
	}
	delete(owned, a)
	return o.save(owned)
}

// Cleanup is CleanupOwnedIPv6.
func (o *ipv6Owner) Cleanup(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.dir == "" {
		return nil
	}
	owned, err := o.load()
	if err != nil || len(owned) == 0 {
		return err
	}
	var errs []error
	for a, e := range owned {
		if err := o.nl.Del(e.Iface, a); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", a, err))
			continue
		}
		delete(owned, a)
	}
	errs = append(errs, o.save(owned))
	clear(o.dadFailed)
	return errors.Join(errs...)
}
