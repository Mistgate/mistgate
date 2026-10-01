// Package awgnl talks to the amneziawg kernel module over generic netlink (family "amneziawg", WG_GENL_VERSION 3,
// the AWG 3.0/3.1 encoding). It is our own client because the only fork of wgctrl that knows AWG (awgctrl-go)
// refuses genl version 3. Attribute numbers and types follow the AmneziaWG kernel module's netlink API and were
// checked against a real module (v3.1.20260906).
//
// The package builds on every OS (netlink itself only works on Linux): constants are local instead of
// golang.org/x/sys/unix so the encoder is testable byte for byte everywhere.
package awgnl

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/mdlayher/genetlink"
	"github.com/mdlayher/netlink"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
)

const (
	familyName = "amneziawg"

	cmdGetDevice   = 0
	cmdSetDevice   = 1
	cmdUnknownPeer = 2 // multicast group "auth"

	// Generic netlink controller (linux/genetlink.h).
	genlIDCtrl       = 0x10
	ctrlCmdGetFamily = 3
	ctrlAttrFamName  = 2
	ctrlAttrMaxAttr  = 5

	afInet  = 2
	afInet6 = 10

	aIfname, aPrivKey, aFlags, aPort, aPeers                                   = 2, 3, 5, 6, 8
	aJc, aJmin, aJmax, aS1, aS2                                                = 9, 10, 11, 12, 13  // u16
	aH1, aH2, aH3, aH4                                                         = 14, 15, 16, 17     // u64 = hi<<32 | lo
	aS3, aS4                                                                   = 19, 20             // u16
	aI1                                                                        = 21                 // I1..I5 = 21..25, NUL string
	aHPK                                                                       = 26                 // 32 bytes
	aCPA                                                                       = 27                 // u32 = hi<<16 | lo
	aRekeyAfter, aRekeyTimeout, aRejectAfter, aKeepaliveTimeout, aMaxHandshake = 28, 29, 30, 31, 32 // same packing
	aRandomTrailers, aDisableCookies                                           = 33, 34             // u8, 3.1 only (maxattr 34)

	pPub, pPsk, pFlags, pEndpoint, pKeepalive, pLastHS, pRx, pTx, pAllowed = 1, 2, 3, 4, 5, 6, 7, 8, 9
	pfRemove, pfReplaceAllowed, pfUpdateOnly                               = 1, 2, 4

	// SetPeers batches: one nested attribute is limited to 64 KiB (1000 peers at once fails in the encoder).
	MaxPeersPerMessage = 250
)

// Info is what the module says about itself. Use it for capability detection, not /sys/module/amneziawg/version
// (it reads 3.1.20260812 on every 3.1 build and 1.0.0 on `make` builds).
type Info struct {
	ID          uint16
	GenlVersion uint8  // 2 = AWG 1.x/2.0 encoding, 3 = AWG 3.x
	MaxAttr     uint32 // 32 = 3.0, 34 = 3.1
}

// Is31 reports whether the module knows RandomTrailers and DisableCookies.
func (i Info) Is31() bool { return i.GenlVersion == 3 && i.MaxAttr >= 34 }

// ErrNoModule means the generic netlink family does not exist: the module is not loaded.
var ErrNoModule = errors.New("awgnl: the amneziawg kernel module is not loaded")

// Client is a connection to the module. Methods are serialised by the caller (the engine holds its own lock).
type Client struct {
	c      *genetlink.Conn
	Info   Info
	family genetlink.Family
	mu     sync.Mutex
}

// Dial opens the connection and asks for the family. ErrNoModule when the family is absent, an error when its
// genl version is not 3 (only that encoding is implemented).
func Dial() (*Client, error) {
	c, err := genetlink.Dial(nil)
	if err != nil {
		return nil, err
	}
	cl, err := probe(c)
	if err != nil {
		c.Close()
		return nil, err
	}
	return cl, nil
}

func probe(c *genetlink.Conn) (*Client, error) {
	fam, err := c.GetFamily(familyName)
	if err != nil {
		if isNotExist(err) {
			return nil, ErrNoModule
		}
		return nil, fmt.Errorf("awgnl: getfamily: %w", err)
	}
	// genetlink.Family has no MaxAttr: ask the controller for CTRL_ATTR_MAXATTR ourselves.
	ae := netlink.NewAttributeEncoder()
	ae.String(ctrlAttrFamName, familyName)
	req, err := ae.Encode()
	if err != nil {
		return nil, err
	}
	msgs, err := c.Execute(genetlink.Message{Header: genetlink.Header{Command: ctrlCmdGetFamily, Version: 1}, Data: req}, genlIDCtrl, netlink.Request)
	if err != nil || len(msgs) == 0 {
		return nil, fmt.Errorf("awgnl: getfamily attrs: %v", err)
	}
	info := Info{ID: fam.ID, GenlVersion: fam.Version}
	ad, err := netlink.NewAttributeDecoder(msgs[0].Data)
	if err != nil {
		return nil, err
	}
	for ad.Next() {
		if ad.Type() == ctrlAttrMaxAttr {
			info.MaxAttr = ad.Uint32()
		}
	}
	if info.GenlVersion != 3 {
		return nil, fmt.Errorf("awgnl: amneziawg genl version %d: only the 3.x encoding is implemented", info.GenlVersion)
	}
	return &Client{c: c, Info: info, family: fam}, nil
}

// isNotExist: the controller answers ENOENT for an unknown family name, genetlink wraps it in os.ErrNotExist.
func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}

// Close closes the connection.
func (c *Client) Close() error { return c.c.Close() }

// SetDevice sends key, port and ALL obfuscation attributes in one message; the module validates the final state.
// It is not atomic: on EINVAL part of the attributes is already applied, so the caller deletes the interface and
// starts over. Changing obfuscation on a live interface drops every session, which is why the engine recreates it.
// The reason for an EINVAL is only in the kernel log (dynamic debug), hence the validator.
func (c *Client) SetDevice(ifname string, priv [32]byte, port uint16, o awgcfg.Obfuscation, peers []awgcfg.Peer) error {
	ae := netlink.NewAttributeEncoder()
	ae.String(aIfname, ifname)
	ae.Bytes(aPrivKey, priv[:])
	ae.Uint16(aPort, port)
	ae.Uint16(aJc, uint16(o.Jc))
	ae.Uint16(aJmin, uint16(o.Jmin))
	ae.Uint16(aJmax, uint16(o.Jmax))
	for i, t := range []uint16{aS1, aS2, aS3, aS4} {
		ae.Uint16(t, uint16(o.S()[i]))
	}
	for i, t := range []uint16{aH1, aH2, aH3, aH4} {
		ae.Uint64(t, u64(o.H()[i]))
	}
	for i, s := range o.I() {
		if s != "" {
			ae.String(aI1+uint16(i), s)
		}
	}
	hpk, ok := o.HPK()
	if !ok {
		return errors.New("awgnl: malformed header_protection_key")
	}
	if hpk != nil {
		ae.Bytes(aHPK, hpk[:])
	}
	ae.Uint32(aCPA, u32(o.ContentPaddingAddition))
	ae.Uint32(aRekeyAfter, u32(o.RekeyAfterTime))
	ae.Uint32(aRekeyTimeout, u32(o.RekeyTimeout))
	ae.Uint32(aRejectAfter, u32(o.RejectAfterTime))
	ae.Uint32(aKeepaliveTimeout, u32(o.KeepaliveTimeout))
	ae.Uint32(aMaxHandshake, u32(o.MaxHandshakeAttempts))
	if c.Info.Is31() {
		ae.Uint8(aRandomTrailers, b2u8(o.RandomTrailers))
		ae.Uint8(aDisableCookies, b2u8(o.DisableCookies))
	} else if o.RandomTrailers || o.DisableCookies {
		return errors.New("awgnl: module is AWG 3.0: RandomTrailers/DisableCookies unsupported")
	}
	encodePeers(ae, peers)
	return c.set(ae)
}

// SetPeers changes peers only (batches of at most MaxPeersPerMessage; the caller batches).
func (c *Client) SetPeers(ifname string, replaceAll bool, peers []awgcfg.Peer) error {
	if len(peers) > MaxPeersPerMessage {
		return fmt.Errorf("awgnl: %d peers in one message, at most %d", len(peers), MaxPeersPerMessage)
	}
	ae := netlink.NewAttributeEncoder()
	ae.String(aIfname, ifname)
	var fl uint32
	if replaceAll {
		fl = 1
	}
	ae.Uint32(aFlags, fl)
	encodePeers(ae, peers)
	return c.set(ae)
}

func (c *Client) set(ae *netlink.AttributeEncoder) error {
	data, err := ae.Encode()
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.c.Execute(genetlink.Message{Header: genetlink.Header{Command: cmdSetDevice, Version: c.Info.GenlVersion}, Data: data}, c.Info.ID, netlink.Request|netlink.Acknowledge)
	return err
}

func encodePeers(ae *netlink.AttributeEncoder, peers []awgcfg.Peer) {
	ae.Nested(aPeers, func(pe *netlink.AttributeEncoder) error {
		for _, p := range peers {
			pe.Nested(0, func(e *netlink.AttributeEncoder) error {
				e.Bytes(pPub, p.PublicKey[:])
				var f uint32
				if p.Remove {
					f |= pfRemove
				}
				if p.UpdateOnly {
					f |= pfUpdateOnly
				}
				if p.ReplaceIPs {
					f |= pfReplaceAllowed
				}
				e.Uint32(pFlags, f)
				if p.PSK != nil {
					e.Bytes(pPsk, p.PSK[:])
				}
				if !p.Keepalive.IsZero() {
					e.Uint32(pKeepalive, u32(p.Keepalive)) // u32 in v3 (u16 in v2)
				}
				if len(p.AllowedIPs) > 0 {
					e.Nested(pAllowed, func(a *netlink.AttributeEncoder) error {
						for _, pf := range p.AllowedIPs {
							a.Nested(0, func(x *netlink.AttributeEncoder) error {
								fam := uint16(afInet)
								if pf.Addr().Is6() {
									fam = afInet6
								}
								x.Uint16(1, fam)
								x.Bytes(2, pf.Addr().AsSlice())
								x.Uint8(3, uint8(pf.Bits()))
								return nil
							})
						}
						return nil
					})
				}
				return nil
			})
		}
		return nil
	})
}

// Stats dumps the device (NLM_F_DUMP; the peers of one device may arrive in several messages). The dump carries
// the interface's private key and the header protection key in clear: nothing here decodes or keeps them.
func (c *Client) Stats(ifname string) ([]awgcfg.PeerStat, error) {
	ae := netlink.NewAttributeEncoder()
	ae.String(aIfname, ifname)
	req, err := ae.Encode()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	msgs, err := c.c.Execute(genetlink.Message{Header: genetlink.Header{Command: cmdGetDevice, Version: c.Info.GenlVersion}, Data: req}, c.Info.ID, netlink.Request|netlink.Dump)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var out []awgcfg.PeerStat
	for _, m := range msgs {
		ps, err := decodeDump(m.Data)
		if err != nil {
			return nil, err
		}
		out = append(out, ps...)
	}
	return out, nil
}

func decodeDump(data []byte) ([]awgcfg.PeerStat, error) {
	ad, err := netlink.NewAttributeDecoder(data)
	if err != nil {
		return nil, err
	}
	var out []awgcfg.PeerStat
	for ad.Next() {
		if ad.Type() != aPeers {
			continue
		}
		ad.Nested(func(nd *netlink.AttributeDecoder) error {
			for nd.Next() {
				nd.Nested(func(pd *netlink.AttributeDecoder) error {
					var s awgcfg.PeerStat
					for pd.Next() {
						switch pd.Type() {
						case pPub:
							copy(s.PublicKey[:], pd.Bytes())
						case pRx:
							s.RxBytes = pd.Uint64()
						case pTx:
							s.TxBytes = pd.Uint64()
						case pLastHS: // struct __kernel_timespec {int64 sec, nsec}
							if b := pd.Bytes(); len(b) == 16 {
								if sec := int64(binary.NativeEndian.Uint64(b[:8])); sec != 0 {
									s.LastHS = time.Unix(sec, int64(binary.NativeEndian.Uint64(b[8:])))
								}
							}
						case pEndpoint:
							s.Endpoint = decodeEndpoint(pd.Bytes())
						case pAllowed:
							pd.Nested(func(ad *netlink.AttributeDecoder) error {
								for ad.Next() {
									ad.Nested(func(xd *netlink.AttributeDecoder) error {
										if p, ok := decodeAllowed(xd); ok {
											s.AllowedIPs = append(s.AllowedIPs, p)
										}
										return nil
									})
								}
								return nil
							})
						}
					}
					out = append(out, s)
					return nil
				})
			}
			return nil
		})
	}
	return out, ad.Err()
}

func decodeAllowed(xd *netlink.AttributeDecoder) (netip.Prefix, bool) {
	var (
		addr netip.Addr
		bits int
		have bool
	)
	for xd.Next() {
		switch xd.Type() {
		case 2:
			if a, ok := netip.AddrFromSlice(xd.Bytes()); ok {
				addr, have = a, true
			}
		case 3:
			bits = int(xd.Uint8())
		}
	}
	if !have {
		return netip.Prefix{}, false
	}
	p := netip.PrefixFrom(addr, bits)
	return p, p.IsValid()
}

// decodeEndpoint reads a sockaddr_in (16 bytes) or sockaddr_in6 (28 bytes), port big-endian.
func decodeEndpoint(b []byte) netip.AddrPort {
	switch len(b) {
	case 16:
		return netip.AddrPortFrom(netip.AddrFrom4([4]byte(b[4:8])), binary.BigEndian.Uint16(b[2:4]))
	case 28:
		return netip.AddrPortFrom(netip.AddrFrom16([16]byte(b[8:24])), binary.BigEndian.Uint16(b[2:4]))
	}
	return netip.AddrPort{}
}

// WatchUnknownPeers joins the multicast group "auth" on a connection of its own and calls fn(ifname) for every
// WG_CMD_UNKNOWN_PEER: a valid handshake initiation from a key the interface does not know, which tells "old key
// on the client" from "parameters differ". Not exercised against a real module:
// best effort, the caller ignores an error. The returned stop function ends the goroutine.
func (c *Client) WatchUnknownPeers(fn func(ifname string)) (stop func(), err error) {
	gid := uint32(0)
	for _, g := range c.family.Groups {
		if g.Name == "auth" {
			gid = g.ID
		}
	}
	if gid == 0 {
		return nil, errors.New("awgnl: multicast group \"auth\" not found")
	}
	wc, err := genetlink.Dial(nil)
	if err != nil {
		return nil, err
	}
	if err := wc.JoinGroup(gid); err != nil {
		wc.Close()
		return nil, err
	}
	go func() {
		for {
			msgs, _, err := wc.Receive()
			if err != nil {
				return // closed by stop
			}
			for _, m := range msgs {
				if name, ok := decodeUnknownPeer(m); ok {
					fn(name)
				}
			}
		}
	}()
	return func() { wc.Close() }, nil
}

// decodeUnknownPeer returns the interface name of a WG_CMD_UNKNOWN_PEER message.
func decodeUnknownPeer(m genetlink.Message) (string, bool) {
	if m.Header.Command != cmdUnknownPeer {
		return "", false
	}
	ad, err := netlink.NewAttributeDecoder(m.Data)
	if err != nil {
		return "", false
	}
	name := ""
	for ad.Next() {
		if ad.Type() == aIfname {
			name = ad.String()
		}
	}
	return name, name != ""
}

func u64(r awgcfg.Range) uint64 { return uint64(r.Hi)<<32 | uint64(r.Lo) }
func u32(r awgcfg.Range) uint32 { return r.Hi<<16 | r.Lo }
func b2u8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}
