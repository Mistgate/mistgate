//go:build linux

package awg

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/node/awg/awguapi"
)

// userspace runs amneziawg-go inside the agent process: one device.Device on one TUN per inbound. The pinned
// module (>= v3.1.20260828) carries the fixes for the RandomTrailers panic, the keepalive use-after-free and
// DisableCookies. Needs /dev/net/tun and CAP_NET_ADMIN.
type userspace struct {
	log *slog.Logger

	mu   sync.Mutex
	devs map[string]*device.Device
}

func newUserspace(log *slog.Logger) (*userspace, error) {
	f, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("/dev/net/tun is not usable: %w", err)
	}
	f.Close()
	return &userspace{log: log, devs: map[string]*device.Device{}}, nil
}

func (u *userspace) Name() string { return "userspace" }
func (u *userspace) Is31() bool   { return true }

// Version reads the module version from the build info (the constant in version.go of amneziawg-go is stale).
func (u *userspace) Version() string {
	v := "v3.1.20260828"
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/amnezia-vpn/amneziawg-go/v3" && d.Version != "" {
				v = d.Version
			}
		}
	}
	return "amneziawg-go " + v
}

func (u *userspace) Create(ctx context.Context, name string, d deviceConfig) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if old := u.devs[name]; old != nil {
		old.Close()
		delete(u.devs, name)
	}
	_ = deleteLink(name) // a stale link of a previous run; a TUN normally vanishes with its process

	tdev, err := tun.CreateTUN(name, int(d.Tunnel.MTU))
	if err != nil {
		return fmt.Errorf("create tun %s: %w", name, err)
	}
	logger := &device.Logger{
		Verbosef: func(string, ...any) {},
		Errorf: func(f string, a ...any) {
			msg := "amneziawg-go: " + fmt.Sprintf(f, a...)
			// A server learns a device's address only when the device writes first: after every restart, replies
			// queued for a device that has not reconnected yet log this until it does. Expected, not an error.
			if strings.Contains(msg, "no known endpoint for peer") {
				u.log.Debug(msg)
				return
			}
			u.log.Error(msg)
		},
	}
	dev := device.NewDevice(tdev, conn.NewDefaultBind(), logger)
	// IpcSet is not atomic, and the port is bound by Up (IpcSet only remembers it while the device is down), so a
	// busy port is reported here and not swallowed by the TUN event reader.
	if err := dev.IpcSet(awguapi.DeviceSet(d.Version, d.PrivateKey, d.Port, d.Obf)); err != nil {
		dev.Close()
		return fmt.Errorf("configure %s: %w", name, err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return fmt.Errorf("bring up %s on udp port %d: %w", name, d.Port, err)
	}
	warn, err := configureLink(name, d.Tunnel)
	if err != nil {
		dev.Close()
		return err
	}
	if warn != nil {
		u.log.Warn("awg interface: IPv6 address not set, the tunnel runs IPv4 only", "iface", name, "err", warn)
	}
	u.devs[name] = dev
	return nil
}

func (u *userspace) dev(name string) (*device.Device, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	d := u.devs[name]
	if d == nil {
		return nil, fmt.Errorf("interface %s does not exist", name)
	}
	return d, nil
}

func (u *userspace) SetPeers(ctx context.Context, name string, replace bool, peers []awgcfg.Peer) error {
	d, err := u.dev(name)
	if err != nil {
		return err
	}
	return d.IpcSet(awguapi.PeersSet(replace, peers))
}

func (u *userspace) Stats(ctx context.Context, name string) ([]awgcfg.PeerStat, error) {
	d, err := u.dev(name)
	if err != nil {
		return nil, err
	}
	text, err := d.IpcGet()
	if err != nil {
		return nil, err
	}
	return awguapi.ParseStats(text)
}

func (u *userspace) SetMTU(ctx context.Context, name string, mtu int) error {
	if _, err := u.dev(name); err != nil {
		return err
	}
	return setLinkMTU(name, mtu)
}

func (u *userspace) LinkUp(name string) bool {
	if _, err := u.dev(name); err != nil {
		return false
	}
	return linkIsUp(name)
}

func (u *userspace) Destroy(ctx context.Context, name string) error {
	u.mu.Lock()
	d := u.devs[name]
	delete(u.devs, name)
	u.mu.Unlock()
	if d != nil {
		d.Close() // closes the TUN, which removes the interface
	}
	return deleteLink(name)
}

func (u *userspace) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	for n, d := range u.devs {
		d.Close()
		_ = deleteLink(n)
		delete(u.devs, n)
	}
	return nil
}
