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
	Intra    int
	Ref      int
	Ref3     int
	Ref4     int
	NewMV    int
	GlobalMV int
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
	newMV, globalMV, _ := singleRefModeContexts(fs, fhdr, nil, 0, 1, bx, by, bw, bh)
	return EncoderSingleRefContexts{
		Intra:    intraCtx(fs, bx, by),
		Ref:      refCtx(fs, fhdr, bx, by),
		Ref3:     ref3Ctx(fs, fhdr, bx, by),
		Ref4:     ref4Ctx(fs, fhdr, bx, by),
		NewMV:    newMV,
		GlobalMV: globalMV,
	}
}

// EncoderBlockGeometry returns the block level and block-size enum used by the
// decoder when it commits a block with the supplied dimensions.
func EncoderBlockGeometry(bw, bh int) (bl, bs uint8) {
	return uint8(blockLevelFromDim(bw, bh)), uint8(maxInt(bsizeFromDim(bw, bh), 0))
}
