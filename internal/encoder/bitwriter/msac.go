package bitwriter

import "math/bits"

// MSACEncoder is the AV1/Daala multi-symbol arithmetic encoder used for tile
// payloads.  It is a Go port of SVT-AV1's svt_od_ec_enc_* implementation in
// Source/Lib/Codec/bitstream_unit.c.
type MSACEncoder struct {
	buf  []byte
	offs int
	low  uint64
	rng  uint32
	cnt  int
}

const (
	msacProbShift = 6
	msacMinProb   = 4
)

// NewMSACEncoder creates a fresh arithmetic encoder.
func NewMSACEncoder(capacity int) *MSACEncoder {
	if capacity < 0 {
		capacity = 0
	}
	return &MSACEncoder{
		buf: make([]byte, 0, capacity),
		rng: 0x8000,
		cnt: -9,
	}
}

// Reset clears the encoder for reuse.
func (e *MSACEncoder) Reset() {
	e.buf = e.buf[:0]
	e.offs = 0
	e.low = 0
	e.rng = 0x8000
	e.cnt = -9
}

func (e *MSACEncoder) propagateCarryBackward(offs int) {
	for offs >= 0 {
		sum := uint16(e.buf[offs]) + 1
		e.buf[offs] = byte(sum)
		if sum < 0x100 {
			return
		}
		offs--
	}
	panic("bitwriter: MSAC carry propagated before start of buffer")
}

func (e *MSACEncoder) emitBytes(output uint64, carry bool, n int) {
	needed := e.offs + n
	for len(e.buf) < needed {
		e.buf = append(e.buf, 0)
	}
	for i := 0; i < n; i++ {
		shift := uint(n-1-i) * 8
		e.buf[e.offs+i] = byte(output >> shift)
	}
	if carry {
		e.propagateCarryBackward(e.offs - 1)
	}
	e.offs += n
}

func (e *MSACEncoder) normalize(low uint64, rng uint32) {
	d := 16 - bits.Len32(rng)
	s := e.cnt + d
	if s >= 40 {
		n := (s >> 3) + 1
		c := e.cnt + 24 - (n << 3)
		output := low >> uint(c)
		low &= (uint64(1) << uint(c)) - 1
		carryMask := uint64(1) << (uint(n) * 8)
		e.emitBytes(output&(carryMask-1), output&carryMask != 0, n)
		s = c + d - 24
	}
	e.low = low << uint(d)
	e.rng = rng << uint(d)
	e.cnt = s
}

// Bool encodes val with probability f/32768 of val being one.
func (e *MSACEncoder) Bool(val uint32, f uint32) {
	r := e.rng
	v := ((r>>8)*(f>>msacProbShift))>>(7-msacProbShift) + msacMinProb
	low := e.low
	var newRng uint32
	if val != 0 {
		low += uint64(r - v)
		newRng = v
	} else {
		newRng = r - v
	}
	e.normalize(low, newRng)
}

// BoolEqui encodes an equiprobable bit.
func (e *MSACEncoder) BoolEqui(val uint32) {
	e.Bool(val, 16384)
}

// Symbol encodes val using an inverse Q15 CDF. n is the number of symbols;
// cdf[n-1] is the zero sentinel.
func (e *MSACEncoder) Symbol(val uint32, cdf []uint16, n int) {
	if n < 2 || n > 16 || len(cdf) < n || val >= uint32(n) {
		panic("bitwriter: invalid MSAC symbol or CDF")
	}
	var fl uint32 = 32768
	if val > 0 {
		fl = uint32(cdf[val-1])
	}
	fh := uint32(cdf[val])
	r := e.rng
	// SVT-AV1 svt_od_ec_encode_q15 uses N = nsyms - 1 for the minimum
	// probability boost. Do not use n here: that creates a self-consistent
	// stream with the old Go test encoder, but diverges from AV1 decoders.
	n32 := uint32(n - 1)
	var u, v uint32
	if fl < 32768 {
		u = ((r>>8)*(fl>>msacProbShift))>>(7-msacProbShift) +
			msacMinProb*(n32-(val-1))
	} else {
		u = r
	}
	v = ((r>>8)*(fh>>msacProbShift))>>(7-msacProbShift) +
		msacMinProb*(n32-val)
	e.normalize(e.low+uint64(r-u), u-v)
}

// SymbolAdapt encodes a symbol and updates the CDF. It is provided for
// frames that allow CDF updates; callers of disable_cdf_update frames should
// use Symbol.
func (e *MSACEncoder) SymbolAdapt(val uint32, cdf []uint16, n int) {
	e.Symbol(val, cdf, n)
	count := uint32(cdf[n])
	rate := 4 + (count >> 4)
	if n-1 > 2 {
		rate++
	}
	for i := uint32(0); i < val; i++ {
		cdf[i] += uint16((32768 - uint32(cdf[i])) >> rate)
	}
	for i := val; i < uint32(n-1); i++ {
		cdf[i] -= uint16(uint32(cdf[i]) >> rate)
	}
	if count < 32 {
		cdf[n] = uint16(count + 1)
	}
}

// BoolAdapt encodes a boolean and updates its two-entry CDF.
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

// Flush finalizes the arithmetic code and returns the encoded tile bytes.
func (e *MSACEncoder) Flush() []byte {
	low := e.low
	c := e.cnt
	s := 10 + c
	const mask = uint64(0x3fff)
	end := ((low + mask) &^ mask) | (mask + 1)
	if s > 0 {
		n := (uint64(1) << uint(c+16)) - 1
		for {
			if e.offs >= len(e.buf) {
				e.buf = append(e.buf, 0)
			}
			val := uint16(end >> uint(c+16))
			e.buf[e.offs] = byte(val)
			if val&0x100 != 0 {
				e.propagateCarryBackward(e.offs - 1)
			}
			e.offs++
			end &= n
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

// Bytes returns the bytes emitted so far.
func (e *MSACEncoder) Bytes() []byte { return e.buf[:e.offs] }

// Len returns the number of bytes emitted so far.
func (e *MSACEncoder) Len() int { return e.offs }
