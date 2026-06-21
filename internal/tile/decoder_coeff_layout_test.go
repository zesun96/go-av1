package tile

import "testing"

func TestPackedCoeffIndexUsesColumnMajorLayout(t *testing.T) {
	tests := []struct {
		name          string
		blockW, blockH int
		packedH       int
		x, y          int
		want          int
	}{
		{name: "2d4x4", blockW: 4, blockH: 4, packedH: 4, x: 2, y: 1, want: 9},
		{name: "h16x4", blockW: 16, blockH: 4, packedH: 4, x: 5, y: 2, want: 22},
		{name: "v4x16", blockW: 4, blockH: 16, packedH: 16, x: 2, y: 5, want: 37},
	}
	for _, tc := range tests {
		if got := packedCoeffIndex(tc.blockW, tc.blockH, tc.packedH, tc.x, tc.y); got != tc.want {
			t.Fatalf("%s: packedCoeffIndex(%d,%d,%d,%d,%d) = %d, want %d",
				tc.name, tc.blockW, tc.blockH, tc.packedH, tc.x, tc.y, got, tc.want)
		}
	}
}

func TestPackedCoeffIndexRejectsOutOfRange(t *testing.T) {
	if got := packedCoeffIndex(16, 4, 4, 16, 0); got != -1 {
		t.Fatalf("x overflow = %d, want -1", got)
	}
	if got := packedCoeffIndex(16, 4, 4, 0, 4); got != -1 {
		t.Fatalf("y overflow = %d, want -1", got)
	}
}

func TestPackedCoeffIndexForClassMapsHClassFromTransposedTraversal(t *testing.T) {
	got := packedCoeffIndexForClass(TxClassH, 16, 4, 4, 2, 5)
	if got != 22 {
		t.Fatalf("H-class packed index = %d, want 22", got)
	}

	got = packedCoeffIndexForClass(TxClassV, 4, 16, 16, 2, 5)
	if got != 37 {
		t.Fatalf("V-class packed index = %d, want 37", got)
	}
}

func TestResidualMagFromTokPassesThroughBaseTokens(t *testing.T) {
	for tok := 0; tok < 15; tok++ {
		if got := residualMagFromTok(nil, tok); got != tok {
			t.Fatalf("tok=%d => %d, want %d", tok, got, tok)
		}
	}
}

func TestCoeffTokenPackingUsesDav1dShiftedNextIndex(t *testing.T) {
	rcTok := (7 << coeffTokShift) | 0x3ab
	if got := rcTok >> coeffTokShift; got != 7 {
		t.Fatalf("packed tok = %d, want 7", got)
	}
	if got := rcTok & coeffNextMask; got != 0x3ab {
		t.Fatalf("packed next = %#x, want 0x3ab", got)
	}
	if rcTok&(1<<10) != 0 {
		t.Fatalf("packed spare bit set in %#x", rcTok)
	}
}

func TestGetLoCtx1DMatchesDav1dNeighbourFeedback(t *testing.T) {
	levels := make([]uint8, 16*6)
	levels[1] = 64
	levels[16] = 65
	levels[2] = 66
	levels[3] = 67
	levels[4] = 68

	ctx0, hiMag0 := getLoCtx1D(levels, 0, 16, 0)
	if hiMag0 != 195 {
		t.Fatalf("hiMag(y=0) = %d, want 195", hiMag0)
	}
	if ctx0 != 29 { // 26 + ((330 + 64) >> 7) = 26 + 3
		t.Fatalf("ctx(y=0) = %d, want 29", ctx0)
	}

	ctx2, hiMag2 := getLoCtx1D(levels, 0, 16, 2)
	if hiMag2 != 195 {
		t.Fatalf("hiMag(y=2) = %d, want 195", hiMag2)
	}
	if ctx2 != 39 { // 36 + 3
		t.Fatalf("ctx(y=2) = %d, want 39", ctx2)
	}
}
