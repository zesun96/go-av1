package bitwriter

import "math/bits"

// MSACEncoder is the AV1 multi-symbol arithmetic encoder, the inverse of
// the MSAC decoder in internal/bitstream/msac.go.
//
// It maintains the [low, low+rng) interval in 15-bit precision with a
// 64-bit carry-propagation window, matching the dav1d/libaom normalization
// convention.
type MSACEncoder struct {
	low uint64 // lower bound of the interval (33-bit + carry region)
	rng uint32 // interval width, always in [0x8000, 0xFFFF]
	cnt int    // number of bits stored in low ready to output
	buf []byte // output buffer
}

const (
	msacProbShift = 6
	msacMinProb   = 4
)

// NewMSACEncoder creates a new arithmetic encoder.
func NewMSACEncoder(cap int) *MSACEncoder {
	return &MSACEncoder{
		rng: 0x8000,
		cnt: -15,
		buf: make([]byte, 0, cap),
	}
}

// Reset clears the encoder for reuse.
func (e *MSACEncoder) Reset() {
	e.low = 0
	e.rng = 0x8000
	e.cnt = -15
	e.buf = e.buf[:0]
}

// norm renormalizes after encoding so rng stays in [0x8000, 0x10000).
func (e *MSACEncoder) norm() {
	d := 15 - bits.Len32(e.rng) + 1
	e.low <<= uint(d)
	e.rng <<= uint(d)
	e.cnt += d
	if e.cnt >= 0 {
		e.carry()
	}
}

// carry propagates carry bits from low into the output buffer.
func (e *MSACEncoder) carry() {
	for e.cnt >= 8 {
		e.cnt -= 8
		b := byte(e.low >> (uint(e.cnt) + 16))
		e.buf = append(e.buf, b)
		e.low &= (1 << (uint(e.cnt) + 16)) - 1
	}
}

// Bool encodes a boolean with probability f/32768 of being 0.
func (e *MSACEncoder) Bool(val uint32, f uint32) {
	r := e.rng
	v := ((r>>8)*(f>>msacProbShift))>>(7-msacProbShift) + msacMinProb
	if val == 0 {
		// Symbol 0: upper portion [v, rng)
		e.low += uint64(v)
		e.rng = r - v
	} else {
		// Symbol 1: lower portion [0, v)
		e.rng = v
	}
	e.norm()
}

// BoolEqui encodes a boolean assuming probability 1/2.
func (e *MSACEncoder) BoolEqui(val uint32) {
	r := e.rng
	v := ((r >> 8) << 7) + msacMinProb
	if val == 0 {
		e.low += uint64(v)
		e.rng = r - v
	} else {
		e.rng = v
	}
	e.norm()
}

// Symbol encodes val in [0, n-1] using a non-adaptive CDF.
// CDF format matches the decoder: cdf[i] is the "inverse CDF" in Q15.
func (e *MSACEncoder) Symbol(val uint32, cdf []uint16, n int) {
	r := e.rng >> 8
	nMinus1 := uint32(n - 1)

	// Compute the interval boundaries for val.
	var lo, hi uint32
	if val == 0 {
		hi = e.rng
	} else {
		hi = r*uint32(cdf[val-1]>>msacProbShift)>>(7-msacProbShift) + msacMinProb*(nMinus1-(val-1))
	}
	lo = r*uint32(cdf[val]>>msacProbShift)>>(7-msacProbShift) + msacMinProb*(nMinus1-val)

	e.low += uint64(lo)
	e.rng = hi - lo
	e.norm()
}

// SymbolAdapt encodes val and updates the CDF (adaptive).
// cdf has n+1 entries: n probabilities + 1 counter.
func (e *MSACEncoder) SymbolAdapt(val uint32, cdf []uint16, n int) {
	e.Symbol(val, cdf, n)

	// CDF update (same logic as decoder).
	count := uint32(cdf[n])
	rate := 4 + (count >> 4)
	if n > 2 {
		rate++
	}
	for i := uint32(0); i < val; i++ {
		cdf[i] += uint16((32768 - uint32(cdf[i])) >> rate)
	}
	for i := val; i < uint32(n); i++ {
		cdf[i] -= uint16(uint32(cdf[i]) >> rate)
	}
	if count < 32 {
		cdf[n] = uint16(count + 1)
	}
}

// BoolAdapt encodes a boolean with adaptive probability.
// cdf[0] = probability of 0 in Q15, cdf[1] = counter.
func (e *MSACEncoder) BoolAdapt(val uint32, cdf []uint16) {
	e.Bool(val, uint32(cdf[0]))

	count := uint32(cdf[1])
	rate := 4 + (count >> 4)
	if val != 0 {
		cdf[0] += uint16((32768 - uint32(cdf[0])) >> rate)
	} else {
		cdf[0] -= uint16(uint32(cdf[0]) >> rate)
	}
	if count < 32 {
		cdf[1] = uint16(count + 1)
	}
}

// Bools encodes n equiprobable bits packed MSB-first in v.
func (e *MSACEncoder) Bools(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		e.BoolEqui((v >> uint(i)) & 1)
	}
}

// Flush finalizes the arithmetic coder and returns the encoded bytes.
// After Flush, the encoder must be Reset before reuse.
func (e *MSACEncoder) Flush() []byte {
	// Determine the minimal value within [low, low+rng) that can be
	// represented in the fewest bits.
	s := 15 + e.cnt
	m := uint64(1)<<uint(s+16) - 1
	low := (e.low + m) & ^m
	// Write remaining bits.
	for s >= 0 {
		e.buf = append(e.buf, byte(low>>(uint(s)+8)))
		low &= (1 << (uint(s) + 8)) - 1
		s -= 8
	}
	return e.buf
}

// Bytes returns the raw buffer (valid only after Flush).
func (e *MSACEncoder) Bytes() []byte {
	return e.buf
}

// Len returns current buffer length.
func (e *MSACEncoder) Len() int {
	return len(e.buf)
}
