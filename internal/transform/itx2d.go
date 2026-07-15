package transform

// 2D inverse transform (row-then-column) with pixel-domain output,
// faithfully ported from dav1d/src/itx_tmpl.c inv_txfm_add_c (lines 43-119)
// and the WHT 4×4 path (lines 184-203).
//
// The 2D transform is decomposed as:
//
//	1. Row-first 1D transform  (first_1d_fn, stride=1)
//	2. Mid-point shift+clip
//	3. Column-second 1D transform (second_1d_fn, stride=w)
//	4. Final shift+round (+8 >> 4) and add to pixel buffer

// pixelClamp clips a value to [0, maxVal]. Used for final pixel output.
func pixelClamp(v, maxVal int) int {
	if v < 0 {
		return 0
	}
	if v > maxVal {
		return maxVal
	}
	return v
}

// InvTxfmAdd applies a 2D inverse transform of the given type and size
// to the coefficient block, adds the result to the pixel buffer dst, and
// zeroes the coefficient block (matching dav1d behaviour).
//
// Parameters:
//   - dst:    pixel buffer (row-major, stride in pixels)
//   - stride: row stride of dst in pixels
//   - coeff:  coefficient block (column-major, [sh][sw] layout as per dav1d);
//     zeroed on return
//   - eob:    end-of-block index (last non-zero coefficient position);
//     eob < 0 is treated as eob = 0
//   - tx:     RectTxfmSize constant (TX4x4..TX64x64, RTX4x8..RTX64x16)
//   - shift:  intermediate shift value (0, 1, or 2 depending on transform size)
//   - txtp:   2D transform type (DCT_DCT, ADST_DCT, etc.)
//   - bitDepth: pixel bit depth (8, 10, or 12)
func InvTxfmAdd(dst []uint8, stride int, coeff []int32, eob int,
	tx uint8, shift int, txtp uint8, bitDepth int) {
	InvTxfmAddWithLastNonzeroCol(dst, stride, coeff, eob, tx, shift, txtp, -1, bitDepth)
}

// InvTxfmAddWithLastNonzeroCol mirrors InvTxfmAdd but allows callers to pass
// dav1d's exact last_nonzero_col_from_eob value when the coefficient scan path
// has it available.
func InvTxfmAddWithLastNonzeroCol(dst []uint8, stride int, coeff []int32, eob int,
	tx uint8, shift int, txtp uint8, exactLastNonzeroCol int, bitDepth int) {
	if txtp == WHT_WHT {
		InvWHT4x4(dst, stride, coeff, bitDepth)
		return
	}

	td := TxfmDimensions[tx]
	w := int(td.W) * 4
	h := int(td.H) * 4

	maxPixelVal := (1 << bitDepth) - 1
	hasDcOnly := txtp == DCT_DCT

	if eob < 0 {
		eob = 0
	}

	sh := h
	if sh > 32 {
		sh = 32
	}
	sw := w
	if sw > 32 {
		sw = 32
	}

	rnd := (1 << shift) >> 1

	// DC-only fast path (mirrors dav1d inv_txfm_add_c lines 58-69)
	if eob < 1 && hasDcOnly {
		dc := int(coeff[0])
		coeff[0] = 0
		isRect2 := w*2 == h || h*2 == w
		if isRect2 {
			dc = (dc*181 + 128) >> 8
		}
		dc = (dc*181 + 128) >> 8
		dc = (dc + rnd) >> shift
		dc = (dc*181 + 128 + 2048) >> 12
		for y := 0; y < h; y++ {
			rowOff := y * stride
			for x := 0; x < w; x++ {
				dst[rowOff+x] = uint8(pixelClamp(int(dst[rowOff+x])+dc, maxPixelVal))
			}
		}
		return
	}

	txtps := Tx1dTypes[txtp]
	// TxfmType names are vertical_horizontal, while the transposed coefficient
	// layout makes dav1d execute the horizontal pass first.
	first1d := Tx1dFns[td.Lw][txtps[1]]
	second1d := Tx1dFns[td.Lh][txtps[0]]

	// Guard against unimplemented transform combinations (e.g. ADST for TX32/TX64).
	// Fall back to DCT if the requested 1D function is nil.
	if first1d == nil {
		first1d = Tx1dFns[td.Lw][Tx1dDCT]
	}
	if second1d == nil {
		second1d = Tx1dFns[td.Lh][Tx1dDCT]
	}

	// Clip bounds for intermediate values
	var rowClipMin, rowClipMax, colClipMin, colClipMax int
	if bitDepth == 8 {
		rowClipMin = -1 << 15 // INT16_MIN
		rowClipMax = 1<<15 - 1
		colClipMin = -1 << 15
		colClipMax = 1<<15 - 1
	} else {
		bdMax := 1 << bitDepth
		rowClipMin = -((bdMax) << 7)
		rowClipMax = (bdMax << 7) - 1
		colClipMin = -((bdMax) << 5)
		colClipMax = (bdMax << 5) - 1
	}

	// Temporary buffer: 64×64 max
	var tmp [64 * 64]int32

	isRect2 := w*2 == h || h*2 == w

	// Compute last nonzero column following dav1d's inv_txfm_add_c().
	// When the caller already derived the exact value from syntax-layer state,
	// prefer that single source of truth so tile/recon do not drift.
	lastNonzeroCol := sh - 1
	if exactLastNonzeroCol >= 0 {
		lastNonzeroCol = exactLastNonzeroCol
	} else if txtps[0] == Tx1dIDENTITY && txtps[1] != Tx1dIDENTITY {
		if eob < lastNonzeroCol {
			lastNonzeroCol = eob
		}
	} else if txtps[1] == Tx1dIDENTITY && txtps[0] != Tx1dIDENTITY {
		lastNonzeroCol = eob >> (int(td.Lw) + 2)
	}
	if lastNonzeroCol >= sh {
		lastNonzeroCol = sh - 1
	}

	// Row-first 1D transform
	c := tmp[:]
	for y := 0; y <= lastNonzeroCol; y++ {
		row := c[y*w : (y+1)*w]
		if isRect2 {
			for x := 0; x < sw; x++ {
				row[x] = int32((int(coeff[y+x*sh])*181 + 128) >> 8)
			}
		} else {
			for x := 0; x < sw; x++ {
				row[x] = coeff[y+x*sh]
			}
		}
		first1d(row, 1, rowClipMin, rowClipMax)
	}
	// Zero remaining rows
	for y := lastNonzeroCol + 1; y < sh; y++ {
		for x := 0; x < w; x++ {
			tmp[y*w+x] = 0
		}
	}

	// Zero coefficient block
	for i := range coeff {
		coeff[i] = 0
	}

	// Mid-point shift + clip
	for i := 0; i < w*sh; i++ {
		v := (int(tmp[i]) + rnd) >> shift
		if v < colClipMin {
			v = colClipMin
		} else if v > colClipMax {
			v = colClipMax
		}
		tmp[i] = int32(v)
	}

	// Column-second 1D transform
	for x := 0; x < w; x++ {
		col := tmp[x:] // stride=w, len=4096-x, sufficient for (sh-1)*w+1
		second1d(col, w, colClipMin, colClipMax)
	}

	// Final pixel add with rounding (+8 >> 4)
	c = tmp[:]
	for y := 0; y < h; y++ {
		rowOff := y * stride
		for x := 0; x < w; x++ {
			dst[rowOff+x] = uint8(pixelClamp(int(dst[rowOff+x])+((int(c[0])+8)>>4), maxPixelVal))
			c = c[1:]
		}
	}
}

// InvWHT4x4 applies the 4×4 Walsh-Hadamard inverse transform (lossless),
// ported from dav1d inv_txfm_add_wht_wht_4x4_c.
// coeff is in column-major [4][4] layout; zeroed on return.
// dst has row stride in pixels.
func InvWHT4x4(dst []uint8, stride int, coeff []int32, bitDepth int) {
	maxPixelVal := (1 << bitDepth) - 1
	var tmp [4 * 4]int32

	// Row-first WHT (with >> 2 pre-shift)
	for y := 0; y < 4; y++ {
		row := tmp[y*4 : (y+1)*4]
		for x := 0; x < 4; x++ {
			row[x] = coeff[y+x*4] >> 2
		}
		InvWHT4(row, 1)
	}
	// Zero coefficient block
	for i := range coeff {
		coeff[i] = 0
	}

	// Column-second WHT
	for x := 0; x < 4; x++ {
		col := tmp[x:] // stride=4, len=16-x, sufficient for 3*4+1=13
		InvWHT4(col, 4)
	}

	// Pixel add
	c := tmp[:]
	for y := 0; y < 4; y++ {
		rowOff := y * stride
		for x := 0; x < 4; x++ {
			dst[rowOff+x] = uint8(pixelClamp(int(dst[rowOff+x])+int(c[0]), maxPixelVal))
			c = c[1:]
		}
	}
}
