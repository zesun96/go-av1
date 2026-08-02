// Package cdef implements the AV1 Constrained Directional Enhancement Filter.
//
// Reference: dav1d/src/cdef_tmpl.c, dav1d/src/tables.c
package cdef

import "math/bits"

// EdgeFlags indicates which edges of a CDEF block are available.
type EdgeFlags uint8

const (
	HaveLeft   EdgeFlags = 1 << 0
	HaveRight  EdgeFlags = 1 << 1
	HaveTop    EdgeFlags = 1 << 2
	HaveBottom EdgeFlags = 1 << 3
)

// cdefDirections is dav1d_cdef_directions[2+8+2][2] with tmp_stride=12.
// Indexed as cdefDirections[dir+2][k] (k=0,1).
var cdefDirections = [12][2]int{
	{1*12 + 0, 2*12 + 0},   // 6
	{1*12 + 0, 2*12 - 1},   // 7
	{-1*12 + 1, -2*12 + 2}, // 0
	{0*12 + 1, -1*12 + 2},  // 1
	{0*12 + 1, 0*12 + 2},   // 2
	{0*12 + 1, 1*12 + 2},   // 3
	{1*12 + 1, 2*12 + 2},   // 4
	{1*12 + 0, 2*12 + 1},   // 5
	{1*12 + 0, 2*12 + 0},   // 6
	{1*12 + 0, 2*12 - 1},   // 7
	{-1*12 + 1, -2*12 + 2}, // 0
	{0*12 + 1, -1*12 + 2},  // 1
}

type directionVector struct {
	dy int
	dx int
}

// The same direction table expressed as row/column vectors. Source-backed
// SIMD kernels use the picture stride directly and therefore cannot use the
// fixed 12-wide int16 scratch offsets above.
var cdefDirectionVectors = [12][2]directionVector{
	{{1, 0}, {2, 0}},
	{{1, 0}, {2, -1}},
	{{-1, 1}, {-2, 2}},
	{{0, 1}, {-1, 2}},
	{{0, 1}, {0, 2}},
	{{0, 1}, {1, 2}},
	{{1, 1}, {2, 2}},
	{{1, 0}, {2, 1}},
	{{1, 0}, {2, 0}},
	{{1, 0}, {2, -1}},
	{{-1, 1}, {-2, 2}},
	{{0, 1}, {-1, 2}},
}

func combinedSourceOffsets(dir, stride int) [cdefCombinedOffsets]int {
	primary := cdefDirectionVectors[dir+2]
	secondary0 := cdefDirectionVectors[dir+4]
	secondary1 := cdefDirectionVectors[dir]
	return [cdefCombinedOffsets]int{
		primary[0].dy*stride + primary[0].dx,
		primary[1].dy*stride + primary[1].dx,
		secondary0[0].dy*stride + secondary0[0].dx,
		secondary1[0].dy*stride + secondary1[0].dx,
		secondary0[1].dy*stride + secondary0[1].dx,
		secondary1[1].dy*stride + secondary1[1].dx,
	}
}

// sgr_x_by_x is used in SGR but also serves as a large table; here we only
// need the CDEF-specific helpers below.

func imax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func umin(a, b int) int {
	if uint(a) < uint(b) {
		return a
	}
	return b
}

func iclip(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// constrain is the CDEF constrain function.
func constrain(diff, threshold, shift int) int {
	sign := 1
	if diff < 0 {
		diff = -diff
		sign = -1
	}
	limit := threshold - (diff >> shift)
	if limit < 0 {
		limit = 0
	}
	if diff > limit {
		diff = limit
	}
	return diff * sign
}

const constrainTableOffset = 255

var constrainTable [65][7][2*constrainTableOffset + 1]int8

func init() {
	for threshold := range constrainTable {
		for shift := range constrainTable[threshold] {
			for diff := -constrainTableOffset; diff <= constrainTableOffset; diff++ {
				constrainTable[threshold][shift][diff+constrainTableOffset] = int8(constrain(diff, threshold, shift))
			}
		}
	}
}

func constrainFromTable(diff int, table *[2*constrainTableOffset + 1]int8) int {
	index := diff + constrainTableOffset
	if uint(index) >= uint(len(table)) {
		return 0
	}
	return int(table[index])
}

// fill sets tmp region to INT16_MIN (used for out-of-bounds padding).
func fill(tmp []int16, tmpBase int, tmpStride, w, h int) {
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			tmp[tmpBase+x] = -32768 // INT16_MIN
		}
		tmpBase += tmpStride
	}
}

const tmpStride = 12

// padding builds the 12-wide extended input buffer around the (w×h) block.
// tmp is the full buffer; tmpBase points to tmp[2][2] (top-left of active region).
// dst is the source pixels (same layout as output). left[y][0..1] are the two
// pixels to the left of each row. top/bottom are the adjacent rows.
func padding(tmp []int16, tmpBase int,
	dst []uint8, dstBase, dstStride int,
	left [][2]uint8,
	top []uint8, topBase, topStride int,
	bottom []uint8, bottomBase, bottomStride int,
	w, h int, edges EdgeFlags) {

	xStart, xEnd := -2, w+2
	yStart, yEnd := -2, h+2

	if edges&HaveTop == 0 {
		fill(tmp, tmpBase-2-2*tmpStride, tmpStride, w+4, 2)
		yStart = 0
	}
	if edges&HaveBottom == 0 {
		fill(tmp, tmpBase+h*tmpStride-2, tmpStride, w+4, 2)
		yEnd -= 2
	}
	if edges&HaveLeft == 0 {
		fill(tmp, tmpBase+yStart*tmpStride-2, tmpStride, 2, yEnd-yStart)
		xStart = 0
	}
	if edges&HaveRight == 0 {
		fill(tmp, tmpBase+yStart*tmpStride+w, tmpStride, 2, yEnd-yStart)
		xEnd -= 2
	}

	// Top rows.
	tb := topBase
	topRowStart := tb - tb%topStride
	topRowEnd := topRowStart + topStride
	for y := yStart; y < 0; y++ {
		srcStart, srcEnd := tb+xStart, tb+xEnd
		dstStart := tmpBase + xStart + y*tmpStride
		if srcStart >= topRowStart && srcEnd <= topRowEnd &&
			srcStart >= 0 && srcEnd <= len(top) {
			srcRow := top[srcStart:srcEnd]
			dstRow := tmp[dstStart : dstStart+len(srcRow)]
			for x, pixel := range srcRow {
				dstRow[x] = int16(pixel)
			}
		} else {
			for x := xStart; x < xEnd; x++ {
				xi := tb + x
				if xi < topRowStart || xi >= topRowEnd || xi < 0 || xi >= len(top) {
					xi = tb + iclip(x, 0, w-1)
				}
				tmp[tmpBase+x+y*tmpStride] = int16(top[xi])
			}
		}
		tb += topStride
		topRowStart += topStride
		topRowEnd += topStride
	}

	// Left columns.
	if xStart < 0 {
		tt := tmpBase
		for y := 0; y < h; y++ {
			tmp[tt-2] = int16(left[y][0])
			tmp[tt-1] = int16(left[y][1])
			tt += tmpStride
		}
	}

	// Main block + right columns.
	sb := dstBase
	tt := tmpBase
	dstRowEnd := dstBase - dstBase%dstStride + dstStride
	for y := 0; y < h; y++ {
		switch w {
		case 8:
			tmp[tt+7] = int16(dst[sb+7])
			tmp[tt+6] = int16(dst[sb+6])
			tmp[tt+5] = int16(dst[sb+5])
			tmp[tt+4] = int16(dst[sb+4])
			fallthrough
		case 4:
			tmp[tt+3] = int16(dst[sb+3])
			tmp[tt+2] = int16(dst[sb+2])
			tmp[tt+1] = int16(dst[sb+1])
			tmp[tt+0] = int16(dst[sb+0])
		default:
			for x := 0; x < w; x++ {
				tmp[tt+x] = int16(dst[sb+x])
			}
		}
		if xEnd > w {
			last := dst[sb+w-1]
			if sb+w < dstRowEnd && sb+w < len(dst) {
				tmp[tt+w] = int16(dst[sb+w])
			} else {
				tmp[tt+w] = int16(last)
			}
			if sb+w+1 < dstRowEnd && sb+w+1 < len(dst) {
				tmp[tt+w+1] = int16(dst[sb+w+1])
			} else {
				tmp[tt+w+1] = int16(last)
			}
		}
		sb += dstStride
		tt += tmpStride
		dstRowEnd += dstStride
	}

	// Bottom rows.
	bb := bottomBase
	tt = tmpBase + h*tmpStride
	bottomRowStart := bb - bb%bottomStride
	bottomRowEnd := bottomRowStart + bottomStride
	for y := h; y < yEnd; y++ {
		srcStart, srcEnd := bb+xStart, bb+xEnd
		dstStart := tt + xStart
		if srcStart >= bottomRowStart && srcEnd <= bottomRowEnd &&
			srcStart >= 0 && srcEnd <= len(bottom) {
			srcRow := bottom[srcStart:srcEnd]
			dstRow := tmp[dstStart : dstStart+len(srcRow)]
			for x, pixel := range srcRow {
				dstRow[x] = int16(pixel)
			}
		} else {
			for x := xStart; x < xEnd; x++ {
				xi := bb + x
				if xi < bottomRowStart || xi >= bottomRowEnd || xi < 0 || xi >= len(bottom) {
					xi = bb + iclip(x, 0, w-1)
				}
				tmp[tt+x] = int16(bottom[xi])
			}
		}
		bb += bottomStride
		tt += tmpStride
		bottomRowStart += bottomStride
		bottomRowEnd += bottomStride
	}
}

func paddingContiguous8x8(tmp *[144]int16, src []uint8, srcBase, srcStride int) {
	if paddingContiguous8x8SIMD(tmp, src, srcBase, srcStride) {
		return
	}
	srcBase -= 2*srcStride + 2
	for y := 0; y < 12; y++ {
		s := srcBase + y*srcStride
		d := y * tmpStride
		tmp[d+0] = int16(src[s+0])
		tmp[d+1] = int16(src[s+1])
		tmp[d+2] = int16(src[s+2])
		tmp[d+3] = int16(src[s+3])
		tmp[d+4] = int16(src[s+4])
		tmp[d+5] = int16(src[s+5])
		tmp[d+6] = int16(src[s+6])
		tmp[d+7] = int16(src[s+7])
		tmp[d+8] = int16(src[s+8])
		tmp[d+9] = int16(src[s+9])
		tmp[d+10] = int16(src[s+10])
		tmp[d+11] = int16(src[s+11])
	}
}

func paddingContiguous4x4(tmp *[144]int16, src []uint8, srcBase, srcStride int) {
	if paddingContiguous4x4SIMD(tmp, src, srcBase, srcStride) {
		return
	}
	srcBase -= 2*srcStride + 2
	for y := 0; y < 8; y++ {
		s := srcBase + y*srcStride
		d := y * tmpStride
		tmp[d+0] = int16(src[s+0])
		tmp[d+1] = int16(src[s+1])
		tmp[d+2] = int16(src[s+2])
		tmp[d+3] = int16(src[s+3])
		tmp[d+4] = int16(src[s+4])
		tmp[d+5] = int16(src[s+5])
		tmp[d+6] = int16(src[s+6])
		tmp[d+7] = int16(src[s+7])
	}
}

// ulog2 returns floor(log2(v)) for v>0.
func ulog2(v int) int {
	if v <= 0 {
		return 0
	}
	return bits.Len(uint(v)) - 1
}

// FilterBlock applies CDEF to a w×h block (w ∈ {4,8}, h ∈ {4,8}).
//
// dst/dstBase/dstStride: pixel buffer of the current block (modified in-place).
// left: left pixels [h][2]. top/bottom: adjacent pixel rows.
// priStrength, secStrength: filter strengths (0 = disabled).
// dir: edge direction (0..7).
// damping: damping factor.
// edges: available edges.
func FilterBlock(dst []uint8, dstBase, dstStride int,
	left [][2]uint8,
	top []uint8, topBase, topStride int,
	bottom []uint8, bottomBase, bottomStride int,
	priStrength, secStrength, dir, damping, w, h int,
	edges EdgeFlags) {

	// 12*12 = 144 for tmp_stride*(h+4)
	var tmpBuf [144]int16
	// tmpBase points to (2,2) in the 12-wide buffer
	tmpBase := 2*tmpStride + 2

	padding(tmpBuf[:], tmpBase, dst, dstBase, dstStride,
		left, top, topBase, topStride, bottom, bottomBase, bottomStride,
		w, h, edges)

	filterBlockPrepared(dst, dstBase, dstStride, &tmpBuf, tmpBase,
		priStrength, secStrength, dir, damping, w, h)
}

// FilterBlock8x8FromSource applies CDEF to an interior 8x8 block whose full
// 12x12 neighbor window is contiguous in the immutable source plane.
func FilterBlock8x8FromSource(dst []uint8, dstBase, dstStride int,
	src []uint8, srcBase, srcStride int,
	priStrength, secStrength, dir, damping int) {
	if priStrength != 0 && secStrength != 0 {
		priTap := 4 - (priStrength & 1)
		if filterCombined8SourceSIMD(dst, dstBase, dstStride, src, srcBase, srcStride,
			combinedSourceOffsets(dir, srcStride),
			priStrength, imax(0, damping-ulog2(priStrength)), priTap, (priTap&3)|2,
			secStrength, damping-ulog2(secStrength), 8, 8) {
			return
		}
	}
	var tmpBuf [144]int16
	paddingContiguous8x8(&tmpBuf, src, srcBase, srcStride)
	filterBlockPrepared(dst, dstBase, dstStride, &tmpBuf, 2*tmpStride+2,
		priStrength, secStrength, dir, damping, 8, 8)
}

// FilterBlock4x4FromSource applies CDEF to an interior 4x4 block whose full
// 8x8 neighbor window is contiguous in the immutable source plane.
func FilterBlock4x4FromSource(dst []uint8, dstBase, dstStride int,
	src []uint8, srcBase, srcStride int,
	priStrength, secStrength, dir, damping int) {
	if priStrength != 0 && secStrength != 0 {
		priTap := 4 - (priStrength & 1)
		if filterCombined8SourceSIMD(dst, dstBase, dstStride, src, srcBase, srcStride,
			combinedSourceOffsets(dir, srcStride),
			priStrength, imax(0, damping-ulog2(priStrength)), priTap, (priTap&3)|2,
			secStrength, damping-ulog2(secStrength), 4, 4) {
			return
		}
	}
	var tmpBuf [144]int16
	paddingContiguous4x4(&tmpBuf, src, srcBase, srcStride)
	filterBlockPrepared(dst, dstBase, dstStride, &tmpBuf, 2*tmpStride+2,
		priStrength, secStrength, dir, damping, 4, 4)
}

func filterBlockPrepared(dst []uint8, dstBase, dstStride int,
	tmpBuf *[144]int16, tmpBase int,
	priStrength, secStrength, dir, damping, w, h int) {
	if priStrength != 0 {
		priTap := 4 - ((priStrength) & 1)
		priShift := imax(0, damping-ulog2(priStrength))
		if secStrength == 0 && (w == 4 || w == 8) &&
			filterPrimary8SIMD(dst, dstBase, dstStride, tmpBuf, tmpBase,
				cdefDirections[dir+2][0], cdefDirections[dir+2][1],
				priStrength, priShift, priTap, (priTap&3)|2, w, h) {
			return
		}
		priConstrain := &constrainTable[priStrength][priShift]
		if secStrength != 0 {
			secShift := damping - ulog2(secStrength)
			if (w == 4 || w == 8) &&
				filterCombined8SIMD(dst, dstBase, dstStride, tmpBuf, tmpBase,
					[cdefCombinedOffsets]int{
						cdefDirections[dir+2][0],
						cdefDirections[dir+2][1],
						cdefDirections[dir+4][0],
						cdefDirections[dir+0][0],
						cdefDirections[dir+4][1],
						cdefDirections[dir+0][1],
					},
					priStrength, priShift, priTap, (priTap&3)|2,
					secStrength, secShift, w, h) {
				return
			}
			secConstrain := &constrainTable[secStrength][secShift]
			priOff0 := cdefDirections[dir+2][0]
			priOff1 := cdefDirections[dir+2][1]
			secOff00 := cdefDirections[dir+4][0]
			secOff01 := cdefDirections[dir+0][0]
			secOff10 := cdefDirections[dir+4][1]
			secOff11 := cdefDirections[dir+0][1]
			priTap1 := (priTap & 3) | 2
			db := dstBase
			tt := tmpBase
			for row := 0; row < h; row++ {
				for x := 0; x < w; x++ {
					px := int(dst[db+x])
					base := tt + x
					p00 := int(tmpBuf[base+priOff0])
					p01 := int(tmpBuf[base-priOff0])
					p10 := int(tmpBuf[base+priOff1])
					p11 := int(tmpBuf[base-priOff1])
					s000 := int(tmpBuf[base+secOff00])
					s001 := int(tmpBuf[base-secOff00])
					s010 := int(tmpBuf[base+secOff01])
					s011 := int(tmpBuf[base-secOff01])
					s100 := int(tmpBuf[base+secOff10])
					s101 := int(tmpBuf[base-secOff10])
					s110 := int(tmpBuf[base+secOff11])
					s111 := int(tmpBuf[base-secOff11])
					sum := priTap * (constrainFromTable(p00-px, priConstrain) +
						constrainFromTable(p01-px, priConstrain))
					sum += priTap1 * (constrainFromTable(p10-px, priConstrain) +
						constrainFromTable(p11-px, priConstrain))
					sum += 2 * (constrainFromTable(s000-px, secConstrain) +
						constrainFromTable(s001-px, secConstrain) +
						constrainFromTable(s010-px, secConstrain) +
						constrainFromTable(s011-px, secConstrain))
					sum += constrainFromTable(s100-px, secConstrain) +
						constrainFromTable(s101-px, secConstrain) +
						constrainFromTable(s110-px, secConstrain) +
						constrainFromTable(s111-px, secConstrain)
					maxV, minV := px, px
					minV, maxV = umin(p00, minV), imax(p00, maxV)
					minV, maxV = umin(p01, minV), imax(p01, maxV)
					minV, maxV = umin(p10, minV), imax(p10, maxV)
					minV, maxV = umin(p11, minV), imax(p11, maxV)
					minV, maxV = umin(s000, minV), imax(s000, maxV)
					minV, maxV = umin(s001, minV), imax(s001, maxV)
					minV, maxV = umin(s010, minV), imax(s010, maxV)
					minV, maxV = umin(s011, minV), imax(s011, maxV)
					minV, maxV = umin(s100, minV), imax(s100, maxV)
					minV, maxV = umin(s101, minV), imax(s101, maxV)
					minV, maxV = umin(s110, minV), imax(s110, maxV)
					minV, maxV = umin(s111, minV), imax(s111, maxV)
					adj := 0
					if sum < 0 {
						adj = -1
					}
					dst[db+x] = uint8(iclip(px+((sum+adj+8)>>4), minV, maxV))
				}
				db += dstStride
				tt += tmpStride
			}
		} else {
			// pri only
			priOff0 := cdefDirections[dir+2][0]
			priOff1 := cdefDirections[dir+2][1]
			db := dstBase
			tt := tmpBase
			for row := 0; row < h; row++ {
				for x := 0; x < w; x++ {
					px := int(dst[db+x])
					p00 := int(tmpBuf[tt+x+priOff0])
					p01 := int(tmpBuf[tt+x-priOff0])
					p10 := int(tmpBuf[tt+x+priOff1])
					p11 := int(tmpBuf[tt+x-priOff1])
					sum := priTap * (constrainFromTable(p00-px, priConstrain) +
						constrainFromTable(p01-px, priConstrain))
					sum += ((priTap & 3) | 2) * (constrainFromTable(p10-px, priConstrain) +
						constrainFromTable(p11-px, priConstrain))
					adj := 0
					if sum < 0 {
						adj = -1
					}
					dst[db+x] = uint8(px + ((sum + adj + 8) >> 4))
				}
				db += dstStride
				tt += tmpStride
			}
		}
	} else {
		// sec only
		secShift := damping - ulog2(secStrength)
		if (w == 4 || w == 8) &&
			filterSecondary8SIMD(dst, dstBase, dstStride, tmpBuf, tmpBase,
				[cdefSecondaryOffsets]int{
					cdefDirections[dir+4][0],
					cdefDirections[dir+0][0],
					cdefDirections[dir+4][1],
					cdefDirections[dir+0][1],
				},
				secStrength, secShift, w, h) {
			return
		}
		secConstrain := &constrainTable[secStrength][secShift]
		secOff00 := cdefDirections[dir+4][0]
		secOff01 := cdefDirections[dir+0][0]
		secOff10 := cdefDirections[dir+4][1]
		secOff11 := cdefDirections[dir+0][1]
		db := dstBase
		tt := tmpBase
		for row := 0; row < h; row++ {
			for x := 0; x < w; x++ {
				px := int(dst[db+x])
				s000 := int(tmpBuf[tt+x+secOff00])
				s001 := int(tmpBuf[tt+x-secOff00])
				s010 := int(tmpBuf[tt+x+secOff01])
				s011 := int(tmpBuf[tt+x-secOff01])
				s100 := int(tmpBuf[tt+x+secOff10])
				s101 := int(tmpBuf[tt+x-secOff10])
				s110 := int(tmpBuf[tt+x+secOff11])
				s111 := int(tmpBuf[tt+x-secOff11])
				sum := 2 * (constrainFromTable(s000-px, secConstrain) +
					constrainFromTable(s001-px, secConstrain) +
					constrainFromTable(s010-px, secConstrain) +
					constrainFromTable(s011-px, secConstrain))
				sum += constrainFromTable(s100-px, secConstrain) +
					constrainFromTable(s101-px, secConstrain) +
					constrainFromTable(s110-px, secConstrain) +
					constrainFromTable(s111-px, secConstrain)
				adj := 0
				if sum < 0 {
					adj = -1
				}
				dst[db+x] = uint8(px + ((sum + adj + 8) >> 4))
			}
			db += dstStride
			tt += tmpStride
		}
	}
}

// FindDir determines the dominant edge direction of an 8×8 pixel block and
// returns it (0..7) along with a variance estimate.
//
// img is the source pixel slice, imgBase its starting offset, stride its row stride.
// If the requested 8×8 region falls outside img, FindDir bails out with dir=0,
// variance=0 instead of panicking — used by the M7 best-effort post-filter
// pipeline to tolerate edge blocks on non-multiple-of-8 picture sizes.
func FindDir(img []uint8, imgBase, stride int) (dir int, variance uint) {
	if imgBase < 0 || stride <= 0 {
		return 0, 0
	}
	if imgBase+7*stride+8 > len(img) {
		return 0, 0
	}
	if dir, variance, ok := findDirSIMD(img, imgBase, stride); ok {
		return dir, variance
	}
	return findDirScalar(img, imgBase, stride)
}

func findDirScalar(img []uint8, imgBase, stride int) (dir int, variance uint) {
	var partialSumHV [2][8]int
	var partialSumDiag [2][15]int
	var partialSumAlt [4][11]int

	ib := imgBase
	for pair := 0; pair < 4; pair++ {
		y0 := pair << 1
		y1 := y0 + 1
		row0 := img[ib : ib+8]
		row1 := img[ib+stride : ib+stride+8]
		p00, p01 := int(row0[0])-128, int(row0[1])-128
		p02, p03 := int(row0[2])-128, int(row0[3])-128
		p04, p05 := int(row0[4])-128, int(row0[5])-128
		p06, p07 := int(row0[6])-128, int(row0[7])-128
		p10, p11 := int(row1[0])-128, int(row1[1])-128
		p12, p13 := int(row1[2])-128, int(row1[3])-128
		p14, p15 := int(row1[4])-128, int(row1[5])-128
		p16, p17 := int(row1[6])-128, int(row1[7])-128

		partialSumHV[0][y0] = p00 + p01 + p02 + p03 + p04 + p05 + p06 + p07
		partialSumHV[0][y1] = p10 + p11 + p12 + p13 + p14 + p15 + p16 + p17

		partialSumDiag[0][y0+0] += p00
		partialSumDiag[0][y0+1] += p01
		partialSumDiag[0][y0+2] += p02
		partialSumDiag[0][y0+3] += p03
		partialSumDiag[0][y0+4] += p04
		partialSumDiag[0][y0+5] += p05
		partialSumDiag[0][y0+6] += p06
		partialSumDiag[0][y0+7] += p07
		partialSumDiag[0][y1+0] += p10
		partialSumDiag[0][y1+1] += p11
		partialSumDiag[0][y1+2] += p12
		partialSumDiag[0][y1+3] += p13
		partialSumDiag[0][y1+4] += p14
		partialSumDiag[0][y1+5] += p15
		partialSumDiag[0][y1+6] += p16
		partialSumDiag[0][y1+7] += p17

		partialSumDiag[1][y0+7] += p00
		partialSumDiag[1][y0+6] += p01
		partialSumDiag[1][y0+5] += p02
		partialSumDiag[1][y0+4] += p03
		partialSumDiag[1][y0+3] += p04
		partialSumDiag[1][y0+2] += p05
		partialSumDiag[1][y0+1] += p06
		partialSumDiag[1][y0+0] += p07
		partialSumDiag[1][y1+7] += p10
		partialSumDiag[1][y1+6] += p11
		partialSumDiag[1][y1+5] += p12
		partialSumDiag[1][y1+4] += p13
		partialSumDiag[1][y1+3] += p14
		partialSumDiag[1][y1+2] += p15
		partialSumDiag[1][y1+1] += p16
		partialSumDiag[1][y1+0] += p17

		partialSumAlt[0][y0+0] += p00 + p01
		partialSumAlt[0][y0+1] += p02 + p03
		partialSumAlt[0][y0+2] += p04 + p05
		partialSumAlt[0][y0+3] += p06 + p07
		partialSumAlt[0][y1+0] += p10 + p11
		partialSumAlt[0][y1+1] += p12 + p13
		partialSumAlt[0][y1+2] += p14 + p15
		partialSumAlt[0][y1+3] += p16 + p17

		partialSumAlt[1][y0+3] += p00 + p01
		partialSumAlt[1][y0+2] += p02 + p03
		partialSumAlt[1][y0+1] += p04 + p05
		partialSumAlt[1][y0+0] += p06 + p07
		partialSumAlt[1][y1+3] += p10 + p11
		partialSumAlt[1][y1+2] += p12 + p13
		partialSumAlt[1][y1+1] += p14 + p15
		partialSumAlt[1][y1+0] += p16 + p17

		q0, q1 := p00+p10, p01+p11
		q2, q3 := p02+p12, p03+p13
		q4, q5 := p04+p14, p05+p15
		q6, q7 := p06+p16, p07+p17
		alt2 := 3 - pair
		alt3 := pair
		partialSumAlt[2][alt2+0] += q0
		partialSumAlt[2][alt2+1] += q1
		partialSumAlt[2][alt2+2] += q2
		partialSumAlt[2][alt2+3] += q3
		partialSumAlt[2][alt2+4] += q4
		partialSumAlt[2][alt2+5] += q5
		partialSumAlt[2][alt2+6] += q6
		partialSumAlt[2][alt2+7] += q7
		partialSumHV[1][0] += q0
		partialSumHV[1][1] += q1
		partialSumHV[1][2] += q2
		partialSumHV[1][3] += q3
		partialSumHV[1][4] += q4
		partialSumHV[1][5] += q5
		partialSumHV[1][6] += q6
		partialSumHV[1][7] += q7
		partialSumAlt[3][alt3+0] += q0
		partialSumAlt[3][alt3+1] += q1
		partialSumAlt[3][alt3+2] += q2
		partialSumAlt[3][alt3+3] += q3
		partialSumAlt[3][alt3+4] += q4
		partialSumAlt[3][alt3+5] += q5
		partialSumAlt[3][alt3+6] += q6
		partialSumAlt[3][alt3+7] += q7

		ib += stride << 1
	}

	var cost [8]uint
	for n := 0; n < 8; n++ {
		cost[2] += uint(partialSumHV[0][n] * partialSumHV[0][n])
		cost[6] += uint(partialSumHV[1][n] * partialSumHV[1][n])
	}
	cost[2] *= 105
	cost[6] *= 105

	divTable := [7]uint{840, 420, 280, 210, 168, 140, 120}
	for n := 0; n < 7; n++ {
		d := divTable[n]
		cost[0] += (uint(partialSumDiag[0][n]*partialSumDiag[0][n]) +
			uint(partialSumDiag[0][14-n]*partialSumDiag[0][14-n])) * d
		cost[4] += (uint(partialSumDiag[1][n]*partialSumDiag[1][n]) +
			uint(partialSumDiag[1][14-n]*partialSumDiag[1][14-n])) * d
	}
	cost[0] += uint(partialSumDiag[0][7]*partialSumDiag[0][7]) * 105
	cost[4] += uint(partialSumDiag[1][7]*partialSumDiag[1][7]) * 105

	for n := 0; n < 4; n++ {
		cp := &cost[n*2+1]
		for m := 0; m < 5; m++ {
			*cp += uint(partialSumAlt[n][3+m] * partialSumAlt[n][3+m])
		}
		*cp *= 105
		for m := 0; m < 3; m++ {
			d := divTable[2*m+1]
			*cp += (uint(partialSumAlt[n][m]*partialSumAlt[n][m]) +
				uint(partialSumAlt[n][10-m]*partialSumAlt[n][10-m])) * d
		}
	}

	bestDir := 0
	bestCost := cost[0]
	for n := 1; n < 8; n++ {
		if cost[n] > bestCost {
			bestCost = cost[n]
			bestDir = n
		}
	}

	variance = (bestCost - cost[bestDir^4]) >> 10
	return bestDir, variance
}
