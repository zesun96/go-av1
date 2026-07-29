//go:build amd64 && !purego

package cdef

import "github.com/zesun96/go-av1/internal/dispatch"

const cdefCombinedOffsets = 6

var havePrimary8SSE41 = func() bool {
	features := dispatch.Active()
	return features.SSSE3 && features.SSE41
}()

func filterPrimary8SIMD(dst []uint8, dstBase, dstStride int,
	tmp *[144]int16, tmpBase, priOff0, priOff1,
	threshold, shift, tap0, tap1, h int) bool {
	if !havePrimary8SSE41 || dispatch.GenericForced() || h <= 0 {
		return false
	}
	filterPrimary8SSE41(&dst[dstBase], dstStride, &tmp[tmpBase],
		priOff0, priOff1, threshold, shift, tap0, tap1, h)
	return true
}

//go:noescape
func filterPrimary8SSE41(dst *uint8, dstStride int, src *int16,
	priOff0, priOff1, threshold, shift, tap0, tap1, h int)

var haveCombined8SSE41 = havePrimary8SSE41

func filterCombined8SIMD(dst []uint8, dstBase, dstStride int,
	tmp *[144]int16, tmpBase int, offsets [cdefCombinedOffsets]int,
	priThreshold, priShift, priTap0, priTap1,
	secThreshold, secShift, h int) bool {
	if !haveCombined8SSE41 || dispatch.GenericForced() || h <= 0 {
		return false
	}
	filterCombined8SSE41(&dst[dstBase], dstStride, &tmp[tmpBase], &offsets,
		priThreshold, priShift, priTap0, priTap1,
		secThreshold, secShift, h)
	return true
}

//go:noescape
func filterCombined8SSE41(dst *uint8, dstStride int, src *int16,
	offsets *[cdefCombinedOffsets]int,
	priThreshold, priShift, priTap0, priTap1,
	secThreshold, secShift, h int)
