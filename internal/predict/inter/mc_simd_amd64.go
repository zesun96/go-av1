//go:build amd64 && !purego

package inter

import "github.com/zesun96/go-av1/internal/dispatch"

var havePut8TapSSE41 = func() bool {
	features := dispatch.Active()
	return features.SSSE3 && features.SSE41
}()

func put8TapHV8SIMD(dst []uint8, dstStride int,
	src []uint8, srcBase, srcStride int, mid []int16,
	w, h int, fh, fv []int8) bool {
	if !havePut8TapSSE41 || dispatch.GenericForced() ||
		w < 8 || w&7 != 0 || h <= 0 {
		return false
	}
	filter8TapHorizontalSSE41(&mid[0], &src[srcBase-3*srcStride],
		srcStride, w, h+7, &fh[0])
	filter8TapVerticalSSE41(&dst[0], dstStride, &mid[0], w, h, &fv[0])
	return true
}

func put8TapH8SIMD(dst []uint8, dstStride int,
	src []uint8, srcBase, srcStride, w, h int, filter []int8) bool {
	if !havePut8TapSSE41 || dispatch.GenericForced() ||
		w < 8 || w&7 != 0 || h <= 0 {
		return false
	}
	filter8TapHorizontalPutSSE41(&dst[0], dstStride, &src[srcBase],
		srcStride, w, h, &filter[0])
	return true
}

func put8TapV8SIMD(dst []uint8, dstStride int,
	src []uint8, srcBase, srcStride, w, h int, filter []int8) bool {
	if !havePut8TapSSE41 || dispatch.GenericForced() ||
		w < 8 || w&7 != 0 || h <= 0 {
		return false
	}
	filter8TapVerticalPutSSE41(&dst[0], dstStride, &src[srcBase],
		srcStride, w, h, &filter[0])
	return true
}

//go:noescape
func filter8TapHorizontalSSE41(dst *int16, src *uint8,
	srcStride, w, h int, filter *int8)

//go:noescape
func filter8TapVerticalSSE41(dst *uint8, dstStride int, src *int16,
	w, h int, filter *int8)

//go:noescape
func filter8TapHorizontalPutSSE41(dst *uint8, dstStride int, src *uint8,
	srcStride, w, h int, filter *int8)

//go:noescape
func filter8TapVerticalPutSSE41(dst *uint8, dstStride int, src *uint8,
	srcStride, w, h int, filter *int8)
