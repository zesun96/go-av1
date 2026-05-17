package obu

import (
	"errors"

	"github.com/zesun96/go-av1/internal/bitstream"
	"github.com/zesun96/go-av1/internal/header"
)

// Metadata OBU parser errors. They mirror the various failure exits in
// dav1d's DAV1D_OBU_METADATA handling (src/obu.c lines 1356-1514).
var (
	// ErrMetadataUnknownType is returned when metadata_type is reserved
	// (zero) or in the standardised-but-unsupported-reserved range
	// 6..31, and StrictStdCompliance is enabled.
	ErrMetadataUnknownType = errors.New("obu: metadata: unknown metadata_type")
	// ErrMetadataITUTT35Malformed is returned when an ITU-T T.35 payload
	// cannot be parsed because the trailing 0x80 marker is missing or
	// the country-code section consumes the whole payload.
	ErrMetadataITUTT35Malformed = errors.New("obu: metadata: malformed ITU-T T.35 message")
)

// Metadata is the parsed form of an OBU_METADATA payload.
//
// Exactly one of the Type-specific pointer fields is populated, matching
// Type. Unknown / reserved metadata_type values yield a Metadata with only
// Type set (and ITUTT35Raw populated when StrictStdCompliance is off).
type Metadata struct {
	// Type identifies which payload sub-structure was parsed.
	Type header.MetadataType
	// HDRCLL is set when Type == MetadataHDRCLL.
	HDRCLL *MetadataHDRCLL
	// HDRMDCV is set when Type == MetadataHDRMDCV.
	HDRMDCV *MetadataHDRMDCV
	// ITUTT35 is set when Type == MetadataITUTT35.
	ITUTT35 *MetadataITUTT35
	// Scalability is set when Type == MetadataScalability.
	Scalability *MetadataScalability
	// Timecode is set when Type == MetadataTimecode.
	Timecode *MetadataTimecode
}

// MetadataHDRCLL mirrors metadata_hdr_cll(), AV1 5.8.2.
type MetadataHDRCLL struct {
	// MaxCLL is max_cll, the content light level upper bound in cd/m².
	MaxCLL uint16
	// MaxFALL is max_fall, the frame-average light level upper bound in
	// cd/m².
	MaxFALL uint16
}

// MetadataHDRMDCV mirrors metadata_hdr_mdcv(), AV1 5.8.3.
type MetadataHDRMDCV struct {
	// PrimaryChromaticityX / PrimaryChromaticityY are the colour-primary
	// chromaticity coordinates in 0.16-fixed-point (0..50000 inclusive).
	PrimaryChromaticityX [3]uint16
	PrimaryChromaticityY [3]uint16
	// WhitePointChromaticityX / Y describe the display white point.
	WhitePointChromaticityX uint16
	WhitePointChromaticityY uint16
	// LuminanceMax is the upper bound of nominal display luminance in
	// 0.0001 cd/m² units.
	LuminanceMax uint32
	// LuminanceMin is the lower bound of nominal display luminance in
	// 0.0001 cd/m² units.
	LuminanceMin uint32
}

// MetadataITUTT35 mirrors metadata_itut_t35(), AV1 5.8.4.
//
// Payload is a copy of the message bytes that follow the country code(s) and
// precede the 0x80 trailing-marker byte. Callers may retain Payload
// independently of the source buffer.
type MetadataITUTT35 struct {
	// CountryCode is itu_t_t35_country_code.
	CountryCode uint8
	// CountryCodeExtension is itu_t_t35_country_code_extension_byte,
	// valid only when CountryCode == 0xFF.
	CountryCodeExtension uint8
	// Payload is the byte sequence between the country code(s) and the
	// 0x80 trailing marker; never nil for a well-formed message but may
	// be empty.
	Payload []byte
}

// MetadataScalability mirrors metadata_scalability(), AV1 5.8.5.
type MetadataScalability struct {
	// ModeIDC is scalability_mode_idc; it selects one of the pre-defined
	// scalability templates or SCALABILITY_SS == 14 for "explicit".
	ModeIDC uint8
	// Structure is populated only when ModeIDC == 14.
	Structure *ScalabilityStructure
}

// ScalabilityStructure mirrors scalability_structure(), AV1 5.8.5.1.
type ScalabilityStructure struct {
	// SpatialLayersCntMinus1 is spatial_layers_cnt_minus_1, range 0..3.
	SpatialLayersCntMinus1 uint8
	// SpatialLayerDimensionsPresent reports whether per-layer max
	// dimensions are encoded.
	SpatialLayerDimensionsPresent bool
	// SpatialLayerDescriptionPresent reports whether per-layer reference
	// IDs are encoded.
	SpatialLayerDescriptionPresent bool
	// TemporalGroupDescriptionPresent reports whether a temporal group
	// description follows.
	TemporalGroupDescriptionPresent bool
	// SpatialLayerMaxWidth / Height are valid only when
	// SpatialLayerDimensionsPresent is true. Indices 0..SpatialLayersCnt
	// inclusive are populated.
	SpatialLayerMaxWidth  [4]uint16
	SpatialLayerMaxHeight [4]uint16
	// SpatialLayerRefID is valid only when SpatialLayerDescriptionPresent
	// is true.
	SpatialLayerRefID [4]uint8
	// TemporalGroup is valid only when TemporalGroupDescriptionPresent
	// is true.
	TemporalGroup []ScalabilityTemporalGroupEntry
}

// ScalabilityTemporalGroupEntry is one entry of the
// temporal_group_description loop in 5.8.5.1.
type ScalabilityTemporalGroupEntry struct {
	// TemporalID is temporal_group_temporal_id, range 0..7.
	TemporalID uint8
	// TemporalSwitchingUpPoint corresponds to the
	// temporal_group_temporal_switching_up_point_flag.
	TemporalSwitchingUpPoint bool
	// SpatialSwitchingUpPoint corresponds to the
	// temporal_group_spatial_switching_up_point_flag.
	SpatialSwitchingUpPoint bool
	// RefPicDiff is the temporal_group_ref_pic_diff[] sub-loop, one
	// entry per RefCnt.
	RefPicDiff []uint8
}

// MetadataTimecode mirrors metadata_timecode(), AV1 5.8.6.
type MetadataTimecode struct {
	// CountingType is counting_type, range 0..31.
	CountingType uint8
	// FullTimestamp reports whether seconds/minutes/hours are present
	// unconditionally.
	FullTimestamp bool
	// Discontinuity is discontinuity_flag.
	Discontinuity bool
	// CntDropped is cnt_dropped_flag.
	CntDropped bool
	// NFrames is n_frames, range 0..511.
	NFrames uint16
	// SecondsPresent reports whether the conditional seconds_value below
	// is meaningful (always true when FullTimestamp is set).
	SecondsPresent bool
	// SecondsValue is in [0,59].
	SecondsValue uint8
	// MinutesPresent reports whether MinutesValue is meaningful.
	MinutesPresent bool
	// MinutesValue is in [0,59].
	MinutesValue uint8
	// HoursPresent reports whether HoursValue is meaningful.
	HoursPresent bool
	// HoursValue is in [0,23].
	HoursValue uint8
	// TimeOffsetLength is time_offset_length, range 0..31.
	TimeOffsetLength uint8
	// TimeOffsetValue is meaningful only when TimeOffsetLength > 0.
	TimeOffsetValue uint32
}

// MetadataParseOptions controls Metadata OBU parsing.
type MetadataParseOptions struct {
	// StrictStdCompliance enables the spec-mandated trailing_bits()
	// checks (where applicable) and rejects reserved metadata_type
	// values that the non-strict path would silently swallow.
	StrictStdCompliance bool
}

// ParseMetadataOBU decodes one metadata_obu payload (the bytes following the
// OBU header and the leb128 size, if any). It is a 1:1 port of the
// DAV1D_OBU_METADATA branch of dav1d_parse_obus.
//
// The HDR-CLL / HDR-MDCV / Timecode / Scalability branches consume a
// trailing_one_bit when StrictStdCompliance is enabled, mirroring dav1d's
// check_trailing_bits behaviour. The ITU-T T.35 branch locates the payload
// end by scanning backwards from the OBU end for a 0x80 marker, matching the
// dav1d "trailing zero bytes" heuristic.
func ParseMetadataOBU(payload []byte, out *Metadata, opts MetadataParseOptions) error {
	if out == nil {
		return errors.New("obu: nil Metadata out")
	}
	*out = Metadata{}
	if len(payload) == 0 {
		return ErrShortBuffer
	}
	gb := bitstream.NewGetBits(payload)
	v, ok := gb.Leb128()
	if !ok {
		return ErrShortBuffer
	}
	out.Type = header.MetadataType(v)
	switch out.Type {
	case header.MetadataHDRCLL:
		out.HDRCLL = &MetadataHDRCLL{}
		if err := parseMetadataHDRCLL(gb, out.HDRCLL); err != nil {
			return err
		}
		return checkMetadataTrailing(gb, opts.StrictStdCompliance)
	case header.MetadataHDRMDCV:
		out.HDRMDCV = &MetadataHDRMDCV{}
		if err := parseMetadataHDRMDCV(gb, out.HDRMDCV); err != nil {
			return err
		}
		return checkMetadataTrailing(gb, opts.StrictStdCompliance)
	case header.MetadataITUTT35:
		t35, err := parseMetadataITUTT35(payload, gb)
		if err != nil {
			return err
		}
		out.ITUTT35 = t35
		return nil
	case header.MetadataScalability:
		out.Scalability = &MetadataScalability{}
		if err := parseMetadataScalability(gb, out.Scalability); err != nil {
			return err
		}
		return checkMetadataTrailing(gb, opts.StrictStdCompliance)
	case header.MetadataTimecode:
		out.Timecode = &MetadataTimecode{}
		if err := parseMetadataTimecode(gb, out.Timecode); err != nil {
			return err
		}
		return checkMetadataTrailing(gb, opts.StrictStdCompliance)
	default:
		// metadata_type values 0 (reserved) and 6..31 are
		// "unregistered user private" per the spec. dav1d only
		// warns; in strict mode we reject reserved (0) but accept
		// the user-private range silently.
		if opts.StrictStdCompliance && out.Type == header.MetadataReserved0 {
			return ErrMetadataUnknownType
		}
		return nil
	}
}

func parseMetadataHDRCLL(gb *bitstream.GetBits, out *MetadataHDRCLL) error {
	out.MaxCLL = uint16(gb.F(16))
	out.MaxFALL = uint16(gb.F(16))
	if gb.Err() {
		return ErrShortBuffer
	}
	return nil
}

func parseMetadataHDRMDCV(gb *bitstream.GetBits, out *MetadataHDRMDCV) error {
	for i := 0; i < 3; i++ {
		out.PrimaryChromaticityX[i] = uint16(gb.F(16))
		out.PrimaryChromaticityY[i] = uint16(gb.F(16))
	}
	out.WhitePointChromaticityX = uint16(gb.F(16))
	out.WhitePointChromaticityY = uint16(gb.F(16))
	out.LuminanceMax = gb.F(32)
	out.LuminanceMin = gb.F(32)
	if gb.Err() {
		return ErrShortBuffer
	}
	return nil
}

func parseMetadataITUTT35(payload []byte, gb *bitstream.GetBits) (*MetadataITUTT35, error) {
	// dav1d locates the end of the T.35 message by scanning back from
	// the OBU tail past any trailing zero bytes, then taking the byte
	// immediately before as the trailing 0x80 marker. The payload then
	// lies between the country-code section and that marker.
	end := len(payload)
	for end > 0 && payload[end-1] == 0 {
		end--
	}
	// end-1 (when in range) is the marker's index.
	end--
	out := &MetadataITUTT35{}
	out.CountryCode = uint8(gb.F(8))
	if out.CountryCode == 0xFF {
		out.CountryCodeExtension = uint8(gb.F(8))
	}
	if gb.Err() {
		return nil, ErrShortBuffer
	}
	start := gb.BytePos()
	// Reject inputs whose tail is missing or precedes the cursor, plus
	// inputs whose marker slot does not actually hold 0x80.
	if end < start || end >= len(payload) || payload[end] != 0x80 {
		return nil, ErrMetadataITUTT35Malformed
	}
	body := make([]byte, end-start)
	copy(body, payload[start:end])
	out.Payload = body
	return out, nil
}

func parseMetadataScalability(gb *bitstream.GetBits, out *MetadataScalability) error {
	out.ModeIDC = uint8(gb.F(8))
	if gb.Err() {
		return ErrShortBuffer
	}
	// SCALABILITY_SS == 14: explicit structure follows.
	if out.ModeIDC != 14 {
		return nil
	}
	s := &ScalabilityStructure{}
	s.SpatialLayersCntMinus1 = uint8(gb.F(2))
	s.SpatialLayerDimensionsPresent = gb.Bit() != 0
	s.SpatialLayerDescriptionPresent = gb.Bit() != 0
	s.TemporalGroupDescriptionPresent = gb.Bit() != 0
	_ = gb.F(3) // scalability_structure_reserved_3bits
	if gb.Err() {
		return ErrShortBuffer
	}
	n := int(s.SpatialLayersCntMinus1) + 1
	if s.SpatialLayerDimensionsPresent {
		for i := 0; i < n; i++ {
			s.SpatialLayerMaxWidth[i] = uint16(gb.F(16))
			s.SpatialLayerMaxHeight[i] = uint16(gb.F(16))
		}
	}
	if s.SpatialLayerDescriptionPresent {
		for i := 0; i < n; i++ {
			s.SpatialLayerRefID[i] = uint8(gb.F(8))
		}
	}
	if s.TemporalGroupDescriptionPresent {
		size := int(gb.F(8))
		if gb.Err() {
			return ErrShortBuffer
		}
		s.TemporalGroup = make([]ScalabilityTemporalGroupEntry, size)
		for i := 0; i < size; i++ {
			e := &s.TemporalGroup[i]
			e.TemporalID = uint8(gb.F(3))
			e.TemporalSwitchingUpPoint = gb.Bit() != 0
			e.SpatialSwitchingUpPoint = gb.Bit() != 0
			refCnt := int(gb.F(3))
			e.RefPicDiff = make([]uint8, refCnt)
			for j := 0; j < refCnt; j++ {
				e.RefPicDiff[j] = uint8(gb.F(8))
			}
		}
	}
	if gb.Err() {
		return ErrShortBuffer
	}
	out.Structure = s
	return nil
}

func parseMetadataTimecode(gb *bitstream.GetBits, out *MetadataTimecode) error {
	out.CountingType = uint8(gb.F(5))
	out.FullTimestamp = gb.Bit() != 0
	out.Discontinuity = gb.Bit() != 0
	out.CntDropped = gb.Bit() != 0
	out.NFrames = uint16(gb.F(9))
	if out.FullTimestamp {
		out.SecondsPresent = true
		out.SecondsValue = uint8(gb.F(6))
		out.MinutesPresent = true
		out.MinutesValue = uint8(gb.F(6))
		out.HoursPresent = true
		out.HoursValue = uint8(gb.F(5))
	} else if gb.Bit() != 0 { // seconds_flag
		out.SecondsPresent = true
		out.SecondsValue = uint8(gb.F(6))
		if gb.Bit() != 0 { // minutes_flag
			out.MinutesPresent = true
			out.MinutesValue = uint8(gb.F(6))
			if gb.Bit() != 0 { // hours_flag
				out.HoursPresent = true
				out.HoursValue = uint8(gb.F(5))
			}
		}
	}
	out.TimeOffsetLength = uint8(gb.F(5))
	if out.TimeOffsetLength > 0 {
		out.TimeOffsetValue = gb.F(int(out.TimeOffsetLength))
	}
	if gb.Err() {
		return ErrShortBuffer
	}
	return nil
}

// checkMetadataTrailing is a thin wrapper around checkTrailingBits used by
// every metadata branch that has a spec-mandated trailing_bits() footer.
// ITU-T T.35 is intentionally excluded: dav1d locates the message end by
// scanning for the 0x80 marker rather than calling check_trailing_bits.
func checkMetadataTrailing(gb *bitstream.GetBits, strict bool) error {
	return checkTrailingBits(gb, strict)
}
