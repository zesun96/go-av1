package obu

import (
	"errors"
	"testing"

	"github.com/zesun96/go-av1/internal/header"
)

// makeOBUHeaderByte builds a 1-byte OBU header. extension/hasSize/forbidden
// are 0/1, type is the 4-bit OBU type, reserved is forced to 0.
func makeOBUHeaderByte(forbidden, t, extFlag, hasSize, reserved uint8) byte {
	var b byte
	b |= (forbidden & 1) << 7
	b |= (t & 0x0F) << 3
	b |= (extFlag & 1) << 2
	b |= (hasSize & 1) << 1
	b |= reserved & 1
	return b
}

func makeExtensionByte(temporalID, spatialID, reserved uint8) byte {
	var b byte
	b |= (temporalID & 0x07) << 5
	b |= (spatialID & 0x03) << 3
	b |= reserved & 0x07
	return b
}

func encodeLeb128(v uint64) []byte {
	if v == 0 {
		return []byte{0}
	}
	var out []byte
	for v != 0 {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		out = append(out, b)
	}
	return out
}

// ---------------------------------------------------------------------------
// readLeb128
// ---------------------------------------------------------------------------

func TestReadLeb128(t *testing.T) {
	cases := []uint64{0, 1, 127, 128, 16383, 16384, 0xFFFFFFFF}
	for _, v := range cases {
		buf := encodeLeb128(v)
		got, n, err := readLeb128(buf)
		if err != nil {
			t.Fatalf("readLeb128(%d) err: %v", v, err)
		}
		if got != v {
			t.Fatalf("readLeb128(%d) = %d", v, got)
		}
		if n != len(buf) {
			t.Fatalf("readLeb128(%d) consumed %d, want %d", v, n, len(buf))
		}
	}
}

func TestReadLeb128_ShortBuffer(t *testing.T) {
	if _, _, err := readLeb128(nil); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("expected ErrShortBuffer, got %v", err)
	}
	// Continuation bit set then EOF.
	if _, _, err := readLeb128([]byte{0x80}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("expected ErrShortBuffer on continuation EOF, got %v", err)
	}
}

func TestReadLeb128_Overflow(t *testing.T) {
	// 8 bytes, all continuation bits set: never terminates within the
	// permitted byte budget.
	buf := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	if _, _, err := readLeb128(buf); !errors.Is(err, ErrLebOverflow) {
		t.Fatalf("expected ErrLebOverflow, got %v", err)
	}
	// Value > 2^32 - 1 also fails: 0x80 0x80 0x80 0x80 0x10 = 1<<32.
	if _, _, err := readLeb128([]byte{0x80, 0x80, 0x80, 0x80, 0x10}); !errors.Is(err, ErrLebOverflow) {
		t.Fatalf("expected ErrLebOverflow for >2^32, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ParseOBUHeader
// ---------------------------------------------------------------------------

func TestParseOBUHeader_NoExtensionNoSize(t *testing.T) {
	b := makeOBUHeaderByte(0, uint8(header.OBUTemporalDelimiter), 0, 0, 0)
	h, n, err := ParseOBUHeader([]byte{b}, ParseOptions{StrictStdCompliance: true})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 1 || h.HeaderSize != 1 {
		t.Fatalf("HeaderSize/n = %d/%d, want 1/1", h.HeaderSize, n)
	}
	if h.Type != header.OBUTemporalDelimiter || h.HasExtension || h.HasSize {
		t.Fatalf("unexpected header: %+v", h)
	}
}

func TestParseOBUHeader_WithExtension(t *testing.T) {
	b := makeOBUHeaderByte(0, uint8(header.OBUFrame), 1, 1, 0)
	ext := makeExtensionByte(2, 1, 0)
	h, n, err := ParseOBUHeader([]byte{b, ext}, ParseOptions{StrictStdCompliance: true})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 2 || h.HeaderSize != 2 {
		t.Fatalf("HeaderSize/n = %d/%d, want 2/2", h.HeaderSize, n)
	}
	if !h.HasExtension || !h.HasSize {
		t.Fatalf("extension/size flags lost: %+v", h)
	}
	if h.TemporalID != 2 || h.SpatialID != 1 {
		t.Fatalf("ext IDs = (%d,%d), want (2,1)", h.TemporalID, h.SpatialID)
	}
}

func TestParseOBUHeader_EmptyBuffer(t *testing.T) {
	if _, _, err := ParseOBUHeader(nil, ParseOptions{}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err = %v, want ErrShortBuffer", err)
	}
}

func TestParseOBUHeader_ExtensionShortBuffer(t *testing.T) {
	b := makeOBUHeaderByte(0, uint8(header.OBUFrame), 1, 0, 0)
	if _, _, err := ParseOBUHeader([]byte{b}, ParseOptions{}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err = %v, want ErrShortBuffer", err)
	}
}

func TestParseOBUHeader_ForbiddenBitStrict(t *testing.T) {
	b := makeOBUHeaderByte(1, uint8(header.OBUFrame), 0, 0, 0)
	if _, _, err := ParseOBUHeader([]byte{b}, ParseOptions{StrictStdCompliance: true}); !errors.Is(err, ErrForbiddenBit) {
		t.Fatalf("err = %v, want ErrForbiddenBit", err)
	}
	// Non-strict accepts the same byte.
	if _, _, err := ParseOBUHeader([]byte{b}, ParseOptions{}); err != nil {
		t.Fatalf("non-strict rejected forbidden bit: %v", err)
	}
}

func TestParseOBUHeader_ReservedBitStrict(t *testing.T) {
	b := makeOBUHeaderByte(0, uint8(header.OBUFrame), 0, 0, 1)
	if _, _, err := ParseOBUHeader([]byte{b}, ParseOptions{StrictStdCompliance: true}); !errors.Is(err, ErrReservedBit) {
		t.Fatalf("err = %v, want ErrReservedBit", err)
	}
}

func TestParseOBUHeader_ExtensionReservedBitStrict(t *testing.T) {
	b := makeOBUHeaderByte(0, uint8(header.OBUFrame), 1, 0, 0)
	ext := makeExtensionByte(0, 0, 7)
	if _, _, err := ParseOBUHeader([]byte{b, ext}, ParseOptions{StrictStdCompliance: true}); !errors.Is(err, ErrReservedBit) {
		t.Fatalf("err = %v, want ErrReservedBit", err)
	}
}

// ---------------------------------------------------------------------------
// ReadOBU / SplitOBUs
// ---------------------------------------------------------------------------

func encodeOBU(t *testing.T, typ header.OBUType, ext bool, hasSize bool, payload []byte) []byte {
	t.Helper()
	var hasExtFlag uint8
	if ext {
		hasExtFlag = 1
	}
	var hasSizeFlag uint8
	if hasSize {
		hasSizeFlag = 1
	}
	out := []byte{makeOBUHeaderByte(0, uint8(typ), hasExtFlag, hasSizeFlag, 0)}
	if ext {
		out = append(out, makeExtensionByte(0, 0, 0))
	}
	if hasSize {
		out = append(out, encodeLeb128(uint64(len(payload)))...)
	}
	out = append(out, payload...)
	return out
}

func TestReadOBU_WithSize(t *testing.T) {
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	buf := encodeOBU(t, header.OBUFrame, false, true, payload)
	o, err := ReadOBU(buf, ParseOptions{})
	if err != nil {
		t.Fatalf("ReadOBU err: %v", err)
	}
	if o.Header.Type != header.OBUFrame {
		t.Fatalf("type = %v", o.Header.Type)
	}
	if string(o.Payload) != string(payload) {
		t.Fatalf("payload = %v, want %v", o.Payload, payload)
	}
	if o.Total != len(buf) {
		t.Fatalf("total = %d, want %d", o.Total, len(buf))
	}
}

func TestReadOBU_NoSizeConsumesRemainder(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5}
	buf := encodeOBU(t, header.OBUTileGroup, false, false, payload)
	o, err := ReadOBU(buf, ParseOptions{})
	if err != nil {
		t.Fatalf("ReadOBU err: %v", err)
	}
	if string(o.Payload) != string(payload) {
		t.Fatalf("payload = %v, want %v", o.Payload, payload)
	}
}

func TestReadOBU_SizeOverflow(t *testing.T) {
	// header says size = 100 but we only provide 4 payload bytes.
	hdrByte := makeOBUHeaderByte(0, uint8(header.OBUFrame), 0, 1, 0)
	buf := append([]byte{hdrByte}, encodeLeb128(100)...)
	buf = append(buf, 1, 2, 3, 4)
	if _, err := ReadOBU(buf, ParseOptions{}); !errors.Is(err, ErrSizeOverflow) {
		t.Fatalf("err = %v, want ErrSizeOverflow", err)
	}
}

func TestReadOBU_HeaderError(t *testing.T) {
	if _, err := ReadOBU(nil, ParseOptions{}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err = %v", err)
	}
}

func TestReadOBU_BadLeb128(t *testing.T) {
	hdrByte := makeOBUHeaderByte(0, uint8(header.OBUFrame), 0, 1, 0)
	// Truncated leb128 (continuation bit but no follow-up).
	buf := []byte{hdrByte, 0x80}
	if _, err := ReadOBU(buf, ParseOptions{}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err = %v, want ErrShortBuffer", err)
	}
}

func TestSplitOBUs_HappyPath(t *testing.T) {
	a := encodeOBU(t, header.OBUTemporalDelimiter, false, true, nil)
	b := encodeOBU(t, header.OBUSequenceHeader, false, true, []byte{0xAA, 0xBB})
	c := encodeOBU(t, header.OBUFrame, true, true, []byte{1, 2, 3})
	stream := append(append(a, b...), c...)
	out, err := SplitOBUs(stream, ParseOptions{})
	if err != nil {
		t.Fatalf("SplitOBUs err: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d OBUs", len(out))
	}
	wantTypes := []header.OBUType{
		header.OBUTemporalDelimiter, header.OBUSequenceHeader, header.OBUFrame,
	}
	for i, w := range wantTypes {
		if out[i].Header.Type != w {
			t.Fatalf("OBU[%d] type = %v, want %v", i, out[i].Header.Type, w)
		}
	}
	if !out[2].Header.HasExtension {
		t.Fatalf("trailing OBU should have extension flag set")
	}
	if out[1].Offset != len(a) {
		t.Fatalf("offsets miscounted: got %d, want %d", out[1].Offset, len(a))
	}
}

func TestSplitOBUs_TrailingNoSizeRejected(t *testing.T) {
	// SplitOBUs requires every OBU to advertise its size; even a lone
	// trailing no-size OBU is rejected because the caller cannot tell
	// where it ends within a multi-OBU stream.
	a := encodeOBU(t, header.OBUSequenceHeader, false, true, []byte{0x10})
	b := encodeOBU(t, header.OBUTileGroup, false, false, []byte{0x42, 0x43, 0x44})
	stream := append(a, b...)
	out, err := SplitOBUs(stream, ParseOptions{})
	if err == nil {
		t.Fatal("expected error for trailing no-size OBU")
	}
	// Partial results are still returned for OBUs that came before.
	if len(out) != 1 || out[0].Header.Type != header.OBUSequenceHeader {
		t.Fatalf("partial result mismatch: %+v", out)
	}
}

func TestSplitOBUs_MissingSizeWithTrailing(t *testing.T) {
	a := encodeOBU(t, header.OBUTileGroup, false, false, []byte{0x42})
	b := encodeOBU(t, header.OBUSequenceHeader, false, true, []byte{0x01})
	stream := append(a, b...)
	if _, err := SplitOBUs(stream, ParseOptions{}); err == nil {
		t.Fatal("expected error for non-final no-size OBU")
	}
}

func TestSplitOBUs_SingleNoSizeRejected(t *testing.T) {
	stream := encodeOBU(t, header.OBUTileGroup, false, false, []byte{0xAB, 0xCD})
	if _, err := SplitOBUs(stream, ParseOptions{}); err == nil {
		t.Fatal("expected error for sole no-size OBU")
	}
}

func TestSplitOBUs_PropagatesParseError(t *testing.T) {
	hdrByte := makeOBUHeaderByte(1, uint8(header.OBUFrame), 0, 1, 0)
	stream := append([]byte{hdrByte}, encodeLeb128(0)...)
	if _, err := SplitOBUs(stream, ParseOptions{StrictStdCompliance: true}); err == nil {
		t.Fatal("expected forbidden-bit error to propagate")
	}
}

func TestSplitOBUs_EmptyInputReturnsNothing(t *testing.T) {
	out, err := SplitOBUs(nil, ParseOptions{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("got %d OBUs from empty input", len(out))
	}
}

// Round-trip: encode several OBUs, split them, and verify payload bytes
// survive the trip exactly.
func TestSplitOBUs_RoundTrip(t *testing.T) {
	cases := []struct {
		typ     header.OBUType
		ext     bool
		payload []byte
	}{
		{header.OBUTemporalDelimiter, false, nil},
		{header.OBUSequenceHeader, false, []byte{0x10, 0x20, 0x30}},
		{header.OBUFrameHeader, true, []byte{0xA0, 0xB0}},
		{header.OBUFrame, true, make([]byte, 200)},
		{header.OBUMetadata, false, []byte{0x05, 0xDE, 0xAD}},
	}
	for i, c := range cases {
		for j := range c.payload {
			c.payload[j] = byte((i*7 + j) & 0xFF)
		}
	}
	var stream []byte
	for _, c := range cases {
		stream = append(stream, encodeOBU(t, c.typ, c.ext, true, c.payload)...)
	}
	out, err := SplitOBUs(stream, ParseOptions{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != len(cases) {
		t.Fatalf("got %d OBUs, want %d", len(out), len(cases))
	}
	for i, c := range cases {
		if out[i].Header.Type != c.typ {
			t.Fatalf("[%d] type mismatch", i)
		}
		if string(out[i].Payload) != string(c.payload) {
			t.Fatalf("[%d] payload mismatch", i)
		}
	}
}
