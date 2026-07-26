package entropy

import (
	"github.com/zesun96/go-av1/internal/encoder/bitwriter"
	"github.com/zesun96/go-av1/internal/tile"
	"github.com/zesun96/go-av1/internal/transform"
)

// EncodeDCT8DCAC1 encodes a TX8x8 DCT block whose only possible non-zero
// coefficients are DC and scan position 1. It is the first AC-capable subset
// of AV1 residual_coding() and mirrors decodeCoeffTokens for eob == 1.
func EncodeDCT8DCAC1(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx, fs *tile.FrameState,
	plane, bx, by, dcLevel, acLevel int,
) uint8 {
	if acLevel == 0 {
		return EncodeDCOnly(ec, ctx, fs, transform.TX8x8, plane, bx, by, 8, 8, dcLevel)
	}

	const tx = transform.TX8x8
	td := transform.TxfmDimensions[tx]
	skipCtx := fs.CoefSkipCtx(plane, bx, by, 8, 8, tx)
	ec.Bool(0, uint32(ctx.CoefSkipFull[td.Ctx][skipCtx][0]))

	// reduced_txtp_set=1: DCT_DCT is symbol 1 for small luma transforms.
	ec.Symbol(1, ctx.TxTypeIntra2CDF[int(td.Min)][tile.DCPred][:], len(tile.TxTypeIntra2Set))

	// EOB bin 1 means scan position 1 is the final non-zero coefficient.
	ec.Symbol(1, ctx.EobBin64Full[0][0][:], 7)

	acMag := absLevel(acLevel)
	acBase := min(2, acMag-1)
	ec.Symbol(uint32(acBase), ctx.EobBaseTokFull[td.Ctx][0][1][:], 3)
	acEscape := false
	acToken := acMag
	if acMag >= 3 {
		acEscape = encodeHiToken(ec, ctx.BrTokFull[td.Ctx][0][7][:], acMag)
		if acToken > 15 {
			acToken = 15
		}
	}

	dcMag := absLevel(dcLevel)
	dcBase := min(3, dcMag)
	ec.Symbol(uint32(dcBase), ctx.BaseTokFull[td.Ctx][0][0][:], 4)
	dcEscape := false
	if dcMag >= 3 {
		// The first AC token is one of the three neighbours used by the
		// decoder's DC high-token context.
		hctx := 6
		if acToken <= 12 {
			hctx = (acToken + 1) >> 1
		}
		dcEscape = encodeHiToken(ec, ctx.BrTokFull[td.Ctx][0][hctx][:], dcMag)
	}

	if dcLevel != 0 {
		signCtx := fs.DCSignCtx(plane, bx, by, tx)
		ec.Bool(boolSymbol(dcLevel < 0), uint32(ctx.DCSignCDF[0][signCtx][0]))
		if dcEscape {
			encodeGolomb(ec, dcMag-15)
		}
	}
	ec.BoolEqui(boolSymbol(acLevel < 0))
	if acEscape {
		encodeGolomb(ec, acMag-15)
	}

	cul := dcMag + acMag
	if cul > 63 {
		cul = 63
	}
	resCtx := uint8(cul)
	switch {
	case dcLevel > 0:
		resCtx |= 0x80
	case dcLevel == 0:
		resCtx |= 0x40
	}
	fs.SetCoefCtxBlock(plane, bx, by, 8, 8, resCtx)
	return resCtx
}

func absLevel(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
