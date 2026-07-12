package tile

import (
	"testing"

	"github.com/zesun96/go-av1/internal/header"
)

func TestChromaRect(t *testing.T) {
	tests := []struct {
		name         string
		ssHor, ssVer uint8
		bx, by       int
		bw, bh       int
		want         [4]int
	}{
		{"420", 1, 1, 8, 12, 16, 20, [4]int{4, 4, 8, 12}},
		{"420-sub8x8-odd", 1, 1, 4, 4, 4, 4, [4]int{0, 0, 4, 4}},
		{"420-8x4-odd-row", 1, 1, 8, 4, 8, 4, [4]int{4, 0, 4, 4}},
		{"420-4x8-odd-col", 1, 1, 4, 8, 4, 8, [4]int{0, 4, 4, 4}},
		{"422", 1, 0, 8, 12, 16, 20, [4]int{4, 12, 8, 20}},
		{"444", 0, 0, 8, 12, 16, 20, [4]int{8, 12, 16, 20}},
	}
	for _, tc := range tests {
		seq := &header.SequenceHeader{SsHor: tc.ssHor, SsVer: tc.ssVer}
		cbx, cby, cbw, cbh := chromaRect(seq, tc.bx, tc.by, tc.bw, tc.bh)
		got := [4]int{cbx, cby, cbw, cbh}
		if got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestBlockHasChroma444(t *testing.T) {
	seq := &header.SequenceHeader{SsHor: 0, SsVer: 0}
	fb := &FrameBuf{
		U:          make([]byte, 64),
		V:          make([]byte, 64),
		ChromaW:    8,
		ChromaH:    8,
		Monochrome: false,
	}
	if !blockHasChroma(seq, fb, 0, 0, 4, 4) {
		t.Fatal("blockHasChroma(444) = false, want true")
	}
}
