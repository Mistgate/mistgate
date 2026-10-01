//go:build !linux

package warp

import (
	"context"
	"log/slog"
	"net/netip"
)

// unsupported is the dataplane of every OS but Linux: the manager reports UNAVAILABLE and the WARP egress fails
// closed, so the repo builds and vets on Windows and macOS.
type unsupported struct{}

func newPlane(Settings, *slog.Logger) dataplane { return unsupported{} }

func (unsupported) Up(context.Context, linkSpec) (string, netip.AddrPort, error) {
	return "", netip.AddrPort{}, ErrUnavailable
}
func (unsupported) SetEndpoint(context.Context, netip.AddrPort) error { return ErrUnavailable }
func (unsupported) Stat(context.Context) (stat, error)                { return stat{}, nil }
func (unsupported) Reassert(context.Context, routeSpec) (bool, error) { return false, nil }
func (unsupported) DownLink(context.Context) error                    { return nil }
func (unsupported) Cleanup(context.Context) error                     { return nil }
func (unsupported) Preflight(context.Context) []Finding               { return nil }

// CleanupHost removes everything a previous run of the manager left on this host. Nothing to do here.
func CleanupHost(context.Context, Settings) error { return nil }
