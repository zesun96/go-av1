package obu

import (
	"errors"
	"testing"

	"github.com/zesun96/go-av1/internal/header"
)

// wrapOBU constructs a leb128-sized OBU envelope around payload. Used by the
// cross-OBU integration tests to splice multiple typed OBUs into one buffer
// the way containers (MKV/MP4) deliver a temporal unit to dav1d.
func wrapOBU(typ header.OBUType, payload []byte) []byte {
	if len(payload) >= 128 {
		panic("integration_test: wrapOBU payload too large for single-byte leb128")
	}
	// header byte layout: forbidden(1) | type(4) | ext(1) | hasSize(1) | reserved(1)
	hb := byte(typ&0x0F) << 3
	hb |= 1 << 1 // hasSize = 1
	out := make([]byte, 0, 2+len(payload))
	out = append(out, hb)
	out = append(out, byte(len(payload)))
	out = append(out, payload...)
	return out
}

// TestSplitOBUs_TemporalUnitDispatch builds a realistic temporal unit
// (TD + SequenceHeader + Metadata HDR-CLL + FrameHeader), walks it with
// SplitOBUs, and dispatches each payload to its typed parser. This is the
// M2.b end-to-end contract test: every OBU writer/reader composes cleanly
// when concatenated the way real-world bitstreams arrive.
func TestSplitOBUs_TemporalUnitDispatch(t *testing.T) {
	seqPayload := buildReducedStillSeqHdr(t, seqOpts{
		profile: 0, stillPicture: true,
	})
	metaPayload := hdrCLLPayload(1000, 400)
	framePayload := writeReducedKeyFrameHdr()

	var buf []byte
	buf = append(buf, wrapOBU(header.OBUTemporalDelimiter, nil)...)
	buf = append(buf, wrapOBU(header.OBUSequenceHeader, seqPayload)...)
	buf = append(buf, wrapOBU(header.OBUMetadata, metaPayload)...)
	buf = append(buf, wrapOBU(header.OBUFrameHeader, framePayload)...)

	obus, err := SplitOBUs(buf, ParseOptions{StrictStdCompliance: true})
	if err != nil {
		t.Fatalf("SplitOBUs: %v", err)
	}
	if len(obus) != 4 {
		t.Fatalf("got %d obus, want 4", len(obus))
	}
	wantTypes := []header.OBUType{
		header.OBUTemporalDelimiter,
		header.OBUSequenceHeader,
		header.OBUMetadata,
		header.OBUFrameHeader,
	}
	for i, o := range obus {
		if o.Header.Type != wantTypes[i] {
			t.Fatalf("obu[%d] type = %v, want %v", i, o.Header.Type, wantTypes[i])
		}
		if !o.Header.HasSize {
			t.Fatalf("obu[%d] HasSize false", i)
		}
		if o.Offset < 0 || o.Total <= 0 {
			t.Fatalf("obu[%d] offsets bad: off=%d total=%d", i, o.Offset, o.Total)
		}
	}
	// Offsets must form a contiguous tiling of the input buffer.
	for i := 1; i < len(obus); i++ {
		if obus[i].Offset != obus[i-1].Offset+obus[i-1].Total {
			t.Fatalf("obu[%d] offset gap: prev off=%d total=%d, this off=%d",
				i, obus[i-1].Offset, obus[i-1].Total, obus[i].Offset)
		}
	}
	if obus[len(obus)-1].Offset+obus[len(obus)-1].Total != len(buf) {
		t.Fatalf("tail of split does not cover buf: end=%d len=%d",
			obus[len(obus)-1].Offset+obus[len(obus)-1].Total, len(buf))
	}

	// Dispatch every payload to its typed parser.
	var (
		seq header.SequenceHeader
		fh  header.FrameHeader
		md  Metadata
	)
	var seenTD, seenSeq, seenMeta, seenFH bool
	for _, o := range obus {
		switch o.Header.Type {
		case header.OBUTemporalDelimiter:
			if len(o.Payload) != 0 {
				t.Fatalf("TD payload non-empty: %d bytes", len(o.Payload))
			}
			seenTD = true
		case header.OBUSequenceHeader:
			if err := ParseSequenceHeader(o.Payload, &seq, ParseOptions{StrictStdCompliance: true}); err != nil {
				t.Fatalf("ParseSequenceHeader: %v", err)
			}
			seenSeq = true
		case header.OBUMetadata:
			if err := ParseMetadataOBU(o.Payload, &md, MetadataParseOptions{StrictStdCompliance: true}); err != nil {
				t.Fatalf("ParseMetadataOBU: %v", err)
			}
			if md.Type != header.MetadataHDRCLL {
				t.Fatalf("metadata type = %v, want HDRCLL", md.Type)
			}
			if md.HDRCLL == nil || md.HDRCLL.MaxCLL != 1000 || md.HDRCLL.MaxFALL != 400 {
				t.Fatalf("hdrcll = %+v", md.HDRCLL)
			}
			seenMeta = true
		case header.OBUFrameHeader:
			if err := ParseFrameHeader(o.Payload, &fh, FrameParseOptions{SeqHeader: &seq}); err != nil {
				t.Fatalf("ParseFrameHeader: %v", err)
			}
			seenFH = true
		default:
			t.Fatalf("unexpected obu type %v", o.Header.Type)
		}
	}
	if !(seenTD && seenSeq && seenMeta && seenFH) {
		t.Fatalf("missing OBU: td=%v seq=%v meta=%v fh=%v", seenTD, seenSeq, seenMeta, seenFH)
	}
	// Cross-OBU consistency: the parsed seq header must match the static
	// reduced-still-picture sequence we built, and the frame header must
	// resolve to a key frame whose dimensions trace back to that seq.
	if !seq.ReducedStillPictureHeader || !seq.StillPicture {
		t.Fatalf("seq flags wrong: %+v", seq)
	}
	if fh.FrameType != header.FrameTypeKey || fh.ShowFrame != 1 {
		t.Fatalf("frame flags wrong: type=%v show=%d", fh.FrameType, fh.ShowFrame)
	}
	if fh.Width[0] != int(seq.MaxWidth) || fh.Height != int(seq.MaxHeight) {
		t.Fatalf("frame size %dx%d != seq max %dx%d",
			fh.Width[0], fh.Height, seq.MaxWidth, seq.MaxHeight)
	}
}

// TestSplitOBUs_StrictForbiddenPropagates ensures that a malformed OBU
// inside a multi-OBU buffer propagates the strict-mode rejection with the
// offending offset wrapped in.
func TestSplitOBUs_StrictForbiddenPropagates(t *testing.T) {
	seqPayload := buildReducedStillSeqHdr(t, seqOpts{
		profile: 0, stillPicture: true,
	})
	var buf []byte
	buf = append(buf, wrapOBU(header.OBUTemporalDelimiter, nil)...)
	good := wrapOBU(header.OBUSequenceHeader, seqPayload)
	// Flip the forbidden bit of the SequenceHeader OBU header.
	good[0] |= 0x80
	buf = append(buf, good...)

	_, err := SplitOBUs(buf, ParseOptions{StrictStdCompliance: true})
	if !errors.Is(err, ErrForbiddenBit) {
		t.Fatalf("err = %v, want ErrForbiddenBit", err)
	}
	// In lenient mode the same buffer must walk cleanly.
	obus, err := SplitOBUs(buf, ParseOptions{})
	if err != nil {
		t.Fatalf("lenient SplitOBUs: %v", err)
	}
	if len(obus) != 2 {
		t.Fatalf("lenient got %d obus, want 2", len(obus))
	}
}

// FuzzSplitOBUs guards against panics in the OBU envelope walker. The
// dispatcher is meant to surface every malformed input as an error, never as
// a runtime panic.
func FuzzSplitOBUs(f *testing.F) {
	// Seed with a well-formed temporal unit and a few malformed prefixes.
	td := wrapOBU(header.OBUTemporalDelimiter, nil)
	f.Add(td)
	f.Add(append([]byte{}, 0x12, 0x00)) // TD with size 0
	f.Add([]byte{0x12})                 // header byte only -> short leb128
	f.Add([]byte{0x12, 0x7F})           // size advertises 127 bytes that aren't there
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = SplitOBUs(data, ParseOptions{})
		_, _ = SplitOBUs(data, ParseOptions{StrictStdCompliance: true})
	})
}

// FuzzParseSequenceHeader guards the sequence-header parser against
// panics on arbitrary inputs. The parser should always surface malformed
// data as a typed error.
func FuzzParseSequenceHeader(f *testing.F) {
	f.Add(buildReducedStillSeqHdr(nil, seqOpts{profile: 0, stillPicture: true}))
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		var hdr header.SequenceHeader
		_ = ParseSequenceHeader(data, &hdr, ParseOptions{})
		_ = ParseSequenceHeader(data, &hdr, ParseOptions{StrictStdCompliance: true})
	})
}

// FuzzParseMetadataOBU guards the metadata parser. The five known
// metadata_type branches plus the strict-mode reserved-type rejection are
// all reachable via random bytes, so the fuzz target only needs to ensure
// no panics escape.
func FuzzParseMetadataOBU(f *testing.F) {
	f.Add(hdrCLLPayload(1000, 400))
	f.Add([]byte{byte(header.MetadataITUTT35), 0x10, 0x80})
	f.Add([]byte{0x00}) // reserved type=0
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		var m Metadata
		_ = ParseMetadataOBU(data, &m, MetadataParseOptions{})
		_ = ParseMetadataOBU(data, &m, MetadataParseOptions{StrictStdCompliance: true})
	})
}
