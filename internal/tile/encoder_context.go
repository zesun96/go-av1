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
	Ref3           int
	Ref4           int
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
	fhdr := &header.FrameHeader{}
	for i := range fhdr.Refidx {
		fhdr.Refidx[i] = int8(refIdx[i] & 7)
	}
	newMV, globalMV, refMV := singleRefModeContexts(fs, fhdr, nil, refSlot, 1, bx, by, bw, bh)
	out := EncoderSingleRefContexts{
		Intra:    intraCtx(fs, bx, by),
		Ref:      refCtx(fs, fhdr, bx, by),
		Ref3:     ref3Ctx(fs, fhdr, bx, by),
		Ref4:     ref4Ctx(fs, fhdr, bx, by),
		NewMV:    newMV,
		GlobalMV: globalMV,
		RefMV:    refMV,
	}
	count, stack := singleRefInterCandidates(fs, fhdr, nil, refSlot, 1, bx, by, bw, bh)
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
	return EncoderCompoundContexts{
		Flag:  compoundFlagContext(fs, bx, by),
		Dir:   compoundDirContext(fs, fhdr, bx, by),
		Ref:   refCtx(fs, fhdr, bx, by),
		UniP1: uniP1Context(fs, fhdr, bx, by),
		Mode:  compoundInterModeContext(fs, fhdr, 1, 2, bx, by, bw, bh),
	}
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
	out := make([]byte, bw*bh)
	for y := 0; y < bh; y++ {
		y0 := minInt(maxInt(by+y, 0), height0-1)
		y1 := minInt(maxInt(by+y, 0), height1-1)
		for x := 0; x < bw; x++ {
			x0 := minInt(maxInt(bx+x, 0), width0-1)
			x1 := minInt(maxInt(bx+x, 0), width1-1)
			a := int(ref0[y0*stride0+x0])
			b := int(ref1[y1*stride1+x1])
			out[y*bw+x] = byte((a + b + 1) >> 1)
		}
	}
	return out
}
