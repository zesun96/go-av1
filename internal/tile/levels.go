package tile

// AV1 block-level enumeration, mirroring dav1d enum BlockLevel.
const (
	BL128X128 = iota
	BL64X64
	BL32X32
	BL16X16
	BL8X8
	NBlockLevels
)

// AV1 block-size enumeration, mirroring dav1d enum BlockSize.
// Indices match dav1d so callers can use the same symbolic constants.
const (
	BS128x128 = iota
	BS128x64
	BS64x128
	BS64x64
	BS64x32
	BS64x16
	BS32x64
	BS32x32
	BS32x16
	BS32x8
	BS16x64
	BS16x32
	BS16x16
	BS16x8
	BS16x4
	BS8x32
	BS8x16
	BS8x8
	BS8x4
	BS4x16
	BS4x8
	BS4x4
	NBlockSizes
)

// AV1 block partition enumeration, mirroring dav1d enum BlockPartition.
const (
	PartitionNone = iota
	PartitionH
	PartitionV
	PartitionSplit
	PartitionTTopSplit
	PartitionTBottomSplit
	PartitionTLeftSplit
	PartitionTRightSplit
	PartitionH4
	PartitionV4
	NPartitions
	NSub8x8Partitions = PartitionTTopSplit
)

// AV1 intra prediction mode enumeration, mirroring dav1d enum IntraPredMode.
const (
	DCPred = iota
	VertPred
	HorPred
	DiagDownLeftPred
	DiagDownRightPred
	VertRightPred
	HorDownPred
	HorUpPred
	VertLeftPred
	SmoothPred
	SmoothVPred
	SmoothHPred
	PaethPred
	NIntraPredModes
	CFLPred = NIntraPredModes
)

// Av1Block holds per-block decoding state, mirroring dav1d Av1Block.
// For M3.d (intra-only), only the intra union fields are populated.
type Av1Block struct {
	Bl       uint8 // BlockLevel
	Bs       uint8 // BlockSize
	Bp       uint8 // BlockPartition
	Intra    bool
	SegID    uint8
	SkipMode bool
	Skip     bool
	Uvtx     uint8 // (Rect)TxfmSize for UV transform

	// Intra fields (populated when Intra == true)
	YMode    uint8   // IntraPredMode for luma
	UvMode   uint8   // IntraPredMode for chroma
	Tx       uint8   // (Rect)TxfmSize for luma transform
	YAngle   int8    // directional angle for luma
	UvAngle  int8    // directional angle for chroma
	CflAlpha [2]int8 // CFL signed scale factors [U, V]

	// Inter fields (populated when Intra == false; future milestone)
}
