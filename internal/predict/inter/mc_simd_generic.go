//go:build !amd64 || purego

package inter

func put8TapHV8SIMD(dst []uint8, dstStride int,
	src []uint8, srcBase, srcStride int, mid []int16,
	w, h int, fh, fv []int8) bool {
	return false
}

func put8TapH8SIMD(dst []uint8, dstStride int,
	src []uint8, srcBase, srcStride, w, h int, filter []int8) bool {
	return false
}

func put8TapV8SIMD(dst []uint8, dstStride int,
	src []uint8, srcBase, srcStride, w, h int, filter []int8) bool {
	return false
}
