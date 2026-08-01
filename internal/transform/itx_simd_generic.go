//go:build !amd64 || purego

package transform

func addDC8SIMD(dst []uint8, stride, w, h, dc int) bool {
	return false
}

func addResidual8SIMD(dst []uint8, stride int, src []int32, w, h int) bool {
	return false
}

func addResidual4x4SIMD(dst []uint8, stride int, src *[16]int32) bool {
	return false
}

func invDCT8x8SIMD(tmp *[64]int32, coeff []int32) bool {
	return false
}

func invDCT16x16SIMD(tmp *[256]int32, coeff []int32) bool {
	return false
}
