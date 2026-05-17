package header

// OBUType identifies an AV1 Open Bitstream Unit. Values match the wire format
// used in section 5.3.1 of the AV1 specification and dav1d's
// enum Dav1dObuType in include/dav1d/headers.h.
type OBUType uint8

const (
	// OBUReserved0 is the value 0; it is reserved by the spec.
	OBUReserved0 OBUType = 0
	// OBUSequenceHeader carries a sequence header.
	OBUSequenceHeader OBUType = 1
	// OBUTemporalDelimiter signals the start of a new temporal unit and
	// resets SeenFrameHeader.
	OBUTemporalDelimiter OBUType = 2
	// OBUFrameHeader carries a frame header.
	OBUFrameHeader OBUType = 3
	// OBUTileGroup carries a tile group payload.
	OBUTileGroup OBUType = 4
	// OBUMetadata carries a metadata payload (HDR, scalability, ...).
	OBUMetadata OBUType = 5
	// OBUFrame is the combined frame header + tile group OBU.
	OBUFrame OBUType = 6
	// OBURedundantFrameHeader is a duplicate frame header carried for
	// resilience.
	OBURedundantFrameHeader OBUType = 7
	// OBUTileList carries a tile list (large-scale tiles).
	OBUTileList OBUType = 8
	// OBUPadding is a payload padding OBU.
	OBUPadding OBUType = 15
)

// String returns a human-readable name for the OBU type. Unknown values are
// rendered as "OBUType(N)".
func (t OBUType) String() string {
	switch t {
	case OBUReserved0:
		return "Reserved0"
	case OBUSequenceHeader:
		return "SequenceHeader"
	case OBUTemporalDelimiter:
		return "TemporalDelimiter"
	case OBUFrameHeader:
		return "FrameHeader"
	case OBUTileGroup:
		return "TileGroup"
	case OBUMetadata:
		return "Metadata"
	case OBUFrame:
		return "Frame"
	case OBURedundantFrameHeader:
		return "RedundantFrameHeader"
	case OBUTileList:
		return "TileList"
	case OBUPadding:
		return "Padding"
	default:
		return "OBUType(" + itoa(uint8(t)) + ")"
	}
}

// IsKnown reports whether t is one of the OBU types defined by AV1.
func (t OBUType) IsKnown() bool {
	switch t {
	case OBUSequenceHeader, OBUTemporalDelimiter, OBUFrameHeader,
		OBUTileGroup, OBUMetadata, OBUFrame, OBURedundantFrameHeader,
		OBUTileList, OBUPadding:
		return true
	default:
		return false
	}
}

// OBUHeader is the parsed form of an AV1 OBU header byte (and optional
// extension byte). It mirrors dav1d's per-OBU temporary state in
// dav1d_parse_obus.
type OBUHeader struct {
	// Type is the value of obu_type.
	Type OBUType
	// HasExtension reports whether obu_extension_flag was set; if true the
	// TemporalID and SpatialID fields are meaningful.
	HasExtension bool
	// HasSize reports whether obu_has_size_field was set, i.e. a leb128
	// payload size follows the header.
	HasSize bool
	// TemporalID is the temporal_id field (0 when HasExtension is false).
	TemporalID uint8
	// SpatialID is the spatial_id field (0 when HasExtension is false).
	SpatialID uint8
	// HeaderSize is the number of bytes consumed by the header itself
	// (1 or 2). It does NOT include the leb128 length field.
	HeaderSize int
}

// itoa is a tiny base-10 formatter used by OBUType.String to avoid pulling
// strconv into a stdlib-style data-only package.
func itoa(v uint8) string {
	if v == 0 {
		return "0"
	}
	var buf [3]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0') + v%10
		v /= 10
	}
	return string(buf[i:])
}

// MetadataType identifies an OBU_METADATA payload kind. Values match
// metadata_type as defined in section 6.7.1 of the AV1 specification.
type MetadataType uint32

const (
	// MetadataReserved0 is the value 0.
	MetadataReserved0 MetadataType = 0
	// MetadataHDRCLL is content-light-level HDR metadata.
	MetadataHDRCLL MetadataType = 1
	// MetadataHDRMDCV is mastering-display-colour-volume HDR metadata.
	MetadataHDRMDCV MetadataType = 2
	// MetadataScalability carries scalability information.
	MetadataScalability MetadataType = 3
	// MetadataITUTT35 carries an ITU-T T.35 user data payload.
	MetadataITUTT35 MetadataType = 4
	// MetadataTimecode carries SMPTE 12-1 timecode information.
	MetadataTimecode MetadataType = 5
)
