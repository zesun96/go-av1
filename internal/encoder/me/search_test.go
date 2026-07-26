package me

import (
	"math/rand"
	"testing"
)

func TestSearchIntegerTranslation(t *testing.T) {
	const width, height = 48, 48
	ref := randomFrame(width, height, 11)
	src := randomFrame(width, height, 12)
	want := MV{X: 3 * 8, Y: -2 * 8}
	copyPredictedBlock(src, ref, width, height, 16, 16, 16, 16, want)

	got, err := Search(testConfig(src, ref, width, height, 16, 16, 16, 16, 5))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.MV != want || got.SAD != 0 {
		t.Fatalf("result=%+v, want MV=%+v SAD=0", got, want)
	}
}

func TestSearchSubpixelTranslation(t *testing.T) {
	const width, height = 48, 48
	ref := randomFrame(width, height, 21)
	src := randomFrame(width, height, 22)
	want := MV{X: 4, Y: -2}
	copyPredictedBlock(src, ref, width, height, 16, 16, 16, 16, want)

	got, err := Search(testConfig(src, ref, width, height, 16, 16, 16, 16, 3))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.MV != want || got.SAD != 0 {
		t.Fatalf("result=%+v, want MV=%+v SAD=0", got, want)
	}
}

func TestSearchFlatBlockPrefersZeroVector(t *testing.T) {
	const width, height = 24, 24
	ref := make([]byte, width*height)
	src := make([]byte, width*height)
	for i := range ref {
		ref[i], src[i] = 90, 90
	}
	got, err := Search(testConfig(src, ref, width, height, 8, 8, 8, 8, 6))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.MV != (MV{}) || got.SAD != 0 {
		t.Fatalf("result=%+v, want zero MV and SAD", got)
	}
}

func TestSearchUsesReplicatedFrameEdges(t *testing.T) {
	const width, height = 24, 24
	ref := randomFrame(width, height, 31)
	src := randomFrame(width, height, 32)
	want := MV{X: -8, Y: -8}
	copyPredictedBlock(src, ref, width, height, 0, 0, 8, 8, want)
	got, err := Search(testConfig(src, ref, width, height, 0, 0, 8, 8, 3))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.MV != want || got.SAD != 0 {
		t.Fatalf("result=%+v, want MV=%+v SAD=0", got, want)
	}
}

func testConfig(src, ref []byte, width, height, x, y, bw, bh, searchRange int) Config {
	return Config{
		Source: src, Reference: ref,
		SourceStride: width, ReferenceStride: width,
		Width: width, Height: height,
		X: x, Y: y, BlockWidth: bw, BlockHeight: bh,
		SearchRange: searchRange,
	}
}

func copyPredictedBlock(dst, ref []byte, width, height, x, y, bw, bh int, mv MV) {
	for row := 0; row < bh; row++ {
		for col := 0; col < bw; col++ {
			dst[(y+row)*width+x+col] = byte(sampleBilinear(
				ref, width, width, height,
				(x+col)*8+mv.X, (y+row)*8+mv.Y))
		}
	}
}

func randomFrame(width, height int, seed int64) []byte {
	rng := rand.New(rand.NewSource(seed))
	frame := make([]byte, width*height)
	for i := range frame {
		frame[i] = byte(rng.Intn(256))
	}
	return frame
}
