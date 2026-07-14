package tile

import "testing"

func TestTileNeighbourStateDoesNotLeak(t *testing.T) {
	leftTile := NewFrameState(640, 480)
	leftTile.SetBlock(316, 0, 4, 4, false, DCPred)
	if got := leftTile.LeftModeCtx(320, 0); got != DCPred {
		t.Fatalf("left tile context = %d, want %d", got, DCPred)
	}

	rightTile := NewFrameState(640, 480)
	if got := rightTile.LeftModeCtx(320, 0); got != 0 {
		t.Fatalf("fresh tile inherited left context %d", got)
	}
}

func TestCloneForFrameResetsCDFCountersOnly(t *testing.T) {
	ctx := NewTileCtx()
	ctx.SkipCDF[0] = [3]uint16{130, 32, 9}
	ctx.EobBin16Full[0][0][0] = 1234
	ctx.EobBin16Full[0][0][4] = 17
	ctx.FilterCDF[0][0][2] = 11
	ctx.ColorMapCDF[0][2][0][3] = 7
	ctx.Partition128CDF[0][7] = 13
	ctx.Partition32CDF[0][9] = 21
	ctx.TxTypeIntra1CDF[1][3][TxTypeIntra1Symbols-1] = 32
	ctx.TxTypeIntra2CDF[2][4][TxTypeIntra2Symbols-1] = 31
	ctx.TxTypeInter1CDF[1][TxTypeInter1Symbols-1] = 30
	ctx.TxTypeInter2CDF[TxTypeInter2Symbols-1] = 29
	ctx.KFYModeCDF[2][3][NIntraPredModes-1] = 28
	ctx.YModeCDF[1][NIntraPredModes-1] = 27
	ctx.AngleDeltaCDF[4][6] = 26
	ctx.FilterIntraModeCDF[4] = 25
	ctx.MVJointCDF[3] = 24
	ctx.MVClassesCDF[1][10] = 23
	ctx.MVClass0FPCDF[0][1][3] = 22
	ctx.MVClassNFPCDF[1][3] = 21
	ctx.LastQIdx = 208
	ctx.LastQIdxValid = true
	ctx.LastDeltaLF = [4]int8{1, -2, 3, -4}

	got := ctx.CloneForFrame()
	if got.SkipCDF[0][0] != 130 || got.SkipCDF[0][1] != 0 {
		t.Fatalf("SkipCDF clone = %v, want probability 130 and counter 0", got.SkipCDF[0])
	}
	if got.EobBin16Full[0][0][0] != 1234 || got.EobBin16Full[0][0][4] != 0 {
		t.Fatalf("EobBin16 clone = %v", got.EobBin16Full[0][0])
	}
	if got.FilterCDF[0][0][2] != 0 {
		t.Fatalf("FilterCDF counter = %d, want 0", got.FilterCDF[0][0][2])
	}
	if got.ColorMapCDF[0][2][0][3] != 0 {
		t.Fatalf("ColorMapCDF counter = %d, want 0", got.ColorMapCDF[0][2][0][3])
	}
	if got.Partition128CDF[0][7] != 0 || got.Partition32CDF[0][9] != 0 {
		t.Fatalf("partition counters = %d/%d, want 0/0", got.Partition128CDF[0][7], got.Partition32CDF[0][9])
	}
	if got.TxTypeIntra1CDF[1][3][TxTypeIntra1Symbols-1] != 0 ||
		got.TxTypeIntra2CDF[2][4][TxTypeIntra2Symbols-1] != 0 ||
		got.TxTypeInter1CDF[1][TxTypeInter1Symbols-1] != 0 ||
		got.TxTypeInter2CDF[TxTypeInter2Symbols-1] != 0 {
		t.Fatalf("transform-type counters were not reset")
	}
	if got.KFYModeCDF[2][3][NIntraPredModes-1] != 0 || got.YModeCDF[1][NIntraPredModes-1] != 0 ||
		got.AngleDeltaCDF[4][6] != 0 || got.FilterIntraModeCDF[4] != 0 {
		t.Fatalf("padded syntax counters were not reset")
	}
	if got.MVJointCDF[3] != 0 || got.MVClassesCDF[1][10] != 0 ||
		got.MVClass0FPCDF[0][1][3] != 0 || got.MVClassNFPCDF[1][3] != 0 {
		t.Fatalf("motion-vector counters were not reset")
	}
	if ctx.SkipCDF[0][1] != 32 {
		t.Fatalf("CloneForFrame mutated source counter: %d", ctx.SkipCDF[0][1])
	}
	if got.LastQIdxValid || got.LastQIdx != 0 {
		t.Fatalf("delta-q predictor leaked into frame: valid=%t qidx=%d", got.LastQIdxValid, got.LastQIdx)
	}
	if got.LastDeltaLF != [4]int8{} {
		t.Fatalf("delta-lf predictors leaked into frame: %v", got.LastDeltaLF)
	}
}
