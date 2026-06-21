package bitstream

import "math/bits"

// msacEncoder is a test-only counterpart to MSAC. It is a Go port of
// SVT-AV1's svt_od_ec_enc_* family (Source/Lib/Codec/bitstream_unit.c),
// which is itself derived from libaom/Daala's range encoder. The byte
// stream it produces is consumable by MSAC.
//
// The implementation keeps the encoder window in a uint64 and matches the
// reference behaviour for renormalisation, carry propagation, and the
// terminating bits emitted by Done().
type msacEncoder struct {
	buf  []byte
	offs int
	low  uint64
	rng  uint32
	cnt  int
}

// newMSACEncoder returns a fresh encoder ready to accept symbols.
func newMSACEncoder() *msacEncoder {
	return &msacEncoder{rng: 0x8000, cnt: -9}
}

// propagateCarryBwd adds one to the previously emitted byte at offs and
// keeps propagating while the addition overflows.
func (e *msacEncoder) propagateCarryBwd(offs int) {
	for offs >= 0 {
		sum := uint16(e.buf[offs]) + 1
		e.buf[offs] = byte(sum)
		if sum < 0x100 {
			return
		}
		offs--
	}
	panic("msacEncoder: carry propagated past start of buffer")
}

// emitBytes writes the high-order numBytesReady bytes of output to the
// buffer in big-endian order, applying any pending carry to the byte that
// precedes the just-emitted region.
func (e *msacEncoder) emitBytes(output uint64, carry bool, numBytesReady int) {
	needed := e.offs + numBytesReady
	for len(e.buf) < needed {
		e.buf = append(e.buf, 0)
	}
	for i := 0; i < numBytesReady; i++ {
		shift := uint(numBytesReady-1-i) << 3
		e.buf[e.offs+i] = byte(output >> shift)
	}
	if carry {
		e.propagateCarryBwd(e.offs - 1)
	}
	e.offs += numBytesReady
}

// normalize matches svt_od_ec_enc_normalize: when there is no longer room
// to safely accommodate another symbol it flushes the high-order bytes of
// low to the output buffer.
func (e *msacEncoder) normalize(low uint64, rng uint32) {
	d := 16 - bits.Len32(rng)
	s := e.cnt + d
	if s >= 40 { // 56 - 16
		numBytesReady := (s >> 3) + 1
		c := e.cnt + 24 - (numBytesReady << 3)
		output := low >> uint(c)
		low &= (uint64(1) << uint(c)) - 1
		mask := uint64(1) << (uint(numBytesReady) << 3)
		carry := output & mask
		output &= mask - 1
		e.emitBytes(output, carry != 0, numBytesReady)
		s = c + d - 24
	}
	e.low = low << uint(d)
	e.rng = rng << uint(d)
	e.cnt = s
}

// encodeBoolQ15 encodes a single binary value with probability f/32768 of
// being one. f must satisfy 0 < f < 32768.
func (e *msacEncoder) encodeBoolQ15(val uint32, f uint32) {
	r := e.rng
	v := ((r >> 8) * (f >> ecProbShift)) >> (7 - ecProbShift)
	v += ecMinProb
	low := e.low
	var newR uint32
	if val != 0 {
		low += uint64(r - v)
		newR = v
	} else {
		newR = r - v
	}
	e.normalize(low, newR)
}

// encodeBoolEqui encodes a single equiprobable bit.
func (e *msacEncoder) encodeBoolEqui(val uint32) {
	e.encodeBoolQ15(val, 16384)
}

// encodeQ15 encodes a symbol given the inverse-CDF entries that bracket
// its range.
func (e *msacEncoder) encodeQ15(fl, fh uint32, s, n int) {
	low := e.low
	r := e.rng
	N := uint32(n)
	var newR uint32
	if fl < 32768 {
		u := ((r >> 8) * (fl >> ecProbShift)) >> (7 - ecProbShift)
		u += ecMinProb * (N - uint32(s-1))
		v := ((r >> 8) * (fh >> ecProbShift)) >> (7 - ecProbShift)
		v += ecMinProb * (N - uint32(s))
		low += uint64(r - u)
		newR = u - v
	} else {
		v := ((r >> 8) * (fh >> ecProbShift)) >> (7 - ecProbShift)
		v += ecMinProb * (N - uint32(s))
		newR = r - v
	}
	e.normalize(low, newR)
}

// encodeCDFQ15 encodes symbol s using the inverse CDF table. icdf must
// satisfy icdf[n-1] == 0 and be monotonically decreasing.
func (e *msacEncoder) encodeCDFQ15(s int, icdf []uint16, n int) {
	var fl uint32
	if s > 0 {
		fl = uint32(icdf[s-1])
	} else {
		fl = 32768 // OD_ICDF(0)
	}
	fh := uint32(icdf[s])
	e.encodeQ15(fl, fh, s, n)
}

// encodeSymbolFromDav1dCDF encodes symbol s from a dav1d-style CDF. The
// dav1d table stores the *inverse* CDF directly (cdf[i] is the probability
// of symbol i being the largest selected so far), so it can be passed in
// unchanged.
func (e *msacEncoder) encodeSymbolFromDav1dCDF(s int, cdf []uint16, n int) {
	// dav1d CDFs are "inverse" already: cdf[n-1] should be 0 (it is the
	// implicit terminator). encodeCDFQ15 expects icdf[n-1] == 0.
	icdf := make([]uint16, n)
	copy(icdf, cdf[:n])
	e.encodeCDFQ15(s, icdf, n)
}

// encodeBools encodes n equiprobable bits MSB-first.
func (e *msacEncoder) encodeBools(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		e.encodeBoolEqui((v >> uint(i)) & 1)
	}
}

// encodeUniform encodes a uniform value v in [0, n-1].
func (e *msacEncoder) encodeUniform(v, n uint32) {
	if n <= 1 {
		return
	}
	l := uint32(bits.Len32(n))
	mm := (uint32(1) << l) - n
	if v < mm {
		e.encodeBools(v, int(l-1))
		return
	}
	w := v + mm
	e.encodeBools(w>>1, int(l-1))
	e.encodeBoolEqui(w & 1)
}

// done finalises the bitstream and returns the encoded bytes.
func (e *msacEncoder) done() []byte {
	low := e.low
	c := e.cnt
	s := 10 + c
	m := uint64(0x3FFF)
	es := ((low + m) &^ m) | (m + 1)
	if s > 0 {
		n := (uint64(1) << uint(c+16)) - 1
		for {
			if e.offs >= len(e.buf) {
				e.buf = append(e.buf, 0)
			}
			val := uint16(es >> uint(c+16))
			e.buf[e.offs] = byte(val & 0xFF)
			if val&0x100 != 0 {
				e.propagateCarryBwd(e.offs - 1)
			}
			e.offs++
			es &= n
			s -= 8
			c -= 8
			n >>= 8
			if s <= 0 {
				break
			}
		}
	}
	return e.buf[:e.offs]
}
