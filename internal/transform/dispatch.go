package transform

// AV1 transform-size enumeration. Indices match dav1d enum TxfmSize so
// that callers porting from the C reference can use the same symbolic
// constants.
const (
	TX4x4 = iota
	TX8x8
	TX16x16
	TX32x32
	TX64x64
	NTxSizes
)

// AV1 rectangular transform-size enumeration. Indices match dav1d enum
// RectTxfmSize. The square sizes share indices 0..4 with TxfmSize above;
// the rectangular sizes start at NTxSizes.
const (
	RTX4x8 = NTxSizes + iota
	RTX8x4
	RTX8x16
	RTX16x8
	RTX16x32
	RTX32x16
	RTX32x64
	RTX64x32
	RTX4x16
	RTX16x4
	RTX8x32
	RTX32x8
	RTX16x64
	RTX64x16
	NRectTxSizes
)

// AV1 1D transform-type enumeration. Indices match dav1d enum Tx1dType.
const (
	Tx1dDCT = iota
	Tx1dADST
	Tx1dFLIPADST
	Tx1dIDENTITY
	NTx1dTypes
)

// AV1 2D transform-type enumeration. Indices match dav1d enum TxfmType.
const (
	DCT_DCT = iota
	ADST_DCT
	DCT_ADST
	ADST_ADST
	FLIPADST_DCT
	DCT_FLIPADST
	FLIPADST_FLIPADST
	ADST_FLIPADST
	FLIPADST_ADST
	IDTX
	V_DCT
	H_DCT
	V_ADST
	H_ADST
	V_FLIPADST
	H_FLIPADST
	NTxTypes
	WHT_WHT = NTxTypes
)

// Itx1dFn is the unified signature shared by every 1D inverse kernel.
// Identity kernels ignore min/max but still receive them so the table
// can be uniformly indexed.
type Itx1dFn func(c []int32, stride, min, max int)

// Tx1dFns dispatches a (size, 1D type) pair to its kernel. Entries
// missing from dav1d_tx1d_fns (e.g. ADST/FLIPADST at 32×32 and 64×64,
// or IDENTITY at 64×64) are nil; callers must not invoke them.
//
// Mirrors dav1d_tx1d_fns from dav1d/src/itx_1d.c lines 1019-1041.
var Tx1dFns = [NTxSizes][NTx1dTypes]Itx1dFn{
	TX4x4: {
		Tx1dDCT:      InvDCT4,
		Tx1dADST:     InvADST4,
		Tx1dFLIPADST: InvFlipADST4,
		Tx1dIDENTITY: InvIdentity4,
	},
	TX8x8: {
		Tx1dDCT:      InvDCT8,
		Tx1dADST:     InvADST8,
		Tx1dFLIPADST: InvFlipADST8,
		Tx1dIDENTITY: InvIdentity8,
	},
	TX16x16: {
		Tx1dDCT:      InvDCT16,
		Tx1dADST:     InvADST16,
		Tx1dFLIPADST: InvFlipADST16,
		Tx1dIDENTITY: InvIdentity16,
	},
	TX32x32: {
		Tx1dDCT:      InvDCT32,
		Tx1dIDENTITY: InvIdentity32,
	},
	TX64x64: {
		Tx1dDCT: InvDCT64,
	},
}

// Tx1dTypes maps a 2D transform type to (col_1d_type, row_1d_type),
// mirroring dav1d_tx1d_types from dav1d/src/itx_1d.c lines 1043-1060.
//
// Convention: index [TxfmType][0] is the column transform, [1] is the
// row transform. dav1d applies the row transform first, then the
// column transform; consumers should follow the same order.
var Tx1dTypes = [NTxTypes][2]uint8{
	DCT_DCT:           {Tx1dDCT, Tx1dDCT},
	ADST_DCT:          {Tx1dADST, Tx1dDCT},
	DCT_ADST:          {Tx1dDCT, Tx1dADST},
	ADST_ADST:         {Tx1dADST, Tx1dADST},
	FLIPADST_DCT:      {Tx1dFLIPADST, Tx1dDCT},
	DCT_FLIPADST:      {Tx1dDCT, Tx1dFLIPADST},
	FLIPADST_FLIPADST: {Tx1dFLIPADST, Tx1dFLIPADST},
	ADST_FLIPADST:     {Tx1dADST, Tx1dFLIPADST},
	FLIPADST_ADST:     {Tx1dFLIPADST, Tx1dADST},
	IDTX:              {Tx1dIDENTITY, Tx1dIDENTITY},
	V_DCT:             {Tx1dDCT, Tx1dIDENTITY},
	H_DCT:             {Tx1dIDENTITY, Tx1dDCT},
	V_ADST:            {Tx1dADST, Tx1dIDENTITY},
	H_ADST:            {Tx1dIDENTITY, Tx1dADST},
	V_FLIPADST:        {Tx1dFLIPADST, Tx1dIDENTITY},
	H_FLIPADST:        {Tx1dIDENTITY, Tx1dFLIPADST},
}

// TxfmDim holds dimension metadata for a transform size, mirroring
// dav1d TxfmInfo. Width and height are expressed in units of 4 pixels
// (w=1 → 4px, w=2 → 8px, etc.). Lw/Lh are log2(w). Min/Max are
// the min/max of lw/lh. Sub is the next smaller square/rect size.
// Ctx is the context index used in coefficient coding.
type TxfmDim struct {
	W, H     uint8 // width/height in 4px units
	Lw, Lh   uint8 // log2 of w/h
	Min, Max uint8 // min/max of lw/lh
	Sub      uint8 // next smaller (Rect)TxfmSize
	Ctx      uint8 // context index for coefficient coding
}

// TxfmDimensions mirrors dav1d_txfm_dimensions from dav1d/src/tables.c.
// Indexed by (Rect)TxfmSize constant (TX4x4..TX64x64, RTX4x8..RTX64x16).
var TxfmDimensions = [NRectTxSizes]TxfmDim{
	TX4x4:    {W: 1, H: 1, Lw: 0, Lh: 0, Min: 0, Max: 0, Sub: 0, Ctx: 0},
	TX8x8:    {W: 2, H: 2, Lw: 1, Lh: 1, Min: 1, Max: 1, Sub: TX4x4, Ctx: 1},
	TX16x16:  {W: 4, H: 4, Lw: 2, Lh: 2, Min: 2, Max: 2, Sub: TX8x8, Ctx: 2},
	TX32x32:  {W: 8, H: 8, Lw: 3, Lh: 3, Min: 3, Max: 3, Sub: TX16x16, Ctx: 3},
	TX64x64:  {W: 16, H: 16, Lw: 4, Lh: 4, Min: 4, Max: 4, Sub: TX32x32, Ctx: 4},
	RTX4x8:   {W: 1, H: 2, Lw: 0, Lh: 1, Min: 0, Max: 1, Sub: TX4x4, Ctx: 1},
	RTX8x4:   {W: 2, H: 1, Lw: 1, Lh: 0, Min: 0, Max: 1, Sub: TX4x4, Ctx: 1},
	RTX8x16:  {W: 2, H: 4, Lw: 1, Lh: 2, Min: 1, Max: 2, Sub: TX8x8, Ctx: 2},
	RTX16x8:  {W: 4, H: 2, Lw: 2, Lh: 1, Min: 1, Max: 2, Sub: TX8x8, Ctx: 2},
	RTX16x32: {W: 4, H: 8, Lw: 2, Lh: 3, Min: 2, Max: 3, Sub: TX16x16, Ctx: 3},
	RTX32x16: {W: 8, H: 4, Lw: 3, Lh: 2, Min: 2, Max: 3, Sub: TX16x16, Ctx: 3},
	RTX32x64: {W: 8, H: 16, Lw: 3, Lh: 4, Min: 3, Max: 4, Sub: TX32x32, Ctx: 4},
	RTX64x32: {W: 16, H: 8, Lw: 4, Lh: 3, Min: 3, Max: 4, Sub: TX32x32, Ctx: 4},
	RTX4x16:  {W: 1, H: 4, Lw: 0, Lh: 2, Min: 0, Max: 2, Sub: RTX4x8, Ctx: 1},
	RTX16x4:  {W: 4, H: 1, Lw: 2, Lh: 0, Min: 0, Max: 2, Sub: RTX8x4, Ctx: 1},
	RTX8x32:  {W: 2, H: 8, Lw: 1, Lh: 3, Min: 1, Max: 3, Sub: RTX8x16, Ctx: 2},
	RTX32x8:  {W: 8, H: 2, Lw: 3, Lh: 1, Min: 1, Max: 3, Sub: RTX16x8, Ctx: 2},
	RTX16x64: {W: 4, H: 16, Lw: 2, Lh: 4, Min: 2, Max: 4, Sub: RTX16x32, Ctx: 3},
	RTX64x16: {W: 16, H: 4, Lw: 4, Lh: 2, Min: 2, Max: 4, Sub: RTX32x16, Ctx: 3},
}
