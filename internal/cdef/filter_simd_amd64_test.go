//go:build amd64 && !purego

package cdef

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestFilterPrimary8SSE41MatchesScalar(t *testing.T) {
	if !havePrimary8SSE41 {
		t.Skip("SSE4.1/SSSE3 unavailable")
	}

	const stride = 12
	rng := rand.New(rand.NewSource(1))
	originalSetting := havePrimary8SSE41
	defer func() { havePrimary8SSE41 = originalSetting }()

	for h := 4; h <= 8; h += 4 {
		for dir := 0; dir < 8; dir++ {
			for strength := 1; strength <= 15; strength++ {
				for damping := 3; damping <= 6; damping++ {
					src := make([]byte, stride*12)
					if _, err := rng.Read(src); err != nil {
						t.Fatal(err)
					}
					scalar := append([]byte(nil), src...)
					simd := append([]byte(nil), src...)
					left := make([][2]uint8, h)
					for y := 0; y < h; y++ {
						left[y][0] = src[(y+2)*stride]
						left[y][1] = src[(y+2)*stride+1]
					}
					top := src[:]
					bottom := src[(h+2)*stride:]

					havePrimary8SSE41 = false
					FilterBlock(scalar, 2*stride+2, stride,
						left, top, 2, stride, bottom, 2, stride,
						strength, 0, dir, damping, 8, h, allEdges())
					havePrimary8SSE41 = true
					FilterBlock(simd, 2*stride+2, stride,
						left, top, 2, stride, bottom, 2, stride,
						strength, 0, dir, damping, 8, h, allEdges())

					if !bytes.Equal(simd, scalar) {
						t.Fatalf("mismatch h=%d dir=%d strength=%d damping=%d", h, dir, strength, damping)
					}
				}
			}
		}
	}
}

func TestFilterCombined8SSE41MatchesScalar(t *testing.T) {
	if !haveCombined8SSE41 {
		t.Skip("SSE4.1/SSSE3 unavailable")
	}

	const stride = 12
	rng := rand.New(rand.NewSource(2))
	originalSetting := haveCombined8SSE41
	defer func() { haveCombined8SSE41 = originalSetting }()
	secondaryStrengths := [...]int{1, 2, 4}

	for h := 4; h <= 8; h += 4 {
		for dir := 0; dir < 8; dir++ {
			for strength := 1; strength <= 15; strength++ {
				for _, secStrength := range secondaryStrengths {
					for damping := 3; damping <= 6; damping++ {
						for edgeBits := 0; edgeBits < 16; edgeBits++ {
							src := make([]byte, stride*12)
							if _, err := rng.Read(src); err != nil {
								t.Fatal(err)
							}
							scalar := append([]byte(nil), src...)
							simd := append([]byte(nil), src...)
							left := make([][2]uint8, h)
							for y := 0; y < h; y++ {
								left[y][0] = src[(y+2)*stride]
								left[y][1] = src[(y+2)*stride+1]
							}
							top := src[:]
							bottom := src[(h+2)*stride:]
							edges := EdgeFlags(edgeBits)

							haveCombined8SSE41 = false
							FilterBlock(scalar, 2*stride+2, stride,
								left, top, 2, stride, bottom, 2, stride,
								strength, secStrength, dir, damping, 8, h, edges)
							haveCombined8SSE41 = true
							FilterBlock(simd, 2*stride+2, stride,
								left, top, 2, stride, bottom, 2, stride,
								strength, secStrength, dir, damping, 8, h, edges)

							if !bytes.Equal(simd, scalar) {
								firstDiff := -1
								for i := range simd {
									if simd[i] != scalar[i] {
										firstDiff = i
										break
									}
								}
								t.Fatalf("mismatch h=%d dir=%d strength=%d sec=%d damping=%d edges=%d first_diff=%d scalar=%d simd=%d",
									h, dir, strength, secStrength, damping, edgeBits,
									firstDiff, scalar[firstDiff], simd[firstDiff])
							}
						}
					}
				}
			}
		}
	}
}

func BenchmarkFilterPrimary8SSE41(b *testing.B) {
	if !havePrimary8SSE41 {
		b.Skip("SSE4.1/SSSE3 unavailable")
	}

	const stride = 12
	src := make([]byte, stride*12)
	for i := range src {
		src[i] = byte((i*29 + 17) & 0xff)
	}
	left := make([][2]uint8, 8)
	for y := range left {
		left[y][0] = src[(y+2)*stride]
		left[y][1] = src[(y+2)*stride+1]
	}
	top := src[:]
	bottom := src[10*stride:]
	originalSetting := havePrimary8SSE41
	defer func() { havePrimary8SSE41 = originalSetting }()

	for _, tc := range []struct {
		name string
		simd bool
	}{
		{name: "Scalar", simd: false},
		{name: "SSE41", simd: true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			dst := append([]byte(nil), src...)
			havePrimary8SSE41 = tc.simd
			b.ReportAllocs()
			b.SetBytes(64)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				copy(dst, src)
				FilterBlock(dst, 2*stride+2, stride,
					left, top, 2, stride, bottom, 2, stride,
					8, 0, 2, 3, 8, 8, allEdges())
			}
		})
	}
}

func BenchmarkFilterCombined8SSE41(b *testing.B) {
	if !haveCombined8SSE41 {
		b.Skip("SSE4.1/SSSE3 unavailable")
	}

	const stride = 12
	src := make([]byte, stride*12)
	for i := range src {
		src[i] = byte((i*31 + 11) & 0xff)
	}
	left := make([][2]uint8, 8)
	for y := range left {
		left[y][0] = src[(y+2)*stride]
		left[y][1] = src[(y+2)*stride+1]
	}
	top := src[:]
	bottom := src[10*stride:]
	originalSetting := haveCombined8SSE41
	defer func() { haveCombined8SSE41 = originalSetting }()

	for _, tc := range []struct {
		name string
		simd bool
	}{
		{name: "Scalar", simd: false},
		{name: "SSE41", simd: true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			dst := append([]byte(nil), src...)
			haveCombined8SSE41 = tc.simd
			b.ReportAllocs()
			b.SetBytes(64)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				copy(dst, src)
				FilterBlock(dst, 2*stride+2, stride,
					left, top, 2, stride, bottom, 2, stride,
					8, 4, 2, 3, 8, 8, allEdges())
			}
		})
	}
}
