package inter

import (
	"slices"
	"testing"
)

func TestWedgeMask8x8Index6(t *testing.T) {
	mask := WedgeMask(8, 8, 6)
	want := []byte{57, 43, 21, 7, 2, 0, 0, 0}
	if len(mask) != 64 || !slices.Equal(mask[:8], want) {
		t.Fatalf("mask first row=%v want %v", mask[:8], want)
	}
	if !slices.Equal(mask[56:], want) {
		t.Fatalf("vertical mask last row=%v want %v", mask[56:], want)
	}
}

func TestWedgeMask8x8Index10(t *testing.T) {
	want := []byte{
		37, 27, 18, 11, 6, 4, 2, 1,
		53, 46, 37, 27, 18, 11, 6, 4,
		60, 58, 53, 46, 37, 27, 18, 11,
		63, 62, 60, 58, 53, 46, 37, 27,
		64, 63, 63, 62, 60, 58, 53, 46,
		64, 64, 64, 63, 63, 62, 60, 58,
		64, 64, 64, 64, 64, 63, 63, 62,
		64, 64, 64, 64, 64, 64, 64, 63,
	}
	if got := WedgeMask(8, 8, 10); !slices.Equal(got, want) {
		t.Fatalf("index-10 mask=%v want %v", got, want)
	}
}

func TestSubsampleWedgeMask420(t *testing.T) {
	mask := WedgeMask(8, 8, 6)
	chroma, w, h := SubsampleMask(mask, 8, 8, 1, 1)
	if w != 4 || h != 4 || !slices.Equal(chroma[:4], []byte{50, 14, 1, 0}) {
		t.Fatalf("420 mask=%dx%d first row=%v", w, h, chroma[:4])
	}
}

func TestBlendMask(t *testing.T) {
	dst := []byte{90, 100, 103, 105, 99, 96, 97, 101}
	intra := []byte{92, 92, 92, 92, 92, 92, 92, 92}
	mask := []byte{57, 43, 21, 7, 2, 0, 0, 0}
	BlendMask(dst, 8, intra, mask, 8, 1)
	want := []byte{92, 95, 99, 104, 99, 96, 97, 101}
	if !slices.Equal(dst, want) {
		t.Fatalf("blend=%v want %v", dst, want)
	}
}

func TestInterIntraMaskRectUsesLongAxisStep(t *testing.T) {
	mask := InterIntraMask(8, 16, 1)
	if got := []byte{mask[0], mask[8], mask[16], mask[24]}; !slices.Equal(got, []byte{60, 45, 34, 26}) {
		t.Fatalf("vertical 8x16 weights=%v", got)
	}
}

func TestInterIntraMaskFourByFourUsesStepEight(t *testing.T) {
	mask := InterIntraMask(4, 4, 1)
	want := []byte{60, 19, 6, 2}
	for y, v := range want {
		if got := mask[y*4]; got != v {
			t.Fatalf("vertical mask row %d=%d want %d", y, got, v)
		}
	}
}
