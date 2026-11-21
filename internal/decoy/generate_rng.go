package decoy

import (
	"crypto/sha256"
	"encoding/binary"
)

// rng is a deterministic stream of numbers from a seed: SHA-256 in counter mode, so the sequence cannot change with a Go
// release the way the standard generators' algorithms may.
type rng struct {
	key  [32]byte
	ctr  uint64
	buf  [4]uint64
	left int
}

func newRNG(parts ...[]byte) *rng {
	h := sha256.New()
	for _, p := range parts {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(p)))
		h.Write(l[:])
		h.Write(p)
	}
	r := &rng{}
	h.Sum(r.key[:0])
	return r
}

// sub is an independent stream named by label, so one draw does not depend on how many others came before it.
func (r *rng) sub(label string) *rng { return newRNG(r.key[:], []byte(label)) }

func (r *rng) u64() uint64 {
	if r.left == 0 {
		var in [40]byte
		copy(in[:32], r.key[:])
		binary.BigEndian.PutUint64(in[32:], r.ctr)
		r.ctr++
		sum := sha256.Sum256(in[:])
		for i := range r.buf {
			r.buf[i] = binary.BigEndian.Uint64(sum[i*8:])
		}
		r.left = len(r.buf)
	}
	r.left--
	return r.buf[r.left]
}

// n is a number in [0, n).
func (r *rng) n(n int) int {
	if n <= 1 {
		return 0
	}
	return int(r.u64() % uint64(n))
}

// between is a number in [lo, hi].
func (r *rng) between(lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + r.n(hi-lo+1)
}

func (r *rng) chance(pct int) bool { return r.n(100) < pct }
func (r *rng) float() float64      { return float64(r.u64()>>11) / (1 << 53) }

func (r *rng) perm(n int) []int {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	for i := n - 1; i > 0; i-- {
		j := r.n(i + 1)
		p[i], p[j] = p[j], p[i]
	}
	return p
}
