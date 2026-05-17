package bitstream

import "math/bits"

// GetBits is a fixed-width bit reader over a byte slice.
//
// It is a 1:1 port of dav1d's GetBits in src/getbits.{c,h}. The state is a
// 64-bit window with the most-significant unread bit aligned to bit 63;
// bitsLeft tracks how many valid bits the window currently holds.
//
// Once Err() reports an out-of-range read every subsequent read is a no-op
// that returns zero, mirroring dav1d's "sticky error" behaviour.
type GetBits struct {
	state    uint64
	bitsLeft int
	ptr      int
	data     []byte
	err      bool
}

// NewGetBits initialises a reader over data. data must be non-empty;
// constructing a reader over an empty slice always reports Err afterwards.
func NewGetBits(data []byte) *GetBits {
	g := &GetBits{}
	g.Init(data)
	return g
}

// Init resets the reader and binds it to data.
func (g *GetBits) Init(data []byte) {
	g.data = data
	g.ptr = 0
	g.state = 0
	g.bitsLeft = 0
	g.err = false
}

// Err reports whether any read so far has gone past the end of the buffer.
// Once set, the flag remains set; subsequent reads return zero.
func (g *GetBits) Err() bool { return g.err }

// Bit reads a single bit.
func (g *GetBits) Bit() uint32 {
	if g.bitsLeft == 0 {
		if g.ptr >= len(g.data) {
			g.err = true
			return 0
		}
		state := uint64(g.data[g.ptr])
		g.ptr++
		g.bitsLeft = 7
		g.state = state << 57
		return uint32(state >> 7)
	}
	state := g.state
	g.bitsLeft--
	g.state = state << 1
	return uint32(state >> 63)
}

// refill loads enough bytes so that at least n bits are buffered, or sets
// the error flag when the underlying buffer is exhausted.
func (g *GetBits) refill(n int) {
	var state uint64
	for {
		if g.ptr >= len(g.data) {
			g.err = true
			if state != 0 {
				break
			}
			return
		}
		state = (state << 8) | uint64(g.data[g.ptr])
		g.ptr++
		g.bitsLeft += 8
		if n <= g.bitsLeft {
			break
		}
	}
	g.state |= state << (64 - uint(g.bitsLeft))
}

// F reads n unsigned bits (n in [1,32]).
func (g *GetBits) F(n int) uint32 {
	if n <= 0 || n > 32 {
		panic("bitstream: F width out of range")
	}
	if uint(n) > uint(g.bitsLeft) {
		g.refill(n)
	}
	state := g.state
	g.bitsLeft -= n
	g.state = state << uint(n)
	return uint32(state >> (64 - uint(n)))
}

// SU reads n bits as a two's complement signed value (n in [1,32]).
func (g *GetBits) SU(n int) int32 {
	if n <= 0 || n > 32 {
		panic("bitstream: SU width out of range")
	}
	if uint(n) > uint(g.bitsLeft) {
		g.refill(n)
	}
	state := g.state
	g.bitsLeft -= n
	g.state = state << uint(n)
	// Arithmetic shift on int64 sign-extends the top bit.
	return int32(int64(state) >> (64 - uint(n)))
}

// Leb128 decodes the AV1 unsigned LEB128 syntax. Up to 8 bytes are consumed.
//
// Returns (0, true) on overflow or when the continuation bit is set on the
// last permitted byte.
func (g *GetBits) Leb128() (uint32, bool) {
	var val uint64
	more := uint32(0)
	i := 0
	for {
		v := g.F(8)
		more = v & 0x80
		val |= uint64(v&0x7F) << i
		i += 7
		if more == 0 || i >= 56 {
			break
		}
	}
	if val > 0xFFFFFFFF || more != 0 {
		g.err = true
		return 0, false
	}
	return uint32(val), true
}

// Uniform reads a value uniformly distributed in [0, max-1]. max must be > 1.
//
// Mirrors dav1d_get_uniform.
func (g *GetBits) Uniform(max uint32) uint32 {
	if max <= 1 {
		panic("bitstream: Uniform requires max > 1")
	}
	l := uint32(bits.Len32(max))
	m := (uint32(1) << l) - max
	v := g.F(int(l - 1))
	if v < m {
		return v
	}
	return (v << 1) - m + g.Bit()
}

// VLC reads an Exp-Golomb-style variable length code. Returns 0xFFFFFFFF on
// overflow (33+ leading zero bits) just like dav1d_get_vlc.
func (g *GetBits) VLC() uint32 {
	if g.Bit() != 0 {
		return 0
	}
	nBits := 0
	for {
		nBits++
		if nBits == 32 {
			return 0xFFFFFFFF
		}
		if g.Bit() != 0 {
			break
		}
	}
	return ((uint32(1) << nBits) - 1) + g.F(nBits)
}

// BitsSubexp reads the Subexp(n) syntax centred on ref. Mirrors
// dav1d_get_bits_subexp.
func (g *GetBits) BitsSubexp(ref int32, n uint32) int32 {
	v := getBitsSubexpU(g, uint32(ref)+(uint32(1)<<n), 2<<n)
	return int32(v) - int32(uint32(1)<<n)
}

func getBitsSubexpU(g *GetBits, ref, n uint32) uint32 {
	var v uint32
	for i := 0; ; i++ {
		var b int
		if i != 0 {
			b = 3 + i - 1
		} else {
			b = 3
		}
		if n < v+3*(uint32(1)<<uint(b)) {
			v += g.Uniform(n - v + 1)
			break
		}
		if g.Bit() == 0 {
			v += g.F(b)
			break
		}
		v += uint32(1) << uint(b)
	}
	if ref*2 <= n {
		return invRecenter(ref, v)
	}
	return n - invRecenter(n-ref, v)
}

// invRecenter mirrors dav1d's inv_recenter helper.
func invRecenter(r, v uint32) uint32 {
	if v > (r << 1) {
		return v
	}
	if v&1 == 0 {
		return (v >> 1) + r
	}
	return r - ((v + 1) >> 1)
}

// ByteAlign discards bits up to the next byte boundary. Matches
// dav1d_bytealign_get_bits: bitsLeft is always <= 7 at this point.
func (g *GetBits) ByteAlign() {
	g.bitsLeft = 0
	g.state = 0
}

// BitPos reports the current absolute bit offset from the start of the
// buffer. It can be used to assert byte alignment between syntax elements.
func (g *GetBits) BitPos() uint64 {
	return uint64(g.ptr)*8 - uint64(g.bitsLeft)
}

// BytePos reports the next unread byte index. Defined only on a byte
// boundary.
func (g *GetBits) BytePos() int {
	if g.bitsLeft != 0 {
		panic("bitstream: BytePos called off byte boundary")
	}
	return g.ptr
}
