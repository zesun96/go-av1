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
	fs.SetInterBlock(16, 16, 16, 16, false, 2, 5, 3, 3, InterModeNearestMV, mv)

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
}
