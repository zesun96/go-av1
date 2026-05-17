package obu

// bitWriter is a test helper that assembles a big-endian, MSB-first bit
// stream byte by byte. It mirrors the wire format consumed by
// bitstream.GetBits.
type bitWriter struct {
	buf      []byte
	curByte  byte
	bitsLeft uint8 // free bits remaining in curByte, [0,8]
}

func newBitWriter() *bitWriter {
	return &bitWriter{bitsLeft: 8}
}

func (w *bitWriter) writeBit(b uint32) {
	if w.bitsLeft == 0 {
		w.buf = append(w.buf, w.curByte)
		w.curByte = 0
		w.bitsLeft = 8
	}
	if b&1 == 1 {
		w.curByte |= 1 << (w.bitsLeft - 1)
	}
	w.bitsLeft--
}

func (w *bitWriter) writeBits(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		w.writeBit((v >> uint(i)) & 1)
	}
}

func (w *bitWriter) writeU64(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		w.writeBit(uint32((v >> uint(i)) & 1))
	}
}

// writeVLC mirrors dav1d_get_vlc: emit n leading zero bits, a stop 1, then
// n explicit bits of (v - ((1<<n) - 1)). The empty case (v=0) writes a
// single 1 bit.
func (w *bitWriter) writeVLC(v uint32) {
	v++ // we encode v as (v+1) zeros / value pattern; matches dav1d_get_vlc
	n := 0
	tmp := v
	for tmp > 1 {
		n++
		tmp >>= 1
	}
	for i := 0; i < n; i++ {
		w.writeBit(0)
	}
	w.writeBit(1)
	if n > 0 {
		// emit the low n bits of v with the leading 1 stripped
		mask := (uint32(1) << uint(n)) - 1
		w.writeBits(v&mask, n)
	}
}

// finishWithTrailingBit appends the AV1 trailing_one_bit + zero padding
// and returns the final byte buffer.
func (w *bitWriter) finishWithTrailingBit() []byte {
	w.writeBit(1)
	// Pad the rest of the current byte with zeros, then flush it.
	for w.bitsLeft != 0 {
		w.writeBit(0)
	}
	w.buf = append(w.buf, w.curByte)
	w.curByte = 0
	w.bitsLeft = 8
	return append([]byte(nil), w.buf...)
}

// bytes returns the assembled buffer without appending trailing bits.
// Any partial byte is zero-padded and flushed.
func (w *bitWriter) bytes() []byte {
	if w.bitsLeft != 8 {
		out := append([]byte(nil), w.buf...)
		out = append(out, w.curByte)
		return out
	}
	return append([]byte(nil), w.buf...)
}
