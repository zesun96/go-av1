// Package tile provides AV1 tile-level CABAC decoding.
// cdfs.go holds the default (initial) CDF tables used to seed per-tile
// context state.
//
// CDF storage format (dav1d convention):
//
//	For n symbols, the array has n elements (or n+1 for SymbolAdapt):
//	  cdf[0..n-2] = 32768 - cumulative_probability  (descending)
//	  cdf[n-1]    = 0  (sentinel for the last symbol; doubles as counter
//	                     initial value when used with SymbolAdapt)
//
// All values below are pre-converted (32768 - dav1d_source_value).
package tile

// ---------------------------------------------------------------------------
// Partition CDFs
//
// AV1 uses different partition sets per block level.
//   BL_128x128 → 8 symbols (NONE/H/V/SPLIT + T_TOP/T_BOT/T_LEFT/T_RIGHT)
//   BL_64x64   → 10 symbols (above + H4/V4)
//   BL_32x32   → 10 symbols
//   BL_16x16   → 10 symbols
//   BL_8x8     →  4 symbols (NONE/H/V/SPLIT only)
//
// We store one representative context (ctx=0) for each level.
// Array sizes = nSymbols + 1 (SymbolAdapt counter slot).
// ---------------------------------------------------------------------------

// DefaultPartition128CDF: 8 symbols, ctx=0.
// Source: dav1d partition[0][0] CDF7(27899,28219,28529,32484,32539,32619,32639)
// After 32768-x: {869,549,239,284,229,149,129,0}
var DefaultPartition128CDF = [9]uint16{
	869, 549, 239, 284, 229, 149, 129, 0, 0,
}

// DefaultPartition64CDF: 10 symbols, ctx=0.
// Source: dav1d partition[1][0] CDF9(20137,21547,23078,29566,29837,30261,30524,30892,31724)
// After 32768-x: {12631,11221,9690,3202,2931,2507,2244,1876,1044,0}
var DefaultPartition64CDF = [11]uint16{
	12631, 11221, 9690, 3202, 2931, 2507, 2244, 1876, 1044, 0, 0,
}

// DefaultPartition32CDF: 10 symbols, ctx=0.
// Source: dav1d partition[2][0] CDF9(18462,20920,23124,27647,28227,29049,29519,30178,31544)
// After 32768-x: {14306,11848,9644,5121,4541,3719,3249,2590,1224,0}
var DefaultPartition32CDF = [11]uint16{
	14306, 11848, 9644, 5121, 4541, 3719, 3249, 2590, 1224, 0, 0,
}

// DefaultPartition16CDF: 10 symbols, ctx=0.
// Source: dav1d partition[3][0] CDF9(15597,20929,24571,26706,27664,28821,29601,30571,31902)
// After 32768-x: {17171,11839,8197,6062,5104,3947,3167,2197,866,0}
var DefaultPartition16CDF = [11]uint16{
	17171, 11839, 8197, 6062, 5104, 3947, 3167, 2197, 866, 0, 0,
}

// DefaultPartition8CDF: 4 symbols (NONE/H/V/SPLIT only), ctx=0.
// Source: dav1d partition[4][0] CDF3(19132,25510,30392)
// After 32768-x: {13636,7258,2376,0}
var DefaultPartition8CDF = [5]uint16{
	13636, 7258, 2376, 0, 0,
}

// ---------------------------------------------------------------------------
// Intra luma mode CDF (13 symbols)
// Source: dav1d kfym tables (averaged), converted with 32768-x.
// We use a uniform flat prior: 32768/13 ≈ 2520 step.
// dav1d real values from m.y_mode for KEY_FRAME, ctx (0,0):
// CDF12(22801,23489,24293,24756,25601,26123,26606,27418,27945,29228,29885,30397)
// After 32768-x: {9967,9279,8475,8012,7167,6645,6162,5350,4823,3540,2883,2371,0}
// ---------------------------------------------------------------------------

var DefaultIntraYModeCDF = [NIntraPredModes + 1]uint16{
	9967, 9279, 8475, 8012, 7167, 6645, 6162, 5350, 4823, 3540, 2883, 2371, 0, 0,
}

// ---------------------------------------------------------------------------
// Intra UV mode CDF (14 symbols, includes CFL at index 13)
// Source: dav1d uv_mode luma_mode=DC_PRED: CDF13(...)
// Using uniform prior for M7 simplicity.
// ---------------------------------------------------------------------------

var DefaultIntraUVModeCDF = [NIntraPredModes + 2]uint16{
	21845, 19661, 17477, 15293, 13107, 10923, 8739, 6554, 4370, 3276, 2185, 1638, 820, 0, 0,
}

// ---------------------------------------------------------------------------
// Skip CDF  [3 contexts][2 symbols + counter]
// Source: dav1d skip[0..2]: CDF1(31671), CDF1(16515), CDF1(4576)
// After 32768-x: {1097,0}, {16253,0}, {28192,0}
// ---------------------------------------------------------------------------

var DefaultSkipCDF = [3][3]uint16{
	{1097, 0, 0},
	{16253, 0, 0},
	{28192, 0, 0},
}

var DefaultIntraCDF = [4][2]uint16{
	{31962, 0},
	{16106, 0},
	{12582, 0},
	{6230, 0},
}

var DefaultIntrabcCDF = [2]uint16{2237, 0}

// ---------------------------------------------------------------------------
// Transform type CDFs (intra luma)
//
// dav1d uses two sets:
//   txtp_intra2: 4 symbols → used when reduced_txtp_set=1 OR tx_min==TX_16X16
//     maps to dav1d_tx_types_per_set[0..4] = {IDTX,DCT_DCT,ADST_ADST,ADST_DCT,DCT_ADST}
//   txtp_intra1: 6 symbols → used when reduced_txtp_set=0 AND tx_min<TX_16X16
//     maps to dav1d_tx_types_per_set[5..11] = {IDTX,DCT_DCT,V_DCT,H_DCT,ADST_ADST,ADST_DCT,DCT_ADST}
//
// For M7 we use a single-context (y_mode=DC) approximation from dav1d's
// txtp_intra2[0][DC_PRED] and txtp_intra1[0][DC_PRED].
//
// dav1d txtp_intra2[TX_4X4][DC_PRED] = CDF3(1392,2500,3879)
//   → 32768-x: {31376,30268,28889,0}
// dav1d txtp_intra1[TX_4X4][DC_PRED] = CDF5(770,2421,5225,12907,20194)
//   → 32768-x: {31998,30347,27543,19861,12574,0}
// ---------------------------------------------------------------------------

const TxTypeIntra2Symbols = 5 // reduced set or TX16
const TxTypeIntra1Symbols = 7 // full set for TX4/TX8
const TxTypeInter2Symbols = 12
const TxTypeInter1Symbols = 16

var DefaultTxTypeIntra2CDF = [TxTypeIntra2Symbols + 1]uint16{
	31376, 30268, 28889, 27648, 0, 0,
}

var DefaultTxTypeIntra1CDF = [TxTypeIntra1Symbols + 1]uint16{
	31998, 30347, 27543, 19861, 12574, 8192, 0, 0,
}

var DefaultTxTypeInter1CDF = [2][TxTypeInter1Symbols + 1]uint16{
	{28310, 27208, 25073, 23059, 19438, 17979, 15231, 12502, 11264, 9920, 8834, 7294, 5041, 3853, 2137, 0, 0},
	{31123, 30195, 27990, 27057, 24961, 24146, 22246, 17411, 15094, 12360, 10251, 7758, 5652, 3912, 2019, 0, 0},
}

var DefaultTxTypeInter2CDF = [TxTypeInter2Symbols + 1]uint16{
	31998, 30347, 27543, 19861, 16949, 13841, 11207, 8679, 6173, 4242, 2239, 0, 0,
}

var DefaultTxTypeInter3CDF = [4][2]uint16{
	{16384, 0},
	{28601, 0},
	{30770, 0},
	{32020, 0},
}

// TxTypeIntra2Set maps the 4-symbol reduced intra tx type index to TxfmType.
// Source: dav1d_tx_types_per_set[0..4]
// {IDTX=9, DCT_DCT=0, ADST_ADST=3, ADST_DCT=1, DCT_ADST=2}
var TxTypeIntra2Set = [TxTypeIntra2Symbols]uint8{9, 0, 3, 1, 2}

// TxTypeIntra1Set maps the 6-symbol full intra tx type index to TxfmType.
// Source: dav1d_tx_types_per_set[5..10]
// {IDTX=9, DCT_DCT=0, V_DCT=10, H_DCT=11, ADST_ADST=3, ADST_DCT=1}
var TxTypeIntra1Set = [TxTypeIntra1Symbols]uint8{9, 0, 10, 11, 3, 1, 2}

// TxTypeInter2Set maps dav1d_tx_types_per_set[12..23].
var TxTypeInter2Set = [TxTypeInter2Symbols]uint8{
	9, 10, 11, 0, 1, 2, 4, 5, 3, 6, 7, 8,
}

// TxTypeInter1Set maps dav1d_tx_types_per_set[24..39].
var TxTypeInter1Set = [TxTypeInter1Symbols]uint8{
	9, 10, 11, 12, 13, 14, 15, 0, 1, 2, 4, 5, 3, 6, 7, 8,
}

// TxtpFromUVMode maps intra UV prediction mode → txtp (luma-derived).
// Source: dav1d_txtp_from_uvmode[N_UV_INTRA_PRED_MODES]
// Values: DCT_DCT=0, ADST_DCT=1, DCT_ADST=2, ADST_ADST=3
var TxtpFromUVMode = [13]uint8{
	0, // DC_PRED → DCT_DCT
	1, // VERT_PRED → ADST_DCT
	2, // HOR_PRED → DCT_ADST
	0, // DIAG_DOWN_LEFT → DCT_DCT
	3, // DIAG_DOWN_RIGHT → ADST_ADST
	1, // VERT_RIGHT → ADST_DCT
	2, // HOR_DOWN → DCT_ADST
	2, // HOR_UP → DCT_ADST
	1, // VERT_LEFT → ADST_DCT
	3, // SMOOTH → ADST_ADST
	1, // SMOOTH_V → ADST_DCT
	2, // SMOOTH_H → DCT_ADST
	3, // PAETH → ADST_ADST
}

// ---------------------------------------------------------------------------
// EOB point CDFs
// Format: 32768 - dav1d_value, descending, last is 0, then counter.
//
//	class 0 (TX4x4):   2 symbols → [3]uint16
//	class 1 (TX8x8):   3 symbols → [4]uint16
//	class 2 (TX16x16): 5 symbols → [6]uint16
//	class 3 (TX32x32): 7 symbols → [8]uint16
//	class 4 (TX64x64): 9 symbols → [10]uint16
//
// Source: dav1d eob_bin_* tables ctx=0 (luma).
// ---------------------------------------------------------------------------

// TX4x4: 2 symbols. dav1d: CDF1(17080) luma, CDF1(10923) chroma.
var DefaultEobPtCDF_4 = [2][3]uint16{
	{15688, 0, 0}, // luma:   32768-17080
	{21845, 0, 0}, // chroma: 32768-10923
}

// TX8x8: 3 symbols. dav1d: CDF2(18240,27979) / CDF2(10923,21845).
var DefaultEobPtCDF_8 = [2][4]uint16{
	{14528, 4789, 0, 0},  // luma
	{21845, 10923, 0, 0}, // chroma
}

// TX16x16: 5 symbols. dav1d: CDF4(14383,22218,27647,30708) / CDF4(10923,21845,27307,30726).
var DefaultEobPtCDF_16 = [2][6]uint16{
	{18385, 10550, 5121, 2060, 0, 0}, // luma
	{21845, 10923, 5461, 2042, 0, 0}, // chroma
}

// TX32x32: 7 symbols. dav1d: CDF6(11239,18899,24323,27874,30400,31674) / CDF6(...).
var DefaultEobPtCDF_32 = [2][8]uint16{
	{21529, 13869, 8445, 4894, 2368, 1094, 0, 0}, // luma
	{21845, 10923, 5461, 2042, 819, 0, 0, 0},     // chroma
}

// TX64x64: 9 symbols. dav1d: CDF8(8761,15395,20925,24519,27433,29413,30816,31862).
var DefaultEobPtCDF_64 = [2][10]uint16{
	{24007, 17373, 11843, 8249, 5335, 3355, 1952, 906, 0, 0}, // luma
	{21845, 10923, 5461, 2042, 819, 200, 0, 0, 0, 0},         // chroma
}

// ---------------------------------------------------------------------------
// Coefficient base token CDF  [4 contexts][4 symbols + counter]
// Source: dav1d coef_base, uniform prior.
// For M7 we use a flat prior: roughly equal probability for 0/1/2/3+.
// dav1d values (luma, eob_ctx 0): CDF3(16138,22223,27797)
// After 32768-x: {16630,10545,4971,0}
// ---------------------------------------------------------------------------

var DefaultCoeffBaseCDF = [4][5]uint16{
	{16630, 10545, 4971, 0, 0},
	{21845, 10923, 5461, 0, 0},
	{26214, 16384, 8192, 0, 0},
	{28399, 19661, 10923, 0, 0},
}

// DefaultCoeffBaseEobCDF  [2 contexts][3 symbols + counter]
// dav1d coef_base_eob, uniform: {21845,10923,0}
var DefaultCoeffBaseEobCDF = [2][4]uint16{
	{10923, 3641, 0, 0},
	{21845, 10923, 0, 0},
}

// ---------------------------------------------------------------------------
// Coefficient high (br) token CDF  [4 contexts][4 symbols + counter]
// dav1d coef_br luma ctx0: CDF3(29900,31872,32678)
// After 32768-x: {2868,896,90,0}
// ---------------------------------------------------------------------------

var DefaultCoeffBrCDF = [4][5]uint16{
	{2868, 896, 90, 0, 0},
	{8192, 3277, 410, 0, 0},
	{16384, 8192, 2048, 0, 0},
	{21845, 10923, 3641, 0, 0},
}

// ---------------------------------------------------------------------------
// DC sign CDF  [3 contexts][2 symbols + counter]
// dav1d dc_sign ctx0: CDF1(16000). After 32768-x: {16768,0}
// ---------------------------------------------------------------------------

var DefaultDCSignCDF = [3][3]uint16{
	{16768, 0, 0},
	{16768, 0, 0},
	{16768, 0, 0},
}
