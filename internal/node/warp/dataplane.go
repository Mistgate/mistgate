package warp

import (
	"context"
	"net/netip"
	"time"
)

// dataplane is everything that touches the kernel. The Linux implementation (dataplane_linux.go) uses netlink,
// wgctrl or amneziawg-go on a TUN, and nft; other OSes get a stub that answers ErrUnavailable; the Manager's
// tests use a fake. All methods are idempotent and are called with the Manager's lock held, one at a time.
type dataplane interface {
	// Up makes the tunnel device match ls: creates it when missing, touches the WireGuard peer only when the
	// live one differs (a ReplacePeers on every reconcile resets the session), sets address, MTU and the
	// link up, rp_filter=2 on it. Returns the backend used and the endpoint the device now has. The endpoint
	// the device already has is kept when it is one of ls.Endpoints (an agent restart must not reset a live session).
	Up(ctx context.Context, ls linkSpec) (backend string, endpoint netip.AddrPort, err error)
	// SetEndpoint changes only the peer endpoint (port rotation); the session is kept where WireGuard can.
	SetEndpoint(ctx context.Context, ep netip.AddrPort) error
	// Stat reads the live device. LinkPresent=false with a nil error means the device does not exist.
	Stat(ctx context.Context) (stat, error)
	// Reassert makes routing table, rules and nft match rs. It reports whether it had to change anything.
	Reassert(ctx context.Context, rs routeSpec) (repaired bool, err error)
	// DownLink removes the tunnel device (and the userspace device behind it) but keeps table and rules, so
	// the selected traffic keeps failing closed.
	DownLink(ctx context.Context) error
	// Cleanup removes everything: rules, routes of the table, the device, the nft table. Never partial on purpose:
	// errors are collected and the rest is still attempted.
	Cleanup(ctx context.Context) error
	// Preflight reports clashes with what is on the host (table, rule preferences, device name, a FORWARD drop policy).
	Preflight(ctx context.Context) []Finding
}

// linkSpec is what Up needs.
type linkSpec struct {
	Backend          string // "auto" | "kernel" | "userspace"
	PrivateKey       string // base64
	PeerKey          string // base64
	Endpoints        []netip.AddrPort
	Endpoint         netip.AddrPort // the one to start with (one of Endpoints)
	AddrV4, AddrV6   netip.Prefix   // AddrV6 invalid = none
	MTU              int
	Reserved         []byte // 0 or 3 bytes
	KeepaliveSeconds int
}

// routeSpec is what Reassert needs.
type routeSpec struct {
	// Configured: any WARP state exists (a spec or a routed subnet). False = leave nothing behind.
	Configured bool
	// LinkUp: the device exists and is up, so the table gets "default dev".
	LinkUp bool
	HasV6  bool
	// Subnets are client subnets whose forwarded traffic goes through the WARP table.
	Subnets []netip.Prefix
	// Reserved + Endpoint: the "reserved" stamping towards Cloudflare (kernel backend only; nil = off).
	Reserved []byte
	Endpoint netip.AddrPort
	// Userspace stamps inside the Bind, so the nft rule is only for the kernel backend.
	Kernel bool
}

type stat struct {
	LinkPresent bool
	LinkUp      bool
	Handshake   time.Time
	Rx, Tx      uint64
	Endpoint    netip.AddrPort
}

// prober measures the path. A fetches Cloudflare's trace through the tunnel, B a host that is not Cloudflare.
type prober interface {
	A(ctx context.Context) (flag, colo string, err error)
	B(ctx context.Context) error
}
