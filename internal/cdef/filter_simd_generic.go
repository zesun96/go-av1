//go:build !amd64 || purego

package cdef

const cdefCombinedOffsets = 6
const cdefSecondaryOffsets = 4

func filterPrimary8SIMD(dst []uint8, dstBase, dstStride int,
	tmp *[144]int16, tmpBase, priOff0, priOff1,
	threshold, shift, tap0, tap1, w, h int) bool {
	return false
}

func filterCombined8SIMD(dst []uint8, dstBase, dstStride int,
	tmp *[144]int16, tmpBase int, offsets [cdefCombinedOffsets]int,
	priThreshold, priShift, priTap0, priTap1,
	secThreshold, secShift, w, h int) bool {
	return false
}

func filterSecondary8SIMD(dst []uint8, dstBase, dstStride int,
	tmp *[144]int16, tmpBase int, offsets [cdefSecondaryOffsets]int,
	threshold, shift, w, h int) bool {
	return false
}

func paddingContiguous8x8SIMD(tmp *[144]int16, src []uint8, srcBase, srcStride int) bool {
	return false
}

func paddingContiguous4x4SIMD(tmp *[144]int16, src []uint8, srcBase, srcStride int) bool {
	return false
}

func findDirSIMD(img []uint8, imgBase, stride int) (dir int, variance uint, ok bool) {
	return 0, 0, false
}
