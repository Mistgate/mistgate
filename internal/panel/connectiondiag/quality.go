package connectiondiag

import (
	"math"
	"math/bits"
)

// AverageRTTMS returns the integer mean of the node's smoothed RTT samples, or zero when there were no samples.
func AverageRTTMS(sum, samples uint64) uint32 {
	if samples == 0 {
		return 0
	}
	return uint32(min(sum/samples, uint64(math.MaxUint32)))
}

// LossPermille returns packets declared lost per thousand sent, or zero when no packets were sent. The division avoids overflowing
// uint64 when the aggregate packet count is large.
func LossPermille(lost, sent uint64) uint32 {
	if sent == 0 {
		return 0
	}
	whole := lost / sent
	if whole > uint64(math.MaxUint32)/1000 {
		return math.MaxUint32
	}
	value := whole * 1000
	remainder := lost % sent
	if remainder != 0 {
		var fraction uint64
		if remainder <= math.MaxUint64/1000 {
			fraction = remainder * 1000 / sent
		} else {
			hi, lo := bits.Mul64(remainder, 1000)
			fraction, _ = bits.Div64(hi, lo, sent)
		}
		value += fraction
	}
	return uint32(min(value, uint64(math.MaxUint32)))
}
