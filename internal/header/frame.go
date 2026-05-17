package header

// SegmentationData mirrors dav1d's Dav1dSegmentationData. Each instance holds
// the per-segment quantiser / loop-filter deltas plus reference / skip /
// globalmv flags that are conveyed in segmentation_params() (section 5.9.14).
type SegmentationData struct {
	DeltaQ                                   int16
	DeltaLFYV, DeltaLFYH, DeltaLFU, DeltaLFV int8
	Ref                                      int8
	Skip                                     uint8
	GlobalMV                                 uint8
}

// SegmentationDataSet collects per-segment parameters along with bookkeeping
// fields filled in by the parser. Mirrors Dav1dSegmentationDataSet.
type SegmentationDataSet struct {
	D               [MaxSegments]SegmentationData
	PreSkip         uint8
	LastActiveSegID int8
}

// LoopfilterModeRefDeltas mirrors Dav1dLoopfilterModeRefDeltas. The first index
// of ModeDelta is is_zeromv (0 or 1); RefDelta is indexed by reference frame
// (INTRA_FRAME .. ALTREF2_FRAME).
type LoopfilterModeRefDeltas struct {
	ModeDelta [2]int8
	RefDelta  [TotalRefsPerFrame]int8
}

// DefaultLoopfilterModeRefDeltas is the spec's "loop_filter_default_ref_deltas"
// constant used at the start of every key frame and intra-only frame.
var DefaultLoopfilterModeRefDeltas = LoopfilterModeRefDeltas{
	ModeDelta: [2]int8{0, 0},
	// INTRA_FRAME=+1, LAST=0, LAST2=0, LAST3=0, GOLDEN=-1, BWDREF=0,
	// ALTREF2=-1, ALTREF=-1 (spec sec 7.2.1).
	RefDelta: [TotalRefsPerFrame]int8{1, 0, 0, 0, -1, 0, -1, -1},
}

// FilmGrainData mirrors Dav1dFilmGrainData and stores film-grain synthesis
// parameters for a single frame (or the previous frame referenced by
// update_grain=0).
type FilmGrainData struct {
	Seed                  uint32
	NumYPoints            int
	YPoints               [14][2]uint8
	ChromaScalingFromLuma int
	NumUVPoints           [2]int
	UVPoints              [2][10][2]uint8
	ScalingShift          int
	ARCoeffLag            int
	ARCoeffsY             [24]int8
	ARCoeffsUV            [2][25 + 3]int8
	ARCoeffShift          uint64
	GrainScaleShift       int
	UVMult                [2]int
	UVLumaMult            [2]int
	UVOffset              [2]int
	OverlapFlag           int
	ClipToRestrictedRange int
}

// FrameHeaderOperatingPoint mirrors the nested
// Dav1dFrameHeaderOperatingPoint struct used inside Dav1dFrameHeader.
type FrameHeaderOperatingPoint struct {
	BufferRemovalTime uint32
}

// FrameHeaderTiling mirrors the nested tiling struct of Dav1dFrameHeader.
type FrameHeaderTiling struct {
	Uniform     uint8
	NBytes      uint8
	MinLog2Cols uint8
	MaxLog2Cols uint8
	Log2Cols    uint8
	Cols        uint8
	MinLog2Rows uint8
	MaxLog2Rows uint8
	Log2Rows    uint8
	Rows        uint8
	ColStartSB  [MaxTileCols + 1]uint16
	RowStartSB  [MaxTileRows + 1]uint16
	Update      uint16
}

// FrameHeaderQuant mirrors the nested quant struct of Dav1dFrameHeader.
type FrameHeaderQuant struct {
	YAC      uint8
	YDCDelta int8
	UDCDelta int8
	UACDelta int8
	VDCDelta int8
	VACDelta int8
	QM       uint8
	QMY      uint8
	QMU      uint8
	QMV      uint8
}

// FrameHeaderSegmentation mirrors the nested segmentation struct of
// Dav1dFrameHeader.
type FrameHeaderSegmentation struct {
	Enabled    uint8
	UpdateMap  uint8
	Temporal   uint8
	UpdateData uint8
	SegData    SegmentationDataSet
	Lossless   [MaxSegments]uint8
	QIdx       [MaxSegments]uint8
}

// FrameHeaderDelta mirrors the nested delta struct of Dav1dFrameHeader.
type FrameHeaderDelta struct {
	Q  FrameHeaderDeltaQ
	LF FrameHeaderDeltaLF
}

// FrameHeaderDeltaQ mirrors the delta.q sub-struct.
type FrameHeaderDeltaQ struct {
	Present uint8
	ResLog2 uint8
}

// FrameHeaderDeltaLF mirrors the delta.lf sub-struct.
type FrameHeaderDeltaLF struct {
	Present uint8
	ResLog2 uint8
	Multi   uint8
}

// FrameHeaderLoopFilter mirrors the nested loopfilter struct of
// Dav1dFrameHeader.
type FrameHeaderLoopFilter struct {
	LevelY              [2]uint8
	LevelU              uint8
	LevelV              uint8
	ModeRefDeltaEnabled uint8
	ModeRefDeltaUpdate  uint8
	ModeRefDeltas       LoopfilterModeRefDeltas
	Sharpness           uint8
}

// FrameHeaderCDEF mirrors the nested cdef struct of Dav1dFrameHeader.
type FrameHeaderCDEF struct {
	Damping    uint8
	NBits      uint8
	YStrength  [MaxCDEFStrengths]uint8
	UVStrength [MaxCDEFStrengths]uint8
}

// FrameHeaderRestoration mirrors the nested restoration struct of
// Dav1dFrameHeader.
type FrameHeaderRestoration struct {
	Type     [3]RestorationType
	UnitSize [2]uint8
}

// FrameHeaderSuperRes mirrors the nested super_res struct of
// Dav1dFrameHeader.
type FrameHeaderSuperRes struct {
	WidthScaleDenominator uint8
	Enabled               uint8
}

// FrameHeaderFilmGrain mirrors the nested film_grain struct of
// Dav1dFrameHeader.
type FrameHeaderFilmGrain struct {
	Data    FilmGrainData
	Present uint8
	Update  uint8
}

// FrameHeader is the 1:1 Go port of Dav1dFrameHeader (include/dav1d/headers.h).
// It is filled in by ParseFrameHeader and consumed by the tile / block decoders.
type FrameHeader struct {
	FilmGrain FrameHeaderFilmGrain
	FrameType FrameType
	// Width holds {coded_width, superresolution_upscaled_width}.
	Width                    [2]int
	Height                   int
	FrameOffset              uint8
	TemporalID               uint8
	SpatialID                uint8
	ShowExistingFrame        uint8
	ExistingFrameIdx         uint8
	FrameID                  uint32
	FramePresentationDelay   uint32
	ShowFrame                uint8
	ShowableFrame            uint8
	ErrorResilientMode       uint8
	DisableCDFUpdate         uint8
	AllowScreenContentTools  uint8
	ForceIntegerMV           uint8
	FrameSizeOverride        uint8
	PrimaryRefFrame          uint8
	BufferRemovalTimePresent uint8
	OperatingPoints          [MaxOperatingPoints]FrameHeaderOperatingPoint
	RefreshFrameFlags        uint8
	RenderWidth              int
	RenderHeight             int
	SuperRes                 FrameHeaderSuperRes
	HaveRenderSize           uint8
	AllowIntrabc             uint8
	FrameRefShortSignaling   uint8
	Refidx                   [RefsPerFrame]int8
	HP                       uint8
	SubpelFilterMode         FilterMode
	SwitchableMotionMode     uint8
	UseRefFrameMVs           uint8
	RefreshContext           uint8
	Tiling                   FrameHeaderTiling
	Quant                    FrameHeaderQuant
	Segmentation             FrameHeaderSegmentation
	Delta                    FrameHeaderDelta
	AllLossless              uint8
	LoopFilter               FrameHeaderLoopFilter
	CDEF                     FrameHeaderCDEF
	Restoration              FrameHeaderRestoration
	TxfmMode                 TxfmMode
	SwitchableCompRefs       uint8
	SkipModeAllowed          uint8
	SkipModeEnabled          uint8
	SkipModeRefs             [2]int8
	WarpMotion               uint8
	ReducedTxtpSet           uint8
	GMV                      [RefsPerFrame]WarpedMotionParams
}
