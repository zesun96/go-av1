package obu

import (
	"bytes"
	"errors"
	"testing"

	"github.com/zesun96/go-av1/internal/header"
)

// Common helpers ------------------------------------------------------------

// metaTrailingByte is the byte that holds the AV1 trailing_one_bit (0x80)
// when the parser sits on a byte boundary.
const metaTrailingByte = 0x80

// hdrCLLPayload builds an HDR-CLL metadata OBU payload that includes the
// leb128 type, the four content fields, and the trailing 0x80 byte.
func hdrCLLPayload(maxCLL, maxFALL uint16) []byte {
	return []byte{
		byte(header.MetadataHDRCLL),
		byte(maxCLL >> 8), byte(maxCLL),
		byte(maxFALL >> 8), byte(maxFALL),
		metaTrailingByte,
	}
}

// HDR-CLL ------------------------------------------------------------------

func TestParseMetadata_HDRCLL_Happy(t *testing.T) {
	payload := hdrCLLPayload(1000, 400)
	var m Metadata
	if err := ParseMetadataOBU(payload, &m, MetadataParseOptions{StrictStdCompliance: true}); err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if m.Type != header.MetadataHDRCLL {
		t.Fatalf("type=%d want HDRCLL", m.Type)
	}
	if m.HDRCLL == nil {
		t.Fatalf("HDRCLL pointer nil")
	}
	if m.HDRCLL.MaxCLL != 1000 || m.HDRCLL.MaxFALL != 400 {
		t.Fatalf("got %+v", m.HDRCLL)
	}
}

func TestParseMetadata_HDRCLL_Short(t *testing.T) {
	// Only type byte + 2 bytes of payload, missing the second u16.
	payload := []byte{byte(header.MetadataHDRCLL), 0x01, 0x23}
	var m Metadata
	if err := ParseMetadataOBU(payload, &m, MetadataParseOptions{}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err=%v want ErrShortBuffer", err)
	}
}

func TestParseMetadata_HDRCLL_TrailingMissingStrict(t *testing.T) {
	// 4-byte body but no trailing marker byte -> Bit() short-buffer in
	// strict mode.
	payload := []byte{byte(header.MetadataHDRCLL), 0, 0, 0, 0}
	var m Metadata
	if err := ParseMetadataOBU(payload, &m, MetadataParseOptions{StrictStdCompliance: true}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err=%v want ErrShortBuffer", err)
	}
}

func TestParseMetadata_HDRCLL_TrailingZeroStrict(t *testing.T) {
	// Trailing byte present but with the trailing-one bit cleared.
	payload := []byte{byte(header.MetadataHDRCLL), 0, 0, 0, 0, 0x00}
	var m Metadata
	if err := ParseMetadataOBU(payload, &m, MetadataParseOptions{StrictStdCompliance: true}); !errors.Is(err, ErrTrailingBits) {
		t.Fatalf("err=%v want ErrTrailingBits", err)
	}
}

func TestParseMetadata_HDRCLL_TrailingZeroNonStrict(t *testing.T) {
	// Non-strict mode tolerates a zero trailing byte.
	payload := []byte{byte(header.MetadataHDRCLL), 0, 0, 0, 0, 0x00}
	var m Metadata
	if err := ParseMetadataOBU(payload, &m, MetadataParseOptions{}); err != nil {
		t.Fatalf("err=%v", err)
	}
	if m.HDRCLL == nil {
		t.Fatalf("HDRCLL nil")
	}
}

// HDR-MDCV -----------------------------------------------------------------

func TestParseMetadata_HDRMDCV_Happy(t *testing.T) {
	w := newBitWriter()
	w.writeBits(uint32(header.MetadataHDRMDCV), 8) // leb128 single byte
	// primaries[3][2] u(16)
	want := MetadataHDRMDCV{
		PrimaryChromaticityX:    [3]uint16{0x1111, 0x3333, 0x5555},
		PrimaryChromaticityY:    [3]uint16{0x2222, 0x4444, 0x6666},
		WhitePointChromaticityX: 0x7777,
		WhitePointChromaticityY: 0x8888,
		LuminanceMax:            0xDEADBEEF,
		LuminanceMin:            0x12345678,
	}
	for i := 0; i < 3; i++ {
		w.writeBits(uint32(want.PrimaryChromaticityX[i]), 16)
		w.writeBits(uint32(want.PrimaryChromaticityY[i]), 16)
	}
	w.writeBits(uint32(want.WhitePointChromaticityX), 16)
	w.writeBits(uint32(want.WhitePointChromaticityY), 16)
	w.writeBits(want.LuminanceMax, 32)
	w.writeBits(want.LuminanceMin, 32)
	w.writeBit(1) // trailing_one_bit
	payload := w.bytes()

	var m Metadata
	if err := ParseMetadataOBU(payload, &m, MetadataParseOptions{StrictStdCompliance: true}); err != nil {
		t.Fatalf("err=%v", err)
	}
	if m.Type != header.MetadataHDRMDCV || m.HDRMDCV == nil {
		t.Fatalf("type=%d hdr=%v", m.Type, m.HDRMDCV)
	}
	if *m.HDRMDCV != want {
		t.Fatalf("got %+v want %+v", *m.HDRMDCV, want)
	}
}

func TestParseMetadata_HDRMDCV_Short(t *testing.T) {
	// Only type + a handful of bytes; reading u(32) at the tail will
	// trip ErrShortBuffer.
	payload := []byte{byte(header.MetadataHDRMDCV), 0, 0, 0, 0, 0, 0, 0, 0}
	var m Metadata
	if err := ParseMetadataOBU(payload, &m, MetadataParseOptions{}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err=%v want ErrShortBuffer", err)
	}
}

// ITU-T T.35 ---------------------------------------------------------------

func TestParseMetadata_ITUTT35_NoExtension(t *testing.T) {
	// Type | country=0xB5 | "hello" | 0x80 | trailing zeros
	body := []byte{byte(header.MetadataITUTT35), 0xB5, 'h', 'e', 'l', 'l', 'o', 0x80, 0, 0}
	var m Metadata
	if err := ParseMetadataOBU(body, &m, MetadataParseOptions{}); err != nil {
		t.Fatalf("err=%v", err)
	}
	if m.Type != header.MetadataITUTT35 || m.ITUTT35 == nil {
		t.Fatalf("type=%d itut=%v", m.Type, m.ITUTT35)
	}
	if m.ITUTT35.CountryCode != 0xB5 || m.ITUTT35.CountryCodeExtension != 0 {
		t.Fatalf("country bytes wrong: %+v", m.ITUTT35)
	}
	if !bytes.Equal(m.ITUTT35.Payload, []byte("hello")) {
		t.Fatalf("payload=%q want hello", m.ITUTT35.Payload)
	}
}

func TestParseMetadata_ITUTT35_WithExtension(t *testing.T) {
	body := []byte{byte(header.MetadataITUTT35), 0xFF, 0x12, 0xCA, 0xFE, 0x80}
	var m Metadata
	if err := ParseMetadataOBU(body, &m, MetadataParseOptions{}); err != nil {
		t.Fatalf("err=%v", err)
	}
	if m.ITUTT35.CountryCode != 0xFF || m.ITUTT35.CountryCodeExtension != 0x12 {
		t.Fatalf("country wrong: %+v", m.ITUTT35)
	}
	if !bytes.Equal(m.ITUTT35.Payload, []byte{0xCA, 0xFE}) {
		t.Fatalf("payload=%v", m.ITUTT35.Payload)
	}
}

func TestParseMetadata_ITUTT35_EmptyPayload(t *testing.T) {
	body := []byte{byte(header.MetadataITUTT35), 0xB5, 0x80}
	var m Metadata
	if err := ParseMetadataOBU(body, &m, MetadataParseOptions{}); err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(m.ITUTT35.Payload) != 0 {
		t.Fatalf("payload should be empty, got %v", m.ITUTT35.Payload)
	}
}

func TestParseMetadata_ITUTT35_MissingMarker(t *testing.T) {
	// No 0x80 anywhere; trailing zeros wipe out the byte that "should"
	// hold the marker, leaving the wrong byte at payload[size].
	body := []byte{byte(header.MetadataITUTT35), 0xB5, 'x', 'y'}
	var m Metadata
	if err := ParseMetadataOBU(body, &m, MetadataParseOptions{}); !errors.Is(err, ErrMetadataITUTT35Malformed) {
		t.Fatalf("err=%v want ErrMetadataITUTT35Malformed", err)
	}
}

func TestParseMetadata_ITUTT35_OnlyMarker(t *testing.T) {
	// Country code 0xFF requires a follow-up extension byte; here the
	// extension byte slot is the trailing marker itself, leaving no
	// room for a real payload terminator.
	body := []byte{byte(header.MetadataITUTT35), 0xFF, 0x80}
	var m Metadata
	if err := ParseMetadataOBU(body, &m, MetadataParseOptions{}); !errors.Is(err, ErrMetadataITUTT35Malformed) {
		t.Fatalf("err=%v want ErrMetadataITUTT35Malformed", err)
	}
}

func TestParseMetadata_ITUTT35_ShortBuffer(t *testing.T) {
	// Country code 0xFF without the extension byte at all -> the GetBits
	// short-buffer path fires before the malformed-marker check.
	body := []byte{byte(header.MetadataITUTT35), 0xFF}
	var m Metadata
	if err := ParseMetadataOBU(body, &m, MetadataParseOptions{}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err=%v want ErrShortBuffer", err)
	}
}

// Scalability --------------------------------------------------------------

func TestParseMetadata_Scalability_PredefinedMode(t *testing.T) {
	// ModeIDC != 14 -> no structure body.
	body := []byte{byte(header.MetadataScalability), 7, metaTrailingByte}
	var m Metadata
	if err := ParseMetadataOBU(body, &m, MetadataParseOptions{StrictStdCompliance: true}); err != nil {
		t.Fatalf("err=%v", err)
	}
	if m.Scalability == nil || m.Scalability.ModeIDC != 7 || m.Scalability.Structure != nil {
		t.Fatalf("unexpected scalability: %+v", m.Scalability)
	}
}

func TestParseMetadata_Scalability_StructureFull(t *testing.T) {
	w := newBitWriter()
	w.writeBits(uint32(header.MetadataScalability), 8)
	w.writeBits(14, 8) // SCALABILITY_SS
	// spatial_layers_cnt_minus_1=1 -> 2 spatial layers
	w.writeBits(1, 2)
	w.writeBit(1) // dimensions present
	w.writeBit(1) // description present
	w.writeBit(1) // temporal group description present
	w.writeBits(0, 3)
	// 2 layers worth of dimensions
	w.writeBits(1920, 16)
	w.writeBits(1080, 16)
	w.writeBits(3840, 16)
	w.writeBits(2160, 16)
	// 2 layers worth of ref ids
	w.writeBits(0, 8)
	w.writeBits(1, 8)
	// temporal group: size=2
	w.writeBits(2, 8)
	// entry 0: temporal_id=0, no switch, ref_cnt=1, ref_diff=1
	w.writeBits(0, 3)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(1, 3)
	w.writeBits(1, 8)
	// entry 1: temporal_id=1, both switch, ref_cnt=2, ref_diff=[1,2]
	w.writeBits(1, 3)
	w.writeBit(1)
	w.writeBit(1)
	w.writeBits(2, 3)
	w.writeBits(1, 8)
	w.writeBits(2, 8)
	w.writeBit(1) // trailing_one
	payload := w.bytes()

	var m Metadata
	if err := ParseMetadataOBU(payload, &m, MetadataParseOptions{StrictStdCompliance: true}); err != nil {
		t.Fatalf("err=%v", err)
	}
	s := m.Scalability.Structure
	if s == nil {
		t.Fatalf("structure nil")
	}
	if s.SpatialLayersCntMinus1 != 1 || !s.SpatialLayerDimensionsPresent ||
		!s.SpatialLayerDescriptionPresent || !s.TemporalGroupDescriptionPresent {
		t.Fatalf("flags wrong: %+v", s)
	}
	if s.SpatialLayerMaxWidth[0] != 1920 || s.SpatialLayerMaxHeight[1] != 2160 {
		t.Fatalf("dimensions wrong: %+v", s)
	}
	if s.SpatialLayerRefID[0] != 0 || s.SpatialLayerRefID[1] != 1 {
		t.Fatalf("refIDs wrong: %+v", s)
	}
	if len(s.TemporalGroup) != 2 {
		t.Fatalf("temporal group len=%d", len(s.TemporalGroup))
	}
	e0 := s.TemporalGroup[0]
	if e0.TemporalID != 0 || e0.TemporalSwitchingUpPoint || e0.SpatialSwitchingUpPoint ||
		len(e0.RefPicDiff) != 1 || e0.RefPicDiff[0] != 1 {
		t.Fatalf("entry0 wrong: %+v", e0)
	}
	e1 := s.TemporalGroup[1]
	if e1.TemporalID != 1 || !e1.TemporalSwitchingUpPoint || !e1.SpatialSwitchingUpPoint ||
		len(e1.RefPicDiff) != 2 || e1.RefPicDiff[1] != 2 {
		t.Fatalf("entry1 wrong: %+v", e1)
	}
}

func TestParseMetadata_Scalability_StructureNoOptionals(t *testing.T) {
	// SCALABILITY_SS with all "present" flags off and 1 spatial layer.
	w := newBitWriter()
	w.writeBits(uint32(header.MetadataScalability), 8)
	w.writeBits(14, 8)
	w.writeBits(0, 2) // cnt-1 = 0
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(0, 3)
	w.writeBit(1)
	payload := w.bytes()

	var m Metadata
	if err := ParseMetadataOBU(payload, &m, MetadataParseOptions{StrictStdCompliance: true}); err != nil {
		t.Fatalf("err=%v", err)
	}
	s := m.Scalability.Structure
	if s == nil || len(s.TemporalGroup) != 0 {
		t.Fatalf("structure wrong: %+v", s)
	}
}

func TestParseMetadata_Scalability_ModeShort(t *testing.T) {
	// Type byte but no mode_idc byte at all.
	body := []byte{byte(header.MetadataScalability)}
	var m Metadata
	if err := ParseMetadataOBU(body, &m, MetadataParseOptions{}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err=%v want ErrShortBuffer", err)
	}
}

func TestParseMetadata_Scalability_StructureHeaderShort(t *testing.T) {
	// SCALABILITY_SS but no flag byte.
	body := []byte{byte(header.MetadataScalability), 14}
	var m Metadata
	if err := ParseMetadataOBU(body, &m, MetadataParseOptions{}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err=%v want ErrShortBuffer", err)
	}
}

func TestParseMetadata_Scalability_TemporalGroupSizeShort(t *testing.T) {
	// Flags announce a temporal group but the size byte is missing.
	w := newBitWriter()
	w.writeBits(uint32(header.MetadataScalability), 8)
	w.writeBits(14, 8)
	w.writeBits(0, 2)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1) // temporal_group_description_present
	w.writeBits(0, 3)
	// no size byte
	body := w.bytes()
	var m Metadata
	if err := ParseMetadataOBU(body, &m, MetadataParseOptions{}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err=%v want ErrShortBuffer", err)
	}
}

func TestParseMetadata_Scalability_TemporalGroupBodyShort(t *testing.T) {
	// Announce a temporal group of size 1 but provide no body bytes.
	w := newBitWriter()
	w.writeBits(uint32(header.MetadataScalability), 8)
	w.writeBits(14, 8)
	w.writeBits(0, 2)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBits(0, 3)
	w.writeBits(1, 8) // size=1
	body := w.bytes()
	var m Metadata
	if err := ParseMetadataOBU(body, &m, MetadataParseOptions{}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err=%v want ErrShortBuffer", err)
	}
}

// Timecode -----------------------------------------------------------------

func TestParseMetadata_Timecode_FullTimestamp(t *testing.T) {
	w := newBitWriter()
	w.writeBits(uint32(header.MetadataTimecode), 8)
	w.writeBits(3, 5)   // counting_type
	w.writeBit(1)       // full_timestamp
	w.writeBit(1)       // discontinuity
	w.writeBit(0)       // cnt_dropped
	w.writeBits(120, 9) // n_frames
	w.writeBits(45, 6)  // seconds
	w.writeBits(30, 6)  // minutes
	w.writeBits(12, 5)  // hours
	w.writeBits(8, 5)   // time_offset_length
	w.writeBits(0xA5, 8)
	w.writeBit(1)
	payload := w.bytes()

	var m Metadata
	if err := ParseMetadataOBU(payload, &m, MetadataParseOptions{StrictStdCompliance: true}); err != nil {
		t.Fatalf("err=%v", err)
	}
	tc := m.Timecode
	if tc == nil || tc.CountingType != 3 || !tc.FullTimestamp || !tc.Discontinuity ||
		tc.CntDropped || tc.NFrames != 120 || tc.SecondsValue != 45 ||
		tc.MinutesValue != 30 || tc.HoursValue != 12 ||
		tc.TimeOffsetLength != 8 || tc.TimeOffsetValue != 0xA5 {
		t.Fatalf("got %+v", tc)
	}
	if !tc.SecondsPresent || !tc.MinutesPresent || !tc.HoursPresent {
		t.Fatalf("present flags wrong: %+v", tc)
	}
}

func TestParseMetadata_Timecode_PartialNoSeconds(t *testing.T) {
	w := newBitWriter()
	w.writeBits(uint32(header.MetadataTimecode), 8)
	w.writeBits(0, 5)
	w.writeBit(0) // full_timestamp=0
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(0, 9)
	w.writeBit(0) // seconds_flag=0
	w.writeBits(0, 5)
	w.writeBit(1)
	payload := w.bytes()
	var m Metadata
	if err := ParseMetadataOBU(payload, &m, MetadataParseOptions{StrictStdCompliance: true}); err != nil {
		t.Fatalf("err=%v", err)
	}
	tc := m.Timecode
	if tc.SecondsPresent || tc.MinutesPresent || tc.HoursPresent || tc.TimeOffsetLength != 0 {
		t.Fatalf("unexpected presence: %+v", tc)
	}
}

func TestParseMetadata_Timecode_PartialSecondsOnly(t *testing.T) {
	w := newBitWriter()
	w.writeBits(uint32(header.MetadataTimecode), 8)
	w.writeBits(0, 5)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(0, 9)
	w.writeBit(1)     // seconds_flag
	w.writeBits(7, 6) // seconds_value
	w.writeBit(0)     // minutes_flag=0
	w.writeBits(0, 5)
	w.writeBit(1)
	payload := w.bytes()
	var m Metadata
	if err := ParseMetadataOBU(payload, &m, MetadataParseOptions{StrictStdCompliance: true}); err != nil {
		t.Fatalf("err=%v", err)
	}
	tc := m.Timecode
	if !tc.SecondsPresent || tc.SecondsValue != 7 || tc.MinutesPresent || tc.HoursPresent {
		t.Fatalf("got %+v", tc)
	}
}

func TestParseMetadata_Timecode_PartialMinutesOnly(t *testing.T) {
	w := newBitWriter()
	w.writeBits(uint32(header.MetadataTimecode), 8)
	w.writeBits(0, 5)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(0, 9)
	w.writeBit(1)
	w.writeBits(5, 6)
	w.writeBit(1) // minutes_flag
	w.writeBits(9, 6)
	w.writeBit(0) // hours_flag=0
	w.writeBits(0, 5)
	w.writeBit(1)
	payload := w.bytes()
	var m Metadata
	if err := ParseMetadataOBU(payload, &m, MetadataParseOptions{StrictStdCompliance: true}); err != nil {
		t.Fatalf("err=%v", err)
	}
	tc := m.Timecode
	if !tc.MinutesPresent || tc.MinutesValue != 9 || tc.HoursPresent {
		t.Fatalf("got %+v", tc)
	}
}

func TestParseMetadata_Timecode_PartialHours(t *testing.T) {
	w := newBitWriter()
	w.writeBits(uint32(header.MetadataTimecode), 8)
	w.writeBits(0, 5)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(0, 9)
	w.writeBit(1)
	w.writeBits(1, 6)
	w.writeBit(1)
	w.writeBits(2, 6)
	w.writeBit(1) // hours_flag
	w.writeBits(3, 5)
	w.writeBits(0, 5)
	w.writeBit(1)
	payload := w.bytes()
	var m Metadata
	if err := ParseMetadataOBU(payload, &m, MetadataParseOptions{StrictStdCompliance: true}); err != nil {
		t.Fatalf("err=%v", err)
	}
	tc := m.Timecode
	if !tc.HoursPresent || tc.HoursValue != 3 {
		t.Fatalf("got %+v", tc)
	}
}

func TestParseMetadata_Timecode_Short(t *testing.T) {
	body := []byte{byte(header.MetadataTimecode), 0x00}
	var m Metadata
	if err := ParseMetadataOBU(body, &m, MetadataParseOptions{}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err=%v want ErrShortBuffer", err)
	}
}

// Unknown / reserved -------------------------------------------------------

func TestParseMetadata_UserPrivate(t *testing.T) {
	// Types 6..31 are "unregistered user private" - silently accepted.
	body := []byte{6, 0xAA, 0xBB}
	var m Metadata
	if err := ParseMetadataOBU(body, &m, MetadataParseOptions{StrictStdCompliance: true}); err != nil {
		t.Fatalf("err=%v", err)
	}
	if m.Type != 6 || m.HDRCLL != nil || m.HDRMDCV != nil || m.ITUTT35 != nil ||
		m.Scalability != nil || m.Timecode != nil {
		t.Fatalf("user private should leave sub-payloads nil: %+v", m)
	}
}

func TestParseMetadata_ReservedStrict(t *testing.T) {
	body := []byte{byte(header.MetadataReserved0)}
	var m Metadata
	if err := ParseMetadataOBU(body, &m, MetadataParseOptions{StrictStdCompliance: true}); !errors.Is(err, ErrMetadataUnknownType) {
		t.Fatalf("err=%v want ErrMetadataUnknownType", err)
	}
}

func TestParseMetadata_ReservedNonStrict(t *testing.T) {
	body := []byte{byte(header.MetadataReserved0)}
	var m Metadata
	if err := ParseMetadataOBU(body, &m, MetadataParseOptions{}); err != nil {
		t.Fatalf("err=%v", err)
	}
	if m.Type != header.MetadataReserved0 {
		t.Fatalf("type=%d", m.Type)
	}
}

// Top-level error paths ----------------------------------------------------

func TestParseMetadata_NilOut(t *testing.T) {
	if err := ParseMetadataOBU([]byte{0x01}, nil, MetadataParseOptions{}); err == nil {
		t.Fatalf("expected error for nil out")
	}
}

func TestParseMetadata_Empty(t *testing.T) {
	var m Metadata
	if err := ParseMetadataOBU(nil, &m, MetadataParseOptions{}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err=%v want ErrShortBuffer", err)
	}
}

func TestParseMetadata_TypeLebOverflow(t *testing.T) {
	// 9 continuation bytes -> Leb128 overflows.
	body := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x01}
	var m Metadata
	if err := ParseMetadataOBU(body, &m, MetadataParseOptions{}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err=%v want ErrShortBuffer", err)
	}
}
