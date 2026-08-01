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
