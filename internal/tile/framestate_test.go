package tile

import (
	"testing"

	"github.com/zesun96/go-av1/internal/refmvs"
)

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
	if dst.TxGrid[0] != 0xff || dst.TxGrid[16] != 2 {
		t.Fatalf("merged transform grid outside=%d inside=%d", dst.TxGrid[0], dst.TxGrid[16])
	}
	if dst.CDEFIndex[1] != 3 {
		t.Fatalf("CDEF index=%d want 3", dst.CDEFIndex[1])
	}
}

func TestTransformOriginsDistinguishEqualSizedLeaves(t *testing.T) {
	fs := NewFrameState(16, 8)
	fs.SetTxState(0, 0, 8, 8, 1)
	fs.SetTxState(8, 0, 8, 8, 1)
	if fs.TxGrid[1] != fs.TxGrid[2] {
		t.Fatal("test requires equal transform sizes")
	}
	if fs.TxOriginX4[1] == fs.TxOriginX4[2] {
		t.Fatal("equal-sized adjacent transform leaves share an origin")
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
		Compound: true, InterMode: InterModeGlobalMV,
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
