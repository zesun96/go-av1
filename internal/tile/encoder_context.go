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
	fhdr := &header.FrameHeader{}
	for i := range fhdr.Refidx {
		fhdr.Refidx[i] = 0
	}
	newMV, globalMV, refMV := singleRefModeContexts(fs, fhdr, nil, 0, 1, bx, by, bw, bh)
	out := EncoderSingleRefContexts{
		Intra:    intraCtx(fs, bx, by),
		Ref:      refCtx(fs, fhdr, bx, by),
		Ref3:     ref3Ctx(fs, fhdr, bx, by),
		Ref4:     ref4Ctx(fs, fhdr, bx, by),
		NewMV:    newMV,
		GlobalMV: globalMV,
		RefMV:    refMV,
	}
	count, stack := singleRefInterCandidates(fs, fhdr, nil, 0, 1, bx, by, bw, bh)
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
