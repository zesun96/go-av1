// Package core implements the single-tile AV1 key-frame encoding loop.
package core

import (
	"github.com/zesun96/go-av1/internal/encoder/bitwriter"
	"github.com/zesun96/go-av1/internal/encoder/entropy"
	"github.com/zesun96/go-av1/internal/encoder/obuwriter"
	encodertx "github.com/zesun96/go-av1/internal/encoder/tx"
	"github.com/zesun96/go-av1/internal/tile"
	"github.com/zesun96/go-av1/internal/transform"
)

// FrameEncoder encodes a single frame.
type FrameEncoder struct {
	Width    int
	Height   int
	QIndex   int
	BitDepth int
}

// EncodeFrame returns one complete AV1 temporal unit.
func (fe *FrameEncoder) EncodeFrame(yPlane, cbPlane, crPlane []byte, frameNum int) []byte {
	ec := bitwriter.NewMSACEncoder(max(64, fe.Width*fe.Height/64))
	fe.encodeKeyTile(ec, yPlane, cbPlane, crPlane)
	tileData := ec.Flush()

	seqParams := &obuwriter.SeqParams{
		Width:    fe.Width,
		Height:   fe.Height,
		BitDepth: 8,
		ChromaSS: 1,
		Use128SB: true,
	}
	return obuwriter.BuildTemporalUnit(seqParams, fe.QIndex, tileData, true)
}

type tileEncodeState struct {
	src   [3][]byte
	recon [3][]byte
	w     [3]int
	h     [3]int
}

// encodeKeyTile writes the syntax consumed by decode_partition() and
// write_modes_b() in SVT-AV1: partition, skip, key-frame Y mode and UV mode.
func (fe *FrameEncoder) encodeKeyTile(ec *bitwriter.MSACEncoder, y, u, v []byte) {
	ctx := tile.NewTileCtxForQIdx(fe.QIndex)
	fs := tile.NewFrameState(fe.Width, fe.Height)
	fs.SetSubsampling(1, 1)
	cw, ch := (fe.Width+1)/2, (fe.Height+1)/2
	st := &tileEncodeState{
		src: [3][]byte{y, u, v},
		w:   [3]int{fe.Width, cw, cw},
		h:   [3]int{fe.Height, ch, ch},
	}
	for plane := range st.recon {
		st.recon[plane] = make([]byte, st.w[plane]*st.h[plane])
		for i := range st.recon[plane] {
			st.recon[plane][i] = 128
		}
	}

	codedW := (fe.Width + 7) &^ 7
	codedH := (fe.Height + 7) &^ 7
	for by := 0; by < codedH; by += 128 {
		for bx := 0; bx < codedW; bx += 128 {
			fe.encodePartition(ec, ctx, fs, st, bx, by, tile.BL128X128)
		}
	}
}

func (fe *FrameEncoder) encodePartition(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx,
	fs *tile.FrameState, st *tileEncodeState, bx, by, bl int,
) {
	codedW := (fe.Width + 7) &^ 7
	codedH := (fe.Height + 7) &^ 7
	if bx >= codedW || by >= codedH {
		return
	}

	size := blockSize(bl)
	half := size / 2
	haveHSplit := codedW > bx+half
	haveVSplit := codedH > by+half

	// A node wholly beyond both half-way boundaries has no partition symbol.
	if !haveHSplit && !haveVSplit {
		if bl == tile.BL8X8 {
			fe.encodeBlock(ec, ctx, fs, st, bx, by, half, half)
			fs.SetPartition(bx, by, bl, tile.PartitionSplit, size)
			return
		}
		fe.encodePartition(ec, ctx, fs, st, bx, by, bl+1)
		return
	}

	partCtx := fs.PartCtx(bx, by, bl)
	cdf, n := partitionCDF(ctx, partCtx, bl)

	// At one-sided frame edges the partition alphabet collapses to a
	// boolean. Split until the fixed 8x8 leaf size is reached.
	if !haveVSplit {
		if bl < tile.BL8X8 {
			ec.Bool(1, gatherTopPartitionProb(cdf, bl))
			fe.encodePartition(ec, ctx, fs, st, bx, by, bl+1)
			fe.encodePartition(ec, ctx, fs, st, bx+half, by, bl+1)
			return
		}
		ec.Bool(0, gatherTopPartitionProb(cdf, bl))
		fe.encodeBlock(ec, ctx, fs, st, bx, by, size, half)
		fs.SetPartition(bx, by, bl, tile.PartitionH, size)
		return
	}
	if !haveHSplit {
		if bl < tile.BL8X8 {
			ec.Bool(1, gatherLeftPartitionProb(cdf, bl))
			fe.encodePartition(ec, ctx, fs, st, bx, by, bl+1)
			fe.encodePartition(ec, ctx, fs, st, bx, by+half, bl+1)
			return
		}
		ec.Bool(0, gatherLeftPartitionProb(cdf, bl))
		fe.encodeBlock(ec, ctx, fs, st, bx, by, half, size)
		fs.SetPartition(bx, by, bl, tile.PartitionV, size)
		return
	}

	if bl < tile.BL8X8 {
		ec.Symbol(tile.PartitionSplit, cdf, n)
		fe.encodePartition(ec, ctx, fs, st, bx, by, bl+1)
		fe.encodePartition(ec, ctx, fs, st, bx+half, by, bl+1)
		fe.encodePartition(ec, ctx, fs, st, bx, by+half, bl+1)
		fe.encodePartition(ec, ctx, fs, st, bx+half, by+half, bl+1)
		return
	}

	ec.Symbol(tile.PartitionNone, cdf, n)
	fe.encodeBlock(ec, ctx, fs, st, bx, by, size, size)
	fs.SetPartition(bx, by, bl, tile.PartitionNone, size)
}

func (fe *FrameEncoder) encodeBlock(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx,
	fs *tile.FrameState, st *tileEncodeState, bx, by, bw, bh int,
) {
	// The final coding block may extend past the visible frame edge. AV1
	// reconstructs on the rounded coded grid and crops to the visible size,
	// so encode it with replicated edge samples instead of leaving a gray
	// skipped strip at non-multiple-of-eight dimensions.
	coded := bw == 8 && bh == 8
	skipCtx := fs.SkipCtx(bx, by)
	ec.Bool(boolSymbol(!coded), uint32(ctx.SkipCDF[skipCtx][0]))

	topMode := fs.TopModeCtx(bx, by)
	leftMode := fs.LeftModeCtx(bx, by)
	ec.Symbol(tile.DCPred, ctx.KFYModeCDF[topMode][leftMode][:], tile.NIntraPredModes)

	if blockHasChroma(bx, by, bw, bh) {
		cfl := 0
		nUV := tile.NIntraPredModes
		if cflAllowed(bw, bh) {
			cfl = 1
			nUV++
		}
		ec.Symbol(tile.DCPred, ctx.UVModeCDF[cfl][tile.DCPred][:], nUV)
	}

	if coded {
		fs.SetIntraTxCtx(bx, by, bw, bh, transform.TX8x8)
		fe.encodePlaneDC(ec, ctx, fs, st, 0, bx, by, 8, 8, transform.TX8x8)
		if blockHasChroma(bx, by, bw, bh) {
			fe.encodePlaneDC(ec, ctx, fs, st, 1, bx/2, by/2, 4, 4, transform.TX4x4)
			fe.encodePlaneDC(ec, ctx, fs, st, 2, bx/2, by/2, 4, 4, transform.TX4x4)
		}
	} else {
		fs.SetCoefCtxBlock(0, bx, by, bw, bh, 0x40)
		if blockHasChroma(bx, by, bw, bh) {
			fs.SetCoefCtxBlock(1, bx/2, by/2, (bw+1)/2, (bh+1)/2, 0x40)
			fs.SetCoefCtxBlock(2, bx/2, by/2, (bw+1)/2, (bh+1)/2, 0x40)
		}
	}

	fs.SetPaletteCtx(bx, by, bw, bh, 0, 0)
	fs.SetBlockState(bx, by, bw, bh, tile.Av1Block{
		Intra:  true,
		Skip:   !coded,
		YMode:  tile.DCPred,
		UvMode: tile.DCPred,
	})
	fs.SetBlock(bx, by, bw, bh, !coded, tile.DCPred)
}

func (fe *FrameEncoder) encodePlaneDC(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx,
	fs *tile.FrameState, st *tileEncodeState, plane, bx, by, bw, bh int, tx uint8,
) {
	pred := dcPredict(st.recon[plane], st.w[plane], st.h[plane], bx, by, bw, bh)
	mean := blockMean(st.src[plane], st.w[plane], st.h[plane], bx, by, bw, bh, pred)
	if (plane == 0 && tx == transform.TX8x8) || (plane > 0 && tx == transform.TX4x4) {
		size := bw
		residual := make([]int16, size*size)
		for y := 0; y < size; y++ {
			srcY := min(by+y, st.h[plane]-1)
			for x := 0; x < size; x++ {
				srcX := min(bx+x, st.w[plane]-1)
				residual[y*size+x] = int16(int(st.src[plane][srcY*st.w[plane]+srcX]) - pred)
			}
		}
		coeff := make([]int32, size*size)
		if size == 8 {
			encodertx.FwdDCT8x8(coeff, residual, size)
		} else {
			encodertx.FwdDCT4x4(coeff, residual, size)
		}
		encodertx.Quantize(coeff, fe.QIndex, fe.BitDepth)
		if plane == 0 {
			entropy.EncodeDCT8(ec, ctx, fs, bx, by, coeff)
		} else {
			entropy.EncodeDCT4(ec, ctx, fs, plane, bx, by, coeff)
		}
		reconstructDCTBlock(st.recon[plane], st.w[plane], st.h[plane],
			bx, by, size, pred, coeff, tx, fe.QIndex, fe.BitDepth)
		return
	}
	diff := mean - pred
	td := transform.TxfmDimensions[tx]
	dqShift := max(0, int(td.Ctx)-2)
	dq := int(transform.DqTbl[fe.BitDepth][fe.QIndex][0])
	level := 0
	if diff != 0 {
		scale := 8 << uint(dqShift)
		mag := (abs(diff)*scale + dq/2) / dq
		if mag == 0 {
			mag = 1
		}
		level = mag
		if diff < 0 {
			level = -level
		}
	}
	entropy.EncodeDCOnly(ec, ctx, fs, tx, plane, bx, by, bw, bh, level)
	fillBlock(st.recon[plane], st.w[plane], st.h[plane], bx, by, bw, bh, byte(mean))
}

func reconstructDCTBlock(dst []byte, width, height, bx, by, size, pred int,
	qcoeff []int32, tx uint8, qindex, hbd int,
) {
	coeff := append([]int32(nil), qcoeff...)
	encodertx.Dequantize(coeff, qindex, hbd)
	packed := make([]int32, size*size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			packed[x*size+y] = coeff[y*size+x]
		}
	}
	block := make([]byte, size*size)
	for i := range block {
		block[i] = byte(pred)
	}
	shift := 0
	if tx == transform.TX8x8 {
		shift = 1
	}
	transform.InvTxfmAdd(block, size, packed, size*size-1, tx, shift, transform.DCT_DCT, 8)
	for y := 0; y < size && by+y < height; y++ {
		n := min(size, width-bx)
		copy(dst[(by+y)*width+bx:(by+y)*width+bx+n], block[y*size:y*size+n])
	}
}

func dcPredict(recon []byte, width, height, bx, by, bw, bh int) int {
	sum, count := 0, 0
	if by > 0 {
		for x := 0; x < bw; x++ {
			srcX := min(bx+x, width-1)
			sum += int(recon[(by-1)*width+srcX])
			count++
		}
	}
	if bx > 0 {
		for y := 0; y < bh; y++ {
			srcY := min(by+y, height-1)
			sum += int(recon[srcY*width+bx-1])
			count++
		}
	}
	if count == 0 {
		return 128
	}
	return (sum + count/2) / count
}

func blockMean(src []byte, width, height, bx, by, bw, bh, fallback int) int {
	sum, count := 0, 0
	for y := 0; y < bh && by+y < height; y++ {
		off := (by+y)*width + bx
		n := min(bw, width-bx)
		if off < 0 || n <= 0 || off+n > len(src) {
			continue
		}
		for _, value := range src[off : off+n] {
			sum += int(value)
		}
		count += n
	}
	if count == 0 {
		return fallback
	}
	return (sum + count/2) / count
}

func fillBlock(dst []byte, width, height, bx, by, bw, bh int, value byte) {
	for y := 0; y < bh && by+y < height; y++ {
		for x := 0; x < bw && bx+x < width; x++ {
			dst[(by+y)*width+bx+x] = value
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func boolSymbol(v bool) uint32 {
	if v {
		return 1
	}
	return 0
}

func blockHasChroma(bx, by, bw, bh int) bool {
	bw4 := (bw + 3) >> 2
	bh4 := (bh + 3) >> 2
	bx4 := bx >> 2
	by4 := by >> 2
	return (bw4 > 1 || bx4&1 != 0) && (bh4 > 1 || by4&1 != 0)
}

func cflAllowed(bw, bh int) bool {
	switch {
	case bw == 32 && (bh == 32 || bh == 16 || bh == 8):
		return true
	case bw == 16 && (bh == 32 || bh == 16 || bh == 8 || bh == 4):
		return true
	case bw == 8 && (bh == 32 || bh == 16 || bh == 8 || bh == 4):
		return true
	case bw == 4 && (bh == 16 || bh == 8 || bh == 4):
		return true
	default:
		return false
	}
}

func blockSize(bl int) int {
	switch bl {
	case tile.BL128X128:
		return 128
	case tile.BL64X64:
		return 64
	case tile.BL32X32:
		return 32
	case tile.BL16X16:
		return 16
	default:
		return 8
	}
}

func partitionCDF(ctx *tile.TileCtx, partCtx, bl int) ([]uint16, int) {
	switch bl {
	case tile.BL128X128:
		return ctx.Partition128CDF[partCtx][:], 8
	case tile.BL64X64:
		return ctx.Partition64CDF[partCtx][:], 10
	case tile.BL32X32:
		return ctx.Partition32CDF[partCtx][:], 10
	case tile.BL16X16:
		return ctx.Partition16CDF[partCtx][:], 10
	default:
		return ctx.Partition8CDF[partCtx][:], 4
	}
}

func gatherLeftPartitionProb(cdf []uint16, bl int) uint32 {
	out := int(cdf[tile.PartitionH-1]) - int(cdf[tile.PartitionH])
	out += int(cdf[tile.PartitionSplit-1]) - int(cdf[tile.PartitionTLeftSplit])
	if bl != tile.BL128X128 {
		out += int(cdf[tile.PartitionH4-1]) - int(cdf[tile.PartitionH4])
	}
	return uint32(max(0, min(32768, out)))
}

func gatherTopPartitionProb(cdf []uint16, bl int) uint32 {
	out := int(cdf[tile.PartitionV-1]) - int(cdf[tile.PartitionTTopSplit])
	out += int(cdf[tile.PartitionTLeftSplit-1])
	if bl != tile.BL128X128 {
		out += int(cdf[tile.PartitionV4-1]) - int(cdf[tile.PartitionTRightSplit])
	}
	return uint32(max(0, min(32768, out)))
}
