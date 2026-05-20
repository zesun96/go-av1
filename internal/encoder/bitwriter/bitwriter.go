// Package bitwriter provides a bit-level writer for AV1 OBU serialization.
//
// It is the encoder-side counterpart of internal/bitstream.GetBits.
// Bits are accumulated MSB-first in a 64-bit window and flushed to the
// underlying byte buffer in big-endian byte order.
package bitwriter

// BitWriter writes individual bits MSB-first into a growing byte buffer.
type BitWriter struct {
	buf      []byte
	held     uint64 // bits awaiting flush, MSB-aligned
	bitsHeld int    // number of valid bits in held (0..63)
}

// New creates a BitWriter with the given initial capacity hint.
func New(cap int) *BitWriter {
	return &BitWriter{buf: make([]byte, 0, cap)}
}

// Reset clears the buffer and resets state for reuse.
func (w *BitWriter) Reset() {
	w.buf = w.buf[:0]
	w.held = 0
	w.bitsHeld = 0
}

// flush writes complete bytes from held to buf.
// Bits in held are MSB-aligned: the first bit written occupies bit 63.
// Each flush iteration takes the top byte (held>>56), outputs it, then
// shifts held left by 8 to expose the next byte.
func (w *BitWriter) flush() {
	for w.bitsHeld >= 8 {
		w.buf = append(w.buf, byte(w.held>>56))
		w.held <<= 8
		w.bitsHeld -= 8
	}
}

// PutBits writes the lowest n bits of v (n in [1,32]).
func (w *BitWriter) PutBits(v uint32, n int) {
	if n <= 0 || n > 32 {
		panic("bitwriter: PutBits width out of range")
	}
	// Mask to n bits and place at proper position.
	masked := uint64(v&((1<<uint(n))-1)) << uint(64-w.bitsHeld-n)
	w.held |= masked
	w.bitsHeld += n
	if w.bitsHeld >= 56 {
		w.flush()
	}
}

// PutBit writes a single bit.
func (w *BitWriter) PutBit(v uint32) {
	w.PutBits(v&1, 1)
}

// PutLiteral writes n bits of v. Alias for PutBits for clarity.
func (w *BitWriter) PutLiteral(v uint32, n int) {
	w.PutBits(v, n)
}

// PutSU writes n bits as a two's complement signed value.
func (w *BitWriter) PutSU(v int32, n int) {
	w.PutBits(uint32(v)&((1<<uint(n))-1), n)
}

// PutLeb128 encodes v as AV1 unsigned LEB128.
func (w *BitWriter) PutLeb128(v uint32) {
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		w.PutBits(uint32(b), 8)
		if v == 0 {
			break
		}
	}
}

// PutUvlc writes an unsigned variable-length code (uvlc) used in AV1
// sequence headers.
func (w *BitWriter) PutUvlc(v uint32) {
	if v == 0 {
		w.PutBit(1)
		return
	}
	v++
	n := 0
	tmp := v
	for tmp > 0 {
		n++
		tmp >>= 1
	}
	n-- // number of leading zeros
	for i := 0; i < n; i++ {
		w.PutBit(0)
	}
	w.PutBits(v, n+1)
}

// BitPos returns the total number of bits written so far.
func (w *BitWriter) BitPos() int {
	return len(w.buf)*8 + w.bitsHeld
}

// ByteAlign pads with zero bits up to the next byte boundary.
func (w *BitWriter) ByteAlign() {
	if w.bitsHeld%8 != 0 {
		w.PutBits(0, 8-w.bitsHeld%8)
	}
}

// TrailingBits writes the AV1 trailing_bits() pattern: a 1 bit followed
// by zeros to the next byte boundary.
func (w *BitWriter) TrailingBits() {
	w.PutBit(1)
	for w.bitsHeld%8 != 0 {
		w.PutBit(0)
	}
}

// Bytes returns the accumulated bytes. After calling Bytes the caller
// must not write more bits without calling Reset first.
func (w *BitWriter) Bytes() []byte {
	// First flush any complete bytes still in the window.
	w.flush()
	// Then flush any remaining partial byte (pad with trailing zeros).
	if w.bitsHeld > 0 {
		// held is MSB-aligned; low bits are implicitly zero (padding).
		w.buf = append(w.buf, byte(w.held>>56))
		w.held = 0
		w.bitsHeld = 0
	}
	return w.buf
}

// Len returns the number of complete bytes flushed so far (excludes
// partial bits still in the window).
func (w *BitWriter) Len() int {
	return len(w.buf)
}

// DirectWrite appends raw bytes directly (must be byte-aligned).
func (w *BitWriter) DirectWrite(data []byte) {
	// Flush any complete bytes still in the window.
	w.flush()
	if w.bitsHeld != 0 {
		panic("bitwriter: DirectWrite requires byte alignment")
	}
	w.buf = append(w.buf, data...)
}
