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
	fs.MVFrame = refmvs.NewFrame(64, 64)
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
	fs.AboveTx[1] = 1 // TX8 width is smaller than the current TX16 width.
	fs.LeftTx[1] = 2  // Equal height does not contribute.
	got := fs.TxCtx(4, 4, transform.TX16x16)
	if got != 1 {
		t.Fatalf("TxCtx smaller-above = %d, want 1", got)
	}

	fs.LeftTx[1] = 0
	got = fs.TxCtx(4, 4, transform.TX16x16)
	if got != 2 {
		t.Fatalf("TxCtx two-smaller-neighbours = %d, want 2", got)
	}
	if got = fs.TxCtx(0, 0, transform.TX16x16); got != 0 {
		t.Fatalf("TxCtx unavailable tile edges = %d, want 0", got)
	}
}

func TestSingleRefModeContextsCountsTopAndLeftMatchesBeforeDedup(t *testing.T) {
	fs := NewFrameState(64, 64)
	fs.MVFrame = refmvs.NewFrame(64, 64)
	fhdr := &header.FrameHeader{}
	fhdr.Refidx[0] = 0
	blk := Av1Block{Intra: false, RefSlot: 0, MV: [2]int16{0, 0}}
	fs.SetBlockState(16, 0, 16, 16, blk)
	fs.SetBlockState(0, 16, 16, 16, blk)
	mvBlk := refmvs.Block{Ref: refmvs.RefPair{1, -1}, BS: BS16x16}
	fs.MVFrame.PutGridBlock(4, 0, 4, 4, mvBlk)
	fs.MVFrame.PutGridBlock(0, 4, 4, 4, mvBlk)

	newCtx, globalCtx, refCtx := singleRefModeContexts(fs, fhdr, nil, 0, 1, 16, 16, 16, 16)
	if newCtx != 5 || globalCtx != 0 || refCtx != 5 {
		t.Fatalf("mode contexts = %d/%d/%d, want 5/0/5", newCtx, globalCtx, refCtx)
	}
}

func TestSingleRefModeContextsIncludesSecondaryLeftMatch(t *testing.T) {
	fs := NewFrameState(320, 64)
	fs.MVFrame = refmvs.NewFrame(320, 64)
	fhdr := &header.FrameHeader{}
	fhdr.Refidx[0] = 0
	fs.SetBlockState(220, 0, 4, 16, Av1Block{Intra: false, RefSlot: 0, InterMode: InterModeNearestMV})
	fs.SetBlockState(224, 0, 16, 16, Av1Block{Intra: true, RefSlot: -1})
	fs.MVFrame.PutGridBlock(55, 0, 1, 4, refmvs.Block{Ref: refmvs.RefPair{1, -1}, BS: BS4x16})

	newCtx, globalCtx, refCtx := singleRefModeContexts(fs, fhdr, nil, 0, 1, 240, 0, 16, 16)
	if newCtx != 1 || globalCtx != 0 || refCtx != 1 {
		t.Fatalf("secondary mode contexts = %d/%d/%d, want 1/0/1", newCtx, globalCtx, refCtx)
	}
}

func TestSingleRefModeContextsCombinesDirectAndSecondaryDirections(t *testing.T) {
	fs := NewFrameState(320, 64)
	fs.MVFrame = refmvs.NewFrame(320, 64)
	fhdr := &header.FrameHeader{}
	fhdr.Refidx[0] = 0
	match := Av1Block{Intra: false, RefSlot: 0, InterMode: InterModeNearestMV}
	topNew := match
	topNew.InterMode = InterModeNewMV
	fs.SetBlockState(240, 0, 16, 16, topNew)
	fs.SetBlockState(220, 16, 4, 16, match)
	fs.SetBlockState(224, 16, 16, 16, Av1Block{Intra: true, RefSlot: -1})
	fs.MVFrame.PutGridBlock(60, 0, 4, 4, refmvs.Block{Ref: refmvs.RefPair{1, -1}, BS: BS16x16, MF: 2})
	fs.MVFrame.PutGridBlock(55, 4, 1, 4, refmvs.Block{Ref: refmvs.RefPair{1, -1}, BS: BS4x16})

	newCtx, globalCtx, refCtx := singleRefModeContexts(fs, fhdr, nil, 0, 1, 240, 16, 16, 16)
	if newCtx != 2 || globalCtx != 0 || refCtx != 4 {
		t.Fatalf("combined mode contexts = %d/%d/%d, want 2/0/4", newCtx, globalCtx, refCtx)
	}
}

func TestRefCtxCountsAllForwardAndBackwardRefs(t *testing.T) {
	fs := NewFrameState(64, 64)
	fhdr := &header.FrameHeader{Refidx: [header.RefsPerFrame]int8{0, 1, 2, 3, 4, 5, 6}}
	fs.SetBlockState(16, 0, 16, 16, Av1Block{Intra: false, RefSlot: 3})
	fs.AbovePresent[4] = 1
	if got := refCtx(fs, fhdr, 16, 16); got != 2 {
		t.Fatalf("forward ref context = %d, want 2", got)
	}
	fs.SetBlockState(16, 0, 16, 16, Av1Block{Intra: false, RefSlot: 5})
	if got := refCtx(fs, fhdr, 16, 16); got != 0 {
		t.Fatalf("backward ref context = %d, want 0", got)
	}
}

func TestSingleRefModeContextsDoNotCrossTileBoundary(t *testing.T) {
	fs := NewFrameState(640, 64)
	fs.TileX0 = 320
	fs.TileY0 = 0
	fhdr := &header.FrameHeader{Refidx: [header.RefsPerFrame]int8{0, 1, 2, 3, 4, 5, 6}}

	newCtx, globalCtx, refCtx := singleRefModeContexts(fs, fhdr, nil, 0, 1, 320, 0, 64, 64)
	if newCtx != 0 || globalCtx != 0 || refCtx != 0 {
		t.Fatalf("tile-boundary mode contexts = %d/%d/%d, want 0/0/0", newCtx, globalCtx, refCtx)
	}
}

func TestFrameStateIntraTxCtxUsesSeparateBlockEdges(t *testing.T) {
	fs := NewFrameState(64, 64)
	fs.SetIntraTxCtx(16, 0, 16, 16, transform.TX16x16)
	fs.SetInterTxIntraCtx(0, 16, 16, 16)
	if got := fs.IntraTxCtx(16, 16, transform.TX16x16); got != 2 {
		t.Fatalf("IntraTxCtx = %d, want 2", got)
	}

	// Var-tx edges remain independent and still use the smaller-neighbour rule.
	fs.AboveTx[4] = 2
	fs.LeftTx[4] = 1
	if got := fs.TxCtx(16, 16, transform.TX16x16); got != 1 {
		t.Fatalf("var TxCtx = %d, want 1", got)
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

func TestFrameStateDCSignCtxBlockUsesVisibleSpan(t *testing.T) {
	fs := NewFrameState(14, 10)
	// Visible width is one 4x4 unit at bx=8, but the next neighbour slot
	// carries stale negative state. The block-scoped ctx must ignore it.
	fs.AboveLCoef[2] = 0x80
	fs.AboveLCoef[3] = 0x00
	got := fs.DCSignCtxBlock(0, 8, 0, 2, 4)
	if got != 2 {
		t.Fatalf("DCSignCtxBlock = %d, want 2", got)
	}
}

func TestBlockHasChromaUsesSyntaxBlockSizeAtFrameEdge(t *testing.T) {
	seq420 := &header.SequenceHeader{SsHor: 1, SsVer: 1}
	fb := &FrameBuf{
		Width:      14,
		Height:     10,
		ChromaW:    7,
		ChromaH:    5,
		StrideY:    14,
		StrideUV:   7,
		Y:          make([]byte, 140),
		U:          make([]byte, 35),
		V:          make([]byte, 35),
		Monochrome: false,
	}
	// bx=12 leaves only 2 visible luma pixels, but a 4x4 syntax block at an odd
	// 4x4 column and row still has chroma per dav1d's block-level test.
	if !blockHasChroma(seq420, fb, 12, 4, 4, 4) {
		t.Fatal("blockHasChroma clipped edge block = false, want true")
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
	blocks := collectTxBlocksFromSplits(0, 0, 32, 32, 32, 32, transform.TX16x16, 1, 0)
	if len(blocks) != 4 {
		t.Fatalf("split root block count = %d, want 4", len(blocks))
	}
	for _, blk := range blocks {
		if blk.tx != transform.TX8x8 {
			t.Fatalf("split root tx = %d, want TX8x8", blk.tx)
		}
	}

	blocks = collectTxBlocksFromSplits(0, 0, 8, 8, 8, 8, transform.TX8x8, 1, 0)
	if len(blocks) != 4 {
		t.Fatalf("split TX8 block count = %d, want 4", len(blocks))
	}
	for _, blk := range blocks {
		if blk.tx != transform.TX4x4 || blk.w != 4 || blk.h != 4 {
			t.Fatalf("split TX8 child = %+v, want TX4x4 4x4", blk)
		}
	}

	blocks = collectTxBlocksFromSplits(0, 0, 32, 16, 32, 16, transform.RTX32x16, 0, 0)
	if len(blocks) != 1 {
		t.Fatalf("unsplit rect block count = %d, want 1", len(blocks))
	}
	if blocks[0].tx != transform.RTX32x16 || blocks[0].w != 32 || blocks[0].h != 16 {
		t.Fatalf("unsplit rect block = %+v, want RTX32x16 32x16", blocks[0])
	}
}

func TestCollectTxBlocksFromSplitsClipsAgainstFrameEdge(t *testing.T) {
	// Mirror dav1d read_coef_tree() semantics: child existence is checked
	// against absolute frame bounds, not just the local block size.
	blocks := collectTxBlocksFromSplits(16, 0, 32, 16, 32, 16, transform.RTX32x16, 1, 0)
	if len(blocks) != 1 {
		t.Fatalf("frame-edge split block count = %d, want 1", len(blocks))
	}
	if blocks[0].x != 0 || blocks[0].y != 0 {
		t.Fatalf("frame-edge split block = %+v, want first child only", blocks[0])
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

func TestInterTxtpGridFillBlockKeepsTxGeometryAtFrameEdge(t *testing.T) {
	grid := newInterTxtpGrid(0, 0, 32, 32, uint8(transform.DCT_DCT))
	// Simulate a border tx block whose visible luma area might be clipped,
	// but whose txtp footprint must still cover the full tx geometry.
	grid.fillBlock(16, 0, 16, 16, uint8(transform.IDTX))
	seq420 := &header.SequenceHeader{SsHor: 1, SsVer: 1}
	if got := grid.sampleChroma(seq420, 8, 0); got != uint8(transform.IDTX) {
		t.Fatalf("sampleChroma frame-edge = %d, want IDTX", got)
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
	if !applySkipModeMotion(&st, fs, fb, fhdr, 4, 4, 8, 8) {
		t.Fatalf("applySkipModeMotion returned false")
	}
	if st.mv != (refmvs.MV{Y: 12, X: -4}) {
		t.Fatalf("skip mode mv = %+v, want {Y:12 X:-4}", st.mv)
	}
}

func TestSingleRefInterCandidatesUsesDecodedNeighbourBlocks(t *testing.T) {
	fs := NewFrameState(64, 64)
	fs.MVFrame = refmvs.NewFrame(64, 64)
	fhdr := &header.FrameHeader{}
	fhdr.Refidx[0] = 2
	fb := &FrameBuf{}
	fb.Refs[2] = &PlaneBuf{}

	fs.SetBlockState(0, 4, 4, 4, Av1Block{
		Intra:   false,
		RefSlot: 2,
		MV:      [2]int16{16, -8},
	})
	fs.MVFrame.PutGridBlock(0, 1, 1, 1, refmvs.Block{Ref: refmvs.RefPair{1, -1}, MV: refmvs.MVPair{{Y: 16, X: -8}}})

	cnt, stack := singleRefInterCandidates(fs, fhdr, fb, 2, 1, 4, 4, 8, 8)
	if cnt == 0 {
		t.Fatalf("singleRefInterCandidates returned no candidates")
	}
	if stack[0].refSlot != 2 || stack[0].mv != (refmvs.MV{Y: 16, X: -8}) {
		t.Fatalf("top candidate = %+v, want refSlot 2 mv {16,-8}", stack[0])
	}
}

func TestSingleRefInterCandidatesIncludesDiagonalDecodedBlock(t *testing.T) {
	fs := NewFrameState(64, 64)
	fs.MVFrame = refmvs.NewFrame(64, 64)
	fhdr := &header.FrameHeader{}
	fhdr.Refidx[0] = 1
	fb := &FrameBuf{}
	fb.Refs[1] = &PlaneBuf{}

	fs.SetBlockState(0, 0, 4, 4, Av1Block{
		Intra:   false,
		RefSlot: 1,
		MV:      [2]int16{24, 12},
	})
	fs.MVFrame.PutGridBlock(0, 0, 1, 1, refmvs.Block{Ref: refmvs.RefPair{1, -1}, MV: refmvs.MVPair{{Y: 24, X: 12}}})

	cnt, stack := singleRefInterCandidates(fs, fhdr, fb, 1, 1, 4, 4, 8, 8)
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
