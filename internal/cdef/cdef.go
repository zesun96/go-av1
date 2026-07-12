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

// sgr_x_by_x is used in SGR but also serves as a large table; here we only
// need the CDEF-specific helpers below.

func imax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func imin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func iabs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func umin(a, b int) int {
	if uint(a) < uint(b) {
		return a
	}
	return b
}

func applySign(v, s int) int {
	if s < 0 {
		return -v
	}
	return v
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
	adiff := iabs(diff)
	return applySign(imin(adiff, imax(0, threshold-(adiff>>shift))), diff)
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
	for y := yStart; y < 0; y++ {
		for x := xStart; x < xEnd; x++ {
			xi := tb + x
			rowStart, rowEnd := tb-(tb%topStride), tb-(tb%topStride)+topStride
			if xi < rowStart || xi >= rowEnd || xi < 0 || xi >= len(top) {
				xi = tb + iclip(x, 0, w-1)
			}
			tmp[tmpBase+x+y*tmpStride] = int16(top[xi])
		}
		tb += topStride
	}

	// Left columns.
	for y := 0; y < h; y++ {
		for x := xStart; x < 0; x++ {
			tmp[tmpBase+x+y*tmpStride] = int16(left[y][2+x])
		}
	}

	// Main block + right columns.
	sb := dstBase
	tt := tmpBase
	for y := 0; y < h; y++ {
		for x := 0; x < xEnd; x++ {
			xi := x
			if xi >= w {
				rowEnd := sb - (sb % dstStride) + dstStride
				if edges&HaveRight == 0 || sb+xi >= rowEnd || sb+xi >= len(dst) {
					xi = w - 1
				}
			}
			tmp[tt+x] = int16(dst[sb+xi])
		}
		sb += dstStride
		tt += tmpStride
	}

	// Bottom rows.
	bb := bottomBase
	tt = tmpBase + h*tmpStride
	for y := h; y < yEnd; y++ {
		for x := xStart; x < xEnd; x++ {
			xi := bb + x
			rowStart, rowEnd := bb-(bb%bottomStride), bb-(bb%bottomStride)+bottomStride
			if xi < rowStart || xi >= rowEnd || xi < 0 || xi >= len(bottom) {
				xi = bb + iclip(x, 0, w-1)
			}
			tmp[tt+x] = int16(bottom[xi])
		}
		bb += bottomStride
		tt += tmpStride
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

	if priStrength != 0 {
		priTap := 4 - ((priStrength) & 1)
		priShift := imax(0, damping-ulog2(priStrength))
		if secStrength != 0 {
			secShift := damping - ulog2(secStrength)
			db := dstBase
			tt := tmpBase
			for row := 0; row < h; row++ {
				for x := 0; x < w; x++ {
					px := int(dst[db+x])
					sum := 0
					maxV, minV := px, px
					priTapK := priTap
					for k := 0; k < 2; k++ {
						off1 := cdefDirections[dir+2][k]
						p0 := int(tmpBuf[tt+x+off1])
						p1 := int(tmpBuf[tt+x-off1])
						sum += priTapK * constrain(p0-px, priStrength, priShift)
						sum += priTapK * constrain(p1-px, priStrength, priShift)
						priTapK = (priTapK & 3) | 2
						minV = umin(p0, minV)
						maxV = imax(p0, maxV)
						minV = umin(p1, minV)
						maxV = imax(p1, maxV)
						off2 := cdefDirections[dir+4][k]
						off3 := cdefDirections[dir+0][k]
						s0 := int(tmpBuf[tt+x+off2])
						s1 := int(tmpBuf[tt+x-off2])
						s2 := int(tmpBuf[tt+x+off3])
						s3 := int(tmpBuf[tt+x-off3])
						secTap := 2 - k
						sum += secTap * constrain(s0-px, secStrength, secShift)
						sum += secTap * constrain(s1-px, secStrength, secShift)
						sum += secTap * constrain(s2-px, secStrength, secShift)
						sum += secTap * constrain(s3-px, secStrength, secShift)
						minV = umin(s0, minV)
						maxV = imax(s0, maxV)
						minV = umin(s1, minV)
						maxV = imax(s1, maxV)
						minV = umin(s2, minV)
						maxV = imax(s2, maxV)
						minV = umin(s3, minV)
						maxV = imax(s3, maxV)
					}
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
			db := dstBase
			tt := tmpBase
			for row := 0; row < h; row++ {
				for x := 0; x < w; x++ {
					px := int(dst[db+x])
					sum := 0
					priTapK := priTap
					for k := 0; k < 2; k++ {
						off := cdefDirections[dir+2][k]
						p0 := int(tmpBuf[tt+x+off])
						p1 := int(tmpBuf[tt+x-off])
						sum += priTapK * constrain(p0-px, priStrength, priShift)
						sum += priTapK * constrain(p1-px, priStrength, priShift)
						priTapK = (priTapK & 3) | 2
					}
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
		db := dstBase
		tt := tmpBase
		for row := 0; row < h; row++ {
			for x := 0; x < w; x++ {
				px := int(dst[db+x])
				sum := 0
				for k := 0; k < 2; k++ {
					off1 := cdefDirections[dir+4][k]
					off2 := cdefDirections[dir+0][k]
					s0 := int(tmpBuf[tt+x+off1])
					s1 := int(tmpBuf[tt+x-off1])
					s2 := int(tmpBuf[tt+x+off2])
					s3 := int(tmpBuf[tt+x-off2])
					secTap := 2 - k
					sum += secTap * constrain(s0-px, secStrength, secShift)
					sum += secTap * constrain(s1-px, secStrength, secShift)
					sum += secTap * constrain(s2-px, secStrength, secShift)
					sum += secTap * constrain(s3-px, secStrength, secShift)
				}
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
	var partialSumHV [2][8]int
	var partialSumDiag [2][15]int
	var partialSumAlt [4][11]int

	ib := imgBase
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			px := int(img[ib+x]) - 128

			partialSumDiag[0][y+x] += px
			partialSumAlt[0][y+(x>>1)] += px
			partialSumHV[0][y] += px
			partialSumAlt[1][3+y-(x>>1)] += px
			partialSumDiag[1][7+y-x] += px
			partialSumAlt[2][3-(y>>1)+x] += px
			partialSumHV[1][x] += px
			partialSumAlt[3][(y>>1)+x] += px
		}
		ib += stride
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
