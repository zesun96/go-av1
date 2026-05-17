package intra

// Directional intra prediction (8-bit, profile 0).
//
// This file is a faithful port of dav1d/src/ipred_tmpl.c, lines
// 327-599: get_filter_strength, get_upsample, filter_edge,
// upsample_edge, ipred_z1_c, ipred_z2_c and ipred_z3_c.
//
// The `angle` parameter follows dav1d's packed encoding:
//   bits 0..8  : raw angle (0..511)
//   bit  9     : is_sm (SMOOTH neighbour flag)
//   bit  10    : enable_intra_edge_filter
//
// PredZ1: angle ∈ (0, 90)
// PredZ2: angle ∈ (90, 180)
// PredZ3: angle ∈ (180, 270)

// getFilterStrength mirrors dav1d's get_filter_strength().
func getFilterStrength(wh, angle, isSm int) int {
	if isSm != 0 {
		switch {
		case wh <= 8:
			if angle >= 64 {
				return 2
			}
			if angle >= 40 {
				return 1
			}
		case wh <= 16:
			if angle >= 48 {
				return 2
			}
			if angle >= 20 {
				return 1
			}
		case wh <= 24:
			if angle >= 4 {
				return 3
			}
		default:
			return 3
		}
	} else {
		switch {
		case wh <= 8:
			if angle >= 56 {
				return 1
			}
		case wh <= 16:
			if angle >= 40 {
				return 1
			}
		case wh <= 24:
			if angle >= 32 {
				return 3
			}
			if angle >= 16 {
				return 2
			}
			if angle >= 8 {
				return 1
			}
		case wh <= 32:
			if angle >= 32 {
				return 3
			}
			if angle >= 4 {
				return 2
			}
			return 1
		default:
			return 3
		}
	}
	return 0
}

// getUpsample mirrors dav1d's get_upsample(): only short low-angle
// edges with SMOOTH neighbours disabled are upsampled.
func getUpsample(wh, angle, isSm int) int {
	if angle < 40 && wh <= (16>>uint(isSm)) {
		return 1
	}
	return 0
}

// edgeKernel3x5 holds the three filter-strength kernels referenced by
// filter_edge in the dav1d reference.
var edgeKernel3x5 = [3][5]int{
	{0, 4, 8, 4, 0},
	{0, 5, 6, 5, 0},
	{2, 4, 4, 4, 2},
}

// clipInt returns x clamped to [lo, hi].
func clipInt(x, lo, hi int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// clipPixel clamps a 32-bit sample to the unsigned 8-bit range.
func clipPixel(x int) uint8 {
	if x < 0 {
		return 0
	}
	if x > 255 {
		return 255
	}
	return uint8(x)
}

// readEdge returns in[clip(i, from, to-1)], matching dav1d's pattern.
// The caller supplies the relative origin in `in` so positive/negative
// indices both work via the slice base.
func readEdge(in []uint8, origin, i, from, to int) uint8 {
	return in[origin+clipInt(i, from, to-1)]
}

// filterEdge ports dav1d's filter_edge(). `in` is a slice whose origin
// (where index 0 of dav1d's pointer lives) is given by `inOrigin`.
// `from` and `to` are dav1d's inclusive/exclusive clip bounds relative
// to that origin. `lim_from`/`lim_to` define the filtered window.
func filterEdge(out []uint8, sz, limFrom, limTo int, in []uint8, inOrigin, from, to, strength int) {
	if strength <= 0 {
		panic("intra: filterEdge: strength must be > 0")
	}
	k := edgeKernel3x5[strength-1]
	i := 0
	upper := limFrom
	if sz < upper {
		upper = sz
	}
	for ; i < upper; i++ {
		out[i] = readEdge(in, inOrigin, i, from, to)
	}
	upper = limTo
	if sz < upper {
		upper = sz
	}
	for ; i < upper; i++ {
		s := 0
		for j := 0; j < 5; j++ {
			s += int(readEdge(in, inOrigin, i-2+j, from, to)) * k[j]
		}
		out[i] = uint8((s + 8) >> 4)
	}
	for ; i < sz; i++ {
		out[i] = readEdge(in, inOrigin, i, from, to)
	}
}

// upsampleEdge ports dav1d's upsample_edge(): writes 2*hsz-1 samples
// to `out`, interleaving originals and 4-tap [-1,9,9,-1]/16 filtered
// midpoints. The final original sample lands at out[2*(hsz-1)].
func upsampleEdge(out []uint8, hsz int, in []uint8, inOrigin, from, to int) {
	kernel := [4]int{-1, 9, 9, -1}
	var i int
	for i = 0; i < hsz-1; i++ {
		out[i*2] = readEdge(in, inOrigin, i, from, to)
		s := 0
		for j := 0; j < 4; j++ {
			s += int(readEdge(in, inOrigin, i+j-1, from, to)) * kernel[j]
		}
		out[i*2+1] = clipPixel((s + 8) >> 4)
	}
	out[i*2] = readEdge(in, inOrigin, i, from, to)
}

// minInt / maxInt: small helpers used by the kernels below.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// PredZ1 implements AV1 directional intra prediction for angles in
// (0, 90). `tl` is the topleft buffer in dav1d layout (tl[c] is TL,
// tl[c+1+i] is top sample i, tl[c-1-i] is left sample i). `c` gives
// the index of the TL sample; the kernel reads top via &tl[c+1].
//
// `angle` is the packed dav1d-style value (raw | is_sm<<9 |
// enableEdgeFilter<<10).
func PredZ1(dst []uint8, stride int, tl []uint8, c, width, height, angle int) {
	isSm := (angle >> 9) & 1
	enableEdgeFilter := (angle >> 10) & 1
	angle &= 511
	if angle <= 0 || angle >= 90 {
		panic("intra: PredZ1: angle must be in (0, 90)")
	}
	dx := int(DrIntraDerivative[angle>>1])

	var topOut [128]uint8
	var top []uint8
	var topBase int
	var maxBaseX int
	upsampleAbove := 0
	if enableEdgeFilter != 0 {
		upsampleAbove = getUpsample(width+height, 90-angle, isSm)
	}
	if upsampleAbove != 0 {
		// dav1d: upsample_edge(top_out, w+h, &topleft_in[1], -1, w+min(w,h))
		upsampleEdge(topOut[:], width+height, tl, c+1, -1, width+minInt(width, height))
		top = topOut[:]
		topBase = 0
		maxBaseX = 2*(width+height) - 2
		dx <<= 1
	} else {
		filterStrength := 0
		if enableEdgeFilter != 0 {
			filterStrength = getFilterStrength(width+height, 90-angle, isSm)
		}
		if filterStrength != 0 {
			filterEdge(topOut[:], width+height, 0, width+height, tl, c+1, -1, width+minInt(width, height), filterStrength)
			top = topOut[:]
			topBase = 0
			maxBaseX = width + height - 1
		} else {
			top = tl
			topBase = c + 1
			maxBaseX = width + minInt(width, height) - 1
		}
	}

	baseInc := 1 + upsampleAbove
	xpos := dx
	for y := 0; y < height; y++ {
		frac := xpos & 0x3E
		base := xpos >> 6
		row := dst[y*stride:]
		x := 0
		for ; x < width; x++ {
			if base < maxBaseX {
				v := int(top[topBase+base])*(64-frac) + int(top[topBase+base+1])*frac
				row[x] = uint8((v + 32) >> 6)
				base += baseInc
			} else {
				// dav1d: pixel_set(&dst[x], top[max_base_x], width - x)
				pad := top[topBase+maxBaseX]
				for ; x < width; x++ {
					row[x] = pad
				}
				break
			}
		}
		xpos += dx
	}
}

// PredZ3 implements AV1 directional intra prediction for angles in
// (180, 270). It reads from the left edge of `tl`. Output is written
// column-by-column.
func PredZ3(dst []uint8, stride int, tl []uint8, c, width, height, angle int) {
	isSm := (angle >> 9) & 1
	enableEdgeFilter := (angle >> 10) & 1
	angle &= 511
	if angle <= 180 || angle >= 270 {
		panic("intra: PredZ3: angle must be in (180, 270)")
	}
	dy := int(DrIntraDerivative[(270-angle)>>1])

	var leftOut [128]uint8
	// `left` is referenced by negative offsets in dav1d (`left[-base]`).
	// We mirror this by tracking a base index into the chosen buffer
	// such that `left[-i]` corresponds to `buf[leftBase-i]`.
	var leftBuf []uint8
	var leftBase int
	var maxBaseY int
	upsampleLeft := 0
	if enableEdgeFilter != 0 {
		upsampleLeft = getUpsample(width+height, angle-180, isSm)
	}
	if upsampleLeft != 0 {
		// dav1d: upsample_edge(left_out, w+h, &topleft_in[-(w+h)],
		//                      max(w-h,0), w+h+1)
		upsampleEdge(leftOut[:], width+height, tl, c-(width+height),
			maxInt(width-height, 0), width+height+1)
		leftBuf = leftOut[:]
		leftBase = 2*(width+height) - 2
		maxBaseY = 2*(width+height) - 2
		dy <<= 1
	} else {
		filterStrength := 0
		if enableEdgeFilter != 0 {
			filterStrength = getFilterStrength(width+height, angle-180, isSm)
		}
		if filterStrength != 0 {
			filterEdge(leftOut[:], width+height, 0, width+height,
				tl, c-(width+height), maxInt(width-height, 0), width+height+1,
				filterStrength)
			leftBuf = leftOut[:]
			leftBase = width + height - 1
			maxBaseY = width + height - 1
		} else {
			// dav1d: left = &topleft_in[-1]; index by left[-base] →
			// tl[c-1-base]. So leftBuf=tl, leftBase=c-1.
			leftBuf = tl
			leftBase = c - 1
			maxBaseY = height + minInt(width, height) - 1
		}
	}

	baseInc := 1 + upsampleLeft
	ypos := dy
	for x := 0; x < width; x++ {
		frac := ypos & 0x3E
		base := ypos >> 6
		y := 0
		for ; y < height; y++ {
			if base < maxBaseY {
				v := int(leftBuf[leftBase-base])*(64-frac) +
					int(leftBuf[leftBase-(base+1)])*frac
				dst[y*stride+x] = uint8((v + 32) >> 6)
				base += baseInc
			} else {
				pad := leftBuf[leftBase-maxBaseY]
				for ; y < height; y++ {
					dst[y*stride+x] = pad
				}
				break
			}
		}
		ypos += dy
	}
}

// PredZ2 implements AV1 directional intra prediction for angles in
// (90, 180), where the kernel projects each sample to either the top
// or left edge depending on which lies "in front" of the projection
// direction. `maxWidth`/`maxHeight` come from the spec's edge-filter
// clipping rules (see dav1d ipred_prepare_tmpl.c) and bound the
// number of filtered taps; pass width/height when there is no
// neighbour restriction.
func PredZ2(dst []uint8, stride int, tl []uint8, c, width, height, angle,
	maxWidth, maxHeight int) {
	isSm := (angle >> 9) & 1
	enableEdgeFilter := (angle >> 10) & 1
	angle &= 511
	if angle <= 90 || angle >= 180 {
		panic("intra: PredZ2: angle must be in (90, 180)")
	}
	dy := int(DrIntraDerivative[(angle-90)>>1])
	dx := int(DrIntraDerivative[(180-angle)>>1])
	upsampleLeft := 0
	upsampleAbove := 0
	if enableEdgeFilter != 0 {
		upsampleLeft = getUpsample(width+height, 180-angle, isSm)
		upsampleAbove = getUpsample(width+height, angle-90, isSm)
	}
	// dav1d: pixel edge[64 + 64 + 1]; pixel *const topleft = &edge[64];
	// We mirror this with a 129-byte buffer plus an origin index. The
	// origin sits at offset 64 so the kernel can address topleft[-h*2
	// .. width] safely with up to 64 samples in either direction.
	var edge [129]uint8
	const tlOrig = 64

	if upsampleAbove != 0 {
		// upsample_edge(topleft, width+1, topleft_in, 0, width+1)
		upsampleEdge(edge[tlOrig:], width+1, tl, c, 0, width+1)
		dx <<= 1
	} else {
		filterStrength := 0
		if enableEdgeFilter != 0 {
			filterStrength = getFilterStrength(width+height, angle-90, isSm)
		}
		if filterStrength != 0 {
			filterEdge(edge[tlOrig+1:], width, 0, maxWidth,
				tl, c+1, -1, width, filterStrength)
		} else {
			copy(edge[tlOrig+1:tlOrig+1+width], tl[c+1:c+1+width])
		}
	}
	if upsampleLeft != 0 {
		// upsample_edge(&topleft[-height*2], height+1, &topleft_in[-height], 0, height+1)
		upsampleEdge(edge[tlOrig-height*2:], height+1, tl, c-height, 0, height+1)
		dy <<= 1
	} else {
		filterStrength := 0
		if enableEdgeFilter != 0 {
			filterStrength = getFilterStrength(width+height, 180-angle, isSm)
		}
		if filterStrength != 0 {
			filterEdge(edge[tlOrig-height:], height, height-maxHeight, height,
				tl, c-height, 0, height+1, filterStrength)
		} else {
			copy(edge[tlOrig-height:tlOrig], tl[c-height:c])
		}
	}
	edge[tlOrig] = tl[c]

	baseIncX := 1 + upsampleAbove
	// left origin: &topleft[-(1 + upsample_left)] → leftBase = tlOrig - (1 + upsample_left)
	leftBase := tlOrig - (1 + upsampleLeft)

	xposStart := ((1 + upsampleAbove) << 6) - dx
	for y := 0; y < height; y++ {
		baseX := xposStart >> 6
		fracX := xposStart & 0x3E
		ypos := (y << uint(6+upsampleLeft)) - dy
		row := dst[y*stride:]
		for x := 0; x < width; x++ {
			var v int
			if baseX >= 0 {
				v = int(edge[tlOrig+baseX])*(64-fracX) +
					int(edge[tlOrig+baseX+1])*fracX
			} else {
				baseY := ypos >> 6
				fracY := ypos & 0x3E
				v = int(edge[leftBase-baseY])*(64-fracY) +
					int(edge[leftBase-(baseY+1)])*fracY
			}
			row[x] = uint8((v + 32) >> 6)
			baseX += baseIncX
			ypos -= dy
		}
		xposStart -= dx
	}
}
