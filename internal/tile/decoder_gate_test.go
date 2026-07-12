package tile

import (
	"testing"

	"github.com/zesun96/go-av1/internal/header"
)

func TestKFYModeCDFsAreMonotonic(t *testing.T) {
	for top := range KFYMCDFDefault {
		for left := range KFYMCDFDefault[top] {
			cdf := KFYMCDFDefault[top][left]
			for i := 1; i < NIntraPredModes; i++ {
				if cdf[i] > cdf[i-1] {
					t.Fatalf("KFY[%d][%d] rises at %d: %d > %d", top, left, i, cdf[i], cdf[i-1])
				}
			}
		}
	}
}

func TestCFLAllowedForBlock(t *testing.T) {
	seq420 := &header.SequenceHeader{SsHor: 1, SsVer: 1}
	seq444 := &header.SequenceHeader{}

	if !cflAllowedForBlock(seq420, 32, 32, false) {
		t.Fatal("32x32 4:2:0 block should allow CFL")
	}
	if cflAllowedForBlock(seq420, 64, 64, false) {
		t.Fatal("64x64 4:2:0 block should not allow CFL")
	}
	if !cflAllowedForBlock(seq420, 4, 4, true) {
		t.Fatal("lossless 4:4 luma block should allow CFL on 4:2:0 chroma")
	}
	if !cflAllowedForBlock(seq420, 8, 8, true) {
		t.Fatal("lossless 8:8 luma block should allow CFL on 4:2:0 chroma")
	}
	if cflAllowedForBlock(seq420, 16, 16, true) {
		t.Fatal("lossless 16:16 luma block should not allow CFL on 4:2:0 chroma")
	}
	if !cflAllowedForBlock(seq444, 4, 4, true) {
		t.Fatal("lossless 4:4:4 4x4 block should allow CFL")
	}
	if cflAllowedForBlock(seq444, 8, 8, true) {
		t.Fatal("lossless 4:4:4 8x8 block should not allow CFL")
	}
}

func TestBuildCflAc420MatchesDav1dShape(t *testing.T) {
	seq := &header.SequenceHeader{SsHor: 1, SsVer: 1}
	fb := &FrameBuf{
		Y: []byte{
			1, 2, 3, 4, 5, 6, 7, 8,
			9, 10, 11, 12, 13, 14, 15, 16,
			17, 18, 19, 20, 21, 22, 23, 24,
			25, 26, 27, 28, 29, 30, 31, 32,
			33, 34, 35, 36, 37, 38, 39, 40,
			41, 42, 43, 44, 45, 46, 47, 48,
			49, 50, 51, 52, 53, 54, 55, 56,
			57, 58, 59, 60, 61, 62, 63, 64,
		},
		StrideY: 8,
		Width:   8,
		Height:  8,
	}

	got := buildCflAc(fb, seq, 0, 0, 8, 8, 4, 4)
	want := buildCflAcRef(fb.Y, fb.StrideY, fb.Width, fb.Height, 0, 0, 8, 8, 4, 4, 1, 1)
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("420 ac[%d]=%d want %d", i, got[i], want[i])
		}
	}
}

func TestBuildCflAc444MatchesDav1dShape(t *testing.T) {
	seq := &header.SequenceHeader{}
	fb := &FrameBuf{
		Y: []byte{
			2, 4, 6, 8,
			10, 12, 14, 16,
			18, 20, 22, 24,
			26, 28, 30, 32,
		},
		StrideY: 4,
		Width:   4,
		Height:  4,
	}

	got := buildCflAc(fb, seq, 0, 0, 4, 4, 4, 4)
	want := buildCflAcRef(fb.Y, fb.StrideY, fb.Width, fb.Height, 0, 0, 4, 4, 4, 4, 0, 0)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("444 ac[%d]=%d want %d", i, got[i], want[i])
		}
	}
}

func TestBuildCflAc420PadsRightAndBottom(t *testing.T) {
	seq := &header.SequenceHeader{SsHor: 1, SsVer: 1}
	fb := &FrameBuf{
		Y: []byte{
			1, 2, 3, 4, 5, 6, 7, 8,
			9, 10, 11, 12, 13, 14, 15, 16,
			17, 18, 19, 20, 21, 22, 23, 24,
			25, 26, 27, 28, 29, 30, 31, 32,
			33, 34, 35, 36, 37, 38, 39, 40,
			41, 42, 43, 44, 45, 46, 47, 48,
			49, 50, 51, 52, 53, 54, 55, 56,
			57, 58, 59, 60, 61, 62, 63, 64,
		},
		StrideY: 8,
		Width:   8,
		Height:  8,
	}

	got := buildCflAc(fb, seq, 2, 2, 6, 6, 4, 4)
	want := buildCflAcRef(fb.Y, fb.StrideY, fb.Width, fb.Height, 2, 2, 6, 6, 4, 4, 1, 1)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("420-pad ac[%d]=%d want %d", i, got[i], want[i])
		}
	}
}

func TestBuildCflAc420AlignsOddOriginToChromaPhase(t *testing.T) {
	seq := &header.SequenceHeader{SsHor: 1, SsVer: 1}
	fb := &FrameBuf{
		Y: []byte{
			1, 2, 3, 4, 5, 6, 7, 8,
			9, 10, 11, 12, 13, 14, 15, 16,
			17, 18, 19, 20, 21, 22, 23, 24,
			25, 26, 27, 28, 29, 30, 31, 32,
			33, 34, 35, 36, 37, 38, 39, 40,
			41, 42, 43, 44, 45, 46, 47, 48,
			49, 50, 51, 52, 53, 54, 55, 56,
			57, 58, 59, 60, 61, 62, 63, 64,
		},
		StrideY: 8,
		Width:   8,
		Height:  8,
	}

	got := buildCflAc(fb, seq, 1, 1, 6, 6, 3, 3)
	want := buildCflAcRef(fb.Y, fb.StrideY, fb.Width, fb.Height, 1, 1, 6, 6, 3, 3, 1, 1)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("420-odd ac[%d]=%d want %d", i, got[i], want[i])
		}
	}
}

func buildCflAcRef(y []byte, stride, width, height, bx, by, bw, bh, cw, ch, ssHor, ssVer int) []int16 {
	ac := make([]int16, cw*ch)
	baseX := bx
	baseY := by
	if ssHor != 0 {
		baseX &^= (1 << ssHor) - 1
	}
	if ssVer != 0 {
		baseY &^= (1 << ssVer) - 1
	}
	validW := cw
	validH := ch
	if remW := width - baseX; remW >= 0 {
		maxW := (remW + (1 << ssHor) - 1) >> ssHor
		if validW > maxW {
			validW = maxW
		}
	} else {
		validW = 0
	}
	if remH := height - baseY; remH >= 0 {
		maxH := (remH + (1 << ssVer) - 1) >> ssVer
		if validH > maxH {
			validH = maxH
		}
	} else {
		validH = 0
	}
	if validW > cw {
		validW = cw
	}
	if validH > ch {
		validH = ch
	}
	for cy := 0; cy < validH; cy++ {
		srcY := baseY + (cy << ssVer)
		if srcY >= height {
			srcY = height - 1
		}
		srcY1 := srcY
		if ssVer != 0 && srcY1+1 < height {
			srcY1++
		}
		for cx := 0; cx < validW; cx++ {
			srcX := baseX + (cx << ssHor)
			if srcX >= width {
				srcX = width - 1
			}
			srcX1 := srcX
			if ssHor != 0 && srcX1+1 < width {
				srcX1++
			}
			acSum := int(y[srcY*stride+srcX])
			if ssHor != 0 {
				acSum += int(y[srcY*stride+srcX1])
			}
			if ssVer != 0 {
				acSum += int(y[srcY1*stride+srcX])
				if ssHor != 0 {
					acSum += int(y[srcY1*stride+srcX1])
				}
			}
			ac[cy*cw+cx] = int16(acSum << (1 + testBoolInt(ssVer == 0) + testBoolInt(ssHor == 0)))
		}
		for cx := validW; cx < cw; cx++ {
			ac[cy*cw+cx] = ac[cy*cw+cx-1]
		}
	}
	for cy := validH; cy < ch; cy++ {
		copy(ac[cy*cw:(cy+1)*cw], ac[(cy-1)*cw:cy*cw])
	}
	log2sz := ctzPow2(cw) + ctzPow2(ch)
	sum := (1 << log2sz) >> 1
	for _, v := range ac {
		sum += int(v)
	}
	sum >>= log2sz
	for i := range ac {
		ac[i] -= int16(sum)
	}
	return ac
}

func testBoolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
