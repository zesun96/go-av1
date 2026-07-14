package tile

import (
	"testing"

	"github.com/zesun96/go-av1/internal/header"
	"github.com/zesun96/go-av1/internal/transform"
)

func TestLumaFilterEdgeCodingBlockAndEqualTxSize(t *testing.T) {
	fs := NewFrameState(16, 8)
	fs.SetBlockState(0, 0, 8, 8, Av1Block{Intra: true})
	fs.SetBlockState(8, 0, 8, 8, Av1Block{Intra: true})
	fs.SetTxState(0, 0, 8, 8, transform.TX8x8)
	fs.SetTxState(8, 0, 8, 8, transform.TX8x8)
	if width, ok := fs.LumaFilterEdge(2, 0, true); !ok || width != 8 {
		t.Fatalf("coding-block edge=(%d,%t), want (8,true)", width, ok)
	}
}

func TestLumaFilterEdgeSkipsInnerTxEdgeForSkippedInter(t *testing.T) {
	fs := NewFrameState(16, 8)
	fs.SetBlockState(0, 0, 16, 8, Av1Block{Skip: true})
	fs.SetTxState(0, 0, 8, 8, transform.TX8x8)
	fs.SetTxState(8, 0, 8, 8, transform.TX8x8)
	if width, ok := fs.LumaFilterEdge(2, 0, true); ok || width != 0 {
		t.Fatalf("skipped inner edge=(%d,%t), want disabled", width, ok)
	}
}

func TestLumaFilterEdgeUsesDirectionalMinimumWidth(t *testing.T) {
	fs := NewFrameState(16, 16)
	fs.SetBlockState(0, 0, 16, 16, Av1Block{Intra: true})
	fs.SetTxState(0, 0, 8, 16, transform.RTX8x16)
	fs.SetTxState(8, 0, 8, 16, transform.RTX8x16)
	if width, ok := fs.LumaFilterEdge(2, 0, true); !ok || width != 8 {
		t.Fatalf("vertical edge=(%d,%t), want width 8", width, ok)
	}
}

func TestLumaFilterLevelAppliesDeltasInNormativeOrder(t *testing.T) {
	fs := NewFrameState(8, 8)
	fs.SetBlockState(0, 0, 8, 8, Av1Block{Intra: true, SegID: 2, LFDelta: [4]int8{3, -2}})
	fh := &header.FrameHeader{}
	fh.LoopFilter.LevelY = [2]uint8{30, 40}
	fh.Delta.LF.Multi = 1
	fh.Segmentation.Enabled = 1
	fh.Segmentation.SegData.D[2].DeltaLFYV = 4
	fh.Segmentation.SegData.D[2].DeltaLFYH = -5
	if got := fs.LumaFilterLevel(fh, 0, 0, true); got != 37 {
		t.Fatalf("vertical level=%d want 37", got)
	}
	if got := fs.LumaFilterLevel(fh, 0, 0, false); got != 33 {
		t.Fatalf("horizontal level=%d want 33", got)
	}
}

func TestLumaFilterLevelModeRefScaling(t *testing.T) {
	fs := NewFrameState(8, 8)
	fs.SetBlockState(0, 0, 8, 8, Av1Block{RefFrame: 3, InterMode: InterModeZeroMV})
	fh := &header.FrameHeader{}
	fh.LoopFilter.LevelY[0] = 32
	fh.LoopFilter.ModeRefDeltaEnabled = 1
	fh.LoopFilter.ModeRefDeltas.RefDelta[3] = -1
	fh.LoopFilter.ModeRefDeltas.RefDelta[4] = 4
	fh.LoopFilter.ModeRefDeltas.ModeDelta[0] = 2
	if got := fs.LumaFilterLevel(fh, 0, 0, true); got != 34 {
		t.Fatalf("mode/ref level=%d want 34", got)
	}
}

func TestChromaFilterLevelUsesReferenceFrameEnumAsDeltaIndex(t *testing.T) {
	fs := NewFrameState(8, 8)
	fs.SetSubsampling(1, 1)
	fs.SetChromaBlockState(0, 0, 8, 8, Av1Block{RefFrame: 3, InterMode: InterModeZeroMV})
	fh := &header.FrameHeader{}
	fh.LoopFilter.LevelU = 32
	fh.LoopFilter.ModeRefDeltaEnabled = 1
	fh.LoopFilter.ModeRefDeltas.RefDelta[3] = -1
	fh.LoopFilter.ModeRefDeltas.RefDelta[4] = 4
	fh.LoopFilter.ModeRefDeltas.ModeDelta[0] = 2
	if got := fs.ChromaFilterLevel(fh, 0, 0, 1); got != 34 {
		t.Fatalf("mode/ref level=%d want 34", got)
	}
}

func TestChromaFilterEdgeUsesChromaOwnerGrid(t *testing.T) {
	fs := NewFrameState(16, 8)
	fs.SetSubsampling(1, 1)
	left := Av1Block{Intra: true, Uvtx: transform.TX4x4}
	right := Av1Block{Intra: true, Uvtx: transform.TX4x4}
	fs.SetChromaBlockState(0, 0, 8, 8, left)
	fs.SetChromaBlockState(8, 0, 8, 8, right)
	if width, ok := fs.ChromaFilterEdge(1, 0, true); !ok || width != 4 {
		t.Fatalf("chroma owner boundary=(%d,%t), want (4,true)", width, ok)
	}
}
