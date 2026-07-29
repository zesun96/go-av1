//go:build amd64 && !purego

package inter

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestPut8TapHVSSE41MatchesScalar(t *testing.T) {
	if !havePut8TapSSE41 {
		t.Skip("SSE4.1/SSSE3 unavailable")
	}
	rng := rand.New(rand.NewSource(41))
	original := havePut8TapSSE41
	defer func() { havePut8TapSSE41 = original }()

	for _, w := range []int{8, 16, 32, 64, 128} {
		for _, h := range []int{4, 8, 16, 32, 64, 128} {
			for f := Filter2D(0); f < Filter2DBilinear; f++ {
				for mx := 1; mx < 16; mx++ {
					for my := 1; my < 16; my++ {
						stride := w + 16
						rows := h + 16
						src := make([]byte, stride*rows)
						if _, err := rng.Read(src); err != nil {
							t.Fatal(err)
						}
						srcBase := 8*stride + 8
						scalar := make([]byte, w*h)
						simd := make([]byte, w*h)

						havePut8TapSSE41 = false
						Put8Tap(scalar, w, src, srcBase, stride,
							w, h, mx, my, f)
						havePut8TapSSE41 = true
						Put8Tap(simd, w, src, srcBase, stride,
							w, h, mx, my, f)
						if !bytes.Equal(simd, scalar) {
							for i := range simd {
								if simd[i] != scalar[i] {
									t.Fatalf("mismatch w=%d h=%d f=%d mx=%d my=%d i=%d scalar=%d simd=%d",
										w, h, f, mx, my, i, scalar[i], simd[i])
								}
							}
						}
					}
				}
			}
		}
	}
}

func TestPut8TapSingleAxisSSE41MatchesScalar(t *testing.T) {
	if !havePut8TapSSE41 {
		t.Skip("SSE4.1/SSSE3 unavailable")
	}
	rng := rand.New(rand.NewSource(42))
	original := havePut8TapSSE41
	defer func() { havePut8TapSSE41 = original }()

	for _, w := range []int{8, 16, 32, 64, 128} {
		for _, h := range []int{4, 8, 16, 32, 64, 128} {
			for f := Filter2D(0); f < Filter2DBilinear; f++ {
				for phase := 1; phase < 16; phase++ {
					stride := w + 16
					src := make([]byte, stride*(h+16))
					if _, err := rng.Read(src); err != nil {
						t.Fatal(err)
					}
					srcBase := 8*stride + 8
					for _, offsets := range [][2]int{{phase, 0}, {0, phase}} {
						scalar := make([]byte, w*h)
						simd := make([]byte, w*h)

						havePut8TapSSE41 = false
						Put8Tap(scalar, w, src, srcBase, stride,
							w, h, offsets[0], offsets[1], f)
						havePut8TapSSE41 = true
						Put8Tap(simd, w, src, srcBase, stride,
							w, h, offsets[0], offsets[1], f)
						if !bytes.Equal(simd, scalar) {
							for i := range simd {
								if simd[i] != scalar[i] {
									t.Fatalf("mismatch w=%d h=%d f=%d mx=%d my=%d i=%d scalar=%d simd=%d",
										w, h, f, offsets[0], offsets[1],
										i, scalar[i], simd[i])
								}
							}
						}
					}
				}
			}
		}
	}
}

func TestPut8TapSSE41PairwiseCoefficientRange(t *testing.T) {
	for filterType := FilterType(0); filterType < NFilterTypes; filterType++ {
		for phase, filter := range McSubpelFilters[filterType] {
			for tap := 0; tap < 8; tap += 2 {
				sum := absInt8(filter[tap]) + absInt8(filter[tap+1])
				if sum > 128 {
					t.Fatalf("filter=%d phase=%d taps=%d/%d absolute sum=%d exceeds PMADDUBSW range",
						filterType, phase+1, tap, tap+1, sum)
				}
			}
		}
	}
}

func TestFilter8TapHorizontalSSE41ExtremeInputs(t *testing.T) {
	if !havePut8TapSSE41 {
		t.Skip("SSE4.1/SSSE3 unavailable")
	}
	for filterType := FilterType(0); filterType < NFilterTypes; filterType++ {
		for phase := range McSubpelFilters[filterType] {
			filter := &McSubpelFilters[filterType][phase]
			for _, positive := range []bool{false, true} {
				src := make([]byte, 15)
				for tap, coefficient := range filter {
					if (coefficient > 0) == positive && coefficient != 0 {
						src[tap] = 255
					}
				}
				var got [128]int16
				filter8TapHorizontalSSE41(&got[0], &src[3], 15, 8, 1, &filter[0])
				for x := 0; x < 8; x++ {
					want := int16((filter8H(src, x+3, filter[:]) + 2) >> 2)
					if got[x] != want {
						t.Fatalf("filter=%d phase=%d positive=%t x=%d got=%d want=%d",
							filterType, phase+1, positive, x, got[x], want)
					}
				}
			}
		}
	}
}

func absInt8(value int8) int {
	if value < 0 {
		return -int(value)
	}
	return int(value)
}
