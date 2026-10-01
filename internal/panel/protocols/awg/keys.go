package awg

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/netip"
)

// X25519 key pair in the WireGuard form: 32 bytes, base64 (standard alphabet), private key clamped.
func genKeyPair() (priv, pub string, err error) {
	var b [32]byte
	if _, err = rand.Read(b[:]); err != nil {
		return "", "", err
	}
	b[0] &= 248
	b[31] = b[31]&127 | 64
	k, err := ecdh.X25519().NewPrivateKey(b[:])
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(b[:]), base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// publicKeyOf derives the public key from a base64 private key.
func publicKeyOf(priv string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(priv)
	if err != nil || len(b) != 32 {
		return "", fmt.Errorf("a private key must be 32 bytes in base64")
	}
	k, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

func validKey(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) == 32
}

func randomKey() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

// peerSecret is Issued.Secret: what the panel keeps (vault) and needs to write a client config. The addresses
// are in it because Render gets neither the peer index nor the credential's node data; they never change for a
// credential (the subnets are fixed once used).
type peerSecret struct {
	Priv string `json:"priv"`
	PSK  string `json:"psk"`
	IP4  string `json:"ip4"`           // client address, no mask
	IP6  string `json:"ip6,omitempty"` // "" when the profile has no IPv6 network
}

// NodeData is Issued.NodeData, the verifier shipped to nodes (UserCred.Data). No private key.
type NodeData struct {
	PublicKey  string   `json:"public_key"`
	AllowedIPs []string `json:"allowed_ips"`
	PSK        string   `json:"psk"`
}

// MaxPeerIndex is the largest peer index the client network of the settings holds: the network and broadcast
// addresses and the node's .1 are not peers, so indexes run 2..size-2. A /22 holds 1021 peers.
func MaxPeerIndex(raw json.RawMessage) (int, error) {
	s, errs := parse(raw)
	if errs != nil {
		return 0, fmt.Errorf("invalid profile settings: %s", errs[0].Message)
	}
	p, err := parseSubnet4(s.Subnet4)
	if err != nil {
		return 0, fmt.Errorf("subnet4: %w", err)
	}
	return 1<<(32-p.Bits()) - 2, nil
}

// PeerAddrs returns the client addresses of peer index idx in the profile's networks (IPv4 always, IPv6 when the
// profile has a network for it); the node is .1 of each.
func PeerAddrs(raw json.RawMessage, idx int) (v4 netip.Addr, v6 netip.Addr, err error) {
	s, errs := parse(raw)
	if errs != nil {
		return v4, v6, fmt.Errorf("invalid profile settings: %s", errs[0].Message)
	}
	return peerAddrs(s, idx)
}

func peerAddrs(s Settings, idx int) (v4 netip.Addr, v6 netip.Addr, err error) {
	p4, err := parseSubnet4(s.Subnet4)
	if err != nil {
		return v4, v6, fmt.Errorf("subnet4: %w", err)
	}
	if limit := 1<<(32-p4.Bits()) - 2; idx < 2 || idx > limit {
		return v4, v6, fmt.Errorf("peer index %d is outside 2-%d", idx, limit)
	}
	b := p4.Addr().As4()
	binary.BigEndian.PutUint32(b[:], binary.BigEndian.Uint32(b[:])+uint32(idx))
	v4 = netip.AddrFrom4(b)
	if s.Subnet6 != "" {
		p6, err := parseSubnet6(s.Subnet6)
		if err != nil {
			return v4, v6, fmt.Errorf("subnet6: %w", err)
		}
		a := p6.Addr().As16()
		binary.BigEndian.PutUint64(a[8:], binary.BigEndian.Uint64(a[8:])+uint64(idx))
		v6 = netip.AddrFrom16(a)
	}
	return v4, v6, nil
}

// serverAddrs returns the node's interface prefixes: .1 of each client network, with the network's length.
func serverAddrs(s Settings) (v4, v6 netip.Prefix, err error) {
	p4, err := parseSubnet4(s.Subnet4)
	if err != nil {
		return v4, v6, fmt.Errorf("subnet4: %w", err)
	}
	v4 = netip.PrefixFrom(p4.Addr().Next(), p4.Bits())
	if s.Subnet6 != "" {
		p6, err := parseSubnet6(s.Subnet6)
		if err != nil {
			return v4, v6, fmt.Errorf("subnet6: %w", err)
		}
		v6 = netip.PrefixFrom(p6.Addr().Next(), p6.Bits())
	}
	return v4, v6, nil
}
