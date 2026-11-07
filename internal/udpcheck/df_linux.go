//go:build linux

package udpcheck

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// setDontFragment sets IP_MTU_DISCOVER = IP_PMTUDISC_DO (IPV6_MTU_DISCOVER for IPv6): the kernel sets DF on every datagram and
// refuses one larger than the route's MTU with EMSGSIZE instead of fragmenting it.
func setDontFragment(c *net.UDPConn, v6 bool) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoDF, err)
	}
	var serr error
	if err := raw.Control(func(fd uintptr) {
		if v6 {
			serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_MTU_DISCOVER, unix.IPV6_PMTUDISC_DO)
			return
		}
		serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MTU_DISCOVER, unix.IP_PMTUDISC_DO)
	}); err != nil {
		return fmt.Errorf("%w: %v", ErrNoDF, err)
	}
	if serr != nil {
		return fmt.Errorf("%w: %v", ErrNoDF, serr)
	}
	return nil
}
