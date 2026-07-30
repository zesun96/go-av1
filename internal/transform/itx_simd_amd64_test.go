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
