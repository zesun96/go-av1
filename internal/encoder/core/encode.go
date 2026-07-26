// Package core implements the single-tile AV1 key-frame encoding loop.
package core

import (
	"github.com/zesun96/go-av1/internal/encoder/bitwriter"
	"github.com/zesun96/go-av1/internal/encoder/entropy"
	"github.com/zesun96/go-av1/internal/encoder/obuwriter"
	encodertx "github.com/zesun96/go-av1/internal/encoder/tx"
	intrapred "github.com/zesun96/go-av1/internal/predict/intra"
	"github.com/zesun96/go-av1/internal/tile"
	"github.com/zesun96/go-av1/internal/transform"
)

// FrameEncoder encodes a single frame.
type FrameEncoder struct {
	Width    int
	Height   int
	QIndex   int
	BitDepth int
	ref      [3][]byte
}

// EncodeShowExisting displays reference slot zero. Every key frame emitted by
// this baseline refreshes all eight slots, so slot zero is always the latest
// independently coded picture.
func (fe *FrameEncoder) EncodeShowExisting() []byte {
	return obuwriter.BuildShowExistingTemporalUnit(0)
}

// EncodeFrame returns one complete AV1 temporal unit.
func (fe *FrameEncoder) EncodeFrame(yPlane, cbPlane, crPlane []byte, frameNum int) []byte {
	ec := bitwriter.NewMSACEncoder(max(64, fe.Width*fe.Height/64))
	st := fe.encodeKeyTile(ec, yPlane, cbPlane, crPlane)
	tileData := ec.Flush()
	fe.saveReference(st)

	seqParams := &obuwriter.SeqParams{
		Width:    fe.Width,
		Height:   fe.Height,
		BitDepth: 8,
		ChromaSS: 1,
		Use128SB: true,
	}
	return obuwriter.BuildTemporalUnit(seqParams, fe.QIndex, tileData, true)
}

// EncodeInterFrame returns an inter temporal unit predicted from the most
// recently reconstructed frame. The first inter baseline uses LAST_FRAME with
// identity global motion and codes the complete residual.
func (fe *FrameEncoder) EncodeInterFrame(yPlane, cbPlane, crPlane []byte) []byte {
	if len(fe.ref[0]) == 0 {
		return fe.EncodeFrame(yPlane, cbPlane, crPlane, 0)
	}
	ec := bitwriter.NewMSACEncoder(max(64, fe.Width*fe.Height/64))
	st := fe.encodeInterTile(ec, yPlane, cbPlane, crPlane)
	tileData := ec.Flush()
	fe.saveReference(st)
	seqParams := &obuwriter.SeqParams{
		Width: fe.Width, Height: fe.Height, BitDepth: 8, ChromaSS: 1, Use128SB: true,
	}
	return obuwriter.BuildInterTemporalUnit(seqParams, fe.QIndex, tileData)
}

type tileEncodeState struct {
	src   [3][]byte
	recon [3][]byte
	w     [3]int
	h     [3]int
}

// encodeKeyTile writes the syntax consumed by decode_partition() and
// write_modes_b() in SVT-AV1: partition, skip, key-frame Y mode and UV mode.
func (fe *FrameEncoder) encodeKeyTile(ec *bitwriter.MSACEncoder, y, u, v []byte) *tileEncodeState {
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
			fe.encodePartition(ec, ctx, fs, st, bx, by, tile.BL128X128, false)
		}
	}
	return st
}

func (fe *FrameEncoder) encodeInterTile(ec *bitwriter.MSACEncoder, y, u, v []byte) *tileEncodeState {
	ctx := tile.NewTileCtxForQIdx(fe.QIndex)
	fs := tile.NewFrameState(fe.Width, fe.Height)
	fs.SetSubsampling(1, 1)
	tile.EnableEncoderMVContexts(fs, fe.Width, fe.Height)
	cw, ch := (fe.Width+1)/2, (fe.Height+1)/2
	st := &tileEncodeState{
		src: [3][]byte{y, u, v},
		w:   [3]int{fe.Width, cw, cw},
		h:   [3]int{fe.Height, ch, ch},
	}
	for plane := range st.recon {
		st.recon[plane] = append([]byte(nil), fe.ref[plane]...)
	}
	codedW := (fe.Width + 7) &^ 7
	codedH := (fe.Height + 7) &^ 7
	for by := 0; by < codedH; by += 128 {
		for bx := 0; bx < codedW; bx += 128 {
			fe.encodePartition(ec, ctx, fs, st, bx, by, tile.BL128X128, true)
		}
	}
	return st
}

func (fe *FrameEncoder) saveReference(st *tileEncodeState) {
	if st == nil {
		return
	}
	for plane := range fe.ref {
		fe.ref[plane] = append(fe.ref[plane][:0], st.recon[plane]...)
	}
}

func (fe *FrameEncoder) encodePartition(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx,
	fs *tile.FrameState, st *tileEncodeState, bx, by, bl int, inter bool,
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
			fe.encodeLeaf(ec, ctx, fs, st, bx, by, half, half, inter)
			fs.SetPartition(bx, by, bl, tile.PartitionSplit, size)
			return
		}
		fe.encodePartition(ec, ctx, fs, st, bx, by, bl+1, inter)
		return
	}

	partCtx := fs.PartCtx(bx, by, bl)
	cdf, n := partitionCDF(ctx, partCtx, bl)

	// At one-sided frame edges the partition alphabet collapses to a
	// boolean. Split until the fixed 8x8 leaf size is reached.
	if !haveVSplit {
		if bl < tile.BL8X8 {
			ec.Bool(1, gatherTopPartitionProb(cdf, bl))
			fe.encodePartition(ec, ctx, fs, st, bx, by, bl+1, inter)
			fe.encodePartition(ec, ctx, fs, st, bx+half, by, bl+1, inter)
			return
		}
		ec.Bool(0, gatherTopPartitionProb(cdf, bl))
		fe.encodeLeaf(ec, ctx, fs, st, bx, by, size, half, inter)
		fs.SetPartition(bx, by, bl, tile.PartitionH, size)
		return
	}
	if !haveHSplit {
		if bl < tile.BL8X8 {
			ec.Bool(1, gatherLeftPartitionProb(cdf, bl))
			fe.encodePartition(ec, ctx, fs, st, bx, by, bl+1, inter)
			fe.encodePartition(ec, ctx, fs, st, bx, by+half, bl+1, inter)
			return
		}
		ec.Bool(0, gatherLeftPartitionProb(cdf, bl))
		fe.encodeLeaf(ec, ctx, fs, st, bx, by, half, size, inter)
		fs.SetPartition(bx, by, bl, tile.PartitionV, size)
		return
	}

	if bl < tile.BL8X8 {
		ec.SymbolAdaptDav1d(tile.PartitionSplit, cdf, n-1)
		fe.encodePartition(ec, ctx, fs, st, bx, by, bl+1, inter)
		fe.encodePartition(ec, ctx, fs, st, bx+half, by, bl+1, inter)
		fe.encodePartition(ec, ctx, fs, st, bx, by+half, bl+1, inter)
		fe.encodePartition(ec, ctx, fs, st, bx+half, by+half, bl+1, inter)
		return
	}

	ec.SymbolAdaptDav1d(tile.PartitionNone, cdf, n-1)
	fe.encodeLeaf(ec, ctx, fs, st, bx, by, size, size, inter)
	fs.SetPartition(bx, by, bl, tile.PartitionNone, size)
}

func (fe *FrameEncoder) encodeLeaf(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx,
	fs *tile.FrameState, st *tileEncodeState, bx, by, bw, bh int, inter bool,
) {
	if inter {
		fe.encodeInterBlock(ec, ctx, fs, st, bx, by, bw, bh)
		return
	}
	fe.encodeBlock(ec, ctx, fs, st, bx, by, bw, bh)
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
	ec.BoolAdapt(boolSymbol(!coded), ctx.SkipCDF[skipCtx][:])

	yMode := fe.chooseLumaMode(st, bx, by, 8)
	topMode := fs.TopModeCtx(bx, by)
	leftMode := fs.LeftModeCtx(bx, by)
	ec.SymbolAdaptDav1d(uint32(yMode), ctx.KFYModeCDF[topMode][leftMode][:], tile.NIntraPredModes-1)
	if yMode >= tile.VertPred && yMode <= tile.VertLeftPred {
		// 8x8 directional modes signal an angle delta. The initial search
		// uses the canonical angle, represented by delta symbol 3.
		ec.SymbolAdaptDav1d(3, ctx.AngleDeltaCDF[yMode-tile.VertPred][:], 6)
	}

	if blockHasChroma(bx, by, bw, bh) {
		cfl := 0
		nUV := tile.NIntraPredModes
		if cflAllowed(bw, bh) {
			cfl = 1
			nUV++
		}
		ec.SymbolAdaptDav1d(tile.DCPred, ctx.UVModeCDF[cfl][yMode][:], nUV-1)
	}

	if coded {
		fs.SetIntraTxCtx(bx, by, bw, bh, transform.TX8x8)
		fe.encodePlaneDC(ec, ctx, fs, st, 0, bx, by, 8, 8, transform.TX8x8, yMode)
		if blockHasChroma(bx, by, bw, bh) {
			fe.encodePlaneDC(ec, ctx, fs, st, 1, bx/2, by/2, 4, 4, transform.TX4x4, tile.DCPred)
			fe.encodePlaneDC(ec, ctx, fs, st, 2, bx/2, by/2, 4, 4, transform.TX4x4, tile.DCPred)
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
		YMode:  uint8(yMode),
		UvMode: tile.DCPred,
	})
	fs.SetBlock(bx, by, bw, bh, !coded, yMode)
}

func (fe *FrameEncoder) encodeInterBlock(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx,
	fs *tile.FrameState, st *tileEncodeState, bx, by, bw, bh int,
) {
	coded := bw == 8 && bh == 8
	skip := !coded
	skipCtx := fs.SkipCtx(bx, by)
	ec.BoolAdapt(boolSymbol(skip), ctx.SkipCDF[skipCtx][:])

	ic := tile.SingleRefEncoderContexts(fs, bx, by, bw, bh)
	ec.BoolAdapt(1, ctx.IntraCDF[ic.Intra][:]) // inter
	ec.BoolAdapt(0, ctx.RefCDF[0][ic.Ref][:])  // forward reference group
	ec.BoolAdapt(0, ctx.RefCDF[2][ic.Ref3][:]) // LAST/LAST2 group
	ec.BoolAdapt(0, ctx.RefCDF[3][ic.Ref4][:]) // LAST_FRAME
	ec.BoolAdapt(1, ctx.NewMVModeCDF[ic.NewMV][:])
	ec.BoolAdapt(0, ctx.GlobalMVModeCDF[ic.GlobalMV][:])

	maxTx := uint8(transform.TX8x8)
	uvtx := uint8(transform.TX4x4)
	fs.SetTxCtx(bx, by, bw, bh, maxTx, false, skip)
	fs.SetInterTxIntraCtx(bx, by, bw, bh)
	if coded {
		fe.encodeInterPlane(ec, ctx, fs, st, 0, bx, by, 8, transform.TX8x8)
		if blockHasChroma(bx, by, bw, bh) {
			fe.encodeInterPlane(ec, ctx, fs, st, 1, bx/2, by/2, 4, transform.TX4x4)
			fe.encodeInterPlane(ec, ctx, fs, st, 2, bx/2, by/2, 4, transform.TX4x4)
		}
	} else {
		fs.SetCoefCtxBlock(0, bx, by, bw, bh, 0x40)
		if blockHasChroma(bx, by, bw, bh) {
			fs.SetCoefCtxBlock(1, bx/2, by/2, (bw+1)/2, (bh+1)/2, 0x40)
			fs.SetCoefCtxBlock(2, bx/2, by/2, (bw+1)/2, (bh+1)/2, 0x40)
		}
	}

	blk := tile.Av1Block{
		Intra: false, Skip: skip,
		InterMode: tile.InterModeGlobalMV,
		RefSlot:   0, RefFrame: 1, RefOrder: 0,
		Tx: maxTx, MaxYTx: maxTx, Uvtx: uvtx,
	}
	blk.Bl, blk.Bs = tile.EncoderBlockGeometry(bw, bh)
	if blockHasChroma(bx, by, bw, bh) {
		fs.SetChromaBlockState(bx, by, bw, bh, blk)
	}
	fs.CommitInterBlock(bx, by, bw, bh, blk, 1, blockHasChroma(bx, by, bw, bh))
}

func (fe *FrameEncoder) encodeInterPlane(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx,
	fs *tile.FrameState, st *tileEncodeState, plane, bx, by, size int, tx uint8,
) {
	pred := make([]byte, size*size)
	residual := make([]int16, size*size)
	for y := 0; y < size; y++ {
		srcY := min(by+y, st.h[plane]-1)
		for x := 0; x < size; x++ {
			srcX := min(bx+x, st.w[plane]-1)
			i := y*size + x
			pred[i] = fe.ref[plane][srcY*st.w[plane]+srcX]
			residual[i] = int16(int(st.src[plane][srcY*st.w[plane]+srcX]) - int(pred[i]))
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
		entropy.EncodeInterDCT8(ec, ctx, fs, bx, by, coeff)
	} else {
		entropy.EncodeInterDCT4(ec, ctx, fs, plane, bx, by, coeff)
	}
	reconstructDCTBlock(st.recon[plane], st.w[plane], st.h[plane],
		bx, by, size, pred, coeff, tx, fe.QIndex, fe.BitDepth)
}

func (fe *FrameEncoder) encodePlaneDC(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx,
	fs *tile.FrameState, st *tileEncodeState, plane, bx, by, bw, bh int, tx uint8, mode int,
) {
	predBlock := intraPredBlock(st.recon[plane], st.w[plane], st.h[plane], bx, by, bw, bh, mode)
	pred := int(predBlock[0])
	mean := blockMean(st.src[plane], st.w[plane], st.h[plane], bx, by, bw, bh, pred)
	if (plane == 0 && tx == transform.TX8x8) || (plane > 0 && tx == transform.TX4x4) {
		size := bw
		residual := make([]int16, size*size)
		for y := 0; y < size; y++ {
			srcY := min(by+y, st.h[plane]-1)
			for x := 0; x < size; x++ {
				srcX := min(bx+x, st.w[plane]-1)
				residual[y*size+x] = int16(int(st.src[plane][srcY*st.w[plane]+srcX]) - int(predBlock[y*size+x]))
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
			entropy.EncodeDCT8(ec, ctx, fs, bx, by, mode, coeff)
		} else {
			entropy.EncodeDCT4(ec, ctx, fs, plane, bx, by, coeff)
		}
		reconstructDCTBlock(st.recon[plane], st.w[plane], st.h[plane],
			bx, by, size, predBlock, coeff, tx, fe.QIndex, fe.BitDepth)
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

func reconstructDCTBlock(dst []byte, width, height, bx, by, size int, pred []byte,
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
	block := append([]byte(nil), pred...)
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

func (fe *FrameEncoder) chooseLumaMode(st *tileEncodeState, bx, by, size int) int {
	modes := []int{tile.DCPred}
	if by > 0 {
		modes = append(modes, tile.VertPred)
	}
	if bx > 0 {
		modes = append(modes, tile.HorPred)
	}
	if bx > 0 && by > 0 {
		modes = append(modes,
			tile.SmoothPred, tile.SmoothVPred, tile.SmoothHPred, tile.PaethPred)
	}
	bestMode := tile.DCPred
	bestSSE := int64(^uint64(0) >> 1)
	for _, mode := range modes {
		pred := intraPredBlock(st.recon[0], st.w[0], st.h[0], bx, by, size, size, mode)
		var sse int64
		for y := 0; y < size; y++ {
			srcY := min(by+y, st.h[0]-1)
			for x := 0; x < size; x++ {
				srcX := min(bx+x, st.w[0]-1)
				diff := int(st.src[0][srcY*st.w[0]+srcX]) - int(pred[y*size+x])
				sse += int64(diff * diff)
			}
		}
		if sse < bestSSE {
			bestSSE = sse
			bestMode = mode
		}
	}
	return bestMode
}

func intraPredBlock(recon []byte, width, height, bx, by, bw, bh, mode int) []byte {
	out := make([]byte, bw*bh)
	switch mode {
	case tile.SmoothPred, tile.SmoothVPred, tile.SmoothHPred, tile.PaethPred:
		maxDim := max(bw, bh)
		tl := 2 * maxDim
		edge := make([]byte, 4*maxDim+2)
		for i := range edge {
			edge[i] = 128
		}
		edge[tl] = recon[(by-1)*width+bx-1]
		for x := 0; x < bw; x++ {
			srcX := min(bx+x, width-1)
			edge[tl+1+x] = recon[(by-1)*width+srcX]
		}
		for y := 0; y < bh; y++ {
			srcY := min(by+y, height-1)
			edge[tl-1-y] = recon[srcY*width+bx-1]
		}
		switch mode {
		case tile.SmoothPred:
			intrapred.PredSmooth(out, bw, edge, tl, bw, bh)
		case tile.SmoothVPred:
			intrapred.PredSmoothV(out, bw, edge, tl, bw, bh)
		case tile.SmoothHPred:
			intrapred.PredSmoothH(out, bw, edge, tl, bw, bh)
		default:
			intrapred.PredPaeth(out, bw, edge, tl, bw, bh)
		}
	case tile.VertPred:
		for y := 0; y < bh; y++ {
			for x := 0; x < bw; x++ {
				srcX := min(bx+x, width-1)
				out[y*bw+x] = recon[(by-1)*width+srcX]
			}
		}
	case tile.HorPred:
		for y := 0; y < bh; y++ {
			srcY := min(by+y, height-1)
			value := recon[srcY*width+bx-1]
			for x := 0; x < bw; x++ {
				out[y*bw+x] = value
			}
		}
	default:
		value := byte(dcPredict(recon, width, height, bx, by, bw, bh))
		for i := range out {
			out[i] = value
		}
	}
	return out
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
