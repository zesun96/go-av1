package tile

import (
	"testing"

	"github.com/zesun96/go-av1/internal/transform"
)

func TestPackedCoeffIndexUsesColumnMajorLayout(t *testing.T) {
	tests := []struct {
		name           string
		blockW, blockH int
		packedH        int
		x, y           int
		want           int
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

func TestCoeffTokenGeomMatchesDav1d1DClasses(t *testing.T) {
	h := transform.TxfmDimensions[transform.RTX16x8]
	if got := 4 << h.Lh; got != int(h.H)*4 {
		t.Fatalf("H-class packedH=%d want %d", got, int(h.H)*4)
	}
	if got := 16 * ((4 << h.Lh) + 2); got != 16*(int(h.H)*4+2) {
		t.Fatalf("H-class levels len=%d want %d", got, 16*(int(h.H)*4+2))
	}

	v := transform.TxfmDimensions[transform.RTX8x16]
	if got := 4 << v.Lh; got != int(v.H)*4 {
		t.Fatalf("V-class packedH=%d want %d", got, int(v.H)*4)
	}
	if got := 16 * ((4 << v.Lw) + 2); got != 16*(int(v.W)*4+2) {
		t.Fatalf("V-class levels len=%d want %d", got, 16*(int(v.W)*4+2))
	}
}

func TestPackedCoeffIndexForClassMatchesDav1dRCFormula(t *testing.T) {
	tdH := transform.TxfmDimensions[transform.RTX16x8]
	maskH := (4 << tdH.Lh) - 1
	shiftH := tdH.Lh + 2
	for i := 0; i < int(tdH.W)*4*int(tdH.H)*4; i++ {
		x := i & int(maskH)
		y := i >> shiftH
		got := packedCoeffIndexForClass(TxClassH, int(tdH.W)*4, int(tdH.H)*4, 4<<tdH.Lh, x, y)
		if got != i {
			t.Fatalf("H-class rc[%d] => %d, want %d", i, got, i)
		}
	}

	tdV := transform.TxfmDimensions[transform.RTX8x16]
	maskV := (4 << tdV.Lw) - 1
	shiftV := tdV.Lw + 2
	shift2V := tdV.Lh + 2
	for i := 0; i < int(tdV.W)*4*int(tdV.H)*4; i++ {
		x := i & int(maskV)
		y := i >> shiftV
		want := (x << shift2V) | y
		got := packedCoeffIndexForClass(TxClassV, int(tdV.W)*4, int(tdV.H)*4, 4<<tdV.Lh, x, y)
		if got != want {
			t.Fatalf("V-class rc[%d] => %d, want %d", i, got, want)
		}
	}
}

func TestCoeffTraversalPointMatchesDav1d1DLayouts(t *testing.T) {
	tests := []struct {
		name string
		cls  TxClass
		tx   uint8
	}{
		{name: "h_16x8", cls: TxClassH, tx: transform.RTX16x8},
		{name: "h_32x8", cls: TxClassH, tx: transform.RTX32x8},
		{name: "v_8x16", cls: TxClassV, tx: transform.RTX8x16},
		{name: "v_8x32", cls: TxClassV, tx: transform.RTX8x32},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			td := transform.TxfmDimensions[tc.tx]
			slw, slh := minInt(int(td.Lw), 3), minInt(int(td.Lh), 3)
			shift := slh + 2
			mask := (4 << slh) - 1
			levelsLen := 16 * ((4 << slh) + 2)
			if tc.cls == TxClassV {
				shift = slw + 2
				mask = (4 << slw) - 1
				levelsLen = 16 * ((4 << slw) + 2)
			}
			geom := coeffTokenGeom{cls: tc.cls, blockW: int(td.W) * 4, blockH: int(td.H) * 4,
				packedH: 4 << slh, stride: 16, shift: uint(shift), mask: mask}
			for pos := 0; pos < geom.blockW*geom.blockH; pos++ {
				x, y, levelIdx, coeffIdx, ok := coeffTraversalPoint(geom, pos, nil)
				if !ok {
					t.Fatalf("position %d rejected", pos)
				}
				if levelIdx != x*16+y || levelIdx >= levelsLen {
					t.Fatalf("position %d level=%d (x=%d y=%d), buffer=%d", pos, levelIdx, x, y, levelsLen)
				}
				wantRC := pos
				if tc.cls == TxClassV {
					wantRC = (x << (slh + 2)) | y
				}
				if coeffIdx != wantRC {
					t.Fatalf("position %d rc=%d, want %d", pos, coeffIdx, wantRC)
				}
			}
		})
	}
}
