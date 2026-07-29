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

func absInt8(value int8) int {
	if value < 0 {
		return -int(value)
	}
	return int(value)
}
