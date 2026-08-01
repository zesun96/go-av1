//go:build amd64 && !purego

package transform

import "github.com/zesun96/go-av1/internal/dispatch"

// SSE2 is part of the Go amd64 baseline.
var haveAddDCSSE2 = true
var haveAddResidualSSE41 = func() bool {
	return dispatch.Active().SSE41
}()
var haveDCT8BatchSSE41 = haveAddResidualSSE41

func addDC8SIMD(dst []uint8, stride, w, h, dc int) bool {
	if !haveAddDCSSE2 || dispatch.GenericForced() ||
		w < 8 || w&7 != 0 || h <= 0 {
		return false
	}
	if dc > 255 {
		dc = 255
	} else if dc < -255 {
		dc = -255
	}
	addDC8SSE2(&dst[0], stride, w, h, dc)
	return true
}

func addResidual8SIMD(dst []uint8, stride int, src []int32, w, h int) bool {
	if !haveAddResidualSSE41 || dispatch.GenericForced() ||
		w < 8 || w&7 != 0 || h <= 0 {
		return false
	}
	addResidual8SSE41(&dst[0], stride, &src[0], w, h)
	return true
}

func addResidual4x4SIMD(dst []uint8, stride int, src *[16]int32) bool {
	if !haveAddResidualSSE41 || dispatch.GenericForced() {
		return false
	}
	addResidual4x4SSE41(&dst[0], stride, &src[0])
	return true
}

func invDCT8x8SIMD(tmp *[64]int32, coeff []int32) bool {
	if !haveDCT8BatchSSE41 || dispatch.GenericForced() || len(coeff) < 64 {
		return false
	}
	var transposed [64]int32
	dct8Batch4SSE41(&tmp[0], &coeff[0])
	dct8Batch4SSE41(&tmp[4], &coeff[4])
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			transposed[y*8+x] = (tmp[x*8+y] + 1) >> 1
		}
	}
	dct8Batch4SSE41(&tmp[0], &transposed[0])
	dct8Batch4SSE41(&tmp[4], &transposed[4])
	return true
}

//go:noescape
func addDC8SSE2(dst *uint8, stride, w, h, dc int)

//go:noescape
func addResidual8SSE41(dst *uint8, stride int, src *int32, w, h int)

//go:noescape
func addResidual4x4SSE41(dst *uint8, stride int, src *int32)

//go:noescape
func dct8Batch4SSE41(dst, src *int32)
