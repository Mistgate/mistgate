//go:build !linux

package udpcheck

import "net"

// setDontFragment: only Linux sets the flag here.
func setDontFragment(*net.UDPConn, bool) error { return ErrNoDF }
