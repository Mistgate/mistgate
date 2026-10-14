//go:build !js

package health

import (
	"context"
	"net"
	"net/netip"
)

func defaultResolve(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
}
