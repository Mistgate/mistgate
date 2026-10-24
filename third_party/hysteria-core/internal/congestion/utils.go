package congestion

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/apernet/hysteria/core/v2/internal/congestion/bbr"
	"github.com/apernet/hysteria/core/v2/internal/congestion/brutal"
	"github.com/apernet/quic-go"
	"github.com/apernet/quic-go/congestion"
)

const (
	TypeBBR  = "bbr"
	TypeReno = "reno"
)

func NormalizeType(congestionType string) (string, error) {
	switch normalized := strings.ToLower(congestionType); normalized {
	case "", TypeBBR:
		return TypeBBR, nil
	case TypeReno:
		return TypeReno, nil
	default:
		return "", fmt.Errorf("unsupported congestion type %q", congestionType)
	}
}

func NormalizeBBRProfile(profile string) (string, error) {
	normalized, err := bbr.ParseProfile(profile)
	if err != nil {
		return "", err
	}
	return string(normalized), nil
}

func UseBBR(conn *quic.Conn, profile bbr.Profile) {
	setCounting(conn, bbr.NewBbrSender(
		bbr.DefaultClock{},
		seedPacketSize(conn.InitialPacketSize(), bbr.GetInitialPacketSize(conn.RemoteAddr())),
		profile,
	))
}

// seedPacketSize picks the datagram size to seed a replacement congestion
// controller with, given the size QUIC itself starts at and the guess derived
// from the remote address.
//
// The seed must not exceed what QUIC actually starts at. If it does, the first
// path MTU probe can land between the two: QUIC sees an increase and reports
// it, but the controller sees a decrease, which it cannot represent. Taking the
// smaller of the two keeps the address-based guess as a floor for connections
// whose path we can't reason about, while never seeding above QUIC.
func seedPacketSize(quicSize, byAddr congestion.ByteCount) congestion.ByteCount {
	if quicSize <= 0 {
		return byAddr
	}
	return min(quicSize, byAddr)
}

func UseBrutal(conn *quic.Conn, tx uint64, disableLossCompensation bool) {
	setCounting(conn, brutal.NewBrutalSender(tx, disableLossCompensation))
}

// Mistgate patch (see MISTGATE.md): quic-go counts lost packets into ConnectionStats.PacketsLost only in its own congestion
// controller, so under BBR or Brutal they stay at zero. quic-go calls OnCongestionEvent once for every packet it declares lost
// whatever the controller, so a wrapper counts them.
type lossCounting struct {
	congestion.CongestionControlEx
	lost *atomic.Uint64
}

func (l lossCounting) OnCongestionEvent(number congestion.PacketNumber, lostBytes, priorInFlight congestion.ByteCount) {
	if lostBytes > 0 { // an ECN congestion event reports no bytes
		l.lost.Add(1)
	}
	l.CongestionControlEx.OnCongestionEvent(number, lostBytes, priorInFlight)
}

var losses sync.Map // *quic.Conn -> *atomic.Uint64

func setCounting(conn *quic.Conn, cc congestion.CongestionControlEx) {
	lost := &atomic.Uint64{}
	losses.Store(conn, lost)
	conn.SetCongestionControl(lossCounting{cc, lost})
}

// LostPackets is the number of packets declared lost on conn while a replacement controller runs; false when quic-go's own
// controller runs, whose ConnectionStats.PacketsLost counts them.
func LostPackets(conn *quic.Conn) (uint64, bool) {
	if v, ok := losses.Load(conn); ok {
		return v.(*atomic.Uint64).Load(), true
	}
	return 0, false
}

// Forget drops conn's loss counter; the server calls it when the connection is done.
func Forget(conn *quic.Conn) { losses.Delete(conn) }

func UseConfigured(conn *quic.Conn, congestionType, bbrProfile string) {
	switch congestionType {
	case TypeReno:
		return
	default:
		UseBBR(conn, bbr.Profile(bbrProfile))
	}
}
