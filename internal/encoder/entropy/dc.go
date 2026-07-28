package entropy

import (
	"github.com/zesun96/go-av1/internal/encoder/bitwriter"
	"github.com/zesun96/go-av1/internal/tile"
	"github.com/zesun96/go-av1/internal/transform"
)

// EncodeDCOnly writes one transform block containing either no coefficients
// or a single DC coefficient. This follows SVT-AV1's
// av1_write_coeffs_txb_1d ordering:
//
//	txb_skip -> transform type (when present) -> eob -> base token -> sign
//
// It returns the coefficient neighbour context consumed by subsequent blocks.
func EncodeDCOnly(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx, fs *tile.FrameState,
	tx uint8, plane, bx, by, blockW, blockH, level int,
) uint8 {
	return encodeDCOnlyMode(ec, ctx, fs, tx, plane, bx, by, blockW, blockH, tile.DCPred, true, level)
}

func encodeDCOnlyMode(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx, fs *tile.FrameState,
	tx uint8, plane, bx, by, blockW, blockH, yMode int, intra bool, level int,
) uint8 {
	td := transform.TxfmDimensions[tx]
	skipCtx := fs.CoefSkipCtx(plane, bx, by, blockW, blockH, tx)
	ec.BoolAdapt(boolSymbol(level == 0), ctx.CoefSkipFull[td.Ctx][skipCtx][:])
	if level == 0 {
		fs.SetCoefCtxBlock(plane, bx, by, blockW, blockH, 0x40)
		return 0x40
	}

	chroma := 0
	if plane > 0 {
		chroma = 1
	}

	// reduced_txtp_set=1. TX32 and TX64 infer DCT_DCT. Small luma
	// transforms signal DCT_DCT as symbol 1 in the reduced intra set;
	// chroma DCT_DCT is inferred from the chroma prediction mode.
	inferredClass := int(td.Max)
	if intra {
		inferredClass++
	}
	if plane == 0 && inferredClass < 4 {
		if intra {
			txClass := min(2, int(td.Min))
			ec.SymbolAdaptDav1d(1, ctx.TxTypeIntra2CDF[txClass][yMode][:], len(tile.TxTypeIntra2Set)-1)
		} else {
			ec.BoolAdapt(1, ctx.TxTypeInter3CDF[min(3, int(td.Min))][:])
		}
	}

	// EOB position zero means exactly one coefficient (DC).
	switch min(6, min(3, int(td.Lw))+min(3, int(td.Lh))) {
	case 0:
		ec.SymbolAdaptDav1d(0, ctx.EobBin16Full[chroma][0][:], 4)
	case 1:
		ec.SymbolAdaptDav1d(0, ctx.EobBin32Full[chroma][0][:], 5)
	case 2:
		ec.SymbolAdaptDav1d(0, ctx.EobBin64Full[chroma][0][:], 6)
	case 3:
		ec.SymbolAdaptDav1d(0, ctx.EobBin128Full[chroma][0][:], 7)
	case 4:
		ec.SymbolAdaptDav1d(0, ctx.EobBin256Full[chroma][0][:], 8)
	case 5:
		ec.SymbolAdaptDav1d(0, ctx.EobBin512Full[chroma][:], 9)
	default:
		ec.SymbolAdaptDav1d(0, ctx.EobBin1024Full[chroma][:], 10)
	}

	magnitude := level
	if magnitude < 0 {
		magnitude = -magnitude
	}
	base := magnitude - 1
	if base > 2 {
		base = 2
	}
	ec.SymbolAdaptDav1d(uint32(base), ctx.EobBaseTokFull[td.Ctx][chroma][0][:], 2)
	escape := false
	if magnitude >= 3 {
		escape = encodeHiToken(ec, ctx.BrTokFull[min(3, int(td.Ctx))][chroma][0][:], magnitude)
	}

	signCtx := fs.DCSignCtx(plane, bx, by, tx)
	ec.BoolAdapt(boolSymbol(level < 0), ctx.DCSignCDF[chroma][signCtx][:])
	// AV1 writes Golomb residuals after coefficient signs, not adjacent to
	// the high-token escape symbol.
	if escape {
		encodeGolomb(ec, magnitude-15)
	}

	cul := magnitude
	if cul > 63 {
		cul = 63
	}
	resCtx := uint8(cul)
	if level > 0 {
		resCtx |= 0x80
	}
	fs.SetCoefCtxBlock(plane, bx, by, blockW, blockH, resCtx)
	return resCtx
}

// encodeHiToken is the inverse of bitstream.MSAC.HiTok.
func encodeHiToken(ec *bitwriter.MSACEncoder, cdf []uint16, magnitude int) bool {
	tok := magnitude
	for base := 3; base <= 12; base += 3 {
		sym := tok - base
		if sym < 0 {
			sym = 0
		}
		if sym > 3 {
			sym = 3
		}
		ec.SymbolAdaptDav1d(uint32(sym), cdf, 3)
		if sym < 3 {
			return false
		}
	}
	return tok >= 15
}

func boolSymbol(v bool) uint32 {
	if v {
		return 1
	}
	return 0
}
