// Package looprestoration implements the AV1 loop restoration filters:
// Wiener filter and Self-Guided Restoration (SGR).
//
// Reference: dav1d/src/looprestoration_tmpl.c (8-bit path)
package looprestoration

// LrEdgeFlags indicates which edges are available.
type LrEdgeFlags uint8

const (
	LrHaveLeft   LrEdgeFlags = 1 << 0
	LrHaveRight  LrEdgeFlags = 1 << 1
	LrHaveTop    LrEdgeFlags = 1 << 2
	LrHaveBottom LrEdgeFlags = 1 << 3
)

// RestUnitStride is the width of the intermediate horizontal-filter buffer.
// 256 * 1.5 + 3 + 3 = 390, rounded up to 400 for safety.
const RestUnitStride = 400

// WienerParams holds the 7-tap Wiener filter coefficients for H and V passes.
// filter[0] = horizontal, filter[1] = vertical.
// Each coefficient is stored in 8 int16 slots (first/last implied symmetric).
type WienerParams struct {
	Filter [2][8]int16
}

// SGRParams holds the Self-Guided Restoration parameters.
type SGRParams struct {
	S0, S1 uint16 // strength parameters
	W0, W1 int    // weighting for 5x5 and 3x3 passes
}

// LooprestorationParams combines both filter types.
type LooprestorationParams struct {
	Wiener WienerParams
	SGR    SGRParams
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func iclipPixel(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
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

func umin(a, b uint) uint {
	if a < b {
		return a
	}
	return b
}

// ─── sgr_x_by_x table ────────────────────────────────────────────────────────

// sgrXbyX is dav1d_sgr_x_by_x[256].
var sgrXbyX = [256]uint8{
	255, 128, 85, 64, 51, 43, 37, 32, 28, 26, 23, 21, 20, 18, 17,
	16, 15, 14, 13, 13, 12, 12, 11, 11, 10, 10, 9, 9, 9, 9,
	8, 8, 8, 8, 7, 7, 7, 7, 7, 6, 6, 6, 6, 6, 6,
	6, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 4, 4, 4, 4,
	4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 3, 3,
	3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3,
	3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 2, 2, 2,
	2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
	2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
	2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
	2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
	2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	0,
}

// ─── Wiener filter ────────────────────────────────────────────────────────────

// wienerFilterH applies the 7-tap horizontal Wiener filter to one source row,
// writing into dst (uint16, H-filtered intermediate).
// src is the current row (0-indexed); left provides the 4-pixel left context.
// roundBitsH = 3 for 8-bit, clipLimit = 1<<(bitdepth+1+7-roundBitsH).
func wienerFilterH(dst []uint16, left []uint8, src []uint8, srcBase, w int,
	fh [8]int16, edges LrEdgeFlags) {

	const bitdepth = 8
	const roundBitsH = 3
	const roundingOffH = 1 << (roundBitsH - 1)
	const clipLimit = 1 << (bitdepth + 1 + 7 - roundBitsH) // 1<<13 = 8192

	// Pad left edge.
	var leftPad [4]uint8
	if edges&LrHaveLeft != 0 {
		if left != nil {
			copy(leftPad[:], left[:4])
		}
		// else left data is inline in src at negative indices
	} else {
		for i := range leftPad {
			leftPad[i] = src[srcBase]
		}
	}

	padPixel := func(idx int) int {
		if idx < 0 {
			if edges&LrHaveLeft != 0 {
				if left != nil {
					return int(leftPad[4+idx])
				}
				// left data is in src at negative offsets only if srcBase >= -idx
				if srcBase+idx >= 0 {
					return int(src[srcBase+idx])
				}
			}
			// pad with leftmost pixel
			return int(src[srcBase])
		}
		if idx >= w {
			if edges&LrHaveRight != 0 && srcBase+idx < len(src) {
				return int(src[srcBase+idx])
			}
			return int(src[srcBase+w-1])
		}
		return int(src[srcBase+idx])
	}

	for x := 0; x < w; x++ {
		// 8-bit: sum starts at src[x]*128 (identity term)
		sum := (1 << (bitdepth + 6)) + int(src[srcBase+x])*128
		for i := 0; i < 7; i++ {
			sum += padPixel(x+i-3) * int(fh[i])
		}
		v := (sum + roundingOffH) >> roundBitsH
		if v < 0 {
			v = 0
		} else if v >= clipLimit {
			v = clipLimit - 1
		}
		dst[x] = uint16(v)
	}
}

// WienerFilter applies the separable 7-tap Wiener filter to a restoration unit.
//
// p: destination pixel slice. pBase: offset into p. stride: row stride.
// left: left[h][4] left context pixels for each row. lpf: loop-filter row (top boundary).
// w, h: unit dimensions. params: Wiener coefficients. edges: edge flags.
func WienerFilter(p []uint8, pBase, stride int,
	left [][4]uint8,
	lpf []uint8, lpfBase, lpfStride int,
	w, h int, params *WienerParams, edges LrEdgeFlags) {

	const bitdepth = 8
	const roundBitsV = 11
	const roundingOffV = 1 << (roundBitsV - 1)
	const roundOffset = 1 << (bitdepth + roundBitsV - 1) // 1<<18

	fh := params.Filter[0]
	fv := params.Filter[1]

	// 6 intermediate row buffers of width RestUnitStride each.
	hor := make([]uint16, 6*RestUnitStride)
	rows := [6][]uint16{
		hor[0*RestUnitStride : 1*RestUnitStride],
		hor[1*RestUnitStride : 2*RestUnitStride],
		hor[2*RestUnitStride : 3*RestUnitStride],
		hor[3*RestUnitStride : 4*RestUnitStride],
		hor[4*RestUnitStride : 5*RestUnitStride],
		hor[5*RestUnitStride : 6*RestUnitStride],
	}

	// ptrs[0..6]: ring of horizontal-filtered rows.
	ptrs := [7][]uint16{}

	// wienerFilterV applies v-filter using ptrs[0..5] + tmp as ptrs[6].
	wienerFilterV := func(dst []uint8, dstOff int, ptrs [7][]uint16) {
		for i := 0; i < w; i++ {
			sum := -roundOffset
			for k := 0; k < 6; k++ {
				sum += int(ptrs[k][i]) * int(fv[k])
			}
			sum += int(ptrs[5][i]) * int(fv[6]) // last row repeated
			dst[dstOff+i] = iclipPixel((sum + roundingOffV) >> roundBitsV)
		}
	}

	rotateV := func(ptrs *[7][]uint16) {
		for i := 0; i < 5; i++ {
			ptrs[i] = ptrs[i+1]
		}
		// ptrs[6] re-used as next write target (caller updates ptrs[6])
	}

	// wienerFilterHV combines H filter + V filter.
	wienerFilterHV := func(dst []uint8, dstOff int, ptrs *[7][]uint16,
		leftRow []uint8, src []uint8, srcOff int) {
		tmp := ptrs[6]
		wienerFilterH(tmp, leftRow, src, srcOff, w, fh, edges)
		for i := 0; i < w; i++ {
			sum := -roundOffset
			for k := 0; k < 6; k++ {
				sum += int(ptrs[k][i]) * int(fv[k])
			}
			sum += int(tmp[i]) * int(fv[6])
			dst[dstOff+i] = iclipPixel((sum + roundingOffV) >> roundBitsV)
		}
		_ = rotateV
		copy(tmp, tmp[:w])
		for i := 0; i < 6; i++ {
			ptrs[i] = ptrs[i+1]
		}
		ptrs[6] = ptrs[0]
	}

	src := p
	srcOff := pBase

	if edges&LrHaveTop != 0 {
		ptrs[0] = rows[0]
		ptrs[1] = rows[0]
		ptrs[2] = rows[1]
		ptrs[3] = rows[2]
		ptrs[4] = rows[2]
		ptrs[5] = rows[2]

		wienerFilterH(rows[0], nil, lpf, lpfBase, w, fh, edges)
		wienerFilterH(rows[1], nil, lpf, lpfBase+lpfStride, w, fh, edges)

		var leftRow []uint8
		if left != nil {
			leftRow = left[0][:]
		}
		wienerFilterH(rows[2], leftRow, src, srcOff, w, fh, edges)
		if left != nil {
			leftRow = left[1][:]
		}
		srcOff += stride

		if h--; h <= 0 {
			goto v1
		}

		ptrs[4] = rows[3]
		ptrs[5] = rows[3]
		wienerFilterH(rows[3], leftRow, src, srcOff, w, fh, edges)
		if left != nil && len(left) > 2 {
			leftRow = left[2][:]
		}
		srcOff += stride

		if h--; h <= 0 {
			goto v2
		}

		ptrs[5] = rows[4]
		wienerFilterH(rows[4], leftRow, src, srcOff, w, fh, edges)
		if left != nil && len(left) > 3 {
			leftRow = left[3][:]
		}
		srcOff += stride

		if h--; h <= 0 {
			goto v3
		}
		_ = leftRow
	} else {
		for i := 0; i < 6; i++ {
			ptrs[i] = rows[0]
		}

		var leftRow []uint8
		if left != nil {
			leftRow = left[0][:]
		}
		wienerFilterH(rows[0], leftRow, src, srcOff, w, fh, edges)
		if left != nil && len(left) > 1 {
			leftRow = left[1][:]
		}
		srcOff += stride

		if h--; h <= 0 {
			goto v1
		}

		ptrs[4] = rows[1]
		ptrs[5] = rows[1]
		wienerFilterH(rows[1], leftRow, src, srcOff, w, fh, edges)
		if left != nil && len(left) > 2 {
			leftRow = left[2][:]
		}
		srcOff += stride

		if h--; h <= 0 {
			goto v2
		}

		ptrs[5] = rows[2]
		wienerFilterH(rows[2], leftRow, src, srcOff, w, fh, edges)
		if left != nil && len(left) > 3 {
			leftRow = left[3][:]
		}
		srcOff += stride

		if h--; h <= 0 {
			goto v3
		}

		ptrs[6] = rows[3]
		var lr4 []uint8
		if left != nil && len(left) > 4 {
			lr4 = left[4][:]
		}
		wienerFilterHV(p, pBase, &ptrs, lr4, src, srcOff)
		if left != nil && len(left) > 5 {
			lr4 = left[5][:]
		}
		srcOff += stride
		pBase += stride

		if h--; h <= 0 {
			goto v3
		}

		ptrs[6] = rows[4]
		wienerFilterHV(p, pBase, &ptrs, lr4, src, srcOff)
		srcOff += stride
		pBase += stride

		if h--; h <= 0 {
			goto v3
		}
	}

	ptrs[6] = rows[5]
	for h > 0 {
		wienerFilterHV(p, pBase, &ptrs, nil, src, srcOff)
		srcOff += stride
		pBase += stride
		h--
	}

	if edges&LrHaveBottom == 0 {
		goto v3
	}
	wienerFilterHV(p, pBase, &ptrs, nil, lpf, lpfBase+6*lpfStride)
	pBase += stride
	wienerFilterHV(p, pBase, &ptrs, nil, lpf, lpfBase+7*lpfStride)
	pBase += stride

v1:
	wienerFilterV(p, pBase, ptrs)
	return

v3:
	wienerFilterV(p, pBase, ptrs)
	pBase += stride
v2:
	wienerFilterV(p, pBase, ptrs)
	pBase += stride
	goto v1
}

// ─── SGR helpers ─────────────────────────────────────────────────────────────

const bufStride = 400 // BUF_STRIDE in dav1d
const filterOutStride = 384

// sgr_box3_row_h: compute horizontal sums+sumsq for a 3-wide box.
func sgrBox3RowH(sumsq []int32, sum []int16,
	left []uint8, src []uint8, srcBase, w int, edges LrEdgeFlags) {

	// index 0 in output = position -1 (one left of block)
	// sumsq[i+1] corresponds to x=i-1
	a := func(idx int) int {
		if idx < 0 {
			if edges&LrHaveLeft != 0 {
				if left != nil {
					return int(left[3+idx])
				}
				if srcBase+idx >= 0 {
					return int(src[srcBase+idx])
				}
			}
			return int(src[srcBase])
		}
		if idx >= w {
			if edges&LrHaveRight != 0 && srcBase+idx < len(src) {
				return int(src[srcBase+idx])
			}
			return int(src[srcBase+w-1])
		}
		return int(src[srcBase+idx])
	}

	pa := a(-2)
	pb := a(-1)
	for x := -1; x < w+1; x++ {
		pc := a(x + 1)
		sum[x+1] = int16(pa + pb + pc)
		sumsq[x+1] = int32(pa*pa + pb*pb + pc*pc)
		pa = pb
		pb = pc
	}
}

// sgr_box5_row_h: compute horizontal sums+sumsq for a 5-wide box.
func sgrBox5RowH(sumsq []int32, sum []int16,
	left []uint8, src []uint8, srcBase, w int, edges LrEdgeFlags) {

	a := func(idx int) int {
		if idx < 0 {
			if edges&LrHaveLeft != 0 {
				if left != nil {
					return int(left[4+idx])
				}
				if srcBase+idx >= 0 {
					return int(src[srcBase+idx])
				}
			}
			return int(src[srcBase])
		}
		if idx >= w {
			if edges&LrHaveRight != 0 && srcBase+idx < len(src) {
				return int(src[srcBase+idx])
			}
			return int(src[srcBase+w-1])
		}
		return int(src[srcBase+idx])
	}

	pa := a(-3)
	pb := a(-2)
	pc := a(-1)
	pd := a(0)
	for x := -1; x < w+1; x++ {
		pe := a(x + 2)
		sum[x+1] = int16(pa + pb + pc + pd + pe)
		sumsq[x+1] = int32(pa*pa + pb*pb + pc*pc + pd*pd + pe*pe)
		pa = pb
		pb = pc
		pc = pd
		pd = pe
	}
}

// sgrBoxRowV: sum 3 (or 5) rows of sumsq/sum vertically.
func sgrBox3RowV(sumsq [3][]int32, sum [3][]int16, sumsqOut []int32, sumOut []int16, w int) {
	for x := 0; x < w+2; x++ {
		sumsqOut[x] = sumsq[0][x] + sumsq[1][x] + sumsq[2][x]
		sumOut[x] = sum[0][x] + sum[1][x] + sum[2][x]
	}
}

func sgrBox5RowV(sumsq [5][]int32, sum [5][]int16, sumsqOut []int32, sumOut []int16, w int) {
	for x := 0; x < w+2; x++ {
		sumsqOut[x] = sumsq[0][x] + sumsq[1][x] + sumsq[2][x] + sumsq[3][x] + sumsq[4][x]
		sumOut[x] = sum[0][x] + sum[1][x] + sum[2][x] + sum[3][x] + sum[4][x]
	}
}

// sgrCalcRowAB: compute A and B values from sumsq/sum, for SGR.
// n=9 for 3x3, n=25 for 5x5. sgr_one_by_x=455 for n=9, 164 for n=25.
func sgrCalcRowAB(AA []int32, BB []int16, w, s, n, sgrOneByX int) {
	for i := 0; i < w+2; i++ {
		a := int(AA[i])
		b := int(BB[i])
		p := uint(0)
		if v := a*n - b*b; v > 0 {
			p = uint(v)
		}
		z := (p*uint(s) + (1 << 19)) >> 20
		x := int(sgrXbyX[umin(z, 255)])

		AA[i] = int32((x*b*sgrOneByX + (1 << 11)) >> 12)
		BB[i] = int16(x)
	}
}

// ─── SGR 3x3 ─────────────────────────────────────────────────────────────────

// SGR3x3 applies the 3×3 Self-Guided Restoration filter.
func SGR3x3(dst []uint8, dstBase, dstStride int,
	left [][4]uint8,
	lpf []uint8, lpfBase, lpfStride int,
	w, h int, params *SGRParams, edges LrEdgeFlags) {

	s := int(params.S1)
	w1 := params.W1

	sumsqBuf := make([]int32, bufStride*3)
	sumBuf := make([]int16, bufStride*3)
	sumsqRows := [3][]int32{
		sumsqBuf[0*bufStride : 1*bufStride],
		sumsqBuf[1*bufStride : 2*bufStride],
		sumsqBuf[2*bufStride : 3*bufStride],
	}
	sumRows := [3][]int16{
		sumBuf[0*bufStride : 1*bufStride],
		sumBuf[1*bufStride : 2*bufStride],
		sumBuf[2*bufStride : 3*bufStride],
	}

	ABuf := make([]int32, bufStride*3)
	BBuf := make([]int16, bufStride*3)
	APtrs := [3][]int32{ABuf[0*bufStride:], ABuf[1*bufStride:], ABuf[2*bufStride:]}
	BPtrs := [3][]int16{BBuf[0*bufStride:], BBuf[1*bufStride:], BBuf[2*bufStride:]}

	src := dst
	srcOff := dstBase
	lpfBottom := lpfBase + 6*lpfStride

	sumsqPtrs := [3][]int32{sumsqRows[0], sumsqRows[1], sumsqRows[2]}
	sumPtrs := [3][]int16{sumRows[0], sumRows[1], sumRows[2]}

	rowIdx := 0

	sgr3Hv := func(leftRow []uint8, srcRow []uint8, srcRowOff int) {
		sumsqPtrs[2] = sumsqRows[rowIdx]
		sumPtrs[2] = sumRows[rowIdx]
		sgrBox3RowH(sumsqPtrs[2], sumPtrs[2], leftRow, srcRow, srcRowOff, w, edges)
		// vertical sum + calc AB
		sgrBox3RowV(sumsqPtrs, sumPtrs, APtrs[2], BPtrs[2], w)
		sgrCalcRowAB(APtrs[2], BPtrs[2], w, s, 9, 455)
		// rotate
		sumsqPtrs[0], sumsqPtrs[1], sumsqPtrs[2] = sumsqPtrs[1], sumsqPtrs[2], sumsqPtrs[0]
		sumPtrs[0], sumPtrs[1], sumPtrs[2] = sumPtrs[1], sumPtrs[2], sumPtrs[0]
		APtrs[0], APtrs[1], APtrs[2] = APtrs[1], APtrs[2], APtrs[0]
		BPtrs[0], BPtrs[1], BPtrs[2] = BPtrs[1], BPtrs[2], BPtrs[0]
		rowIdx = (rowIdx + 1) % 3
	}

	finish1 := func(dstRow []uint8, dstRowOff int) {
		for i := 0; i < w; i++ {
			a := (int(BPtrs[1][i+1])*6 +
				(int(BPtrs[1][i])+int(BPtrs[1][i+2]))*5 +
				(int(BPtrs[0][i+1])+int(BPtrs[2][i+1]))*4 +
				(int(BPtrs[0][i])+int(BPtrs[0][i+2])+
					int(BPtrs[2][i])+int(BPtrs[2][i+2]))*3) // sum of B (8 neighbours)
			b := (int(APtrs[1][i+1])*6 +
				(int(APtrs[1][i])+int(APtrs[1][i+2]))*5 +
				(int(APtrs[0][i+1])+int(APtrs[2][i+1]))*4 +
				(int(APtrs[0][i])+int(APtrs[0][i+2])+
					int(APtrs[2][i])+int(APtrs[2][i+2]))*3)
			tmp := (b - a*int(dstRow[dstRowOff+i]) + (1 << 8)) >> 9
			v := w1 * tmp
			dstRow[dstRowOff+i] = iclipPixel(int(dstRow[dstRowOff+i]) + ((v + (1 << 10)) >> 11))
		}
		APtrs[0], APtrs[1], APtrs[2] = APtrs[1], APtrs[2], APtrs[0]
		BPtrs[0], BPtrs[1], BPtrs[2] = BPtrs[1], BPtrs[2], BPtrs[0]
	}

	leftIdx := 0
	getLeft := func() []uint8 {
		if left != nil && leftIdx < len(left) {
			r := left[leftIdx][:]
			leftIdx++
			return r
		}
		return nil
	}

	if edges&LrHaveTop != 0 {
		// Pre-fill top two rows from loop filter.
		sgrBox3RowH(sumsqRows[0], sumRows[0], nil, lpf, lpfBase, w, edges)
		sgrBox3RowH(sumsqRows[1], sumRows[1], nil, lpf, lpfBase+lpfStride, w, edges)
		rowIdx = 2

		sgr3Hv(getLeft(), src, srcOff)
		srcOff += dstStride

		if h--; h <= 0 {
			goto vert1
		}

		sgr3Hv(getLeft(), src, srcOff)
		srcOff += dstStride

		if h--; h <= 0 {
			goto vert2
		}
	} else {
		// No top: pad with first source row.
		sgrBox3RowH(sumsqRows[0], sumRows[0], getLeft(), src, srcOff, w, edges)
		srcOff += dstStride
		sumsqPtrs[0] = sumsqRows[0]
		sumsqPtrs[1] = sumsqRows[0]
		sumsqPtrs[2] = sumsqRows[0]
		sumPtrs[0] = sumRows[0]
		sumPtrs[1] = sumRows[0]
		sumPtrs[2] = sumRows[0]
		sgrBox3RowV(sumsqPtrs, sumPtrs, APtrs[2], BPtrs[2], w)
		sgrCalcRowAB(APtrs[2], BPtrs[2], w, s, 9, 455)
		APtrs[0], APtrs[1], APtrs[2] = APtrs[1], APtrs[2], APtrs[0]
		BPtrs[0], BPtrs[1], BPtrs[2] = BPtrs[1], BPtrs[2], BPtrs[0]
		rowIdx = 1

		if h--; h <= 0 {
			goto vert1
		}

		sumsqPtrs[2] = sumsqRows[1]
		sumPtrs[2] = sumRows[1]
		sgr3Hv(getLeft(), src, srcOff)
		srcOff += dstStride

		if h--; h <= 0 {
			goto vert2
		}

		sumsqPtrs[2] = sumsqRows[2]
		sumPtrs[2] = sumRows[2]
	}

	for h > 0 {
		sgr3Hv(getLeft(), src, srcOff)
		srcOff += dstStride
		finish1(dst, dstBase)
		dstBase += dstStride
		h--
	}

	if edges&LrHaveBottom == 0 {
		goto vert2
	}

	sgr3Hv(nil, lpf, lpfBottom)
	finish1(dst, dstBase)
	dstBase += dstStride
	lpfBottom += lpfStride

	sgr3Hv(nil, lpf, lpfBottom)
	finish1(dst, dstBase)
	return

vert2:
	sumsqPtrs[2] = sumsqPtrs[1]
	sumPtrs[2] = sumPtrs[1]
	sgrBox3RowV(sumsqPtrs, sumPtrs, APtrs[2], BPtrs[2], w)
	sgrCalcRowAB(APtrs[2], BPtrs[2], w, s, 9, 455)
	APtrs[0], APtrs[1], APtrs[2] = APtrs[1], APtrs[2], APtrs[0]
	BPtrs[0], BPtrs[1], BPtrs[2] = BPtrs[1], BPtrs[2], BPtrs[0]
	finish1(dst, dstBase)
	dstBase += dstStride

vert1:
	sumsqPtrs[2] = sumsqPtrs[1]
	sumPtrs[2] = sumPtrs[1]
	sgrBox3RowV(sumsqPtrs, sumPtrs, APtrs[2], BPtrs[2], w)
	sgrCalcRowAB(APtrs[2], BPtrs[2], w, s, 9, 455)
	APtrs[0], APtrs[1], APtrs[2] = APtrs[1], APtrs[2], APtrs[0]
	BPtrs[0], BPtrs[1], BPtrs[2] = BPtrs[1], BPtrs[2], BPtrs[0]
	finish1(dst, dstBase)
	_ = lpfBottom
}

// ─── SGR 5x5 (simplified stub for completeness) ──────────────────────────────

// SGR5x5 applies the 5×5 Self-Guided Restoration filter.
// This is a simplified implementation that handles the core algorithm.
func SGR5x5(dst []uint8, dstBase, dstStride int,
	left [][4]uint8,
	lpf []uint8, lpfBase, lpfStride int,
	w, h int, params *SGRParams, edges LrEdgeFlags) {

	s := int(params.S0)
	w0 := params.W0

	// For each output row y, compute the 5x5 box sum for rows y-2..y+2,
	// then compute A/B per pixel, then apply weighting.
	// We use a simplified row-by-row accumulation with 5 row buffers.

	const N = 5
	sumsqBuf := make([]int32, bufStride*N)
	sumBuf := make([]int16, bufStride*N)
	sumsqRows := [N][]int32{}
	sumRows := [N][]int16{}
	for i := 0; i < N; i++ {
		sumsqRows[i] = sumsqBuf[i*bufStride : (i+1)*bufStride]
		sumRows[i] = sumBuf[i*bufStride : (i+1)*bufStride]
	}

	ABuf := make([]int32, bufStride)
	BBuf := make([]int16, bufStride)

	getLeft := func(idx int) []uint8 {
		if left != nil && idx >= 0 && idx < len(left) {
			return left[idx][:]
		}
		return nil
	}

	// Fill horizontal row sums for row rIdx of the image.
	// We need rows from -2 to h+1 (clamp to [0,h-1] or use lpf).
	fillRow := func(buf int, srcOff int) {
		sb := dst
		var lr []uint8
		if srcOff < dstBase {
			// above image: use lpf or clamp
			if edges&LrHaveTop != 0 {
				sb = lpf
				srcOff = lpfBase // approximate
			} else {
				srcOff = dstBase
			}
		} else if srcOff >= dstBase+h*dstStride {
			if edges&LrHaveBottom != 0 {
				sb = lpf
				srcOff = lpfBase
			} else {
				srcOff = dstBase + (h-1)*dstStride
			}
		}
		sgrBox5RowH(sumsqRows[buf], sumRows[buf], lr, sb, srcOff, w, edges)
	}

	// For each output row y, sum 5 horizontal rows.
	for y := 0; y < h; y++ {
		for dy := -2; dy <= 2; dy++ {
			buf := (dy + 2) % N
			rowOff := dstBase + (y+dy)*dstStride
			var lr []uint8
			if y+dy >= 0 && y+dy < h {
				lr = getLeft(y + dy)
			}
			_ = lr
			fillRow(buf, rowOff)
		}

		// Vertical sum.
		var sq5 [5][]int32
		var s5 [5][]int16
		for i := 0; i < N; i++ {
			sq5[i] = sumsqRows[i]
			s5[i] = sumRows[i]
		}
		sgrBox5RowV(sq5, s5, ABuf, BBuf, w)
		sgrCalcRowAB(ABuf, BBuf, w, s, 25, 164)

		// Apply weighted correction.
		dstOff := dstBase + y*dstStride
		for i := 0; i < w; i++ {
			a := (int(BBuf[i+1])*6 + (int(BBuf[i])+int(BBuf[i+2]))*5)
			b := (int(ABuf[i+1])*6 + (int(ABuf[i])+int(ABuf[i+2]))*5)
			tmp := (b - a*int(dst[dstOff+i]) + (1 << 8)) >> 9
			v := w0 * tmp
			dst[dstOff+i] = iclipPixel(int(dst[dstOff+i]) + ((v + (1 << 10)) >> 11))
		}
	}
}
