package tile

import (
	"github.com/zesun96/go-av1/internal/header"
	"github.com/zesun96/go-av1/internal/refmvs"
)

// EncoderSingleRefContexts contains the adaptive contexts needed to encode a
// single-reference LAST_FRAME inter block. Keeping this derivation beside the
// decoder prevents the encoder and decoder reference-neighbour rules from
// drifting apart.
type EncoderSingleRefContexts struct {
	Intra          int
	Ref            int
	Ref2           int
	Ref3           int
	Ref4           int
	Ref5           int
	Ref6           int
	NewMV          int
	GlobalMV       int
	RefMV          int
	CandidateCount int
	DRL0           int
	BaseMVX        int
	BaseMVY        int
}

// EncoderCompoundContexts contains the adaptive contexts for a unidirectional
// LAST_FRAME + LAST2_FRAME compound block.
type EncoderCompoundContexts struct {
	Flag  int
	Dir   int
	Ref   int
	UniP1 int
	Mode  int
	DRL0  int
	BaseX [2]int
	BaseY [2]int
}

// EnableEncoderMVContexts initializes the reference-MV grids used by inter
// syntax context derivation.
func EnableEncoderMVContexts(fs *FrameState, width, height int) {
	if fs != nil {
		fs.MVFrame = refmvs.NewFrame(width, height)
	}
}

// SingleRefEncoderContexts derives contexts for a block that selects reference
// slot zero as LAST_FRAME. It uses the already committed blocks in fs.
func SingleRefEncoderContexts(fs *FrameState, bx, by, bw, bh int) EncoderSingleRefContexts {
	return SingleRefEncoderContextsForSlot(fs, [7]uint8{}, 0, bx, by, bw, bh)
}

// SingleRefEncoderContextsForSlot derives LAST_FRAME contexts for an explicit
// frame-header reference map and physical reference slot.
func SingleRefEncoderContextsForSlot(fs *FrameState, refIdx [7]uint8, refSlot,
	bx, by, bw, bh int,
) EncoderSingleRefContexts {
	return SingleRefEncoderContextsForReference(
		fs, refIdx, refSlot, 1, bx, by, bw, bh)
}

// SingleRefEncoderContextsForReference derives contexts for an explicit AV1
// reference type (LAST_FRAME=1, LAST2_FRAME=2, and so on).
func SingleRefEncoderContextsForReference(fs *FrameState, refIdx [7]uint8,
	refSlot, refFrame, bx, by, bw, bh int,
) EncoderSingleRefContexts {
	fhdr := &header.FrameHeader{}
	for i := range fhdr.Refidx {
		fhdr.Refidx[i] = int8(refIdx[i] & 7)
	}
	newMV, globalMV, refMV := singleRefModeContexts(
		fs, fhdr, nil, refSlot, refFrame, bx, by, bw, bh)
	out := EncoderSingleRefContexts{
		Intra:    intraCtx(fs, bx, by),
		Ref:      refCtx(fs, fhdr, bx, by),
		Ref2:     ref2Ctx(fs, fhdr, bx, by),
		Ref3:     ref3Ctx(fs, fhdr, bx, by),
		Ref4:     ref4Ctx(fs, fhdr, bx, by),
		Ref5:     ref5Ctx(fs, fhdr, bx, by),
		Ref6:     ref6Ctx(fs, fhdr, bx, by),
		NewMV:    newMV,
		GlobalMV: globalMV,
		RefMV:    refMV,
	}
	count, stack := singleRefInterCandidates(
		fs, fhdr, nil, refSlot, refFrame, bx, by, bw, bh)
	out.CandidateCount = count
	if out.CandidateCount > 0 {
		out.BaseMVY = int(stack[0].mv.Y)
		out.BaseMVX = int(stack[0].mv.X)
	}
	if out.CandidateCount > 1 {
		out.DRL0 = refmvs.DRLContext(candidateWeights(stack, out.CandidateCount), 0)
	}
	return out
}

// CompoundEncoderContexts derives contexts for LAST + LAST2 average compound.
func CompoundEncoderContexts(fs *FrameState, refIdx [7]uint8,
	bx, by, bw, bh int,
) EncoderCompoundContexts {
	fhdr := &header.FrameHeader{SwitchableCompRefs: 1}
	for i := range fhdr.Refidx {
		fhdr.Refidx[i] = int8(refIdx[i] & 7)
	}
	out := EncoderCompoundContexts{
		Flag:  compoundFlagContext(fs, bx, by),
		Dir:   compoundDirContext(fs, fhdr, bx, by),
		Ref:   refCtx(fs, fhdr, bx, by),
		UniP1: uniP1Context(fs, fhdr, bx, by),
		Mode:  compoundInterModeContext(fs, fhdr, 1, 2, bx, by, bw, bh),
	}
	slots := [2]int{int(refIdx[0] & 7), int(refIdx[1] & 7)}
	counts := [2]int{}
	stacks := [2][8]interCandidate{}
	for i := range counts {
		counts[i], stacks[i] = singleRefInterCandidates(
			fs, fhdr, nil, slots[i], i+1, bx, by, bw, bh)
	}
	var pairs refmvs.SearchResult
	if fs != nil && fs.MVFrame != nil {
		pairs = refmvs.Find(refmvs.SearchConfig{
			Frame: fs.MVFrame, Ref: 1, Ref2: 2,
			TargetSlot: slots[0], TargetSlot2: slots[1],
			Bx4: bx >> 2, By4: by >> 2,
			Bw4: (bw + 3) >> 2, Bh4: (bh + 3) >> 2,
			TileX0: fs.TileX0 >> 2, TileY0: fs.TileY0 >> 2,
			TileX1: fs.TileX1 >> 2, TileY1: fs.TileY1 >> 2,
			TopRightKnown:     fs.RefMVTopRightKnown,
			TopRightAvailable: fs.RefMVTopRightAvailable,
			BlockDims:         refMVBlockDims[:],
		})
	}
	baseCount := pairs.Count
	for pairs.Count < 2 {
		n := pairs.Count
		extIdx := n - baseCount
		var pair refmvs.MVPair
		for i := range pair {
			if counts[i] > extIdx {
				pair[i] = stacks[i][extIdx].mv
			} else if counts[i] > 0 {
				pair[i] = stacks[i][counts[i]-1].mv
			}
		}
		pairs.Candidates[n] = refmvs.Candidate{MV: pair, Weight: 2}
		pairs.Count++
	}
	if pairs.Count > 0 {
		for i := range out.BaseX {
			out.BaseX[i] = int(pairs.Candidates[0].MV[i].X)
			out.BaseY[i] = int(pairs.Candidates[0].MV[i].Y)
		}
	}
	if pairs.Count > 1 {
		weights := make([]int, pairs.Count)
		for i := range weights {
			weights[i] = pairs.Candidates[i].Weight
		}
		out.DRL0 = refmvs.DRLContext(weights, 0)
	}
	return out
}

// EncoderBlockGeometry returns the block level and block-size enum used by the
// decoder when it commits a block with the supplied dimensions.
func EncoderBlockGeometry(bw, bh int) (bl, bs uint8) {
	return uint8(blockLevelFromDim(bw, bh)), uint8(maxInt(bsizeFromDim(bw, bh), 0))
}

// EncoderInterPrediction builds the same regular 8-tap translational
// prediction used by the decoder.
func EncoderInterPrediction(ref []byte, stride, width, height, bx, by, bw, bh,
	mvX, mvY, ssHor, ssVer int,
) []byte {
	return makeInterPredictionPlane(ref, stride, width, height, bx, by, bw, bh,
		refmvs.MV{X: int16(mvX), Y: int16(mvY)},
		header.FilterMode8TapRegular, header.FilterMode8TapRegular, ssHor, ssVer)
}

// EncoderCompoundPrediction builds the decoder-identical equal-weight average
// of two zero-motion regular-filter reference predictions.
func EncoderCompoundPrediction(ref0 []byte, stride0, width0, height0 int,
	ref1 []byte, stride1, width1, height1 int,
	bx, by, bw, bh, ssHor, ssVer int,
) []byte {
	return EncoderCompoundPredictionMV(ref0, stride0, width0, height0,
		ref1, stride1, width1, height1, bx, by, bw, bh,
		0, 0, 0, 0, ssHor, ssVer)
}

// EncoderCompoundPredictionMV builds an equal-weight compound prediction with
// independent regular 8-tap translational motion vectors.
func EncoderCompoundPredictionMV(ref0 []byte, stride0, width0, height0 int,
	ref1 []byte, stride1, width1, height1 int,
	bx, by, bw, bh, mv0X, mv0Y, mv1X, mv1Y, ssHor, ssVer int,
) []byte {
	pred0 := makeInterPredictionPlane(ref0, stride0, width0, height0,
		bx, by, bw, bh, refmvs.MV{X: int16(mv0X), Y: int16(mv0Y)},
		header.FilterMode8TapRegular, header.FilterMode8TapRegular, ssHor, ssVer)
	pred1 := makeInterPredictionPlane(ref1, stride1, width1, height1,
		bx, by, bw, bh, refmvs.MV{X: int16(mv1X), Y: int16(mv1Y)},
		header.FilterMode8TapRegular, header.FilterMode8TapRegular, ssHor, ssVer)
	out := make([]byte, bw*bh)
	for i := range out {
		out[i] = byte((int(pred0[i]) + int(pred1[i]) + 1) >> 1)
	}
	return out
}

// EncoderOBMCContext reports whether an 8x8-or-larger single-reference block
// carries motion_mode syntax and returns the OBMC CDF block-size context.
func EncoderOBMCContext(fs *FrameState, bx, by, bw, bh, refSlot int) (bool, int) {
	bs := bsizeFromDim(bw, bh)
	if bs < 0 || minInt((bw+3)>>2, (bh+3)>>2) < 2 {
		return false, 0
	}
	overlap, _ := motionModeNeighbours(fs, bx, by, bw, bh, refSlot)
	return overlap, bs
}

// EncoderOBMCPrediction applies the decoder's luma OBMC blend to an existing
// single-reference prediction. The current encoder uses 8x8 coding blocks, for
// which AV1 does not apply chroma OBMC.
func EncoderOBMCPrediction(base []byte, fs *FrameState, refs [8][3][]byte,
	width, height, bx, by, bw, bh int,
) []byte {
	out := append([]byte(nil), base...)
	if fs == nil || fs.MVFrame == nil || len(out) < bw*bh || bw <= 0 || bh <= 0 {
		return out
	}
	bw4, bh4 := (bw+3)>>2, (bh+3)>>2
	if by > fs.TileY0 {
		for i, x4 := 0, 0; x4 < bw4 && i < minInt(int(BlockDimensions[bsizeFromDim(bw, bh)][2]), 4); {
			blk, ok := fs.BlockState(bx+(x4+1)*4, by-4)
			if !ok || blk.Intra || int(blk.RefSlot) < 0 || int(blk.RefSlot) >= len(refs) ||
				len(refs[blk.RefSlot][0]) == 0 {
				x4 += 2
				continue
			}
			step4 := clampInt(int(BlockDimensions[blk.Bs][0]), 2, 16)
			ow4, oh4 := minInt(step4, bw4), minInt(bh4, 16)>>1
			predH := ((oh4*3 + 3) >> 2) * 4
			pred := EncoderInterPrediction(refs[blk.RefSlot][0], width, width, height,
				bx+x4*4, by, ow4*4, predH, int(blk.MV[1]), int(blk.MV[0]), 0, 0)
			blendOBMCH(out, bw, bw, bh, x4*4, 0, pred, ow4*4, oh4*4)
			i++
			x4 += step4
		}
	}
	if bx > fs.TileX0 {
		for i, y4 := 0, 0; y4 < bh4 && i < minInt(int(BlockDimensions[bsizeFromDim(bw, bh)][3]), 4); {
			blk, ok := fs.BlockState(bx-4, by+(y4+1)*4)
			if !ok || blk.Intra || int(blk.RefSlot) < 0 || int(blk.RefSlot) >= len(refs) ||
				len(refs[blk.RefSlot][0]) == 0 {
				y4 += 2
				continue
			}
			step4 := clampInt(int(BlockDimensions[blk.Bs][1]), 2, 16)
			ow4, oh4 := minInt(bw4, 16)>>1, minInt(step4, bh4)
			pred := EncoderInterPrediction(refs[blk.RefSlot][0], width, width, height,
				bx, by+y4*4, ow4*4, oh4*4, int(blk.MV[1]), int(blk.MV[0]), 0, 0)
			blendOBMCV(out, bw, bw, bh, 0, y4*4, pred, ow4*4, oh4*4)
			i++
			y4 += step4
		}
	}
	return out
}
