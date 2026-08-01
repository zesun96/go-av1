//go:build amd64 && !purego

package transform

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestAddDCSSE2MatchesScalar(t *testing.T) {
	if !haveAddDCSSE2 {
		t.Skip("SSE2 unavailable")
	}
	rng := rand.New(rand.NewSource(91))
	original := haveAddDCSSE2
	defer func() { haveAddDCSSE2 = original }()
	coefficients := [...]int32{-1_000_000, -10_000, -1, 0, 1, 10_000, 1_000_000}

	for tx := uint8(0); tx < NRectTxSizes; tx++ {
		td := TxfmDimensions[tx]
		w, h := int(td.W)*4, int(td.H)*4
		if w < 8 {
			continue
		}
		for shift := 0; shift <= 2; shift++ {
			for _, coefficient := range coefficients {
				stride := w + 3
				template := make([]byte, stride*h)
				if _, err := rng.Read(template); err != nil {
					t.Fatal(err)
				}
				scalar := append([]byte(nil), template...)
				simd := append([]byte(nil), template...)
				scalarCoeff := make([]int32, w*h)
				simdCoeff := make([]int32, w*h)
				scalarCoeff[0] = coefficient
				simdCoeff[0] = coefficient

				haveAddDCSSE2 = false
				InvTxfmAdd(scalar, stride, scalarCoeff, 0,
					tx, shift, DCT_DCT, 8)
				haveAddDCSSE2 = true
				InvTxfmAdd(simd, stride, simdCoeff, 0,
					tx, shift, DCT_DCT, 8)
				if !bytes.Equal(simd, scalar) {
					for i := range simd {
						if simd[i] != scalar[i] {
							t.Fatalf("mismatch tx=%d w=%d h=%d shift=%d coefficient=%d i=%d scalar=%d simd=%d",
								tx, w, h, shift, coefficient, i, scalar[i], simd[i])
						}
					}
				}
			}
		}
	}
}

func TestAddResidualSSE41MatchesScalar(t *testing.T) {
	if !haveAddResidualSSE41 {
		t.Skip("SSE4.1 unavailable")
	}
	rng := rand.New(rand.NewSource(92))
	for tx := uint8(0); tx < NRectTxSizes; tx++ {
		td := TxfmDimensions[tx]
		w, h := int(td.W)*4, int(td.H)*4
		if w < 8 {
			continue
		}
		for iteration := 0; iteration < 100; iteration++ {
			stride := w + 5
			scalar := make([]byte, stride*h)
			if _, err := rng.Read(scalar); err != nil {
				t.Fatal(err)
			}
			simd := append([]byte(nil), scalar...)
			src := make([]int32, w*h)
			for i := range src {
				src[i] = int32(rng.Intn(1<<16) - (1 << 15))
			}
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					i := y*w + x
					d := y*stride + x
					scalar[d] = uint8(pixelClamp(
						int(scalar[d])+((int(src[i])+8)>>4), 255))
				}
			}
			addResidual8SSE41(&simd[0], stride, &src[0], w, h)
			if !bytes.Equal(simd, scalar) {
				for i := range simd {
					if simd[i] != scalar[i] {
						t.Fatalf("mismatch tx=%d w=%d h=%d iteration=%d i=%d scalar=%d simd=%d",
							tx, w, h, iteration, i, scalar[i], simd[i])
					}
				}
			}
		}
	}
}

func TestDCT8Batch4SSE41MatchesScalar(t *testing.T) {
	if !haveDCT8BatchSSE41 {
		t.Skip("SSE4.1 unavailable")
	}
	rng := rand.New(rand.NewSource(93))
	for iteration := 0; iteration < 2000; iteration++ {
		var src, got, want [64]int32
		for i := range src {
			src[i] = int32(rng.Intn(1<<16) - (1 << 15))
		}
		for lane := 0; lane < 8; lane++ {
			var values [8]int32
			for position := range values {
				values[position] = src[position*8+lane]
			}
			InvDCT8(values[:], 1, -1<<15, 1<<15-1)
			for position := range values {
				want[position*8+lane] = values[position]
			}
		}
		dct8Batch4SSE41(&got[0], &src[0])
		dct8Batch4SSE41(&got[4], &src[4])
		if got != want {
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("iteration=%d value=%d: SIMD=%d scalar=%d", iteration, i, got[i], want[i])
				}
			}
		}
	}
}

func TestDCT16Batch4SSE41MatchesScalar(t *testing.T) {
	if !haveDCT8BatchSSE41 {
		t.Skip("SSE4.1 unavailable")
	}
	rng := rand.New(rand.NewSource(94))
	for iteration := 0; iteration < 2000; iteration++ {
		var src, got, want [256]int32
		for i := range src {
			src[i] = int32(rng.Intn(1<<16) - (1 << 15))
		}
		for lane := 0; lane < 16; lane++ {
			var values [16]int32
			for position := range values {
				values[position] = src[position*16+lane]
			}
			InvDCT16(values[:], 1, -1<<15, 1<<15-1)
			for position := range values {
				want[position*16+lane] = values[position]
			}
		}
		for lane := 0; lane < 16; lane += 4 {
			dct16Batch4SIMD(got[lane:], src[lane:])
		}
		if got != want {
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("iteration=%d value=%d: SIMD=%d scalar=%d", iteration, i, got[i], want[i])
				}
			}
		}
	}
}

func BenchmarkAddDC16x16(b *testing.B) {
	const width, height, stride, dc = 16, 16, 19, -37
	template := make([]byte, stride*height)
	for i := range template {
		template[i] = byte(i*29 + 17)
	}
	b.Run("Scalar", func(b *testing.B) {
		dst := append([]byte(nil), template...)
		b.ReportAllocs()
		b.SetBytes(width * height)
		for n := 0; n < b.N; n++ {
			for y := 0; y < height; y++ {
				row := y * stride
				for x := 0; x < width; x++ {
					dst[row+x] = uint8(pixelClamp(int(dst[row+x])+dc, 255))
				}
			}
		}
		benchmarkTransformSink = dst[b.N%len(dst)]
	})
	b.Run("SSE2", func(b *testing.B) {
		dst := append([]byte(nil), template...)
		b.ReportAllocs()
		b.SetBytes(width * height)
		for n := 0; n < b.N; n++ {
			addDC8SSE2(&dst[0], stride, width, height, dc)
		}
		benchmarkTransformSink = dst[b.N%len(dst)]
	})
}

func BenchmarkAddResidual16x16(b *testing.B) {
	const width, height, stride = 16, 16, 19
	template := make([]byte, stride*height)
	src := make([]int32, width*height)
	for i := range template {
		template[i] = byte(i*29 + 17)
	}
	for i := range src {
		src[i] = int32((i*997)&0xffff) - (1 << 15)
	}
	b.Run("Scalar", func(b *testing.B) {
		dst := append([]byte(nil), template...)
		b.ReportAllocs()
		b.SetBytes(width * height)
		for n := 0; n < b.N; n++ {
			for y := 0; y < height; y++ {
				for x := 0; x < width; x++ {
					s := y*width + x
					d := y*stride + x
					dst[d] = uint8(pixelClamp(
						int(dst[d])+((int(src[s])+8)>>4), 255))
				}
			}
		}
		benchmarkTransformSink = dst[b.N%len(dst)]
	})
	b.Run("SSE41", func(b *testing.B) {
		dst := append([]byte(nil), template...)
		b.ReportAllocs()
		b.SetBytes(width * height)
		for n := 0; n < b.N; n++ {
			addResidual8SSE41(&dst[0], stride, &src[0], width, height)
		}
		benchmarkTransformSink = dst[b.N%len(dst)]
	})
}
