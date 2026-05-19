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
		Tx1dIDENTITY: InvIdentity32,
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
