//go:build !amd64 || purego

package cdef

func filterPrimary8SIMD(dst []uint8, dstBase, dstStride int,
	tmp *[144]int16, tmpBase, priOff0, priOff1,
	threshold, shift, tap0, tap1, h int) bool {
	return false
}
