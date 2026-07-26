package entropy

import (
	"github.com/zesun96/go-av1/internal/encoder/bitwriter"
	"github.com/zesun96/go-av1/internal/tile"
	"github.com/zesun96/go-av1/internal/transform"
)

// EncodeDCT8 writes a complete luma TX8x8 DCT_DCT coefficient block.
// coeff is row-major, while AV1's 2-D scan entries use packed (x<<3)|y.
// The token walk mirrors tile.decodeCoeffTokens: EOB first, then the
// remaining coefficients in reverse scan order, followed by signs and
// Golomb residuals in forward non-zero order.
func EncodeDCT8(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx, fs *tile.FrameState,
	bx, by int, coeff []int32,
) uint8 {
	return encodeDCTSquare(ec, ctx, fs, transform.TX8x8, 0, bx, by, coeff)
}

// EncodeDCT4 writes a complete TX4x4 DCT_DCT coefficient block. It is used
// for the two 4:2:0 chroma planes of an 8x8 luma coding block.
func EncodeDCT4(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx, fs *tile.FrameState,
	plane, bx, by int, coeff []int32,
) uint8 {
	return encodeDCTSquare(ec, ctx, fs, transform.TX4x4, plane, bx, by, coeff)
}

func encodeDCTSquare(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx, fs *tile.FrameState,
	tx uint8, plane, bx, by int, coeff []int32,
) uint8 {
	td := transform.TxfmDimensions[tx]
	blockSize := int(td.W) * 4
	shift := td.Lh + 2
	mask := blockSize - 1
	chroma := 0
	if plane > 0 {
		chroma = 1
	}
	scan := tile.GetScan(td.Lw, td.Lh, tile.TxClass2D)
	eob := -1
	for pos, packed := range scan {
		x, y := int(packed)>>shift, int(packed)&mask
		if coeff[y*blockSize+x] != 0 {
			eob = pos
		}
	}
	if eob < 0 {
		return EncodeDCOnly(ec, ctx, fs, tx, plane, bx, by, blockSize, blockSize, 0)
	}
	if eob == 0 {
		return EncodeDCOnly(ec, ctx, fs, tx, plane, bx, by, blockSize, blockSize, int(coeff[0]))
	}

	skipCtx := fs.CoefSkipCtx(plane, bx, by, blockSize, blockSize, tx)
	ec.Bool(0, uint32(ctx.CoefSkipFull[td.Ctx][skipCtx][0]))
	if plane == 0 {
		// reduced_txtp_set=1: DCT_DCT is symbol 1. Chroma inherits DCT_DCT
		// from its DC prediction mode and does not signal a transform type.
		ec.Symbol(1, ctx.TxTypeIntra2CDF[int(td.Min)][tile.DCPred][:], len(tile.TxTypeIntra2Set))
	}
	encodeSquareEOB(ec, ctx, td, chroma, eob)

	stride := blockSize
	levels := make([]uint8, stride*(blockSize+2))
	magnitudes := make([]int, eob+1)
	escapes := make([]bool, eob+1)

	// The EOB coefficient uses its own base-token CDF and is non-zero.
	packed := int(scan[eob])
	x, y := packed>>shift, packed&mask
	mag := absLevel(int(coeff[y*blockSize+x]))
	magnitudes[eob] = mag
	bctx := 1
	if eob > 2<<(td.Lw+td.Lh) {
		bctx++
	}
	if eob > 4<<(td.Lw+td.Lh) {
		bctx++
	}
	if bctx > 3 {
		bctx = 3
	}
	base := min(2, mag-1)
	ec.Symbol(uint32(base), ctx.EobBaseTokFull[td.Ctx][chroma][bctx][:], 3)
	tok := mag
	if mag >= 3 {
		hctx := 7
		if (x | y) > 1 {
			hctx = 14
		}
		escapes[eob] = encodeHiToken(ec, ctx.BrTokFull[td.Ctx][chroma][hctx][:], mag)
		tok = min(tok, 15)
		levels[packed] = uint8(tok + 3<<6)
	} else {
		levels[packed] = uint8(tok * 0x41)
	}

	ctxOff := &tile.DAV1DLoCtxOffsets[0]
	for pos := eob - 1; pos > 0; pos-- {
		packed = int(scan[pos])
		x, y = packed>>shift, packed&mask
		mag = absLevel(int(coeff[y*blockSize+x]))
		magnitudes[pos] = mag
		loCtx, hiMag := encoderLoCtx2D(levels, packed, stride, ctxOff, x, y)
		base = min(3, mag)
		ec.Symbol(uint32(base), ctx.BaseTokFull[td.Ctx][chroma][loCtx][:], 4)
		if mag >= 3 {
			hctx := 7
			if (x | y) > 1 {
				hctx = 14
			}
			neighbourMag := hiMag & 63
			if neighbourMag > 12 {
				hctx += 6
			} else {
				hctx += (neighbourMag + 1) >> 1
			}
			escapes[pos] = encodeHiToken(ec, ctx.BrTokFull[td.Ctx][chroma][hctx][:], mag)
			levels[packed] = uint8(min(mag, 15) + 3<<6)
		} else {
			levels[packed] = uint8(mag * 0x41)
		}
	}

	dcMag := absLevel(int(coeff[0]))
	magnitudes[0] = dcMag
	ec.Symbol(uint32(min(3, dcMag)), ctx.BaseTokFull[td.Ctx][chroma][0][:], 4)
	if dcMag >= 3 {
		neighbourMag := (int(levels[1]) + int(levels[stride]) + int(levels[stride+1])) & 63
		hctx := 6
		if neighbourMag <= 12 {
			hctx = (neighbourMag + 1) >> 1
		}
		escapes[0] = encodeHiToken(ec, ctx.BrTokFull[td.Ctx][chroma][hctx][:], dcMag)
	}

	if dcMag != 0 {
		signCtx := fs.DCSignCtx(plane, bx, by, tx)
		ec.Bool(boolSymbol(coeff[0] < 0), uint32(ctx.DCSignCDF[chroma][signCtx][0]))
		if escapes[0] {
			encodeGolomb(ec, dcMag-15)
		}
	}
	cul := dcMag
	for pos := 1; pos <= eob; pos++ {
		mag = magnitudes[pos]
		if mag == 0 {
			continue
		}
		packed = int(scan[pos])
		x, y = packed>>shift, packed&mask
		ec.BoolEqui(boolSymbol(coeff[y*blockSize+x] < 0))
		if escapes[pos] {
			encodeGolomb(ec, mag-15)
		}
		cul += mag
	}

	if cul > 63 {
		cul = 63
	}
	resCtx := uint8(cul)
	switch {
	case coeff[0] > 0:
		resCtx |= 0x80
	case coeff[0] == 0:
		resCtx |= 0x40
	}
	fs.SetCoefCtxBlock(plane, bx, by, blockSize, blockSize, resCtx)
	return resCtx
}

func encodeSquareEOB(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx, td transform.TxfmDim, chroma, eob int) {
	bin := eob
	if eob > 1 {
		bin = 1
		for value := eob; value > 1; value >>= 1 {
			bin++
		}
	}
	if td.Lw == 0 {
		ec.Symbol(uint32(bin), ctx.EobBin16Full[chroma][0][:], 5)
	} else {
		ec.Symbol(uint32(bin), ctx.EobBin64Full[chroma][0][:], 7)
	}
	if bin <= 1 {
		return
	}
	extraBits := bin - 2
	hi := (eob >> uint(extraBits)) & 1
	ec.Bool(uint32(hi), uint32(ctx.EobHiBitFull[td.Ctx][chroma][extraBits][0]))
	if extraBits > 0 {
		ec.Bools(uint32(eob&((1<<uint(extraBits))-1)), extraBits)
	}
}

func encoderLoCtx2D(levels []uint8, base, stride int, ctxOff *[5][5]uint8, x, y int) (int, int) {
	mag := int(levels[base+1]) + int(levels[base+stride]) + int(levels[base+stride+1])
	hiMag := mag
	mag += int(levels[base+2]) + int(levels[base+2*stride])
	x = min(x, 4)
	y = min(y, 4)
	add := 4
	if mag <= 512 {
		add = (mag + 64) >> 7
	}
	return int(ctxOff[y][x]) + add, hiMag
}
