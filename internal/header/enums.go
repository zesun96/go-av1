package header

// Constants from section 3 ("Symbols and abbreviated terms") of the AV1
// specification. Names mirror the DAV1D_* macros in
// dav1d/include/dav1d/headers.h so that cross-referencing the reference
// implementation stays trivial.
const (
	// MaxCDEFStrengths bounds the per-frame CDEF strength tables.
	MaxCDEFStrengths = 8
	// MaxOperatingPoints bounds the per-sequence operating-point arrays.
	MaxOperatingPoints = 32
	// MaxTileCols is the per-frame maximum number of tile columns.
	MaxTileCols = 64
	// MaxTileRows is the per-frame maximum number of tile rows.
	MaxTileRows = 64
	// MaxSegments is the maximum number of segments allowed per frame.
	MaxSegments = 8
	// NumRefFrames is the size of the decoded reference frame buffer.
	NumRefFrames = 8
	// PrimaryRefNone marks a frame that does not inherit context from any
	// reference (primary_ref_frame == 7).
	PrimaryRefNone = 7
	// RefsPerFrame is the number of references an inter frame can use.
	RefsPerFrame = 7
	// TotalRefsPerFrame includes the implicit INTRA_FRAME slot.
	TotalRefsPerFrame = RefsPerFrame + 1
)

// PixelLayout selects the chroma subsampling and matches Dav1dPixelLayout.
type PixelLayout uint8

const (
	// PixelLayoutI400 is monochrome (no chroma planes).
	PixelLayoutI400 PixelLayout = 0
	// PixelLayoutI420 is 4:2:0 planar.
	PixelLayoutI420 PixelLayout = 1
	// PixelLayoutI422 is 4:2:2 planar.
	PixelLayoutI422 PixelLayout = 2
	// PixelLayoutI444 is 4:4:4 planar.
	PixelLayoutI444 PixelLayout = 3
)

// ColorPrimaries enumerates the color_primaries syntax element, mirroring
// Dav1dColorPrimaries.
type ColorPrimaries uint8

const (
	ColorPriBT709    ColorPrimaries = 1
	ColorPriUnknown  ColorPrimaries = 2
	ColorPriBT470M   ColorPrimaries = 4
	ColorPriBT470BG  ColorPrimaries = 5
	ColorPriBT601    ColorPrimaries = 6
	ColorPriSMPTE240 ColorPrimaries = 7
	ColorPriFilm     ColorPrimaries = 8
	ColorPriBT2020   ColorPrimaries = 9
	ColorPriXYZ      ColorPrimaries = 10
	ColorPriSMPTE431 ColorPrimaries = 11
	ColorPriSMPTE432 ColorPrimaries = 12
	ColorPriEBU3213  ColorPrimaries = 22
	ColorPriReserved ColorPrimaries = 255
)

// TransferCharacteristics enumerates transfer_characteristics, mirroring
// Dav1dTransferCharacteristics.
type TransferCharacteristics uint8

const (
	TRCBT709        TransferCharacteristics = 1
	TRCUnknown      TransferCharacteristics = 2
	TRCBT470M       TransferCharacteristics = 4
	TRCBT470BG      TransferCharacteristics = 5
	TRCBT601        TransferCharacteristics = 6
	TRCSMPTE240     TransferCharacteristics = 7
	TRCLinear       TransferCharacteristics = 8
	TRCLog100       TransferCharacteristics = 9
	TRCLog100Sqrt10 TransferCharacteristics = 10
	TRCIEC61966     TransferCharacteristics = 11
	TRCBT1361       TransferCharacteristics = 12
	TRCSRGB         TransferCharacteristics = 13
	TRCBT2020_10    TransferCharacteristics = 14
	TRCBT2020_12    TransferCharacteristics = 15
	TRCSMPTE2084    TransferCharacteristics = 16
	TRCSMPTE428     TransferCharacteristics = 17
	TRCHLG          TransferCharacteristics = 18
	TRCReserved     TransferCharacteristics = 255
)

// MatrixCoefficients enumerates matrix_coefficients, mirroring
// Dav1dMatrixCoefficients.
type MatrixCoefficients uint8

const (
	MCIdentity    MatrixCoefficients = 0
	MCBT709       MatrixCoefficients = 1
	MCUnknown     MatrixCoefficients = 2
	MCFCC         MatrixCoefficients = 4
	MCBT470BG     MatrixCoefficients = 5
	MCBT601       MatrixCoefficients = 6
	MCSMPTE240    MatrixCoefficients = 7
	MCSMPTEYCgCo  MatrixCoefficients = 8
	MCBT2020NCL   MatrixCoefficients = 9
	MCBT2020CL    MatrixCoefficients = 10
	MCSMPTE2085   MatrixCoefficients = 11
	MCChromatNCL  MatrixCoefficients = 12
	MCChromatCL   MatrixCoefficients = 13
	MCICtCp       MatrixCoefficients = 14
	MCReserved255 MatrixCoefficients = 255
)

// ChromaSamplePosition enumerates chroma_sample_position, mirroring
// Dav1dChromaSamplePosition.
type ChromaSamplePosition uint8

const (
	// ChromaUnknown leaves the position unspecified.
	ChromaUnknown ChromaSamplePosition = 0
	// ChromaVertical is horizontally co-located with luma(0,0),
	// between two vertical samples.
	ChromaVertical ChromaSamplePosition = 1
	// ChromaColocated is co-located with luma(0,0).
	ChromaColocated ChromaSamplePosition = 2
)

// AdaptiveBoolean mirrors Dav1dAdaptiveBoolean for tri-state syntax
// elements such as screen_content_tools / force_integer_mv.
type AdaptiveBoolean uint8

const (
	AdaptiveOff      AdaptiveBoolean = 0
	AdaptiveOn       AdaptiveBoolean = 1
	AdaptiveAdaptive AdaptiveBoolean = 2
)

// FrameType enumerates frame_type as carried in the frame header.
type FrameType uint8

const (
	FrameTypeKey    FrameType = 0
	FrameTypeInter  FrameType = 1
	FrameTypeIntra  FrameType = 2
	FrameTypeSwitch FrameType = 3
)

// IsIntra reports whether the frame type is a KEY or INTRA frame.
func (t FrameType) IsIntra() bool {
	return t == FrameTypeKey || t == FrameTypeIntra
}

// TxfmMode enumerates the transform-size selection mode.
type TxfmMode uint8

const (
	TxfmMode4x4Only    TxfmMode = 0
	TxfmModeLargest    TxfmMode = 1
	TxfmModeSwitchable TxfmMode = 2
	NumTxfmModes                = 3
)

// FilterMode enumerates the interpolation filter family.
type FilterMode uint8

const (
	FilterMode8TapRegular FilterMode = 0
	FilterMode8TapSmooth  FilterMode = 1
	FilterMode8TapSharp   FilterMode = 2
	NumSwitchableFilters             = 3
	FilterModeBilinear    FilterMode = 3
	NumFilters                       = 4
	FilterModeSwitchable  FilterMode = 4
)

// RestorationType enumerates loop restoration kinds, mirroring
// Dav1dRestorationType.
type RestorationType uint8

const (
	RestorationNone       RestorationType = 0
	RestorationSwitchable RestorationType = 1
	RestorationWiener     RestorationType = 2
	RestorationSGRProj    RestorationType = 3
)

// WarpedMotionType enumerates Dav1dWarpedMotionType.
type WarpedMotionType uint8

const (
	WMTypeIdentity    WarpedMotionType = 0
	WMTypeTranslation WarpedMotionType = 1
	WMTypeRotZoom     WarpedMotionType = 2
	WMTypeAffine      WarpedMotionType = 3
)

// WarpedMotionParams mirrors Dav1dWarpedMotionParams.
type WarpedMotionParams struct {
	Type   WarpedMotionType
	Matrix [6]int32
	// Alpha/Beta/Gamma/Delta are the affine model factors derived from
	// Matrix; ABCD aliases them as an array.
	Alpha, Beta, Gamma, Delta int16
}

// ABCD returns the affine model factors as a 4-element array.
func (w WarpedMotionParams) ABCD() [4]int16 {
	return [4]int16{w.Alpha, w.Beta, w.Gamma, w.Delta}
}
