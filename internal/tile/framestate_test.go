package tile

import (
	"math/rand"
	"testing"

	"github.com/zesun96/go-av1/internal/bitstream"
	"github.com/zesun96/go-av1/internal/refmvs"
)

func TestSetTxStateMatchesReferenceRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(102))
	for iteration := 0; iteration < 10_000; iteration++ {
		w4 := 1 + rng.Intn(64)
		h4 := 1 + rng.Intn(64)
		initial := make([]uint8, w4*h4)
		if _, err := rng.Read(initial); err != nil {
			t.Fatal(err)
		}
		got := &FrameState{W4: w4, H4: h4, TxGrid: append([]uint8(nil), initial...)}
		want := append([]uint8(nil), initial...)
		bx := rng.Intn((w4+8)*4) - 16
		by := rng.Intn((h4+8)*4) - 16
		bw := 1 + rng.Intn(260)
		bh := 1 + rng.Intn(260)
		tx := uint8(rng.Intn(256))

		got.SetTxState(bx, by, bw, bh, tx)
		setTxStateReference(want, w4, h4, bx, by, bw, bh, tx)
		for i := range want {
			if got.TxGrid[i] != want[i] {
				t.Fatalf("iteration=%d index=%d grid=%dx%d rect=(%d,%d %dx%d) tx=%d got=%d want=%d",
					iteration, i, w4, h4, bx, by, bw, bh, tx, got.TxGrid[i], want[i])
			}
		}
	}
}

func TestFillUint8SizesAndValues(t *testing.T) {
	for _, size := range []int{0, 1, 15, 16, 31, 32, 33, 63, 64, 65, 255, 256, 4096} {
		for _, value := range []uint8{0, 1, 0x40, 0xff} {
			values := make([]uint8, size)
			for i := range values {
				values[i] = uint8(i*37 + 11)
			}
			fillUint8(values, value)
			for i, got := range values {
				if got != value {
					t.Fatalf("size=%d value=%d index=%d got=%d", size, value, i, got)
				}
			}
		}
	}
}

func setTxStateReference(grid []uint8, w4, h4, bx, by, bw, bh int, tx uint8) {
	x0 := clampInt(bx/4, 0, w4)
	x1 := clampInt((bx+bw+3)/4, 0, w4)
	y0 := clampInt(by/4, 0, h4)
	y1 := clampInt((by+bh+3)/4, 0, h4)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			entry := tx & txSizeMask
			if x == x0 {
				entry |= txBoundaryLeft
			}
			if y == y0 {
				entry |= txBoundaryTop
			}
			grid[y*w4+x] = entry
		}
	}
}

func TestDisabledBlockTraceHasZeroAllocations(t *testing.T) {
	fs := NewFrameState(64, 64)
	m := bitstream.NewMSAC([]byte{0xff, 0xff, 0xff, 0xff}, false)
	if got := testing.AllocsPerRun(1000, func() {
		traceInterModeDone(m, fs, 0, 0, InterModeNearestMV, 0, 0, 0, 0)
	}); got != 0 {
		t.Fatalf("disabled block trace allocations = %v, want 0", got)
	}
}

func TestCoefficientScratchReuseClearsActiveFootprint(t *testing.T) {
	fs := NewFrameState(64, 64)
	coeff, levels := fs.coefficientScratch(32*32, 34*32)
	for i := range coeff {
		coeff[i] = int32(i + 1)
	}
	for i := range levels {
		levels[i] = uint8(i%255 + 1)
	}

	smallCoeff, smallLevels := fs.coefficientScratch(16, 32)
	if &smallCoeff[0] != &coeff[0] || &smallLevels[0] != &levels[0] {
		t.Fatal("coefficient scratch was reallocated")
	}
	for i, value := range smallCoeff {
		if value != 0 {
			t.Fatalf("small coefficient scratch[%d] = %d, want 0", i, value)
		}
	}
	for i, value := range smallLevels {
		if value != 0 {
			t.Fatalf("small levels scratch[%d] = %d, want 0", i, value)
		}
	}
	if coeff[16] == 0 || levels[32] == 0 {
		t.Fatal("scratch clear extended beyond the active transform footprint")
	}

	coeff, levels = fs.coefficientScratch(32*32, 34*32)
	for i, value := range coeff {
		if value != 0 {
			t.Fatalf("largest coefficient scratch[%d] = %d, want 0", i, value)
		}
	}
	for i, value := range levels {
		if value != 0 {
			t.Fatalf("largest levels scratch[%d] = %d, want 0", i, value)
		}
	}
}

func TestCoefficientScratchHasZeroSteadyStateAllocations(t *testing.T) {
	fs := NewFrameState(64, 64)
	fs.coefficientScratch(32*32, 34*32)
	if got := testing.AllocsPerRun(1000, func() {
		fs.coefficientScratch(32*32, 34*32)
	}); got != 0 {
		t.Fatalf("steady-state scratch allocations = %v, want 0", got)
	}
}

func TestNewFrameStateIncludesFinalCodedFourByFourRow(t *testing.T) {
	fs := NewFrameState(1510, 1012)
	if fs.W4 != 378 || fs.H4 != 254 {
		t.Fatalf("coded 4x4 grid = %dx%d, want 378x254", fs.W4, fs.H4)
	}
	fs.SetCoefCtxBlock(0, 0, 1008, 4, 8, 7)
	if got := fs.LeftLCoef[253]; got != 7 {
		t.Fatalf("last coded luma context = %d, want 7", got)
	}
}

func TestMergeFilterStateCopiesOnlyTileRegion(t *testing.T) {
	dst := NewFrameState(128, 64)
	src := NewFrameState(128, 64)
	src.TileX0, src.TileX1 = 64, 128
	src.TileY0, src.TileY1 = 0, 64
	src.SetBlockState(0, 0, 64, 64, Av1Block{SegID: 1})
	src.SetBlockState(64, 0, 64, 64, Av1Block{SegID: 2})
	src.SetTxState(0, 0, 64, 64, 1)
	src.SetTxState(64, 0, 64, 64, 2)
	src.CDEFIndex[1] = 3

	dst.MergeFilterState(src)
	if got, _ := dst.BlockState(0, 0); got.SegID != 0 {
		t.Fatalf("metadata outside tile was copied: %+v", got)
	}
	if got, _ := dst.BlockState(64, 0); got.SegID != 2 {
		t.Fatalf("tile metadata missing: %+v", got)
	}
	insideTx, _, insideOK := dst.txStateAtGrid(16)
	if dst.TxGrid[0] != 0xff || !insideOK || insideTx != 2 {
		t.Fatalf("merged transform grid outside=%d inside=(%d,%t)", dst.TxGrid[0], insideTx, insideOK)
	}
	if dst.CDEFIndex[1] != 3 {
		t.Fatalf("CDEF index=%d want 3", dst.CDEFIndex[1])
	}
}

func TestBlockGridSharesPerBlockMetadata(t *testing.T) {
	fs := NewFrameState(32, 32)
	fs.SetBlockState(0, 0, 16, 16, Av1Block{SegID: 3, Skip: true})

	if len(fs.Blocks) != 1 {
		t.Fatalf("metadata entries=%d want 1", len(fs.Blocks))
	}
	index := fs.BlockGrid[0]
	if index == 0 {
		t.Fatal("block grid entry is unset")
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if got := fs.BlockGrid[y*fs.W4+x]; got != index {
				t.Fatalf("grid[%d,%d]=%d want shared index %d", x, y, got, index)
			}
		}
	}
	if got, ok := fs.BlockState(12, 12); !ok || got.SegID != 3 || !got.Skip {
		t.Fatalf("shared block state=%+v ok=%v", got, ok)
	}
}

func TestBlockGridPromotesWithoutLosingCompactIndexes(t *testing.T) {
	fs := NewFrameState(16, 8)
	fs.setBlockGridIndex(0, 7)
	fs.setBlockGridIndex(1, maxCompactBlockIndex+1)
	if got := fs.blockGridIndex(0); got != 7 {
		t.Fatalf("compact index after promotion=%d want 7", got)
	}
	if got := fs.blockGridIndex(1); got != maxCompactBlockIndex+1 {
		t.Fatalf("wide index=%d want %d", got, maxCompactBlockIndex+1)
	}

	fs.setChromaBlockGridIndex(0, 9)
	fs.setChromaBlockGridIndex(1, maxCompactBlockIndex+2)
	if got := fs.chromaBlockGridIndex(0); got != 9 {
		t.Fatalf("compact chroma index after promotion=%d want 9", got)
	}
	if got := fs.chromaBlockGridIndex(1); got != maxCompactBlockIndex+2 {
		t.Fatalf("wide chroma index=%d want %d", got, maxCompactBlockIndex+2)
	}
}

func TestFrameStateResetRestoresSentinelsAndMetadata(t *testing.T) {
	fs := NewFrameState(32, 32)
	fs.AboveSkip[0] = 1
	fs.AboveRef[0] = 3
	fs.AboveTxIntra[0] = 2
	fs.AboveLCoef[0] = 7
	fs.CDEFIndex[0] = 4
	fs.SetBlockState(0, 0, 8, 8, Av1Block{SegID: 5})
	fs.SetChromaBlockState(0, 0, 8, 8, Av1Block{SegID: 6})
	fs.SetTxState(0, 0, 8, 8, 1)
	fs.RestorationUnits = append(fs.RestorationUnits, RestorationUnit{Plane: 1})
	fs.Tracef = func(string, ...any) {}
	fs.TileX1 = 32

	fs.Reset()

	if fs.AboveSkip[0] != 0 || fs.AboveRef[0] != -1 || fs.AboveTxIntra[0] != 0xff || fs.AboveLCoef[0] != 0x40 {
		t.Fatalf("rolling context was not reset: skip=%d ref=%d tx=%d coef=%d",
			fs.AboveSkip[0], fs.AboveRef[0], fs.AboveTxIntra[0], fs.AboveLCoef[0])
	}
	if fs.CDEFIndex[0] != -1 || len(fs.RestorationUnits) != 0 || len(fs.Blocks) != 0 {
		t.Fatalf("durable state was not reset: cdef=%d restoration=%d blocks=%d",
			fs.CDEFIndex[0], len(fs.RestorationUnits), len(fs.Blocks))
	}
	if block, _ := fs.BlockState(0, 0); block != (Av1Block{}) {
		t.Fatalf("block grid retained metadata: %+v", block)
	}
	if _, _, ok := fs.txStateAtGrid(0); ok {
		t.Fatal("transform grid entry remained set")
	}
	if fs.Tracef != nil || fs.TileX1 != 0 {
		t.Fatal("diagnostic or tile state was not reset")
	}
}

func TestMergeFilterStateRemapsChromaMetadata(t *testing.T) {
	dst := NewFrameState(128, 64)
	src := NewFrameState(128, 64)
	src.TileX0, src.TileX1 = 64, 128
	src.TileY0, src.TileY1 = 0, 64
	src.SetChromaBlockState(64, 0, 64, 64, Av1Block{SegID: 5})

	dst.MergeFilterState(src)
	index := dst.ChromaBlockGrid[dst.CW4/2]
	if got := dst.chromaBlockStateAtGrid(dst.CW4 / 2); index == 0 || got.SegID != 5 {
		t.Fatalf("merged chroma index=%d metadata=%+v", index, got)
	}
}

func TestTransformBoundariesDistinguishEqualSizedLeaves(t *testing.T) {
	fs := NewFrameState(16, 8)
	fs.SetTxState(0, 0, 8, 8, 1)
	fs.SetTxState(8, 0, 8, 8, 1)
	leftSize, leftBoundaries, leftOK := fs.txStateAtGrid(1)
	rightSize, rightBoundaries, rightOK := fs.txStateAtGrid(2)
	if !leftOK || !rightOK || leftSize != rightSize {
		t.Fatal("test requires equal transform sizes")
	}
	if leftBoundaries&txBoundaryLeft != 0 || rightBoundaries&txBoundaryLeft == 0 {
		t.Fatal("equal-sized adjacent transform leaves lack a separating boundary")
	}
}

func TestFrameStatePartCtxBitOrder(t *testing.T) {
	fs := NewFrameState(64, 64)
	bx, by := 16, 16
	bl := BL64X64

	col8 := bx / 8
	row8 := by / 8
	shift := 4 - bl

	fs.AbovePartition[col8] = 1 << uint(shift)
	fs.LeftPartition[row8] = 0
	if got := fs.PartCtx(bx, by, bl); got != 1 {
		t.Fatalf("top-only PartCtx = %d, want 1", got)
	}

	fs.AbovePartition[col8] = 0
	fs.LeftPartition[row8] = 1 << uint(shift)
	if got := fs.PartCtx(bx, by, bl); got != 2 {
		t.Fatalf("left-only PartCtx = %d, want 2", got)
	}

	fs.AbovePartition[col8] = 1 << uint(shift)
	fs.LeftPartition[row8] = 1 << uint(shift)
	if got := fs.PartCtx(bx, by, bl); got != 3 {
		t.Fatalf("top+left PartCtx = %d, want 3", got)
	}
}

func TestFrameStatePaletteCtx(t *testing.T) {
	fs := NewFrameState(64, 64)
	fs.SetPaletteCtx(16, 16, 16, 16, 4, 3)

	if got := fs.PaletteYCtx(32, 16); got != 1 {
		t.Fatalf("PaletteYCtx top-only = %d, want 1", got)
	}
	if got := fs.PaletteYCtx(16, 32); got != 1 {
		t.Fatalf("PaletteYCtx left-only = %d, want 1", got)
	}
	if got := fs.PaletteUVCtx(8, 16); got != 1 {
		t.Fatalf("PaletteUVCtx top-only = %d, want 1", got)
	}

	fs.SetPaletteCtx(16, 16, 16, 16, 0, 0)
	if got := fs.PaletteYCtx(32, 16); got != 0 {
		t.Fatalf("PaletteYCtx after inter clear = %d, want 0", got)
	}
	if got := fs.PaletteUVCtx(8, 16); got != 0 {
		t.Fatalf("PaletteUVCtx after inter clear = %d, want 0", got)
	}
}

func TestFrameStateSetInterBlock(t *testing.T) {
	fs := NewFrameState(64, 64)
	fs.MVFrame = refmvs.NewFrame(64, 64)
	mv := refmvs.MV{Y: -12, X: 20}
	fs.SetBlockState(16, 16, 16, 16, Av1Block{
		Intra:     false,
		SegID:     2,
		InterMode: InterModeNearestMV,
		RefSlot:   5,
		MV:        [2]int16{mv.Y, mv.X},
	})
	fs.SetInterBlock(16, 16, 16, 16, false, 2, 5, 3, 3, 3, InterModeNearestMV, mv)

	if got := fs.AboveRef[4]; got != 5 {
		t.Fatalf("AboveRef=%d want 5", got)
	}
	if got := fs.LeftRef[4]; got != 5 {
		t.Fatalf("LeftRef=%d want 5", got)
	}
	if got := fs.AboveFilter[4]; got != 3 {
		t.Fatalf("AboveFilter=%d want 3", got)
	}
	if got := fs.LeftFilter[4]; got != 3 {
		t.Fatalf("LeftFilter=%d want 3", got)
	}
	if got := fs.AboveMV[0][4]; got != -12 {
		t.Fatalf("AboveMV.Y=%d want -12", got)
	}
	if got := fs.LeftMV[1][4]; got != 20 {
		t.Fatalf("LeftMV.X=%d want 20", got)
	}
	if got := fs.AboveSegID[4]; got != 2 {
		t.Fatalf("AboveSegID=%d want 2", got)
	}
	tb := fs.MVFrame.RP[2*fs.MVFrame.RPStride+2]
	if tb.MV != mv || tb.Ref != 3 {
		t.Fatalf("temporal block=(mv=%+v ref=%d) want (%+v,3 logical ref)", tb.MV, tb.Ref, mv)
	}
	blk, ok := fs.BlockState(16, 16)
	if !ok {
		t.Fatal("BlockState missing")
	}
	if blk.Intra || blk.InterMode != InterModeNearestMV || blk.RefSlot != 5 || blk.MV != [2]int16{-12, 20} {
		t.Fatalf("block state=%+v", blk)
	}
}

func TestCommitInterBlockWithoutChromaPreservesUVModeEdges(t *testing.T) {
	fs := NewFrameState(32, 32)
	fs.SsHor, fs.SsVer = 1, 1
	fs.SetUVModeState(4, 4, 4, 4, SmoothPred)
	blk := Av1Block{RefSlot: 0, RefFrame: 1, InterMode: InterModeNearestMV}

	fs.CommitInterBlock(8, 8, 8, 4, blk, 1, false)
	if got := fs.AboveUVMode[1]; got != SmoothPred {
		t.Fatalf("AboveUVMode=%d want preserved smooth mode %d", got, SmoothPred)
	}
	if got := fs.LeftUVMode[1]; got != SmoothPred {
		t.Fatalf("LeftUVMode=%d want preserved smooth mode %d", got, SmoothPred)
	}

	fs.CommitInterBlock(8, 8, 8, 8, blk, 1, true)
	if got := fs.AboveUVMode[1]; got != DCPred {
		t.Fatalf("AboveUVMode=%d want inter chroma mode %d", got, DCPred)
	}
}

func TestCommitCompoundBlockSavesProjectableSecondTemporalMV(t *testing.T) {
	fs := NewFrameState(32, 32)
	fs.MVFrame = refmvs.NewFrame(32, 32)
	fs.MVFrame.OrderHint = 10
	fs.MVFrame.OrderBits = 5
	fs.MVFrame.RefFrameOrderHints[0] = 12 // First reference is in the future.
	fs.MVFrame.RefFrameOrderHints[6] = 8  // Second reference is in the past.
	blk := Av1Block{
		Compound:  true,
		RefSlot:   0,
		RefFrame:  1,
		RefSlot2:  6,
		RefFrame2: 7,
		MV:        [2]int16{3, 5},
		MV2:       [2]int16{-7, 9},
	}

	fs.CommitInterBlock(8, 8, 8, 8, blk, 1)
	got := fs.MVFrame.RP[fs.MVFrame.RPStride+1]
	want := refmvs.TemporalBlock{MV: refmvs.MV{Y: -7, X: 9}, Ref: 7}
	if got != want {
		t.Fatalf("compound temporal block=%+v want %+v", got, want)
	}
}

func TestCommitIntraBlockClearsStaleTemporalMV(t *testing.T) {
	fs := NewFrameState(32, 32)
	fs.MVFrame = refmvs.NewFrame(32, 32)
	fs.MVFrame.RP[fs.MVFrame.RPStride+1] = refmvs.TemporalBlock{
		MV: refmvs.MV{Y: 5, X: -3}, Ref: 1,
	}

	fs.CommitIntraMVBlock(12, 12, 4, 4)
	if got := fs.MVFrame.RP[fs.MVFrame.RPStride+1]; got != (refmvs.TemporalBlock{}) {
		t.Fatalf("stale temporal block after intra commit=%+v", got)
	}
}

func TestSub8BlockOutsideTemporalSampleDoesNotOverwrite(t *testing.T) {
	fs := NewFrameState(32, 32)
	fs.MVFrame = refmvs.NewFrame(32, 32)
	want := refmvs.TemporalBlock{MV: refmvs.MV{Y: 5, X: -3}, Ref: 1}
	fs.MVFrame.RP[fs.MVFrame.RPStride+1] = want

	fs.CommitIntraMVBlock(8, 8, 4, 4)
	if got := fs.MVFrame.RP[fs.MVFrame.RPStride+1]; got != want {
		t.Fatalf("non-sample sub-8x8 block overwrote temporal block: got %+v want %+v", got, want)
	}
}

func TestFrameStateIntraBlockClearsInterEdges(t *testing.T) {
	fs := NewFrameState(64, 64)
	fs.SetInterBlock(0, 0, 16, 16, false, 0, 2, 1, 2, 1, InterModeNearestMV, refmvs.MV{Y: 4, X: -2})
	fs.SetBlock(16, 0, 16, 16, false, DCPred)

	if fs.LeftRef[0] != -1 || fs.LeftFilter[0] != 0 || fs.LeftFilterV[0] != 0 {
		t.Fatalf("intra left edge retained inter state: ref=%d filter=%d/%d", fs.LeftRef[0], fs.LeftFilter[0], fs.LeftFilterV[0])
	}
	if fs.LeftMV[0][0] != 0 || fs.LeftMV[1][0] != 0 {
		t.Fatalf("intra left edge retained MV: %d/%d", fs.LeftMV[0][0], fs.LeftMV[1][0])
	}
}

func TestFrameStateCommitInterBlockSetsNewMVFlag(t *testing.T) {
	fs := NewFrameState(64, 64)
	fs.MVFrame = refmvs.NewFrame(64, 64)
	blk := Av1Block{
		Intra:     false,
		SegID:     1,
		Skip:      false,
		InterMode: InterModeNewMV,
		RefSlot:   3,
		Filter:    2,
		BaseMV:    [2]int16{8, -4},
		DeltaMV:   [2]int16{2, 6},
		MV:        [2]int16{10, 2},
	}

	fs.CommitInterBlock(8, 8, 16, 16, blk, 4)

	gridBlk, ok := fs.GridInterBlock(8, 8)
	if !ok {
		t.Fatal("GridInterBlock missing")
	}
	if gridBlk.MF != 2 {
		t.Fatalf("grid MF=%d want 2", gridBlk.MF)
	}
	got, ok := fs.BlockState(8, 8)
	if !ok {
		t.Fatal("BlockState missing")
	}
	if got.InterMode != InterModeNewMV || got.BaseMV != [2]int16{8, -4} || got.DeltaMV != [2]int16{2, 6} || got.MV != [2]int16{10, 2} {
		t.Fatalf("block state=%+v", got)
	}
}

func TestFrameStateGlobalMVFlagRequiresEightPixelsInBothDimensions(t *testing.T) {
	fs := NewFrameState(64, 64)
	fs.MVFrame = refmvs.NewFrame(64, 64)

	fs.setCurrentMVBlock(0, 0, 8, 4, 1, InterModeGlobalMV, refmvs.MV{X: -7})
	if got, _ := fs.MVFrame.GridBlock(0, 0); got.MF != 0 {
		t.Fatalf("8x4 GLOBALMV flag=%d want 0", got.MF)
	}
	fs.setCurrentMVBlock(8, 0, 8, 8, 1, InterModeGlobalMV, refmvs.MV{X: -7})
	if got, _ := fs.MVFrame.GridBlock(2, 0); got.MF != 1 {
		t.Fatalf("8x8 GLOBALMV flag=%d want 1", got.MF)
	}
}

func TestFrameStateInterIntraStoresIntraSecondReference(t *testing.T) {
	fs := NewFrameState(64, 64)
	fs.MVFrame = refmvs.NewFrame(64, 64)
	blk := Av1Block{InterMode: InterModeRefMV, RefFrame: 1, RefSlot: 0, InterIntra: true}
	fs.CommitInterBlock(8, 8, 8, 16, blk, 1)
	got, ok := fs.MVFrame.GridBlock(2, 2)
	if !ok || got.Ref != (refmvs.RefPair{1, 0}) {
		t.Fatalf("inter-intra MV reference=%v ok=%t want {1,0}", got.Ref, ok)
	}
}

func TestFrameStateCommitIntraMVBlockStoresActualSize(t *testing.T) {
	fs := NewFrameState(64, 64)
	fs.MVFrame = refmvs.NewFrame(64, 64)

	fs.CommitIntraMVBlock(8, 4, 16, 32)

	for y := 1; y < 9; y++ {
		for x := 2; x < 6; x++ {
			blk, ok := fs.MVFrame.GridBlock(x, y)
			if !ok {
				t.Fatalf("GridBlock(%d,%d) missing", x, y)
			}
			if !blk.Ref.IsIntra() || blk.Ref[1] != -1 {
				t.Fatalf("GridBlock(%d,%d) ref=%v want intra", x, y, blk.Ref)
			}
			if blk.BS != BS16x32 || blk.X4 != 2 || blk.Y4 != 1 {
				t.Fatalf("GridBlock(%d,%d)=%+v", x, y, blk)
			}
		}
	}
}

func TestFrameStateCommitCompoundBlockStoresReferencePair(t *testing.T) {
	fs := NewFrameState(64, 64)
	fs.MVFrame = refmvs.NewFrame(64, 64)
	blk := Av1Block{
		Compound: true, InterMode: compInterModeGlobalGlobal,
		RefSlot: 2, RefFrame: 1, MV: [2]int16{8, -4},
		RefSlot2: 6, RefFrame2: 7, MV2: [2]int16{-12, 20},
	}

	fs.CommitInterBlock(16, 8, 32, 16, blk, 1)

	got, ok := fs.MVFrame.GridBlock(4, 2)
	if !ok {
		t.Fatal("compound MV grid block missing")
	}
	if got.Ref != (refmvs.RefPair{1, 7}) || got.MV != (refmvs.MVPair{{Y: 8, X: -4}, {Y: -12, X: 20}}) {
		t.Fatalf("compound MV grid block=%+v", got)
	}
	if got.BS != BS32x16 || got.MF != 1 {
		t.Fatalf("compound MV metadata bs=%d mf=%d", got.BS, got.MF)
	}
}

func TestSkipModeCtxUsesDecodedNeighbours(t *testing.T) {
	fs := NewFrameState(32, 32)
	fs.SetBlockState(8, 0, 8, 8, Av1Block{SkipMode: true})
	fs.SetBlockState(0, 8, 8, 8, Av1Block{SkipMode: true})
	if got := fs.SkipModeCtx(8, 8); got != 2 {
		t.Fatalf("SkipModeCtx=%d want 2", got)
	}
}
