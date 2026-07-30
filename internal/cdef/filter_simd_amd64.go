//go:build amd64 && !purego

package cdef

import "github.com/zesun96/go-av1/internal/dispatch"

const cdefCombinedOffsets = 6
const cdefSecondaryOffsets = 4

var havePrimary8SSE41 = func() bool {
	features := dispatch.Active()
	return features.SSSE3 && features.SSE41
}()

func filterPrimary8SIMD(dst []uint8, dstBase, dstStride int,
	tmp *[144]int16, tmpBase, priOff0, priOff1,
	threshold, shift, tap0, tap1, w, h int) bool {
	if !havePrimary8SSE41 || dispatch.GenericForced() || h <= 0 {
		return false
	}
	filterPrimary8SSE41(&dst[dstBase], dstStride, &tmp[tmpBase],
		priOff0, priOff1, threshold, shift, tap0, tap1, w, h)
	return true
}

//go:noescape
func filterPrimary8SSE41(dst *uint8, dstStride int, src *int16,
	priOff0, priOff1, threshold, shift, tap0, tap1, w, h int)

var haveCombined8SSE41 = havePrimary8SSE41

func filterCombined8SIMD(dst []uint8, dstBase, dstStride int,
	tmp *[144]int16, tmpBase int, offsets [cdefCombinedOffsets]int,
	priThreshold, priShift, priTap0, priTap1,
	secThreshold, secShift, w, h int) bool {
	if !haveCombined8SSE41 || dispatch.GenericForced() || h <= 0 {
		return false
	}
	filterCombined8SSE41(&dst[dstBase], dstStride, &tmp[tmpBase], &offsets,
		priThreshold, priShift, priTap0, priTap1,
		secThreshold, secShift, w, h)
	return true
}

//go:noescape
func filterCombined8SSE41(dst *uint8, dstStride int, src *int16,
	offsets *[cdefCombinedOffsets]int,
	priThreshold, priShift, priTap0, priTap1,
	secThreshold, secShift, w, h int)

var haveSecondary8SSE41 = havePrimary8SSE41

func filterSecondary8SIMD(dst []uint8, dstBase, dstStride int,
	tmp *[144]int16, tmpBase int, offsets [cdefSecondaryOffsets]int,
	threshold, shift, w, h int) bool {
	if !haveSecondary8SSE41 || dispatch.GenericForced() || h <= 0 {
		return false
	}
	filterSecondary8SSE41(&dst[dstBase], dstStride, &tmp[tmpBase], &offsets,
		threshold, shift, w, h)
	return true
}

//go:noescape
func filterSecondary8SSE41(dst *uint8, dstStride int, src *int16,
	offsets *[cdefSecondaryOffsets]int, threshold, shift, w, h int)

func paddingContiguous8x8SIMD(tmp *[144]int16, src []uint8, srcBase, srcStride int) bool {
	if !havePrimary8SSE41 || dispatch.GenericForced() ||
		srcBase < 2*srcStride+2 || srcBase+9*srcStride+9 >= len(src) {
		return false
	}
	paddingContiguous8x8SSE41(&tmp[0], &src[srcBase-2*srcStride-2], srcStride)
	return true
}

//go:noescape
func paddingContiguous8x8SSE41(dst *int16, src *uint8, srcStride int)

func findDirSIMD(img []uint8, imgBase, stride int) (dir int, variance uint, ok bool) {
	if !havePrimary8SSE41 || dispatch.GenericForced() {
		return 0, 0, false
	}
	var cost [8]uint32
	findDirCostsSSE41(&img[imgBase], stride, &cost)
	bestDir := 0
	bestCost := cost[0]
	for n := 1; n < 8; n++ {
		if cost[n] > bestCost {
			bestCost = cost[n]
			bestDir = n
		}
	}
	return bestDir, uint((bestCost - cost[bestDir^4]) >> 10), true
}

//go:noescape
func findDirCostsSSE41(src *uint8, stride int, cost *[8]uint32)
