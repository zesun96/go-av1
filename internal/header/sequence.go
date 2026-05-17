package header

// OperatingPoint mirrors Dav1dSequenceHeaderOperatingPoint.
//
// idc/level/tier follow the AV1 syntax exactly. The decoder fills these
// directly from sequence_header_obu().
type OperatingPoint struct {
	// MajorLevel is seq_level_idx[i]/22+2, but kept as a single value
	// for compatibility with dav1d which exposes it as major_level.
	MajorLevel uint8
	// MinorLevel is seq_level_idx[i]%22 (low 2 bits, see spec 5.5.1).
	MinorLevel uint8
	// InitialDisplayDelay is initial_display_delay_minus_1 + 1, or 10
	// when the syntax element is absent.
	InitialDisplayDelay uint8
	// IDC is operating_point_idc, the 12-bit scalability mask.
	IDC uint16
	// Tier selects main (0) vs high (1) tier; only meaningful when
	// MajorLevel > 3.
	Tier uint8
	// DecoderModelParamPresent reports whether the corresponding
	// OperatingParameterInfo entry was decoded.
	DecoderModelParamPresent bool
	// DisplayModelParamPresent reports whether display_model_info was
	// present for this operating point.
	DisplayModelParamPresent bool
}

// OperatingParameterInfo mirrors Dav1dSequenceHeaderOperatingParameterInfo
// and carries the per-operating-point HRD parameters.
type OperatingParameterInfo struct {
	DecoderBufferDelay uint32
	EncoderBufferDelay uint32
	LowDelayMode       bool
}

// SequenceHeader is the Go form of Dav1dSequenceHeader. Field names match
// the AV1 syntax elements they parse, with dav1d's compatibility tweaks
// (e.g. HBD packing 8/10/12 into 0/1/2) preserved.
//
// All slice-shaped data has been turned into fixed arrays so the structure
// is allocation-free; the parser zero-initialises it before populating.
type SequenceHeader struct {
	// Profile is seq_profile (0..2).
	Profile uint8
	// MaxWidth/MaxHeight are max_frame_width_minus_1 + 1 and
	// max_frame_height_minus_1 + 1.
	MaxWidth, MaxHeight int
	// Layout is the derived pixel layout.
	Layout PixelLayout
	// Color description.
	Pri  ColorPrimaries
	TRC  TransferCharacteristics
	Mtrx MatrixCoefficients
	Chr  ChromaSamplePosition
	// HBD packs 8/10/12-bit into 0/1/2 (see Dav1dSequenceHeader.hbd).
	HBD uint8
	// ColorRange selects full (1) or limited (0) range.
	ColorRange bool

	NumOperatingPoints        uint8
	OperatingPoints           [MaxOperatingPoints]OperatingPoint
	OperatingParameterInfo    [MaxOperatingPoints]OperatingParameterInfo
	StillPicture              bool
	ReducedStillPictureHeader bool

	TimingInfoPresent               bool
	NumUnitsInTick                  uint32
	TimeScale                       uint32
	EqualPictureInterval            bool
	NumTicksPerPicture              uint32
	DecoderModelInfoPresent         bool
	EncoderDecoderBufferDelayLength uint8
	NumUnitsInDecodingTick          uint32
	BufferRemovalDelayLength        uint8
	FramePresentationDelayLength    uint8
	DisplayModelInfoPresent         bool

	WidthNBits, HeightNBits uint8
	FrameIDNumbersPresent   bool
	DeltaFrameIDNBits       uint8
	FrameIDNBits            uint8

	SB128              bool // use_128x128_superblock
	FilterIntra        bool // enable_filter_intra
	IntraEdgeFilter    bool // enable_intra_edge_filter
	InterIntra         bool // enable_interintra_compound
	MaskedCompound     bool // enable_masked_compound
	WarpedMotion       bool // enable_warped_motion
	DualFilter         bool // enable_dual_filter
	OrderHint          bool // enable_order_hint
	JntComp            bool // enable_jnt_comp
	RefFrameMVs        bool // enable_ref_frame_mvs
	ScreenContentTools AdaptiveBoolean
	ForceIntegerMV     AdaptiveBoolean
	OrderHintNBits     uint8

	SuperRes    bool // enable_superres
	CDEF        bool // enable_cdef
	Restoration bool // enable_restoration

	// SsHor / SsVer are subsampling_x / subsampling_y from the spec.
	SsHor                   uint8
	SsVer                   uint8
	Monochrome              bool
	ColorDescriptionPresent bool
	SeparateUVDeltaQ        bool
	FilmGrainPresent        bool
}
