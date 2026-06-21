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

var alPartCtx = [2][NBlockLevels][NPartitions]uint8{
	{
		{0x00, 0x00, 0x10, 0xff, 0x00, 0x10, 0x10, 0x10, 0xff, 0xff},
		{0x10, 0x10, 0x18, 0xff, 0x10, 0x18, 0x18, 0x18, 0x10, 0x1c},
		{0x18, 0x18, 0x1c, 0xff, 0x18, 0x1c, 0x1c, 0x1c, 0x18, 0x1e},
		{0x1c, 0x1c, 0x1e, 0xff, 0x1c, 0x1e, 0x1e, 0x1e, 0x1c, 0x1f},
		{0x1e, 0x1e, 0x1f, 0x1f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	},
	{
		{0x00, 0x10, 0x00, 0xff, 0x10, 0x10, 0x00, 0x10, 0xff, 0xff},
		{0x10, 0x18, 0x10, 0xff, 0x18, 0x18, 0x10, 0x18, 0x1c, 0x10},
		{0x18, 0x1c, 0x18, 0xff, 0x1c, 0x1c, 0x18, 0x1c, 0x1e, 0x18},
		{0x1c, 0x1e, 0x1c, 0xff, 0x1e, 0x1e, 0x1c, 0x1e, 0x1f, 0x1c},
		{0x1e, 0x1f, 0x1e, 0x1f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	},
}

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

var FilterModeToYMode = [5]uint8{
	DCPred,
	VertPred,
	HorPred,
	HorDownPred,
	DCPred,
}

const (
	InterModeZeroMV = iota
	InterModeGlobalMV
	InterModeNearestMV
	InterModeNearMV
	InterModeRefMV
	InterModeNewMV
)

// Av1Block holds per-block decoding state, mirroring dav1d Av1Block.
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
	YMode    uint8 // IntraPredMode for luma
	UvMode   uint8 // IntraPredMode for chroma
	PalSz    [2]uint8
	Tx       uint8   // (Rect)TxfmSize for luma transform
	MaxYTx   uint8   // largest luma transform allowed by block size
	TxSplit0 uint8   // var-tx split mask depth 0
	TxSplit1 uint16  // var-tx split mask depth 1
	YAngle   int8    // directional angle for luma
	UvAngle  int8    // directional angle for chroma
	CflAlpha [2]int8 // CFL signed scale factors [U, V]

	// Inter fields (populated when Intra == false; future milestone)
	InterMode uint8
	RefSlot   int8
	Filter    uint8
	FilterV   uint8
	BaseMV    [2]int16 // [Y, X] in 1/8-pel
	DeltaMV   [2]int16 // [Y, X] in 1/8-pel
	MV        [2]int16 // [Y, X] in 1/8-pel
}

// bsizeFromDim maps a (bw, bh) pair in luma pixel units to the BlockSize
// enum index (BS128x128 .. BS4x4). Returns -1 if the dimensions do not
// match a valid AV1 block size.
func bsizeFromDim(bw, bh int) int {
	switch {
	case bw == 128 && bh == 128:
		return BS128x128
	case bw == 128 && bh == 64:
		return BS128x64
	case bw == 64 && bh == 128:
		return BS64x128
	case bw == 64 && bh == 64:
		return BS64x64
	case bw == 64 && bh == 32:
		return BS64x32
	case bw == 64 && bh == 16:
		return BS64x16
	case bw == 32 && bh == 64:
		return BS32x64
	case bw == 32 && bh == 32:
		return BS32x32
	case bw == 32 && bh == 16:
		return BS32x16
	case bw == 32 && bh == 8:
		return BS32x8
	case bw == 16 && bh == 64:
		return BS16x64
	case bw == 16 && bh == 32:
		return BS16x32
	case bw == 16 && bh == 16:
		return BS16x16
	case bw == 16 && bh == 8:
		return BS16x8
	case bw == 16 && bh == 4:
		return BS16x4
	case bw == 8 && bh == 32:
		return BS8x32
	case bw == 8 && bh == 16:
		return BS8x16
	case bw == 8 && bh == 8:
		return BS8x8
	case bw == 8 && bh == 4:
		return BS8x4
	case bw == 4 && bh == 16:
		return BS4x16
	case bw == 4 && bh == 8:
		return BS4x8
	case bw == 4 && bh == 4:
		return BS4x4
	}
	return -1
}

func blockLevelFromDim(bw, bh int) int {
	d := bw
	if bh > d {
		d = bh
	}
	switch {
	case d >= 128:
		return BL128X128
	case d >= 64:
		return BL64X64
	case d >= 32:
		return BL32X32
	case d >= 16:
		return BL16X16
	default:
		return BL8X8
	}
}

// palSzCtx returns dav1d's sz_ctx = log2(bw4) + log2(bh4) - 2 used by
// pal_y / pal_sz CDFs. Caller must guarantee bw,bh >= 4 (i.e. bw4,bh4 >= 1).
// The result is clamped to [0,6].
func palSzCtx(bw, bh int) int {
	log2 := func(v int) int {
		n := 0
		for v > 1 {
			v >>= 1
			n++
		}
		return n
	}
	bw4 := bw >> 2
	bh4 := bh >> 2
	if bw4 < 1 {
		bw4 = 1
	}
	if bh4 < 1 {
		bh4 = 1
	}
	c := log2(bw4) + log2(bh4) - 2
	if c < 0 {
		return 0
	}
	if c > 6 {
		return 6
	}
	return c
}

func angleDeltaAllowed(bw, bh int) bool {
	log2 := func(v int) int {
		n := 0
		for v > 1 {
			v >>= 1
			n++
		}
		return n
	}
	bw4 := bw >> 2
	bh4 := bh >> 2
	if bw4 < 1 {
		bw4 = 1
	}
	if bh4 < 1 {
		bh4 = 1
	}
	return log2(bw4)+log2(bh4) >= 2
}
