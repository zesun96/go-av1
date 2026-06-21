package tile

import (
	"testing"

	"github.com/zesun96/go-av1/internal/refmvs"
)

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
	if got := fs.PaletteUVCtx(32, 16); got != 1 {
		t.Fatalf("PaletteUVCtx top-only = %d, want 1", got)
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
	if tb.MV != mv || tb.Ref != 6 {
		t.Fatalf("temporal block=(mv=%+v ref=%d) want (%+v,6)", tb.MV, tb.Ref, mv)
	}
	blk, ok := fs.BlockState(16, 16)
	if !ok {
		t.Fatal("BlockState missing")
	}
	if blk.Intra || blk.InterMode != InterModeNearestMV || blk.RefSlot != 5 || blk.MV != [2]int16{-12, 20} {
		t.Fatalf("block state=%+v", blk)
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
