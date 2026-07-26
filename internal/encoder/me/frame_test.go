package me

import "testing"

func TestAnalyzeFrameFindsTranslationIncludingEdgeBlocks(t *testing.T) {
	const width, height = 45, 37
	ref := randomFrame(width, height, 41)
	src := make([]byte, width*height)
	want := MV{X: 4, Y: -2}
	copyPredictedBlock(src, ref, width, height, 0, 0, width, height, want)

	got, err := AnalyzeFrame(FrameConfig{
		Source: src, Reference: ref,
		SourceStride: width, ReferenceStride: width,
		Width: width, Height: height,
		BlockSize: 16, SearchRange: 3,
	})
	if err != nil {
		t.Fatalf("AnalyzeFrame: %v", err)
	}
	if len(got.Blocks) != 9 {
		t.Fatalf("blocks=%d, want 9", len(got.Blocks))
	}
	if got.TotalSAD != 0 {
		t.Fatalf("TotalSAD=%d, want 0", got.TotalSAD)
	}
	if got.ZeroSAD == 0 {
		t.Fatal("ZeroSAD=0, expected motion compensation to improve distortion")
	}
	for i, block := range got.Blocks {
		if block.Result.MV != want || block.Result.SAD != 0 {
			t.Fatalf("block %d at (%d,%d) result=%+v, want MV=%+v SAD=0",
				i, block.X, block.Y, block.Result, want)
		}
	}
	last := got.Blocks[len(got.Blocks)-1]
	if last.Width != 13 || last.Height != 5 {
		t.Fatalf("last block=%dx%d, want 13x5", last.Width, last.Height)
	}
}

func TestAnalyzeFrameRejectsInvalidBlockSize(t *testing.T) {
	const width, height = 8, 8
	frame := make([]byte, width*height)
	_, err := AnalyzeFrame(FrameConfig{
		Source: frame, Reference: frame,
		SourceStride: width, ReferenceStride: width,
		Width: width, Height: height,
	})
	if err == nil {
		t.Fatal("AnalyzeFrame accepted zero block size")
	}
}
