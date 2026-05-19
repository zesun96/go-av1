// Package inter – motion compensation (inter prediction).
//
// This file implements the pure-Go generic kernels for:
//   - put8tap:   copy subpel-filtered reference pixels into dst (single reference)
//   - prep8tap:  produce int16 intermediate buffer for compound prediction
//   - putBilin:  bilinear (2-tap) subpel filter
//   - prepBilin: bilinear intermediate buffer
//   - Avg:       simple average of two compound buffers → dst
//   - WAvg:      weighted average (dav1d w_avg)
//
// Reference: dav1d/src/mc_tmpl.c (8-bit path, PREP_BIAS=0).
//
// Padding contract (mirroring dav1d emu_edge):
//
//	Callers must ensure the src buffer has at least 3 pixels of padding on all
//	four sides relative to the first active pixel.  The srcBase parameter
//	points to row 0, column 0 of the active area within the larger buffer.
//	This means src[srcBase - 3*srcStride - 3] must be a valid address.
package inter

// intermediateBits is the number of extra precision bits kept in the
// horizontal-pass intermediate buffer (8-bit path = 4, matching dav1d).
const intermediateBits = 4

// clampPixel clamps v to [0, 255].
func clampPixel(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// filter8H applies one 8-tap horizontal convolution.
// base = srcBase + row*srcStride + x (index of the "current" pixel).
// Taps read src[base-3 .. base+4].
func filter8H(src []uint8, base int, f []int8) int {
	return int(f[0])*int(src[base-3]) +
		int(f[1])*int(src[base-2]) +
		int(f[2])*int(src[base-1]) +
		int(f[3])*int(src[base+0]) +
		int(f[4])*int(src[base+1]) +
		int(f[5])*int(src[base+2]) +
		int(f[6])*int(src[base+3]) +
		int(f[7])*int(src[base+4])
}

// filter8V applies one 8-tap vertical convolution.
// base = srcBase + row*srcStride + x.
// Taps read src[base-3*stride .. base+4*stride].
func filter8V(src []uint8, base, stride int, f []int8) int {
	return int(f[0])*int(src[base-3*stride]) +
		int(f[1])*int(src[base-2*stride]) +
		int(f[2])*int(src[base-1*stride]) +
		int(f[3])*int(src[base+0*stride]) +
		int(f[4])*int(src[base+1*stride]) +
		int(f[5])*int(src[base+2*stride]) +
		int(f[6])*int(src[base+3*stride]) +
		int(f[7])*int(src[base+4*stride])
}

// filter8i16 applies one 8-tap convolution on an int16 intermediate buffer.
// midBase is the index of the row-3 entry; rows are separated by midStride.
func filter8i16(mid []int16, midBase, x, midStride int, f []int8) int {
	return int(f[0])*int(mid[midBase+0*midStride+x]) +
		int(f[1])*int(mid[midBase+1*midStride+x]) +
		int(f[2])*int(mid[midBase+2*midStride+x]) +
		int(f[3])*int(mid[midBase+3*midStride+x]) +
		int(f[4])*int(mid[midBase+4*midStride+x]) +
		int(f[5])*int(mid[midBase+5*midStride+x]) +
		int(f[6])*int(mid[midBase+6*midStride+x]) +
		int(f[7])*int(mid[midBase+7*midStride+x])
}

// Put8Tap writes the motion-compensated block into dst.
//
//   - dst: destination slice; dst[0] is the top-left of the output block.
//   - dstStride: row stride of dst (pixels).
//   - src: reference plane buffer.
//   - srcBase: index of (row=0, col=0) within src.  Must have 3-pixel padding
//     on all sides (i.e. src[srcBase-3*srcStride-3] must be valid).
//   - srcStride: row stride of src.
//   - w, h: block width/height in pixels.
//   - mx, my: horizontal/vertical 1/16-pel sub-pixel offsets (1..15; 0 = integer).
//   - f: 2-D filter selector.
//
// 1:1 port of dav1d put_8tap_c (8-bit path).
func Put8Tap(dst []uint8, dstStride int,
	src []uint8, srcBase, srcStride int,
	w, h, mx, my int, f Filter2D) {

	fh, fv := GetFilters(f, w, mx, my)

	switch {
	case fh != nil && fv != nil:
		// Two-pass: H then V through an int16 intermediate buffer.
		tmpH := h + 7 // 3 extra rows above + 4 below for the V tap range
		midStride := 128
		mid := make([]int16, midStride*tmpH)

		// H pass: start 3 rows above the block.
		rowBase := srcBase - 3*srcStride
		for row := 0; row < tmpH; row++ {
			for x := 0; x < w; x++ {
				v := filter8H(src, rowBase+x, fh)
				mid[row*midStride+x] = int16((v + (1 << (5 - intermediateBits))) >> (6 - intermediateBits))
			}
			rowBase += srcStride
		}

		// V pass over mid; row 3 of mid corresponds to the block's row 0.
		dstOff := 0
		for row := 0; row < h; row++ {
			midBase := row * midStride // tap[0] is at row, tap[-3] at row-3=mid row
			for x := 0; x < w; x++ {
				v := filter8i16(mid, midBase, x, midStride, fv)
				dst[dstOff+x] = clampPixel((v + (1 << (5 + intermediateBits))) >> (6 + intermediateBits))
			}
			dstOff += dstStride
		}

	case fh != nil:
		// Horizontal-only.
		intermediateRnd := 32 + ((1 << (6 - intermediateBits)) >> 1)
		rowBase := srcBase
		dstOff := 0
		for row := 0; row < h; row++ {
			for x := 0; x < w; x++ {
				v := filter8H(src, rowBase+x, fh)
				dst[dstOff+x] = clampPixel((v + intermediateRnd) >> 6)
			}
			rowBase += srcStride
			dstOff += dstStride
		}

	case fv != nil:
		// Vertical-only.
		rowBase := srcBase
		dstOff := 0
		for row := 0; row < h; row++ {
			for x := 0; x < w; x++ {
				v := filter8V(src, rowBase+x, srcStride, fv)
				dst[dstOff+x] = clampPixel((v + 32) >> 6)
			}
			rowBase += srcStride
			dstOff += dstStride
		}

	default:
		// Integer-pel copy.
		rowBase := srcBase
		dstOff := 0
		for row := 0; row < h; row++ {
			copy(dst[dstOff:dstOff+w], src[rowBase:rowBase+w])
			rowBase += srcStride
			dstOff += dstStride
		}
	}
}

// PutCopy copies a w×h block starting at src[srcBase] into dst.
func PutCopy(dst []uint8, dstStride int,
	src []uint8, srcBase, srcStride int, w, h int) {
	dstOff := 0
	rowBase := srcBase
	for row := 0; row < h; row++ {
		copy(dst[dstOff:dstOff+w], src[rowBase:rowBase+w])
		rowBase += srcStride
		dstOff += dstStride
	}
}

// Prep8Tap fills tmp with the Q4 intermediate compound buffer for one reference.
// tmp must have capacity w*h.
//
// 1:1 port of dav1d prep_8tap_c (8-bit).
func Prep8Tap(tmp []int16,
	src []uint8, srcBase, srcStride int,
	w, h, mx, my int, f Filter2D) {

	fh, fv := GetFilters(f, w, mx, my)

	switch {
	case fh != nil && fv != nil:
		tmpH := h + 7
		midStride := 128
		mid := make([]int16, midStride*tmpH)

		rowBase := srcBase - 3*srcStride
		for row := 0; row < tmpH; row++ {
			for x := 0; x < w; x++ {
				v := filter8H(src, rowBase+x, fh)
				mid[row*midStride+x] = int16((v + (1 << (5 - intermediateBits))) >> (6 - intermediateBits))
			}
			rowBase += srcStride
		}

		tmpOff := 0
		for row := 0; row < h; row++ {
			midBase := row * midStride
			for x := 0; x < w; x++ {
				v := filter8i16(mid, midBase, x, midStride, fv)
				tmp[tmpOff+x] = int16((v + (1 << 5)) >> 6)
			}
			tmpOff += w
		}

	case fh != nil:
		rowBase := srcBase
		tmpOff := 0
		for row := 0; row < h; row++ {
			for x := 0; x < w; x++ {
				v := filter8H(src, rowBase+x, fh)
				tmp[tmpOff+x] = int16((v + (1 << (5 - intermediateBits))) >> (6 - intermediateBits))
			}
			rowBase += srcStride
			tmpOff += w
		}

	case fv != nil:
		rowBase := srcBase
		tmpOff := 0
		for row := 0; row < h; row++ {
			for x := 0; x < w; x++ {
				v := filter8V(src, rowBase+x, srcStride, fv)
				tmp[tmpOff+x] = int16((v + (1 << (5 - intermediateBits))) >> (6 - intermediateBits))
			}
			rowBase += srcStride
			tmpOff += w
		}

	default:
		rowBase := srcBase
		tmpOff := 0
		for row := 0; row < h; row++ {
			for x := 0; x < w; x++ {
				tmp[tmpOff+x] = int16(src[rowBase+x]) << intermediateBits
			}
			rowBase += srcStride
			tmpOff += w
		}
	}
}

// PutBilin writes a bilinearly-interpolated block into dst.
// mx, my are 1/16-pel offsets (0 = integer position).
func PutBilin(dst []uint8, dstStride int,
	src []uint8, srcBase, srcStride int,
	w, h, mx, my int) {

	switch {
	case mx != 0 && my != 0:
		tmpStride := w
		tmp := make([]int16, w*(h+1))

		rowBase := srcBase
		for row := 0; row <= h; row++ {
			for x := 0; x < w; x++ {
				v := int(src[rowBase+x])*(16-mx) + int(src[rowBase+x+1])*mx
				tmp[row*tmpStride+x] = int16((v+8)>>4) << intermediateBits
			}
			rowBase += srcStride
		}

		dstOff := 0
		for row := 0; row < h; row++ {
			for x := 0; x < w; x++ {
				v := int(tmp[row*tmpStride+x])*(16-my) + int(tmp[(row+1)*tmpStride+x])*my
				dst[dstOff+x] = clampPixel((v + (1 << (3 + intermediateBits))) >> (4 + intermediateBits))
			}
			dstOff += dstStride
		}

	case mx != 0:
		rowBase := srcBase
		dstOff := 0
		for row := 0; row < h; row++ {
			for x := 0; x < w; x++ {
				v := int(src[rowBase+x])*(16-mx) + int(src[rowBase+x+1])*mx
				dst[dstOff+x] = clampPixel((v + 8) >> 4)
			}
			rowBase += srcStride
			dstOff += dstStride
		}

	case my != 0:
		rowBase := srcBase
		dstOff := 0
		for row := 0; row < h; row++ {
			for x := 0; x < w; x++ {
				v := int(src[rowBase+x])*(16-my) + int(src[rowBase+srcStride+x])*my
				dst[dstOff+x] = clampPixel((v + 8) >> 4)
			}
			rowBase += srcStride
			dstOff += dstStride
		}

	default:
		PutCopy(dst, dstStride, src, srcBase, srcStride, w, h)
	}
}

// PrepBilin fills the compound intermediate buffer using bilinear filtering.
func PrepBilin(tmp []int16,
	src []uint8, srcBase, srcStride int,
	w, h, mx, my int) {

	switch {
	case mx != 0 && my != 0:
		tmpStride := w
		mid := make([]int16, w*(h+1))

		rowBase := srcBase
		for row := 0; row <= h; row++ {
			for x := 0; x < w; x++ {
				v := int(src[rowBase+x])*(16-mx) + int(src[rowBase+x+1])*mx
				mid[row*tmpStride+x] = int16((v+8)>>4) << intermediateBits
			}
			rowBase += srcStride
		}

		tmpOff := 0
		for row := 0; row < h; row++ {
			for x := 0; x < w; x++ {
				v := int(mid[row*tmpStride+x])*(16-my) + int(mid[(row+1)*tmpStride+x])*my
				tmp[tmpOff+x] = int16((v + (1 << (3 + intermediateBits))) >> (4 + intermediateBits))
			}
			tmpOff += w
		}

	case mx != 0:
		rowBase := srcBase
		tmpOff := 0
		for row := 0; row < h; row++ {
			for x := 0; x < w; x++ {
				v := int(src[rowBase+x])*(16-mx) + int(src[rowBase+x+1])*mx
				tmp[tmpOff+x] = int16((v+8)>>4) << intermediateBits
			}
			rowBase += srcStride
			tmpOff += w
		}

	case my != 0:
		rowBase := srcBase
		tmpOff := 0
		for row := 0; row < h; row++ {
			for x := 0; x < w; x++ {
				v := int(src[rowBase+x])*(16-my) + int(src[rowBase+srcStride+x])*my
				tmp[tmpOff+x] = int16((v+8)>>4) << intermediateBits
			}
			rowBase += srcStride
			tmpOff += w
		}

	default:
		rowBase := srcBase
		tmpOff := 0
		for row := 0; row < h; row++ {
			for x := 0; x < w; x++ {
				tmp[tmpOff+x] = int16(src[rowBase+x]) << intermediateBits
			}
			rowBase += srcStride
			tmpOff += w
		}
	}
}

// Avg averages two compound intermediate buffers (tmp1, tmp2) into dst.
// Used for equal-weight compound inter prediction.
//
// 1:1 port of dav1d avg_c (8-bit).
func Avg(dst []uint8, dstStride int,
	tmp1, tmp2 []int16, w, h int) {

	dstOff := 0
	off := 0
	for row := 0; row < h; row++ {
		for x := 0; x < w; x++ {
			v := (int(tmp1[off+x]) + int(tmp2[off+x]) + (1 << intermediateBits)) >> (intermediateBits + 1)
			dst[dstOff+x] = clampPixel(v)
		}
		dstOff += dstStride
		off += w
	}
}

// WAvg blends two compound buffers: weight is the weight of tmp1 in [0,16].
// weight=8 gives equal blend; weight=16 gives only tmp1; weight=0 gives only tmp2.
//
// 1:1 port of dav1d w_avg_c (8-bit).
func WAvg(dst []uint8, dstStride int,
	tmp1, tmp2 []int16, w, h, weight int) {

	const rnd = (1 << intermediateBits) >> 1 // = 8
	dstOff := 0
	off := 0
	for row := 0; row < h; row++ {
		for x := 0; x < w; x++ {
			v := int(tmp1[off+x])*weight + int(tmp2[off+x])*(16-weight)
			dst[dstOff+x] = clampPixel((v + rnd*16) >> (intermediateBits + 4))
		}
		dstOff += dstStride
		off += w
	}
}
