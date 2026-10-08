//go:build !linux

package hostctl

import (
	"context"
	"log/slog"
	"os"
	"runtime"
)

// stub is the host owner on non-Linux systems (dev machines): it reports what it cheaply can and changes nothing.
type stub struct{ log *slog.Logger }

// New returns a no-op Host; nft, sysctl (including UDP buffer sizing) and journald only exist on Linux.
func New(log *slog.Logger) Host { return stub{log} }

func (stub) Facts(context.Context) Facts {
	h, _ := os.Hostname()
	return Facts{Hostname: h, OS: runtime.GOOS, Arch: runtime.GOARCH, CPUCount: runtime.NumCPU(), Virt: "unknown", HasIPv6: hasGlobalIPv6()}
}
func (stub) Metrics() Metrics                                            { return Metrics{} }
func (stub) ApplyBaseline(context.Context) error                         { return nil }
func (stub) SetPortHops(context.Context, []Hop) error                    { return nil }
func (stub) SyncInboundUDPPorts(context.Context, []UDPInboundPort) error { return nil }
func (stub) Cleanup(context.Context) error                               { return nil }
func (stub) SSHPorts() []uint16                                          { return []uint16{22} }

var _ Fixer = stub{}

func (stub) VacuumJournal(context.Context) error { return ErrUnsupported }
func (stub) ResolverPlan(context.Context, []string) (ResolverPlan, error) {
	return ResolverPlan{}, ErrUnsupported
}
func (stub) SetResolver(context.Context, []string) error { return ErrUnsupported }
