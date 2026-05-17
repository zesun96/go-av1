package bitstream

import "math/bits"

// MSAC implements the AV1 multi-symbol arithmetic decoder (Section 9.4 of
// the specification). It is a 1:1 port of dav1d/src/msac.c with a 64-bit
// window.
//
// Per the AV1 spec, CDFs are stored in 15-bit "Q15" probability space and
// the entry past the symbol count carries the adaptation rate counter.
type MSAC struct {
	bufPos         int
	data           []byte
	dif            uint64 // 64-bit decoder window
	rng            uint32
	cnt            int
	allowUpdateCDF bool
}

const (
	ecProbShift = 6
	ecMinProb   = 4
	ecWinSize   = 64
)

// NewMSAC creates a decoder reading from data. Pass disableCDFUpdate=true
// to skip CDF adaptation (used during certain frame header passes).
func NewMSAC(data []byte, disableCDFUpdate bool) *MSAC {
	m := &MSAC{}
	m.Init(data, disableCDFUpdate)
	return m
}

// Init resets and binds the decoder to data.
func (m *MSAC) Init(data []byte, disableCDFUpdate bool) {
	m.data = data
	m.bufPos = 0
	m.dif = 0
	m.rng = 0x8000
	m.cnt = -15
	m.allowUpdateCDF = !disableCDFUpdate
	m.refill()
}

// AllowUpdateCDF reports whether adaptive variants update their input CDF.
func (m *MSAC) AllowUpdateCDF() bool { return m.allowUpdateCDF }

// SetAllowUpdateCDF toggles CDF adaptation.
func (m *MSAC) SetAllowUpdateCDF(v bool) { m.allowUpdateCDF = v }

func (m *MSAC) refill() {
	c := ecWinSize - m.cnt - 24
	dif := m.dif
	for c >= 0 {
		if m.bufPos >= len(m.data) {
			// Pad remaining low-order bits with ones, mirroring dav1d.
			dif |= ^(^uint64(0xFF) << uint(c))
			break
		}
		dif |= uint64(m.data[m.bufPos]^0xFF) << uint(c)
		m.bufPos++
		c -= 8
	}
	m.dif = dif
	m.cnt = ecWinSize - c - 24
}

// ctxNorm renormalises the decoder window so 0x8000 <= rng < 0x10000.
func (m *MSAC) ctxNorm(dif uint64, rng uint32) {
	d := 15 ^ (31 ^ bits.LeadingZeros32(rng))
	cnt := m.cnt
	m.dif = dif << uint(d)
	m.rng = rng << uint(d)
	m.cnt = cnt - d
	// Compare as unsigned to skip refill at end-of-buffer (dav1d trick).
	if uint(cnt) < uint(d) {
		m.refill()
	}
}

// BoolEqui decodes a single bit assuming probability 1/2.
func (m *MSAC) BoolEqui() uint32 {
	r := m.rng
	dif := m.dif
	v := ((r >> 8) << 7) + ecMinProb
	vw := uint64(v) << (ecWinSize - 16)
	var ret uint32
	if dif >= vw {
		ret = 1
	}
	dif -= uint64(ret) * vw
	v += ret * (r - 2*v)
	m.ctxNorm(dif, v)
	if ret == 0 {
		return 1
	}
	return 0
}

// Bool decodes a single bit with probability f / 32768 of being 1.
func (m *MSAC) Bool(f uint32) uint32 {
	r := m.rng
	dif := m.dif
	v := ((r>>8)*(f>>ecProbShift))>>(7-ecProbShift) + ecMinProb
	vw := uint64(v) << (ecWinSize - 16)
	var ret uint32
	if dif >= vw {
		ret = 1
	}
	dif -= uint64(ret) * vw
	v += ret * (r - 2*v)
	m.ctxNorm(dif, v)
	if ret == 0 {
		return 1
	}
	return 0
}

// Symbol decodes a value in [0, n-1] using a non-adaptive CDF.
//
// cdf must hold n entries in Q15, in dav1d's "inverse CDF" form (the entry
// for the last symbol is implicitly 0). Use SymbolAdapt for the adaptive
// flavour with the per-context counter trailing the table.
//
// The boost term follows the AV1 specification (9.4.1.3) and the dav1d
// SIMD min_prob table: EC_MIN_PROB * ((n-1) - val). The dav1d C reference
// in src/msac.c uses (n - val), which differs by one EC_MIN_PROB and is
// inconsistent with the spec / its own assembly path; we follow the spec
// here so the decoder round-trips with libaom/SVT-style encoders.
func (m *MSAC) Symbol(cdf []uint16, n int) uint32 {
	if n < 1 || n > 15 {
		panic("bitstream: Symbol n out of range")
	}
	c := uint32(m.dif >> (ecWinSize - 16))
	r := m.rng >> 8
	var u, v uint32 = 0, m.rng
	val := uint32(0xFFFFFFFF)
	nMinus1 := uint32(n - 1)
	for {
		val++
		u = v
		v = r * uint32(cdf[val]>>ecProbShift)
		v >>= 7 - ecProbShift
		v += ecMinProb * (nMinus1 - val)
		if c >= v {
			break
		}
	}
	m.ctxNorm(m.dif-(uint64(v)<<(ecWinSize-16)), u-v)
	return val
}

// SymbolAdapt is like Symbol but updates the CDF after decoding.
//
// cdf must contain n+1 entries: n probability slots plus a counter at index
// n. The first call should leave the counter as zero so adaptation starts
// at the highest rate.
func (m *MSAC) SymbolAdapt(cdf []uint16, n int) uint32 {
	val := m.Symbol(cdf, n)
	if !m.allowUpdateCDF {
		return val
	}
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
	return val
}

// BoolAdapt is the adaptive flavour of Bool.
//
// cdf must contain two entries: cdf[0] is the probability that the bit is 0
// in Q15 form (matching dav1d's storage), cdf[1] is the count.
func (m *MSAC) BoolAdapt(cdf []uint16) uint32 {
	bit := m.Bool(uint32(cdf[0]))
	if !m.allowUpdateCDF {
		return bit
	}
	count := uint32(cdf[1])
	rate := 4 + (count >> 4)
	if bit != 0 {
		cdf[0] += uint16((32768 - uint32(cdf[0])) >> rate)
	} else {
		cdf[0] -= uint16(uint32(cdf[0]) >> rate)
	}
	if count < 32 {
		cdf[1] = uint16(count + 1)
	}
	return bit
}

// Bools decodes n equiprobable bits and packs them MSB-first.
func (m *MSAC) Bools(n int) uint32 {
	v := uint32(0)
	for i := 0; i < n; i++ {
		v = (v << 1) | m.BoolEqui()
	}
	return v
}

// Uniform decodes a uniformly distributed value in [0, n-1]. n must be > 0;
// n == 1 always returns 0 without consuming bits.
func (m *MSAC) Uniform(n uint32) uint32 {
	if n == 0 {
		panic("bitstream: MSAC.Uniform requires n > 0")
	}
	if n == 1 {
		return 0
	}
	l := uint32(bits.Len32(n))
	mm := (uint32(1) << l) - n
	v := m.Bools(int(l - 1))
	if v < mm {
		return v
	}
	return (v << 1) - mm + m.BoolEqui()
}

// Subexp decodes a Subexp(k) value centred on ref with span n.
//
// Mirrors dav1d_msac_decode_subexp; n must equal 8 << k.
func (m *MSAC) Subexp(ref, n int32, k uint32) int32 {
	if n>>k != 8 {
		panic("bitstream: MSAC.Subexp requires n == 8<<k")
	}
	var a uint32
	if m.BoolEqui() != 0 {
		if m.BoolEqui() != 0 {
			k += m.BoolEqui() + 1
		}
		a = uint32(1) << k
	}
	v := m.Bools(int(k)) + a
	if ref*2 <= n {
		return int32(invRecenter(uint32(ref), v))
	}
	return n - 1 - int32(invRecenter(uint32(n-1-ref), v))
}

// HiTok decodes the high-token escape symbol used during coefficient parsing.
//
// dav1d's API uses "n_symbols" == numSyms-1 (the last CDF index), so its
// high-token call passes 3 for the 4-symbol CDF. Our SymbolAdapt takes the
// number of symbols directly, hence n=4 here.
func (m *MSAC) HiTok(cdf []uint16) uint32 {
	tokBr := m.SymbolAdapt(cdf, 4)
	tok := 3 + tokBr
	if tokBr == 3 {
		tokBr = m.SymbolAdapt(cdf, 4)
		tok = 6 + tokBr
		if tokBr == 3 {
			tokBr = m.SymbolAdapt(cdf, 4)
			tok = 9 + tokBr
			if tokBr == 3 {
				tok = 12 + m.SymbolAdapt(cdf, 4)
			}
		}
	}
	return tok
}
