package tile

import (
	"testing"

	"github.com/zesun96/go-av1/internal/bitstream"
	"github.com/zesun96/go-av1/internal/header"
	"github.com/zesun96/go-av1/internal/refmvs"
	"github.com/zesun96/go-av1/internal/transform"
)

func TestFrameStateCoefSkipCtxUsesMergedResidualLowBits(t *testing.T) {
	fs := NewFrameState(64, 64)
	bx, by := 0, 16

	// dav1d merges neighbour res_ctx bytes with bitwise OR before clamping the
	// low 6 bits into dav1d_skip_ctx[5][5].
	fs.AboveLCoef[0] = 0x41
	fs.AboveLCoef[1] = 0x44
	fs.LeftLCoef[by>>2] = 0x42

	got := fs.CoefSkipCtx(0, bx, by, 32, 16, transform.TX16x16)
	want := int(DAV1DSkipCtx[4][2])
	if got != want {
		t.Fatalf("CoefSkipCtx luma = %d, want %d", got, want)
	}
}

func TestFrameStateCoefSkipCtxSingleTransformBlockIsZero(t *testing.T) {
	fs := NewFrameState(64, 64)
	got := fs.CoefSkipCtx(0, 0, 0, 16, 16, transform.TX16x16)
	if got != 0 {
		t.Fatalf("CoefSkipCtx single tx block = %d, want 0", got)
	}
}

func TestFrameStateTxCtxUsesNeighbourTransformLogs(t *testing.T) {
	fs := NewFrameState(64, 64)

	fs.SetTxCtx(0, 0, 32, 16, transform.TX16x16, true, false)
	got := fs.TxCtx(0, 16, transform.TX16x16)
	if got != 1 {
		t.Fatalf("TxCtx top-only = %d, want 1", got)
	}

	fs.SetTxCtx(0, 16, 16, 32, transform.TX16x16, true, false)
	got = fs.TxCtx(16, 16, transform.TX16x16)
	if got != 2 {
		t.Fatalf("TxCtx left-only = %d, want 2", got)
	}
}

func TestFrameStateCoefSkipCtxChromaUsesSubsampling(t *testing.T) {
	fs444 := NewFrameState(64, 64)
	fs444.SetSubsampling(0, 0)
	got444 := fs444.CoefSkipCtx(1, 0, 0, 16, 16, transform.TX8x8)
	if got444 != 10 {
		t.Fatalf("CoefSkipCtx 4:4:4 = %d, want 10", got444)
	}

	fs420 := NewFrameState(64, 64)
	fs420.SetSubsampling(1, 1)
	got420 := fs420.CoefSkipCtx(1, 0, 0, 8, 8, transform.TX8x8)
	if got420 != 7 {
		t.Fatalf("CoefSkipCtx 4:2:0 = %d, want 7", got420)
	}
}

func TestFrameStateSetCoefCtxClipsToChromaPlaneBounds(t *testing.T) {
	fs := NewFrameState(10, 10)
	fs.SetSubsampling(1, 1)

	fs.SetCoefCtx(1, 4, 0, transform.TX8x8, 0x55)

	if fs.AboveCCoef[0][1] != 0x55 {
		t.Fatalf("AboveCCoef[1] = 0x%x, want 0x55", fs.AboveCCoef[0][1])
	}
	if fs.AboveCCoef[0][2] != 0x40 {
		t.Fatalf("AboveCCoef[2] = 0x%x, want neutral 0x40", fs.AboveCCoef[0][2])
	}
}

func TestGetUVInterTxtp(t *testing.T) {
	if got := getUVInterTxtp(transform.TxfmDimensions[transform.TX32x32], transform.IDTX); got != transform.IDTX {
		t.Fatalf("TX32x32 IDTX = %d, want IDTX", got)
	}
	if got := getUVInterTxtp(transform.TxfmDimensions[transform.TX32x32], transform.ADST_DCT); got != transform.DCT_DCT {
		t.Fatalf("TX32x32 ADST_DCT = %d, want DCT_DCT", got)
	}
	if got := getUVInterTxtp(transform.TxfmDimensions[transform.TX16x16], transform.H_ADST); got != transform.DCT_DCT {
		t.Fatalf("TX16x16 H_ADST = %d, want DCT_DCT", got)
	}
	if got := getUVInterTxtp(transform.TxfmDimensions[transform.TX16x16], transform.ADST_DCT); got != transform.ADST_DCT {
		t.Fatalf("TX16x16 ADST_DCT = %d, want ADST_DCT", got)
	}
}

func TestDecodeCoeffTransformTypeInter3(t *testing.T) {
	ctx := NewTileCtx()
	td := transform.TxfmDimensions[transform.TX32x32]

	zeroBit := bitstream.NewMSAC([]byte{0x00, 0x00, 0x00, 0x00}, true)
	if got := decodeCoeffTransformType(zeroBit, ctx, td, 0, DCPred, false, transform.DCT_DCT, true, false, false); got != transform.IDTX {
		t.Fatalf("inter3 zero bit = %d, want IDTX", got)
	}

	oneBit := bitstream.NewMSAC([]byte{0xFF, 0xFF, 0xFF, 0xFF}, true)
	if got := decodeCoeffTransformType(oneBit, ctx, td, 0, DCPred, false, transform.DCT_DCT, true, false, false); got != transform.DCT_DCT {
		t.Fatalf("inter3 one bit = %d, want DCT_DCT", got)
	}
}

func TestIntraCtx(t *testing.T) {
	fs := NewFrameState(32, 32)
	if got := intraCtx(fs, 0, 0); got != 0 {
		t.Fatalf("origin intraCtx = %d, want 0", got)
	}

	fs.SetBlockState(0, 0, 4, 4, Av1Block{Intra: true})
	if got := intraCtx(fs, 4, 0); got != 2 {
		t.Fatalf("left-only intraCtx = %d, want 2", got)
	}
	if got := intraCtx(fs, 0, 4); got != 2 {
		t.Fatalf("top-only intraCtx = %d, want 2", got)
	}

	fs.SetBlockState(4, 0, 4, 4, Av1Block{Intra: true})
	fs.SetBlockState(0, 4, 4, 4, Av1Block{Intra: true})
	if got := intraCtx(fs, 4, 4); got != 3 {
		t.Fatalf("top+left intraCtx = %d, want 3", got)
	}
}

func TestMaxTxForBlockSize(t *testing.T) {
	seq420 := &header.SequenceHeader{SsHor: 1, SsVer: 1}
	if got := maxTxForBlockSize(seq420, 32, 16, 0); got != transform.RTX32x16 {
		t.Fatalf("luma 32x16 = %d, want RTX32x16", got)
	}
	if got := maxTxForBlockSize(seq420, 32, 16, 1); got != transform.RTX16x8 {
		t.Fatalf("chroma420 32x16 = %d, want RTX16x8", got)
	}

	seq444 := &header.SequenceHeader{SsHor: 0, SsVer: 0}
	if got := maxTxForBlockSize(seq444, 16, 16, 1); got != transform.TX16x16 {
		t.Fatalf("chroma444 16x16 = %d, want TX16x16", got)
	}
}

func TestCollectTxBlocksFromSplits(t *testing.T) {
	blocks := collectTxBlocksFromSplits(32, 32, transform.TX16x16, 1, 0)
	if len(blocks) != 4 {
		t.Fatalf("split root block count = %d, want 4", len(blocks))
	}
	for _, blk := range blocks {
		if blk.tx != transform.TX8x8 {
			t.Fatalf("split root tx = %d, want TX8x8", blk.tx)
		}
	}

	blocks = collectTxBlocksFromSplits(32, 16, transform.RTX32x16, 0, 0)
	if len(blocks) != 1 {
		t.Fatalf("unsplit rect block count = %d, want 1", len(blocks))
	}
	if blocks[0].tx != transform.RTX32x16 || blocks[0].w != 32 || blocks[0].h != 16 {
		t.Fatalf("unsplit rect block = %+v, want RTX32x16 32x16", blocks[0])
	}
}

func TestGetLoCtx1DReturnsDav1dHiMag(t *testing.T) {
	levels := make([]uint8, 16*6)
	levels[1] = 2
	levels[16] = 5
	levels[2] = 7
	levels[3] = 63

	_, hiMag := getLoCtx1D(levels, 0, 16, 0)
	if hiMag != 14 {
		t.Fatalf("getLoCtx1D hiMag = %d, want 14", hiMag)
	}
}

func TestLastNonzeroColFromEOBUsesPackedX(t *testing.T) {
	col0, ok := LastNonzeroColFromEOB(transform.TX4x4, 0)
	if !ok {
		t.Fatalf("LastNonzeroColFromEOB TX4x4 missing exact table")
	}
	if col0 != 0 {
		t.Fatalf("TX4x4 eob0 col = %d, want 0", col0)
	}

	col1, ok := LastNonzeroColFromEOB(transform.TX4x4, 1)
	if !ok {
		t.Fatalf("LastNonzeroColFromEOB TX4x4 missing exact table")
	}
	if col1 != 1 {
		t.Fatalf("TX4x4 eob1 col = %d, want 1", col1)
	}
}

func TestInterTxtpGridSamplesPerChromaBlock(t *testing.T) {
	grid := newInterTxtpGrid(0, 0, 32, 32, uint8(transform.DCT_DCT))
	grid.fillBlock(0, 0, 16, 16, uint8(transform.IDTX))
	grid.fillBlock(16, 0, 16, 16, uint8(transform.ADST_DCT))

	seq420 := &header.SequenceHeader{SsHor: 1, SsVer: 1}
	if got := grid.sampleChroma(seq420, 0, 0); got != uint8(transform.IDTX) {
		t.Fatalf("sampleChroma left = %d, want IDTX", got)
	}
	if got := grid.sampleChroma(seq420, 8, 0); got != uint8(transform.ADST_DCT) {
		t.Fatalf("sampleChroma right = %d, want ADST_DCT", got)
	}
}

func TestCoefQCatFromQIdx(t *testing.T) {
	tests := []struct {
		qidx int
		want int
	}{
		{0, 0},
		{20, 0},
		{21, 1},
		{60, 1},
		{61, 2},
		{120, 2},
		{121, 3},
	}
	for _, tc := range tests {
		if got := coefQCatFromQIdx(tc.qidx); got != tc.want {
			t.Fatalf("coefQCatFromQIdx(%d) = %d, want %d", tc.qidx, got, tc.want)
		}
	}
}

func TestCollectUniformTxBlocks(t *testing.T) {
	blocks := collectUniformTxBlocks(20, 10, transform.TX8x8)
	if len(blocks) != 6 {
		t.Fatalf("uniform TX8x8 block count = %d, want 6", len(blocks))
	}
	if blocks[0].x != 0 || blocks[0].y != 0 || blocks[1].x != 8 || blocks[1].y != 0 {
		t.Fatalf("unexpected first row blocks: %+v %+v", blocks[0], blocks[1])
	}
}

func TestFrameStateSetCoefCtxBlock(t *testing.T) {
	fs := NewFrameState(32, 32)
	fs.SetCoefCtxBlock(0, 0, 0, 16, 8, 0x55)
	for i := 0; i < 4; i++ {
		if fs.AboveLCoef[i] != 0x55 {
			t.Fatalf("AboveLCoef[%d] = 0x%x, want 0x55", i, fs.AboveLCoef[i])
		}
	}
	for i := 0; i < 2; i++ {
		if fs.LeftLCoef[i] != 0x55 {
			t.Fatalf("LeftLCoef[%d] = 0x%x, want 0x55", i, fs.LeftLCoef[i])
		}
	}
}

func TestApplySkipModeMotionUsesMatchingRefCandidate(t *testing.T) {
	fs := NewFrameState(64, 64)
	fs.MVFrame = refmvs.NewFrame(64, 64)
	fhdr := &header.FrameHeader{}
	fhdr.Refidx[0] = 3
	fb := &FrameBuf{}
	fb.Refs[3] = &PlaneBuf{}

	fs.MVFrame.PutGridBlock(1, 0, 1, 1, refmvs.Block{
		Ref: refmvs.RefPair{1, -1},
		MV:  refmvs.MVPair{{Y: 12, X: -4}},
	})
	fs.MVFrame.PutGridBlock(0, 1, 1, 1, refmvs.Block{
		Ref: refmvs.RefPair{1, -1},
		MV:  refmvs.MVPair{{Y: 20, X: 8}},
	})

	st := interState{skipMode: true, refSlot: 3}
	if !applySkipModeMotion(&st, fs, fb, fhdr, 4, 4) {
		t.Fatalf("applySkipModeMotion returned false")
	}
	if st.mv != (refmvs.MV{Y: 12, X: -4}) {
		t.Fatalf("skip mode mv = %+v, want {Y:12 X:-4}", st.mv)
	}
}

func TestSingleRefInterCandidatesUsesDecodedNeighbourBlocks(t *testing.T) {
	fs := NewFrameState(64, 64)
	fhdr := &header.FrameHeader{}
	fhdr.Refidx[0] = 2
	fb := &FrameBuf{}
	fb.Refs[2] = &PlaneBuf{}

	fs.SetBlockState(0, 4, 4, 4, Av1Block{
		Intra:   false,
		RefSlot: 2,
		MV:      [2]int16{16, -8},
	})

	cnt, stack := singleRefInterCandidates(fs, fhdr, fb, 4, 4)
	if cnt == 0 {
		t.Fatalf("singleRefInterCandidates returned no candidates")
	}
	if stack[0].refSlot != 2 || stack[0].mv != (refmvs.MV{Y: 16, X: -8}) {
		t.Fatalf("top candidate = %+v, want refSlot 2 mv {16,-8}", stack[0])
	}
}

func TestSingleRefInterCandidatesIncludesDiagonalDecodedBlock(t *testing.T) {
	fs := NewFrameState(64, 64)
	fhdr := &header.FrameHeader{}
	fhdr.Refidx[0] = 1
	fb := &FrameBuf{}
	fb.Refs[1] = &PlaneBuf{}

	fs.SetBlockState(0, 0, 4, 4, Av1Block{
		Intra:   false,
		RefSlot: 1,
		MV:      [2]int16{24, 12},
	})

	cnt, stack := singleRefInterCandidates(fs, fhdr, fb, 4, 4)
	if cnt == 0 {
		t.Fatalf("singleRefInterCandidates returned no candidates")
	}
	found := false
	for i := 0; i < cnt; i++ {
		if stack[i].refSlot == 1 && stack[i].mv == (refmvs.MV{Y: 24, X: 12}) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("diagonal decoded block candidate not found: %+v", stack[:cnt])
	}
}
