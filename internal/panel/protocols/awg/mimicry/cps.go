// Package mimicry builds and checks the I1..I5 "custom protocol signature" packets of AmneziaWG 2.0/3.x.
//
// A chain is a string of tags: <b 0xHEX> static bytes, <r N> N random bytes, <rc N> N random Latin letters,
// <rd N> N random digits, <t> the 4-byte unix time. The initiator sends the packets before every handshake and
// the receiver drops them, so they only exist to look like another protocol to a DPI box. A frozen packet is a
// signature by itself, which is why the presets here tag every field the real protocol draws at random.
//
// The generators are written from the public specifications (RFC 9000, 9001, 8446, 1035, 6891, 9460, 5389, 3261,
// 5905, 3550, 6347 and the UPnP device architecture). The QUIC presets build a real, decryptable client Initial
// (quic.go, clienthello.go); their ClientHello fingerprints and the idea of a built-in domain pool come from
// Sketchystan1/payloadGen (MIT) through pumbaX/awg-multi-script and hoaxisr/awg-manager (both MIT). They imitate
// the SHAPE of the first packets of each protocol; nothing here was measured against a real DPI system.
//
// GenerateChain is the entry point: it returns I1..I5 for a preset and an optional domain. Generate keeps the
// older one-packet form.
package mimicry

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Limits the validator enforces on top of what the implementations accept:
// awg show hangs on long lines, a packet larger than the tunnel MTU fragments and is a signature of its own.
const (
	MaxChainChars = 3500 // all of I1..I5 together, see the awg validator
	MaxPacketSize = 1200 // bytes one chain expands to
	MaxRandomRun  = 1000 // N of <r N>, <rc N>, <rd N>
)

// Info is what Parse learns about a chain.
type Info struct {
	Size int // bytes the chain expands to
	Tags int
}

// ErrCounterTag is returned for <c>: only the kernel module knows it, amneziawg-go and every client reject the
// whole config because of it.
var ErrCounterTag = errors.New("tag <c> is not supported by the clients")

// Parse checks one chain and returns the packet size. The empty string is a valid "no packet" and has size 0.
// The grammar is strict on purpose: nothing but tags, one space between a tag name and its argument, no
// comment character, at most one <t>.
func Parse(s string) (Info, error) {
	var info Info
	if s == "" {
		return info, nil
	}
	if len(s) > MaxChainChars {
		return info, fmt.Errorf("longer than %d characters", MaxChainChars)
	}
	timeTags := 0
	for i := 0; i < len(s); {
		if s[i] != '<' {
			return info, fmt.Errorf("unexpected %q at %d: only tags are allowed", s[i], i)
		}
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			return info, fmt.Errorf("tag at %d is not closed", i)
		}
		body := s[i+1 : i+end]
		i += end + 1
		name, arg, hasArg := strings.Cut(body, " ")
		switch name {
		case "b":
			if !hasArg {
				return info, errors.New("<b> needs 0x<hex>")
			}
			hex, ok := strings.CutPrefix(arg, "0x")
			if !ok || hex == "" || len(hex)%2 != 0 || !isHex(hex) {
				return info, fmt.Errorf("<b %s>: expected 0x followed by an even, non-zero number of hex digits", arg)
			}
			info.Size += len(hex) / 2
		case "r", "rc", "rd":
			if !hasArg {
				return info, fmt.Errorf("<%s> needs a count", name)
			}
			n, err := count(arg)
			if err != nil {
				return info, fmt.Errorf("<%s %s>: %w", name, arg, err)
			}
			info.Size += n
		case "t":
			if hasArg {
				return info, errors.New("<t> takes no argument")
			}
			if timeTags++; timeTags > 1 {
				return info, errors.New("more than one <t> in a chain")
			}
			info.Size += 4
		case "c":
			return info, ErrCounterTag
		default:
			return info, fmt.Errorf("unknown tag <%s>", name)
		}
		info.Tags++
		if info.Size > MaxPacketSize {
			return info, fmt.Errorf("expands to more than %d bytes", MaxPacketSize)
		}
	}
	return info, nil
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func count(s string) (int, error) {
	if s == "" || len(s) > 4 {
		return 0, errors.New("expected a number 1-1000")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, errors.New("expected a number 1-1000")
		}
	}
	n, _ := strconv.Atoi(s)
	if n < 1 || n > MaxRandomRun {
		return 0, errors.New("expected a number 1-1000")
	}
	return n, nil
}

// ---- builder used by the presets ----

type segKind uint8

const (
	segLit segKind = iota
	segR
	segRC
	segRD
	segT
)

type seg struct {
	kind segKind
	n    int
	lit  []byte
}

// chain is a list of segments; the presets build the inner parts first so that length fields come out right.
type chain []seg

func (c *chain) lit(b ...byte) {
	if len(b) > 0 {
		*c = append(*c, seg{kind: segLit, lit: b})
	}
}

func (c *chain) text(s string) { c.lit([]byte(s)...) }

func (c *chain) rnd(kind segKind, n int) {
	for ; n > 0; n -= MaxRandomRun { // one tag carries at most 1000
		*c = append(*c, seg{kind: kind, n: min(n, MaxRandomRun)})
	}
}

func (c *chain) r(n int)  { c.rnd(segR, n) }
func (c *chain) rc(n int) { c.rnd(segRC, n) }
func (c *chain) rd(n int) { c.rnd(segRD, n) }
func (c *chain) t()       { *c = append(*c, seg{kind: segT}) }

func (c *chain) add(o chain) { *c = append(*c, o...) }

func (c chain) size() int {
	n := 0
	for _, s := range c {
		switch s.kind {
		case segLit:
			n += len(s.lit)
		case segT:
			n += 4
		default:
			n += s.n
		}
	}
	return n
}

// String renders the chain, merging neighbouring literals and neighbouring <r> runs.
func (c chain) String() string {
	var sb strings.Builder
	for i := 0; i < len(c); i++ {
		s := c[i]
		switch s.kind {
		case segLit:
			lit := append([]byte(nil), s.lit...)
			for i+1 < len(c) && c[i+1].kind == segLit {
				i++
				lit = append(lit, c[i].lit...)
			}
			const hexd = "0123456789abcdef"
			sb.WriteString("<b 0x")
			for _, b := range lit {
				sb.WriteByte(hexd[b>>4])
				sb.WriteByte(hexd[b&15])
			}
			sb.WriteByte('>')
		case segR:
			n := s.n
			for i+1 < len(c) && c[i+1].kind == segR && n+c[i+1].n <= MaxRandomRun {
				i++
				n += c[i].n
			}
			fmt.Fprintf(&sb, "<r %d>", n)
		case segRC:
			fmt.Fprintf(&sb, "<rc %d>", s.n)
		case segRD:
			fmt.Fprintf(&sb, "<rd %d>", s.n)
		case segT:
			sb.WriteString("<t>")
		}
	}
	return sb.String()
}
