//go:build !amd64 || purego

package cdef

const cdefCombinedOffsets = 6

func filterPrimary8SIMD(dst []uint8, dstBase, dstStride int,
	tmp *[144]int16, tmpBase, priOff0, priOff1,
	threshold, shift, tap0, tap1, h int) bool {
	return false
}

func filterCombined8SIMD(dst []uint8, dstBase, dstStride int,
	tmp *[144]int16, tmpBase int, offsets [cdefCombinedOffsets]int,
	priThreshold, priShift, priTap0, priTap1,
	secThreshold, secShift, h int) bool {
	return false
}
