package tile

// TileCtx holds all adaptive CDF state for one tile.
// It is initialised from the default tables in cdfs.go and discarded after
// the tile is decoded (no cross-tile CDF propagation in M7).
type TileCtx struct {
	// Partition CDFs: different block levels use different symbol counts.
	//   Partition128CDF: 8 symbols (BL_128x128)
	//   Partition64CDF:  10 symbols (BL_64x64)
	//   Partition32CDF:  10 symbols (BL_32x32)
	//   Partition16CDF:  10 symbols (BL_16x16)
	//   Partition8CDF:    4 symbols (BL_8x8, NONE/H/V/SPLIT only)
	Partition128CDF [9]uint16
	Partition64CDF  [11]uint16
	Partition32CDF  [11]uint16
	Partition16CDF  [11]uint16
	Partition8CDF   [5]uint16

	// Intra prediction mode CDFs.
	IntraYModeCDF  [NIntraPredModes + 1]uint16 // 13 symbols + counter
	IntraUVModeCDF [NIntraPredModes + 2]uint16 // 14 symbols (incl. CFL) + counter

	// Skip CDF: 3 contexts × 2 symbols + counter.
	SkipCDF [3][3]uint16

	// Transform type CDFs (intra luma).
	// TxTypeIntra2CDF: 4 symbols, used when reduced_txtp_set=1 or tx_min>=TX_16X16.
	// TxTypeIntra1CDF: 6 symbols, used when reduced_txtp_set=0 and tx_min<TX_16X16.
	TxTypeIntra2CDF [TxTypeIntra2Symbols + 1]uint16
	TxTypeIntra1CDF [TxTypeIntra1Symbols + 1]uint16

	// EOB point CDFs per tx size class (0=4x4 … 4=64x64), 2 luma/chroma ctx.
	EobPtCDF4  [2][3]uint16
	EobPtCDF8  [2][4]uint16
	EobPtCDF16 [2][6]uint16
	EobPtCDF32 [2][8]uint16
	EobPtCDF64 [2][10]uint16

	// Coefficient token CDFs.
	CoeffBaseCDF    [4][5]uint16
	CoeffBaseEobCDF [2][4]uint16
	CoeffBrCDF      [4][5]uint16

	// DC sign CDF [3 ctx][2 symbols + counter].
	DCSignCDF [3][3]uint16
}

// NewTileCtx allocates a TileCtx and copies the default CDF values into it.
func NewTileCtx() *TileCtx {
	ctx := &TileCtx{}
	ctx.Partition128CDF = DefaultPartition128CDF
	ctx.Partition64CDF = DefaultPartition64CDF
	ctx.Partition32CDF = DefaultPartition32CDF
	ctx.Partition16CDF = DefaultPartition16CDF
	ctx.Partition8CDF = DefaultPartition8CDF
	ctx.IntraYModeCDF = DefaultIntraYModeCDF
	ctx.IntraUVModeCDF = DefaultIntraUVModeCDF
	ctx.SkipCDF = DefaultSkipCDF
	ctx.TxTypeIntra2CDF = DefaultTxTypeIntra2CDF
	ctx.TxTypeIntra1CDF = DefaultTxTypeIntra1CDF
	ctx.EobPtCDF4 = DefaultEobPtCDF_4
	ctx.EobPtCDF8 = DefaultEobPtCDF_8
	ctx.EobPtCDF16 = DefaultEobPtCDF_16
	ctx.EobPtCDF32 = DefaultEobPtCDF_32
	ctx.EobPtCDF64 = DefaultEobPtCDF_64
	ctx.CoeffBaseCDF = DefaultCoeffBaseCDF
	ctx.CoeffBaseEobCDF = DefaultCoeffBaseEobCDF
	ctx.CoeffBrCDF = DefaultCoeffBrCDF
	ctx.DCSignCDF = DefaultDCSignCDF
	return ctx
}
