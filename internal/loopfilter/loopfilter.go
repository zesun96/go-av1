// Package loopfilter implements the AV1 deblocking (loop) filter.
//
// Reference: dav1d/src/loopfilter_tmpl.c
package loopfilter

// FilterLUT holds the E and I threshold lookup tables for all 64 filter
// strength levels (indices 0..63).  E and I are indexed by the combined
// "L" value (lower 4 bits = H, upper 4 bits = E/I selector).
type FilterLUT struct {
	E [64]uint8
	I [64]uint8
}

// iclip clamps v to [lo, hi].
func iclip(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// iclipPixel clamps v to [0, 255] (8-bit path only).
func iclipPixel(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func iabs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// loopFilter applies the AV1 deblocking filter across a single edge, processing
// 4 parallel rows/columns.
//
// dst is the pixel slice. stridea is the step along the edge (perpendicular to
// the filter direction) and strideb is the step across the edge (filter
// direction). E, I, H are the filter threshold values (already shifted for
// 8-bit). wd selects the filter width (4, 6, 8, or 16).
func loopFilter(dst []uint8, dstBase int, E, I, H int, stridea, strideb int, wd int) {
	for i := 0; i < 4; i++ {
		p1 := int(dst[dstBase+strideb*-2])
		p0 := int(dst[dstBase+strideb*-1])
		q0 := int(dst[dstBase+strideb*0])
		q1 := int(dst[dstBase+strideb*1])

		var p2, p3, q2, q3 int

		// Basic filter mask.
		fm := iabs(p1-p0) <= I && iabs(q1-q0) <= I &&
			iabs(p0-q0)*2+(iabs(p1-q1)>>1) <= E

		if wd > 4 {
			p2 = int(dst[dstBase+strideb*-3])
			q2 = int(dst[dstBase+strideb*2])
			fm = fm && iabs(p2-p1) <= I && iabs(q2-q1) <= I

			if wd > 6 {
				p3 = int(dst[dstBase+strideb*-4])
				q3 = int(dst[dstBase+strideb*3])
				fm = fm && iabs(p3-p2) <= I && iabs(q3-q2) <= I
			}
		}

		if !fm {
			dstBase += stridea
			continue
		}

		var flat8out, flat8in bool
		var p4, p5, p6, q4, q5, q6 int

		if wd >= 16 {
			p6 = int(dst[dstBase+strideb*-7])
			p5 = int(dst[dstBase+strideb*-6])
			p4 = int(dst[dstBase+strideb*-5])
			q4 = int(dst[dstBase+strideb*4])
			q5 = int(dst[dstBase+strideb*5])
			q6 = int(dst[dstBase+strideb*6])

			flat8out = iabs(p6-p0) <= 1 && iabs(p5-p0) <= 1 &&
				iabs(p4-p0) <= 1 && iabs(q4-q0) <= 1 &&
				iabs(q5-q0) <= 1 && iabs(q6-q0) <= 1
		}

		if wd >= 6 {
			flat8in = iabs(p2-p0) <= 1 && iabs(p1-p0) <= 1 &&
				iabs(q1-q0) <= 1 && iabs(q2-q0) <= 1
		}
		if wd >= 8 {
			flat8in = flat8in && iabs(p3-p0) <= 1 && iabs(q3-q0) <= 1
		}

		if wd >= 16 && flat8out && flat8in {
			dst[dstBase+strideb*-6] = iclipPixel((p6 + p6 + p6 + p6 + p6 + p6*2 + p5*2 + p4*2 + p3 + p2 + p1 + p0 + q0 + 8) >> 4)
			dst[dstBase+strideb*-5] = iclipPixel((p6 + p6 + p6 + p6 + p6 + p5*2 + p4*2 + p3*2 + p2 + p1 + p0 + q0 + q1 + 8) >> 4)
			dst[dstBase+strideb*-4] = iclipPixel((p6 + p6 + p6 + p6 + p5 + p4*2 + p3*2 + p2*2 + p1 + p0 + q0 + q1 + q2 + 8) >> 4)
			dst[dstBase+strideb*-3] = iclipPixel((p6 + p6 + p6 + p5 + p4 + p3*2 + p2*2 + p1*2 + p0 + q0 + q1 + q2 + q3 + 8) >> 4)
			dst[dstBase+strideb*-2] = iclipPixel((p6 + p6 + p5 + p4 + p3 + p2*2 + p1*2 + p0*2 + q0 + q1 + q2 + q3 + q4 + 8) >> 4)
			dst[dstBase+strideb*-1] = iclipPixel((p6 + p5 + p4 + p3 + p2 + p1*2 + p0*2 + q0*2 + q1 + q2 + q3 + q4 + q5 + 8) >> 4)
			dst[dstBase+strideb*0] = iclipPixel((p5 + p4 + p3 + p2 + p1 + p0*2 + q0*2 + q1*2 + q2 + q3 + q4 + q5 + q6 + 8) >> 4)
			dst[dstBase+strideb*1] = iclipPixel((p4 + p3 + p2 + p1 + p0 + q0*2 + q1*2 + q2*2 + q3 + q4 + q5 + q6 + q6 + 8) >> 4)
			dst[dstBase+strideb*2] = iclipPixel((p3 + p2 + p1 + p0 + q0 + q1*2 + q2*2 + q3*2 + q4 + q5 + q6 + q6 + q6 + 8) >> 4)
			dst[dstBase+strideb*3] = iclipPixel((p2 + p1 + p0 + q0 + q1 + q2*2 + q3*2 + q4*2 + q5 + q6 + q6 + q6 + q6 + 8) >> 4)
			dst[dstBase+strideb*4] = iclipPixel((p1 + p0 + q0 + q1 + q2 + q3*2 + q4*2 + q5*2 + q6 + q6 + q6 + q6 + q6 + 8) >> 4)
			dst[dstBase+strideb*5] = iclipPixel((p0 + q0 + q1 + q2 + q3 + q4*2 + q5*2 + q6*2 + q6 + q6 + q6 + q6 + q6 + 8) >> 4)
		} else if wd >= 8 && flat8in {
			dst[dstBase+strideb*-3] = iclipPixel((p3 + p3 + p3 + 2*p2 + p1 + p0 + q0 + 4) >> 3)
			dst[dstBase+strideb*-2] = iclipPixel((p3 + p3 + p2 + 2*p1 + p0 + q0 + q1 + 4) >> 3)
			dst[dstBase+strideb*-1] = iclipPixel((p3 + p2 + p1 + 2*p0 + q0 + q1 + q2 + 4) >> 3)
			dst[dstBase+strideb*0] = iclipPixel((p2 + p1 + p0 + 2*q0 + q1 + q2 + q3 + 4) >> 3)
			dst[dstBase+strideb*1] = iclipPixel((p1 + p0 + q0 + 2*q1 + q2 + q3 + q3 + 4) >> 3)
			dst[dstBase+strideb*2] = iclipPixel((p0 + q0 + q1 + 2*q2 + q3 + q3 + q3 + 4) >> 3)
		} else if wd == 6 && flat8in {
			dst[dstBase+strideb*-2] = iclipPixel((p2 + 2*p2 + 2*p1 + 2*p0 + q0 + 4) >> 3)
			dst[dstBase+strideb*-1] = iclipPixel((p2 + 2*p1 + 2*p0 + 2*q0 + q1 + 4) >> 3)
			dst[dstBase+strideb*0] = iclipPixel((p1 + 2*p0 + 2*q0 + 2*q1 + q2 + 4) >> 3)
			dst[dstBase+strideb*1] = iclipPixel((p0 + 2*q0 + 2*q1 + 2*q2 + q2 + 4) >> 3)
		} else {
			// Narrow filter (wd=4 or flat8in=false).
			hev := iabs(p1-p0) > H || iabs(q1-q0) > H
			const limit = 128 // 128 << 0 for 8-bit (bitdepth_min_8=0)
			iclipDiff := func(v int) int { return iclip(v, -limit, limit-1) }

			if hev {
				f := iclipDiff(p1 - q1)
				f = iclipDiff(3*(q0-p0) + f)
				f1 := iclip(f+4, -limit, limit-1) >> 3
				f2 := iclip(f+3, -limit, limit-1) >> 3
				dst[dstBase+strideb*-1] = iclipPixel(p0 + f2)
				dst[dstBase+strideb*0] = iclipPixel(q0 - f1)
			} else {
				f := iclipDiff(3 * (q0 - p0))
				f1 := iclip(f+4, -limit, limit-1) >> 3
				f2 := iclip(f+3, -limit, limit-1) >> 3
				dst[dstBase+strideb*-1] = iclipPixel(p0 + f2)
				dst[dstBase+strideb*0] = iclipPixel(q0 - f1)
				f3 := (f1 + 1) >> 1
				dst[dstBase+strideb*-2] = iclipPixel(p1 + f3)
				dst[dstBase+strideb*1] = iclipPixel(q1 - f3)
			}
		}

		dstBase += stridea
	}
}

// LoopFilterH applies the deblocking filter on a horizontal edge (filtering
// vertically across the edge). The edge is at the top of dst.
//
// dst: pixel buffer at the top-left of the 128-wide super-block row starting
// at the edge.  stride is the row stride in bytes.  vmask[0..2] are the
// 32-bit column bitmasks for wd=4/8/16 (luma), or wd=4/6 (chroma, vmask[2]
// unused).  l is the [Nx4]uint8 filter-level array; b4Stride is its column
// stride.  lut is the E/I lookup table.  h is the height in 4-px units.
// isChroma selects the chroma path (max wd=6 instead of wd=16).
func LoopFilterH(dst []uint8, dstBase int, stride int,
	vmask [3]uint32,
	l []uint8, b4Stride int,
	lut *FilterLUT, h int, isChroma bool) {

	var vm uint32
	if isChroma {
		vm = vmask[0] | vmask[1]
	} else {
		vm = vmask[0] | vmask[1] | vmask[2]
	}

	base := dstBase
	lBase := 0
	for y := uint32(1); vm&^(y-1) != 0; y <<= 1 {
		if vm&y != 0 {
			// Pick level from current or previous row.
			lv := l[lBase]
			if lv == 0 {
				if lBase-b4Stride >= 0 {
					lv = l[lBase-b4Stride]
				}
			}
			if lv != 0 {
				H := int(lv >> 4)
				L := int(lv)
				E := int(lut.E[L&63])
				I := int(lut.I[L&63])
				var idx int
				if !isChroma {
					if vmask[2]&y != 0 {
						idx = 2
					} else if vmask[1]&y != 0 {
						idx = 1
					}
				} else {
					if vmask[1]&y != 0 {
						idx = 1
					}
				}
				wd := 4 << idx
				loopFilter(dst, base, E, I, H, stride, 1, wd)
			}
		}
		base += 4 * stride
		lBase += b4Stride
	}
}

// LoopFilterV applies the deblocking filter on a vertical edge (filtering
// horizontally across the edge). The edge is at the left of dst.
func LoopFilterV(dst []uint8, dstBase int, stride int,
	vmask [3]uint32,
	l []uint8, b4Stride int,
	lut *FilterLUT, w int, isChroma bool) {

	var vm uint32
	if isChroma {
		vm = vmask[0] | vmask[1]
	} else {
		vm = vmask[0] | vmask[1] | vmask[2]
	}

	base := dstBase
	lBase := 0
	for x := uint32(1); vm&^(x-1) != 0; x <<= 1 {
		if vm&x != 0 {
			lv := l[lBase]
			if lv == 0 {
				if lBase-1 >= 0 {
					lv = l[lBase-1]
				}
			}
			if lv != 0 {
				H := int(lv >> 4)
				L := int(lv)
				E := int(lut.E[L&63])
				I := int(lut.I[L&63])
				var idx int
				if !isChroma {
					if vmask[2]&x != 0 {
						idx = 2
					} else if vmask[1]&x != 0 {
						idx = 1
					}
				} else {
					if vmask[1]&x != 0 {
						idx = 1
					}
				}
				wd := 4 << idx
				loopFilter(dst, base, E, I, H, 1, stride, wd)
			}
		}
		base += 4
		lBase++
	}
}
