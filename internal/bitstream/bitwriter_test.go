package bitstream

import "math/bits"

// bitWriter is a test-only counterpart to GetBits. It packs syntax elements
// MSB-first into a byte slice in the same order GetBits would consume them.
type bitWriter struct {
	buf      []byte
	cur      uint64 // bits queued, top-aligned at bit 63
	bitsHeld int
}

func newBitWriter() *bitWriter { return &bitWriter{} }

// f writes the lowest n bits of v MSB-first.
func (w *bitWriter) f(n int, v uint32) {
	if n <= 0 || n > 32 {
		panic("bitWriter.f: width out of range")
	}
	w.cur |= uint64(v&((1<<uint(n))-1)) << uint(64-w.bitsHeld-n)
	w.bitsHeld += n
	for w.bitsHeld >= 8 {
		w.buf = append(w.buf, byte(w.cur>>56))
		w.cur <<= 8
		w.bitsHeld -= 8
	}
}

// su writes an n-bit signed value in two's complement.
func (w *bitWriter) su(n int, v int32) {
	mask := uint32(1)<<uint(n) - 1
	w.f(n, uint32(v)&mask)
}

// uvlc writes a value using AV1 Exp-Golomb coding.
func (w *bitWriter) uvlc(v uint32) {
	if v == 0 {
		w.f(1, 1)
		return
	}
	leading := bits.Len32(v + 1)
	for i := 0; i < leading-1; i++ {
		w.f(1, 0)
	}
	w.f(leading, v+1)
}

// leb128 writes the AV1 unsigned LEB128 representation.
func (w *bitWriter) leb128(v uint32) {
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		w.f(8, uint32(b))
		if v == 0 {
			return
		}
	}
}

// uniform writes a value in [0, max-1].
func (w *bitWriter) uniform(max, value uint32) {
	if max <= 1 {
		panic("bitWriter.uniform: max must be > 1")
	}
	if value >= max {
		panic("bitWriter.uniform: value out of range")
	}
	l := uint32(bits.Len32(max))
	m := (uint32(1) << l) - max
	if value < m {
		w.f(int(l-1), value)
		return
	}
	v := (value + m) >> 1
	w.f(int(l-1), v)
	w.f(1, (value+m)&1)
}

// vlc writes the AV1 variable-length code dav1d_get_vlc inverts.
func (w *bitWriter) vlc(v uint32) {
	if v == 0 {
		w.f(1, 1)
		return
	}
	n := uint32(bits.Len32(v + 1))
	for i := uint32(0); i < n-1; i++ {
		w.f(1, 0)
	}
	w.f(1, 1)
	suffix := (v + 1) - (uint32(1) << (n - 1))
	w.f(int(n-1), suffix)
}

// align flushes any pending bits to the next byte boundary, padding with
// zeros.
func (w *bitWriter) align() {
	if w.bitsHeld == 0 {
		return
	}
	w.buf = append(w.buf, byte(w.cur>>56))
	w.cur = 0
	w.bitsHeld = 0
}

// bytes returns the encoded buffer; pending bits are flushed.
func (w *bitWriter) bytes() []byte {
	w.align()
	return w.buf
}
