// Package core implements the single-tile AV1 key-frame encoding loop.
package core

import (
	"math"

	"github.com/zesun96/go-av1/internal/encoder/bitwriter"
	"github.com/zesun96/go-av1/internal/encoder/entropy"
	"github.com/zesun96/go-av1/internal/encoder/me"
	"github.com/zesun96/go-av1/internal/encoder/obuwriter"
	"github.com/zesun96/go-av1/internal/encoder/rdo"
	"github.com/zesun96/go-av1/internal/encoder/reference"
	encodertx "github.com/zesun96/go-av1/internal/encoder/tx"
	intrapred "github.com/zesun96/go-av1/internal/predict/intra"
	"github.com/zesun96/go-av1/internal/tile"
	"github.com/zesun96/go-av1/internal/transform"
)

// FrameEncoder encodes a single frame.
type FrameEncoder struct {
	Width          int
	Height         int
	QIndex         int
	BitDepth       int
	ref            [3][]byte
	refs           [8][3][]byte
	refMgr         reference.Manager
	refIdx         [7]uint8
	refSlot        int
	ref2           [3][]byte
	refSlot2       int
	compound       bool
	EnableOBMC     bool
	SearchRange    int
	HierarchicalME bool
	IntegerMEOnly  bool
	EnableCompound bool
}

// EncodeShowExisting displays reference slot zero. Every key frame emitted by
// this baseline refreshes all eight slots, so slot zero is always the latest
// independently coded picture.
func (fe *FrameEncoder) EncodeShowExisting() []byte {
	return obuwriter.BuildShowExistingTemporalUnit(fe.refMgr.Latest())
}

// EncodeFrame returns one complete AV1 temporal unit.
func (fe *FrameEncoder) EncodeFrame(yPlane, cbPlane, crPlane []byte, frameNum int) []byte {
	ec := bitwriter.NewMSACEncoder(max(64, fe.Width*fe.Height/64))
	st := fe.encodeKeyTile(ec, yPlane, cbPlane, crPlane)
	tileData := ec.Flush()
	fe.saveKeyReferences(st)

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
	plan := fe.refMgr.PlanInter()
	fe.refIdx = plan.RefIdx
	fe.refSlot = int(plan.RefIdx[0])
	fe.ref = fe.refs[fe.refSlot]
	fe.refSlot2 = int(plan.RefIdx[1])
	fe.ref2 = fe.refs[fe.refSlot2]
	fe.compound = fe.EnableCompound && fe.refSlot2 != fe.refSlot && len(fe.ref2[0]) != 0
	ec := bitwriter.NewMSACEncoder(max(64, fe.Width*fe.Height/64))
	st := fe.encodeInterTile(ec, yPlane, cbPlane, crPlane)
	tileData := ec.Flush()
	fe.saveInterReference(plan.TargetSlot, st)
	fe.refMgr.CommitInter(plan.TargetSlot)
	seqParams := &obuwriter.SeqParams{
		Width: fe.Width, Height: fe.Height, BitDepth: 8, ChromaSS: 1, Use128SB: true,
	}
	return obuwriter.BuildInterTemporalUnitWithParams(seqParams, fe.QIndex, tileData,
		obuwriter.InterFrameParams{
			RefIdx: plan.RefIdx, RefreshFlags: plan.RefreshFlags,
			EnableCompound: fe.compound, EnableOBMC: fe.EnableOBMC,
		})
}

type tileEncodeState struct {
	src   [3][]byte
	recon [3][]byte
	w     [3]int
	h     [3]int
	large map[[2]int]largeInterCandidate
}

type largeInterCandidate struct {
	mv       me.MV
	planes   []*interPlaneEncode
	refSlot  int
	refFrame int
	refOrder int
}

// encodeKeyTile writes the syntax consumed by decode_partition() and
// write_modes_b() in SVT-AV1: partition, skip, key-frame Y mode and UV mode.
func (fe *FrameEncoder) encodeKeyTile(ec *bitwriter.MSACEncoder, y, u, v []byte) *tileEncodeState {
	ctx := tile.NewTileCtxForQIdx(fe.QIndex)
	fs := tile.NewFrameState(fe.Width, fe.Height)
	fs.SetSubsampling(1, 1)
	cw, ch := (fe.Width+1)/2, (fe.Height+1)/2
	st := &tileEncodeState{
		src:   [3][]byte{y, u, v},
		w:     [3]int{fe.Width, cw, cw},
		h:     [3]int{fe.Height, ch, ch},
		large: make(map[[2]int]largeInterCandidate),
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
	tile.ApplyEncoderLoopFilter(fs, st.recon, st.w, st.h, fe.loopFilterLevel())
	return st
}

func (fe *FrameEncoder) encodeInterTile(ec *bitwriter.MSACEncoder, y, u, v []byte) *tileEncodeState {
	ctx := tile.NewTileCtxForQIdx(fe.QIndex)
	fs := tile.NewFrameState(fe.Width, fe.Height)
	fs.SetSubsampling(1, 1)
	tile.EnableEncoderMVContexts(fs, fe.Width, fe.Height)
	cw, ch := (fe.Width+1)/2, (fe.Height+1)/2
	st := &tileEncodeState{
		src:   [3][]byte{y, u, v},
		w:     [3]int{fe.Width, cw, cw},
		h:     [3]int{fe.Height, ch, ch},
		large: make(map[[2]int]largeInterCandidate),
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
	tile.ApplyEncoderLoopFilter(fs, st.recon, st.w, st.h, fe.loopFilterLevel())
	return st
}

func (fe *FrameEncoder) loopFilterLevel() int {
	if fe.QIndex < 8 {
		return 0
	}
	return min(fe.QIndex>>3, 32)
}

func (fe *FrameEncoder) saveKeyReferences(st *tileEncodeState) {
	if st == nil {
		return
	}
	for slot := range fe.refs {
		for plane := range fe.refs[slot] {
			fe.refs[slot][plane] = append(fe.refs[slot][plane][:0], st.recon[plane]...)
		}
	}
	fe.refMgr.ResetKey()
	fe.refSlot = 0
	fe.ref = fe.refs[0]
	fe.refSlot2 = 0
	fe.ref2 = fe.refs[0]
	fe.compound = false
}

func (fe *FrameEncoder) saveInterReference(slot int, st *tileEncodeState) {
	if st == nil || slot < 0 || slot >= len(fe.refs) {
		return
	}
	for plane := range fe.refs[slot] {
		fe.refs[slot][plane] = append(fe.refs[slot][plane][:0], st.recon[plane]...)
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
		if (bl == tile.BL16X16 || bl == tile.BL32X32) &&
			((inter && fe.shouldUseLargeInterBlock(st, bx, by, size)) ||
				(!inter && size == 16 && bx+size <= fe.Width && by+size <= fe.Height &&
					fe.shouldUseLargeIntraBlock(st, bx, by, size))) {
			ec.SymbolAdaptDav1d(tile.PartitionNone, cdf, n-1)
			fe.encodeLeaf(ec, ctx, fs, st, bx, by, size, size, inter)
			fs.SetPartition(bx, by, bl, tile.PartitionNone, size)
			return
		}
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
	coded := bw == bh && (bw == 8 || bw == 16)
	skipCtx := fs.SkipCtx(bx, by)
	ec.BoolAdapt(boolSymbol(!coded), ctx.SkipCDF[skipCtx][:])

	yMode := fe.chooseLumaMode(st, bx, by, bw)
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
		yTx, uvTx := uint8(transform.TX8x8), uint8(transform.TX4x4)
		if bw == 16 {
			yTx, uvTx = transform.TX16x16, transform.TX8x8
		}
		fs.SetIntraTxCtx(bx, by, bw, bh, yTx)
		fe.encodePlaneDC(ec, ctx, fs, st, 0, bx, by, bw, bh, yTx, yMode)
		if blockHasChroma(bx, by, bw, bh) {
			fe.encodePlaneDC(ec, ctx, fs, st, 1, bx/2, by/2, bw/2, bh/2, uvTx, tile.DCPred)
			fe.encodePlaneDC(ec, ctx, fs, st, 2, bx/2, by/2, bw/2, bh/2, uvTx, tile.DCPred)
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
	largeBlock := bw == bh && (bw == 16 || bw == 32)
	coded := bw == 8 && bh == 8 || largeBlock
	mv := me.MV{}
	mv2 := me.MV{}
	refSlot, refFrame, refOrder := fe.refSlot, 1, 0
	var planes []*interPlaneEncode
	if largeBlock {
		candidate, ok := st.large[[2]int{bx, by}]
		if !ok {
			return
		}
		mv = candidate.mv
		planes = candidate.planes
		refSlot = candidate.refSlot
		refFrame = candidate.refFrame
		refOrder = candidate.refOrder
	} else if coded && bx+bw <= fe.Width && by+bh <= fe.Height {
		searchRange := fe.SearchRange
		if searchRange <= 0 {
			searchRange = 4
		}
		searchConfig := me.Config{
			Source: st.src[0], Reference: fe.ref[0],
			SourceStride: fe.Width, ReferenceStride: fe.Width,
			Width: fe.Width, Height: fe.Height,
			X: bx, Y: by, BlockWidth: bw, BlockHeight: bh,
			SearchRange: searchRange, IntegerOnly: fe.IntegerMEOnly,
		}
		var result me.Result
		var err error
		if fe.HierarchicalME {
			result, err = me.SearchHierarchical(searchConfig)
		} else {
			result, err = me.Search(searchConfig)
		}
		if err == nil {
			mv = result.MV
		}
	}
	useCompound := false
	useOBMC := false
	compoundNew := false
	obmcPresent, obmcBS := false, 0
	if coded && !largeBlock {
		bestCost := 0.0
		mv, planes, bestCost = fe.refineInterCandidate(
			st, fe.ref, bx, by, bw, mv, 4)
		seenSlots := map[int]bool{fe.refSlot: true}
		for candidateOrder := 1; candidateOrder <= 6; candidateOrder++ {
			candidateSlot := int(fe.refIdx[candidateOrder])
			if seenSlots[candidateSlot] || len(fe.refs[candidateSlot][0]) == 0 {
				continue
			}
			seenSlots[candidateSlot] = true
			searchRange := fe.SearchRange
			if searchRange <= 0 {
				searchRange = 4
			}
			candidateConfig := me.Config{
				Source: st.src[0], Reference: fe.refs[candidateSlot][0],
				SourceStride: fe.Width, ReferenceStride: fe.Width,
				Width: fe.Width, Height: fe.Height,
				X: bx, Y: by, BlockWidth: bw, BlockHeight: bh,
				SearchRange: searchRange, IntegerOnly: fe.IntegerMEOnly,
			}
			var candidateResult me.Result
			var err error
			if fe.HierarchicalME {
				candidateResult, err = me.SearchHierarchical(candidateConfig)
			} else {
				candidateResult, err = me.Search(candidateConfig)
			}
			if err != nil {
				continue
			}
			candidateRefs := fe.refs[candidateSlot]
			refinedMV, candidatePlanes, cost := fe.refineInterCandidate(
				st, candidateRefs, bx, by, bw, candidateResult.MV, 4)
			if cost < bestCost {
				planes = candidatePlanes
				bestCost = cost
				mv = refinedMV
				refSlot = candidateSlot
				refFrame = candidateOrder + 1
				refOrder = candidateOrder
			}
		}
		if fe.EnableOBMC {
			obmcPresent, obmcBS = tile.EncoderOBMCContext(fs, bx, by, bw, bh, refSlot)
			if obmcPresent {
				pred := tile.EncoderOBMCPrediction(
					planes[0].pred, fs, fe.refs, fe.Width, fe.Height, bx, by, bw, bh)
				obmcPlane := fe.analyzeInterPlanePrediction(
					st, 0, bx, by, 8, transform.TX8x8, pred)
				obmcPlanes := append([]*interPlaneEncode(nil), planes...)
				obmcPlanes[0] = obmcPlane
				if cost := interPlanesRDOCost(st, obmcPlanes, 4, fe.QIndex, fe.BitDepth); cost < bestCost {
					planes = obmcPlanes
					bestCost = cost
					useOBMC = true
				}
			}
		}
		if fe.compound {
			compoundMV0, compoundMV1, compoundPlanes, newMVMode, cost :=
				fe.refineCompoundCandidate(st, bx, by, bw)
			if cost < bestCost {
				planes = compoundPlanes
				bestCost = cost
				useCompound = true
				useOBMC = false
				mv, mv2 = compoundMV0, compoundMV1
				compoundNew = newMVMode
			}
		}
	}
	if fe.EnableOBMC && largeBlock {
		obmcPresent, obmcBS = tile.EncoderOBMCContext(fs, bx, by, bw, bh, refSlot)
	}
	skip := !coded || interPlanesAllZero(planes)
	skipCtx := fs.SkipCtx(bx, by)
	ec.BoolAdapt(boolSymbol(skip), ctx.SkipCDF[skipCtx][:])

	ic := tile.SingleRefEncoderContextsForReference(
		fs, fe.refIdx, refSlot, refFrame, bx, by, bw, bh)
	ec.BoolAdapt(1, ctx.IntraCDF[ic.Intra][:]) // inter
	interMode := tile.InterModeGlobalMV
	baseX, baseY := 0, 0
	deltaX, deltaY := 0, 0
	compoundCtx := tile.EncoderCompoundContexts{}
	if fe.compound {
		compoundCtx = tile.CompoundEncoderContexts(fs, fe.refIdx, bx, by, bw, bh)
		ec.BoolAdapt(boolSymbol(useCompound), ctx.CompCDF[compoundCtx.Flag][:])
		if useCompound {
			ec.BoolAdapt(0, ctx.CompDirCDF[compoundCtx.Dir][:])         // unidirectional
			ec.BoolAdapt(0, ctx.CompUniRefCDF[0][compoundCtx.Ref][:])   // forward pair
			ec.BoolAdapt(0, ctx.CompUniRefCDF[1][compoundCtx.UniP1][:]) // LAST + LAST2
			if compoundNew {
				ec.SymbolAdaptDav1d(7, ctx.CompInterModeCDF[compoundCtx.Mode][:], 7)
				ec.BoolAdapt(0, ctx.DRLBitCDF[compoundCtx.DRL0][:])
				baseX, baseY = compoundCtx.BaseX[0], compoundCtx.BaseY[0]
				deltaX, deltaY = mv.X-baseX, mv.Y-baseY
				entropy.EncodeMVResidual(ec, ctx, deltaX, deltaY, true)
				entropy.EncodeMVResidual(ec, ctx,
					mv2.X-compoundCtx.BaseX[1], mv2.Y-compoundCtx.BaseY[1], true)
			} else {
				ec.SymbolAdaptDav1d(6, ctx.CompInterModeCDF[compoundCtx.Mode][:], 7)
				mv, mv2 = me.MV{}, me.MV{}
			}
			refSlot, refFrame, refOrder = fe.refSlot, 1, 0
		}
	}
	if !useCompound {
		backwardGroup := refOrder >= 4
		ec.BoolAdapt(boolSymbol(backwardGroup), ctx.RefCDF[0][ic.Ref][:])
		if backwardGroup {
			altref := refOrder == 6
			ec.BoolAdapt(boolSymbol(altref), ctx.RefCDF[1][ic.Ref2][:])
			if !altref {
				ec.BoolAdapt(boolSymbol(refOrder == 5), ctx.RefCDF[5][ic.Ref6][:])
			}
		} else {
			olderGroup := refOrder >= 2
			ec.BoolAdapt(boolSymbol(olderGroup), ctx.RefCDF[2][ic.Ref3][:])
			if olderGroup {
				ec.BoolAdapt(boolSymbol(refOrder == 3), ctx.RefCDF[4][ic.Ref5][:])
			} else {
				ec.BoolAdapt(boolSymbol(refOrder == 1), ctx.RefCDF[3][ic.Ref4][:])
			}
		}

		switch {
		case ic.CandidateCount > 0 && mv.X == ic.BaseMVX && mv.Y == ic.BaseMVY:
			interMode = tile.InterModeNearestMV
			baseX, baseY = ic.BaseMVX, ic.BaseMVY
			ec.BoolAdapt(1, ctx.NewMVModeCDF[ic.NewMV][:])
			ec.BoolAdapt(1, ctx.GlobalMVModeCDF[ic.GlobalMV][:])
			ec.BoolAdapt(0, ctx.RefMVModeCDF[ic.RefMV][:])
		case mv.X != 0 || mv.Y != 0:
			interMode = tile.InterModeNewMV
			baseX, baseY = ic.BaseMVX, ic.BaseMVY
			deltaX, deltaY = mv.X-baseX, mv.Y-baseY
			ec.BoolAdapt(0, ctx.NewMVModeCDF[ic.NewMV][:])
			if ic.CandidateCount > 1 {
				ec.BoolAdapt(0, ctx.DRLBitCDF[ic.DRL0][:])
			}
			entropy.EncodeMVResidual(ec, ctx, deltaX, deltaY, true)
		default:
			ec.BoolAdapt(1, ctx.NewMVModeCDF[ic.NewMV][:])
			ec.BoolAdapt(0, ctx.GlobalMVModeCDF[ic.GlobalMV][:])
		}
		if fe.EnableOBMC && obmcPresent {
			ec.BoolAdapt(boolSymbol(useOBMC), ctx.OBMCCDF[obmcBS][:])
		}
	}

	maxTx := uint8(transform.TX8x8)
	uvtx := uint8(transform.TX4x4)
	if bw == 16 {
		maxTx = transform.TX16x16
		uvtx = transform.TX8x8
	} else if bw == 32 {
		maxTx = transform.TX32x32
		uvtx = transform.TX16x16
	}
	fs.SetTxCtx(bx, by, bw, bh, maxTx, false, skip)
	fs.SetInterTxIntraCtx(bx, by, bw, bh)
	if !skip {
		for _, plane := range planes {
			fe.encodeInterPlane(ec, ctx, fs, st, plane)
		}
	} else {
		for _, plane := range planes {
			copyPredictedBlock(st.recon[plane.plane], st.w[plane.plane], st.h[plane.plane],
				plane.bx, plane.by, plane.size, plane.pred)
		}
		fs.SetCoefCtxBlock(0, bx, by, bw, bh, 0x40)
		if blockHasChroma(bx, by, bw, bh) {
			fs.SetCoefCtxBlock(1, bx/2, by/2, (bw+1)/2, (bh+1)/2, 0x40)
			fs.SetCoefCtxBlock(2, bx/2, by/2, (bw+1)/2, (bh+1)/2, 0x40)
		}
	}

	blk := tile.Av1Block{
		Intra: false, Skip: skip,
		InterMode: uint8(interMode),
		RefSlot:   int8(refSlot), RefFrame: int8(refFrame), RefOrder: int8(refOrder),
		Tx: maxTx, MaxYTx: maxTx, Uvtx: uvtx,
		BaseMV:  [2]int16{int16(baseY), int16(baseX)},
		DeltaMV: [2]int16{int16(deltaY), int16(deltaX)},
		MV:      [2]int16{int16(mv.Y), int16(mv.X)},
	}
	if useCompound {
		blk.Compound = true
		blk.InterMode = 6
		if compoundNew {
			blk.InterMode = 7
		}
		blk.RefSlot2 = int8(fe.refSlot2)
		blk.RefFrame2 = 2
		blk.RefOrder2 = 1
		blk.CompType = 2
		blk.MV2 = [2]int16{int16(mv2.Y), int16(mv2.X)}
	}
	blk.Bl, blk.Bs = tile.EncoderBlockGeometry(bw, bh)
	if blockHasChroma(bx, by, bw, bh) {
		fs.SetChromaBlockState(bx, by, bw, bh, blk)
	}
	fs.CommitInterBlock(bx, by, bw, bh, blk, 1, blockHasChroma(bx, by, bw, bh))
}

func (fe *FrameEncoder) refineInterCandidate(st *tileEncodeState, refs [3][]byte,
	bx, by, size int, seed me.MV, baseSyntaxBits float64,
) (me.MV, []*interPlaneEncode, float64) {
	bestMV := seed
	var bestPlanes []*interPlaneEncode
	bestCost := math.Inf(1)
	yTx, uvTx := uint8(transform.TX8x8), uint8(transform.TX4x4)
	if size == 16 {
		yTx, uvTx = transform.TX16x16, transform.TX8x8
	} else if size == 32 {
		yTx, uvTx = transform.TX32x32, transform.TX16x16
	}
	evaluate := func(mv me.MV) {
		planes := []*interPlaneEncode{
			fe.analyzeInterPlaneFrom(st, refs, 0, bx, by, size,
				yTx, mv.X, mv.Y),
		}
		if blockHasChroma(bx, by, size, size) {
			planes = append(planes,
				fe.analyzeInterPlaneFrom(st, refs, 1, bx/2, by/2, size/2,
					uvTx, mv.X, mv.Y),
				fe.analyzeInterPlaneFrom(st, refs, 2, bx/2, by/2, size/2,
					uvTx, mv.X, mv.Y))
		}
		cost := interPlanesRDOCost(st, planes,
			baseSyntaxBits+motionSyntaxBits(mv), fe.QIndex, fe.BitDepth)
		if cost < bestCost {
			bestMV, bestPlanes, bestCost = mv, planes, cost
		}
	}
	if fe.IntegerMEOnly {
		evaluate(seed)
		return bestMV, bestPlanes, bestCost
	}
	if size == 32 {
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				evaluate(me.MV{X: seed.X + dx, Y: seed.Y + dy})
			}
		}
		return bestMV, bestPlanes, bestCost
	}
	for dy := -28; dy <= 28; dy += 4 {
		for dx := -28; dx <= 28; dx += 4 {
			evaluate(me.MV{X: seed.X + dx, Y: seed.Y + dy})
		}
	}
	coarseBest := bestMV
	for dy := -3; dy <= 3; dy++ {
		for dx := -3; dx <= 3; dx++ {
			evaluate(me.MV{X: coarseBest.X + dx, Y: coarseBest.Y + dy})
		}
	}
	return bestMV, bestPlanes, bestCost
}

type interPlaneEncode struct {
	plane int
	bx    int
	by    int
	size  int
	tx    uint8
	pred  []byte
	coeff []int32
}

func (fe *FrameEncoder) analyzeInterPlane(st *tileEncodeState,
	plane, bx, by, size int, tx uint8, mvX, mvY int,
) *interPlaneEncode {
	return fe.analyzeInterPlaneFrom(
		st, fe.ref, plane, bx, by, size, tx, mvX, mvY)
}

func (fe *FrameEncoder) analyzeInterPlaneFrom(st *tileEncodeState, refs [3][]byte,
	plane, bx, by, size int, tx uint8, mvX, mvY int,
) *interPlaneEncode {
	ss := 0
	if plane > 0 {
		ss = 1
	}
	pred := tile.EncoderInterPrediction(refs[plane], st.w[plane], st.w[plane], st.h[plane],
		bx, by, size, size, mvX, mvY, ss, ss)
	return fe.analyzeInterPlanePrediction(st, plane, bx, by, size, tx, pred)
}

func (fe *FrameEncoder) analyzeCompoundInterPlane(st *tileEncodeState,
	plane, bx, by, size int, tx uint8, mv0, mv1 me.MV,
) *interPlaneEncode {
	ss := 0
	if plane > 0 {
		ss = 1
	}
	pred := tile.EncoderCompoundPredictionMV(
		fe.ref[plane], st.w[plane], st.w[plane], st.h[plane],
		fe.ref2[plane], st.w[plane], st.w[plane], st.h[plane],
		bx, by, size, size, mv0.X, mv0.Y, mv1.X, mv1.Y, ss, ss)
	return fe.analyzeInterPlanePrediction(st, plane, bx, by, size, tx, pred)
}

func (fe *FrameEncoder) refineCompoundCandidate(st *tileEncodeState,
	bx, by, size int,
) (me.MV, me.MV, []*interPlaneEncode, bool, float64) {
	search := func(ref []byte) me.MV {
		searchRange := fe.SearchRange
		if searchRange <= 0 {
			searchRange = 4
		}
		cfg := me.Config{
			Source: st.src[0], Reference: ref,
			SourceStride: fe.Width, ReferenceStride: fe.Width,
			Width: fe.Width, Height: fe.Height,
			X: bx, Y: by, BlockWidth: size, BlockHeight: size,
			SearchRange: searchRange, IntegerOnly: fe.IntegerMEOnly,
		}
		var result me.Result
		var err error
		if fe.HierarchicalME {
			result, err = me.SearchHierarchical(cfg)
		} else {
			result, err = me.Search(cfg)
		}
		if err != nil {
			return me.MV{}
		}
		return result.MV
	}
	mv0, mv1 := search(fe.ref[0]), search(fe.ref2[0])
	build := func(a, b me.MV) ([]*interPlaneEncode, float64) {
		planes := []*interPlaneEncode{
			fe.analyzeCompoundInterPlane(st, 0, bx, by, size,
				transform.TX8x8, a, b),
		}
		if blockHasChroma(bx, by, size, size) {
			planes = append(planes,
				fe.analyzeCompoundInterPlane(st, 1, bx/2, by/2, size/2,
					transform.TX4x4, a, b),
				fe.analyzeCompoundInterPlane(st, 2, bx/2, by/2, size/2,
					transform.TX4x4, a, b))
		}
		syntaxBits := 11 + motionSyntaxBits(a) + motionSyntaxBits(b)
		return planes, interPlanesRDOCost(
			st, planes, syntaxBits, fe.QIndex, fe.BitDepth)
	}
	bestPlanes, bestCost := build(mv0, mv1)
	offsets := []int{-2, -1, 0, 1, 2}
	if fe.IntegerMEOnly {
		offsets = []int{0}
	}
	for pass := 0; pass < 2; pass++ {
		seed := [2]me.MV{mv0, mv1}[pass]
		for _, dy := range offsets {
			for _, dx := range offsets {
				candidate := me.MV{X: seed.X + dx, Y: seed.Y + dy}
				a, b := mv0, mv1
				if pass == 0 {
					a = candidate
				} else {
					b = candidate
				}
				planes, cost := build(a, b)
				if cost < bestCost {
					mv0, mv1, bestPlanes, bestCost = a, b, planes, cost
				}
			}
		}
	}
	globalPlanes, globalCost := build(me.MV{}, me.MV{})
	// GLOBAL_GLOBAL has no DRL or MV residuals.
	globalCost = interPlanesRDOCost(
		st, globalPlanes, 7, fe.QIndex, fe.BitDepth)
	if globalCost <= bestCost {
		return me.MV{}, me.MV{}, globalPlanes, false, globalCost
	}
	return mv0, mv1, bestPlanes, true, bestCost
}

func (fe *FrameEncoder) analyzeSkipInterPlane(st *tileEncodeState,
	plane, bx, by, size, mvX, mvY int,
) *interPlaneEncode {
	ss := 0
	if plane > 0 {
		ss = 1
	}
	pred := tile.EncoderInterPrediction(fe.ref[plane], st.w[plane], st.w[plane], st.h[plane],
		bx, by, size, size, mvX, mvY, ss, ss)
	return &interPlaneEncode{
		plane: plane, bx: bx, by: by, size: size, pred: pred,
	}
}

func (fe *FrameEncoder) shouldUseLargeInterBlock(st *tileEncodeState, bx, by, size int) bool {
	if (size != 16 && size != 32) || bx+size > fe.Width || by+size > fe.Height {
		return false
	}
	searchRange := fe.SearchRange
	if searchRange <= 0 {
		searchRange = 4
	}
	baseConfig := me.Config{
		Source: st.src[0], Reference: fe.ref[0],
		SourceStride: fe.Width, ReferenceStride: fe.Width,
		Width: fe.Width, Height: fe.Height,
		X: bx, Y: by, BlockWidth: size, BlockHeight: size,
		SearchRange: searchRange, IntegerOnly: fe.IntegerMEOnly,
	}
	search := func(cfg me.Config) (me.Result, error) {
		if fe.HierarchicalME {
			return me.SearchHierarchical(cfg)
		}
		return me.Search(cfg)
	}
	referenceOrders := make([]int, 0, 4)
	seenSlots := make(map[int]bool)
	for order := 0; order <= 6; order++ {
		slot := int(fe.refIdx[order])
		if seenSlots[slot] || len(fe.refs[slot][0]) == 0 {
			continue
		}
		seenSlots[slot] = true
		referenceOrders = append(referenceOrders, order)
	}

	sharedCost := math.Inf(1)
	var bestShared largeInterCandidate
	for _, order := range referenceOrders {
		slot := int(fe.refIdx[order])
		cfg := baseConfig
		cfg.Reference = fe.refs[slot][0]
		result, err := search(cfg)
		if err != nil {
			continue
		}
		refs := fe.refs[slot]
		refinedMV, planes, cost := fe.refineInterCandidate(
			st, refs, bx, by, size, result.MV, 5)
		if cost < sharedCost {
			sharedCost = cost
			bestShared = largeInterCandidate{
				mv: refinedMV, planes: planes,
				refSlot: slot, refFrame: order + 1, refOrder: order,
			}
		}
	}
	if math.IsInf(sharedCost, 1) {
		return false
	}
	splitSize := size / 2
	splitCost := 0.0
	for subY := by; subY < by+size; subY += splitSize {
		for subX := bx; subX < bx+size; subX += splitSize {
			bestSubCost := math.Inf(1)
			for _, order := range referenceOrders {
				slot := int(fe.refIdx[order])
				subCfg := baseConfig
				subCfg.Reference = fe.refs[slot][0]
				subCfg.X, subCfg.Y = subX, subY
				subCfg.BlockWidth, subCfg.BlockHeight = splitSize, splitSize
				subResult, err := search(subCfg)
				if err != nil {
					continue
				}
				refs := fe.refs[slot]
				_, _, cost := fe.refineInterCandidate(
					st, refs, subX, subY, splitSize, subResult.MV, 4)
				if cost < bestSubCost {
					bestSubCost = cost
				}
			}
			if math.IsInf(bestSubCost, 1) {
				return false
			}
			splitCost += bestSubCost
		}
	}
	threshold := 1.0
	if size == 32 && !interPlanesAllZero(bestShared.planes) {
		threshold = 0.70
	}
	if sharedCost >= splitCost*threshold {
		return false
	}
	st.large[[2]int{bx, by}] = bestShared
	return true
}

func (fe *FrameEncoder) shouldUseLargeIntraBlock(st *tileEncodeState, bx, by, size int) bool {
	if size != 16 || bx+size > fe.Width || by+size > fe.Height {
		return false
	}
	shared := fe.analyzeIntraBlock(st, bx, by, size)
	sharedCost := interPlanesRDOCost(st, shared, 4, fe.QIndex, fe.BitDepth)

	// The lower and right 8x8 candidates depend on the already reconstructed
	// neighbours. Simulate the split in coding order so its RDO comparison
	// sees the same edges that the real encoder will use.
	sim := &tileEncodeState{src: st.src, w: st.w, h: st.h}
	for plane := range sim.recon {
		sim.recon[plane] = append([]byte(nil), st.recon[plane]...)
	}
	splitCost := rdo.Lambda(fe.QIndex, fe.BitDepth)
	for subY := by; subY < by+size; subY += 8 {
		for subX := bx; subX < bx+size; subX += 8 {
			planes := fe.analyzeIntraBlock(sim, subX, subY, 8)
			splitCost += interPlanesRDOCost(sim, planes, 3, fe.QIndex, fe.BitDepth)
			for _, plane := range planes {
				reconstructDCTBlock(sim.recon[plane.plane], sim.w[plane.plane],
					sim.h[plane.plane], plane.bx, plane.by, plane.size,
					plane.pred, plane.coeff, plane.tx, fe.QIndex, fe.BitDepth)
			}
		}
	}
	// Partition-rate estimates are deliberately approximate; require a clear
	// win before replacing four independently predicted 8x8 blocks.
	return sharedCost < splitCost*0.90
}

func (fe *FrameEncoder) analyzeIntraBlock(st *tileEncodeState,
	bx, by, size int,
) []*interPlaneEncode {
	yMode := fe.chooseLumaMode(st, bx, by, size)
	yTx, uvTx := uint8(transform.TX8x8), uint8(transform.TX4x4)
	if size == 16 {
		yTx, uvTx = transform.TX16x16, transform.TX8x8
	}
	yPred := intraPredBlock(st.recon[0], st.w[0], st.h[0],
		bx, by, size, size, yMode)
	planes := []*interPlaneEncode{
		fe.analyzeInterPlanePrediction(st, 0, bx, by, size, yTx, yPred),
	}
	if blockHasChroma(bx, by, size, size) {
		chromaSize := size / 2
		for plane := 1; plane <= 2; plane++ {
			pred := intraPredBlock(st.recon[plane], st.w[plane], st.h[plane],
				bx/2, by/2, chromaSize, chromaSize, tile.DCPred)
			planes = append(planes, fe.analyzeInterPlanePrediction(st, plane,
				bx/2, by/2, chromaSize, uvTx, pred))
		}
	}
	return planes
}

func motionSyntaxBits(mv me.MV) float64 {
	if mv.X == 0 && mv.Y == 0 {
		return 2
	}
	return 6 + math.Log2(float64(abs(mv.X)+abs(mv.Y)+2))
}

func (fe *FrameEncoder) analyzeInterPlanePrediction(st *tileEncodeState,
	plane, bx, by, size int, tx uint8, pred []byte,
) *interPlaneEncode {
	residual := make([]int16, size*size)
	for y := 0; y < size; y++ {
		srcY := min(by+y, st.h[plane]-1)
		for x := 0; x < size; x++ {
			srcX := min(bx+x, st.w[plane]-1)
			i := y*size + x
			residual[i] = int16(int(st.src[plane][srcY*st.w[plane]+srcX]) - int(pred[i]))
		}
	}
	coeff := make([]int32, size*size)
	switch size {
	case 32:
		encodertx.FwdDCT32x32(coeff, residual, size)
	case 16:
		encodertx.FwdDCT16x16(coeff, residual, size)
	case 8:
		encodertx.FwdDCT8x8(coeff, residual, size)
	default:
		encodertx.FwdDCT4x4(coeff, residual, size)
	}
	encodertx.QuantizeTx(coeff, fe.QIndex, fe.BitDepth, tx)
	return &interPlaneEncode{
		plane: plane, bx: bx, by: by, size: size, tx: tx,
		pred: pred, coeff: coeff,
	}
}

func interPredictionSSE(st *tileEncodeState, plane *interPlaneEncode) int64 {
	var sse int64
	for y := 0; y < plane.size; y++ {
		srcY := min(plane.by+y, st.h[plane.plane]-1)
		for x := 0; x < plane.size; x++ {
			srcX := min(plane.bx+x, st.w[plane.plane]-1)
			d := int(st.src[plane.plane][srcY*st.w[plane.plane]+srcX]) -
				int(plane.pred[y*plane.size+x])
			sse += int64(d * d)
		}
	}
	return sse
}

func interPlanesRDOCost(st *tileEncodeState, planes []*interPlaneEncode,
	syntaxBits float64, qindex, bitDepth int,
) float64 {
	cost := rdo.Lambda(qindex, bitDepth) * syntaxBits
	for _, plane := range planes {
		cost += rdo.Cost(interReconstructionSSE(st, plane, qindex, bitDepth),
			plane.coeff, 0, qindex, bitDepth)
	}
	return cost
}

func interReconstructionSSE(st *tileEncodeState, plane *interPlaneEncode,
	qindex, bitDepth int,
) int64 {
	recon := reconstructDCTPixels(
		plane.pred, plane.coeff, plane.size, plane.tx, qindex, bitDepth)
	var sse int64
	for y := 0; y < plane.size; y++ {
		srcY := min(plane.by+y, st.h[plane.plane]-1)
		for x := 0; x < plane.size; x++ {
			srcX := min(plane.bx+x, st.w[plane.plane]-1)
			d := int(st.src[plane.plane][srcY*st.w[plane.plane]+srcX]) -
				int(recon[y*plane.size+x])
			sse += int64(d * d)
		}
	}
	return sse
}

func (fe *FrameEncoder) encodeInterPlane(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx,
	fs *tile.FrameState, st *tileEncodeState, plane *interPlaneEncode,
) {
	if plane.plane == 0 {
		if plane.tx == transform.TX32x32 {
			entropy.EncodeInterDCT32(ec, ctx, fs, plane.bx, plane.by, plane.coeff)
		} else if plane.tx == transform.TX16x16 {
			entropy.EncodeInterDCT16(ec, ctx, fs, plane.bx, plane.by, plane.coeff)
		} else {
			entropy.EncodeInterDCT8(ec, ctx, fs, plane.bx, plane.by, plane.coeff)
		}
	} else {
		if plane.tx == transform.TX16x16 {
			entropy.EncodeInterDCT16Plane(ec, ctx, fs, plane.plane, plane.bx, plane.by, plane.coeff)
		} else if plane.tx == transform.TX8x8 {
			entropy.EncodeInterDCT8Plane(ec, ctx, fs, plane.plane, plane.bx, plane.by, plane.coeff)
		} else {
			entropy.EncodeInterDCT4(ec, ctx, fs, plane.plane, plane.bx, plane.by, plane.coeff)
		}
	}
	reconstructDCTBlock(st.recon[plane.plane], st.w[plane.plane], st.h[plane.plane],
		plane.bx, plane.by, plane.size, plane.pred, plane.coeff, plane.tx, fe.QIndex, fe.BitDepth)
}

func interPlanesAllZero(planes []*interPlaneEncode) bool {
	if len(planes) == 0 {
		return false
	}
	for _, plane := range planes {
		for _, coeff := range plane.coeff {
			if coeff != 0 {
				return false
			}
		}
	}
	return true
}

func copyPredictedBlock(dst []byte, width, height, bx, by, size int, pred []byte) {
	for y := 0; y < size && by+y < height; y++ {
		n := min(size, width-bx)
		copy(dst[(by+y)*width+bx:(by+y)*width+bx+n], pred[y*size:y*size+n])
	}
}

func (fe *FrameEncoder) encodePlaneDC(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx,
	fs *tile.FrameState, st *tileEncodeState, plane, bx, by, bw, bh int, tx uint8, mode int,
) {
	predBlock := intraPredBlock(st.recon[plane], st.w[plane], st.h[plane], bx, by, bw, bh, mode)
	pred := int(predBlock[0])
	mean := blockMean(st.src[plane], st.w[plane], st.h[plane], bx, by, bw, bh, pred)
	if (plane == 0 && (tx == transform.TX8x8 || tx == transform.TX16x16)) ||
		(plane > 0 && (tx == transform.TX4x4 || tx == transform.TX8x8)) {
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
		switch size {
		case 16:
			encodertx.FwdDCT16x16(coeff, residual, size)
		case 8:
			encodertx.FwdDCT8x8(coeff, residual, size)
		default:
			encodertx.FwdDCT4x4(coeff, residual, size)
		}
		encodertx.QuantizeTx(coeff, fe.QIndex, fe.BitDepth, tx)
		if plane == 0 {
			if tx == transform.TX16x16 {
				entropy.EncodeDCT16(ec, ctx, fs, bx, by, mode, coeff)
			} else {
				entropy.EncodeDCT8(ec, ctx, fs, bx, by, mode, coeff)
			}
		} else {
			if tx == transform.TX8x8 {
				entropy.EncodeDCT8Plane(ec, ctx, fs, plane, bx, by, coeff)
			} else {
				entropy.EncodeDCT4(ec, ctx, fs, plane, bx, by, coeff)
			}
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
	block := reconstructDCTPixels(pred, qcoeff, size, tx, qindex, hbd)
	for y := 0; y < size && by+y < height; y++ {
		n := min(size, width-bx)
		copy(dst[(by+y)*width+bx:(by+y)*width+bx+n], block[y*size:y*size+n])
	}
}

func reconstructDCTPixels(pred []byte, qcoeff []int32, size int,
	tx uint8, qindex, hbd int,
) []byte {
	coeff := append([]int32(nil), qcoeff...)
	encodertx.DequantizeTx(coeff, qindex, hbd, tx)
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
	} else if tx == transform.TX16x16 || tx == transform.TX32x32 {
		shift = 2
	}
	transform.InvTxfmAdd(block, size, packed, size*size-1, tx, shift, transform.DCT_DCT, 8)
	return block
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
	bestCost := float64(^uint64(0) >> 1)
	for _, mode := range modes {
		pred := intraPredBlock(st.recon[0], st.w[0], st.h[0], bx, by, size, size, mode)
		var sse int64
		residual := make([]int16, size*size)
		for y := 0; y < size; y++ {
			srcY := min(by+y, st.h[0]-1)
			for x := 0; x < size; x++ {
				srcX := min(bx+x, st.w[0]-1)
				diff := int(st.src[0][srcY*st.w[0]+srcX]) - int(pred[y*size+x])
				sse += int64(diff * diff)
				residual[y*size+x] = int16(diff)
			}
		}
		coeff := make([]int32, size*size)
		if size == 16 {
			encodertx.FwdDCT16x16(coeff, residual, size)
		} else {
			encodertx.FwdDCT8x8(coeff, residual, size)
		}
		encodertx.Quantize(coeff, fe.QIndex, fe.BitDepth)
		syntaxBits := 1.0
		if mode != tile.DCPred {
			syntaxBits = 3
		}
		cost := rdo.Cost(sse, coeff, syntaxBits, fe.QIndex, fe.BitDepth)
		if cost < bestCost {
			bestCost = cost
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
