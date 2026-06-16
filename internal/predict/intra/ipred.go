package intra

import "math/bits"

var filterIntraTaps = [5][8][7]int8{
	{
		{0, -6, 10, 0, 0, 0, 12},
		{-5, 2, 10, 0, 0, 9, 0},
		{-3, 1, 1, 10, 0, 7, 0},
		{-3, 1, 1, 2, 10, 5, 0},
		{-4, 6, 0, 0, 0, 2, 12},
		{-3, 2, 6, 0, 0, 2, 9},
		{-3, 2, 2, 6, 0, 2, 7},
		{-3, 1, 2, 2, 6, 3, 5},
	},
	{
		{-10, 16, 0, 0, 0, 10, 0},
		{-6, 0, 16, 0, 0, 6, 0},
		{-4, 0, 0, 16, 0, 4, 0},
		{-2, 0, 0, 0, 16, 2, 0},
		{-10, 16, 0, 0, 0, 0, 10},
		{-6, 0, 16, 0, 0, 0, 6},
		{-4, 0, 0, 16, 0, 0, 4},
		{-2, 0, 0, 0, 16, 0, 2},
	},
	{
		{-8, 8, 0, 0, 0, 16, 0},
		{-8, 0, 8, 0, 0, 16, 0},
		{-8, 0, 0, 8, 0, 16, 0},
		{-8, 0, 0, 0, 8, 16, 0},
		{-4, 4, 0, 0, 0, 0, 16},
		{-4, 0, 4, 0, 0, 0, 16},
		{-4, 0, 0, 4, 0, 0, 16},
		{-4, 0, 0, 0, 4, 0, 16},
	},
	{
		{-2, 8, 0, 0, 0, 10, 0},
		{-1, 3, 8, 0, 0, 6, 0},
		{-1, 2, 3, 8, 0, 4, 0},
		{0, 1, 2, 3, 8, 2, 0},
		{-1, 4, 0, 0, 0, 3, 10},
		{-1, 3, 4, 0, 0, 4, 6},
		{-1, 2, 3, 4, 0, 4, 4},
		{-1, 2, 2, 3, 4, 3, 3},
	},
	{
		{-12, 14, 0, 0, 0, 14, 0},
		{-10, 0, 14, 0, 0, 12, 0},
		{-9, 0, 0, 14, 0, 11, 0},
		{-8, 0, 0, 0, 14, 10, 0},
		{-10, 12, 0, 0, 0, 0, 14},
		{-9, 1, 12, 0, 0, 0, 12},
		{-8, 0, 0, 12, 0, 1, 11},
		{-7, 0, 0, 1, 12, 1, 9},
	},
}

// Topleft buffer layout (mirrors dav1d's `pixel *topleft`):
//
//   topleft[tl-1-i] = left sample i  (i = 0 .. height-1, top→bottom)
//   topleft[tl    ] = top-left sample
//   topleft[tl+1+i] = top sample i   (i = 0 .. width-1,  left→right)
//
// SMOOTH-family kernels additionally read topleft[tl-height]
// ("bottom") and topleft[tl+width] ("right"); callers must pre-fill
// those slots when issuing SMOOTH predictions, just like dav1d's
// ipred_prepare_tmpl.c does for the C reference.
//
// All kernels write `height` rows of `width` samples to `dst`, with
// the row pitch given by `stride`.

// splatDC fills a width×height block with a single sample value.
func splatDC(dst []uint8, stride, width, height, dc int) {
	row := dst
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			row[x] = uint8(dc)
		}
		row = row[stride:]
	}
}

// dcGenTop implements dav1d dc_gen_top(): average of the top row only.
func dcGenTop(topleft []uint8, tl, width int) int {
	dc := width >> 1
	for i := 0; i < width; i++ {
		dc += int(topleft[tl+1+i])
	}
	return dc >> bits.TrailingZeros(uint(width))
}

// dcGenLeft implements dav1d dc_gen_left(): average of the left col only.
func dcGenLeft(topleft []uint8, tl, height int) int {
	dc := height >> 1
	for i := 0; i < height; i++ {
		dc += int(topleft[tl-1-i])
	}
	return dc >> bits.TrailingZeros(uint(height))
}

// dcGen implements dav1d dc_gen() for arbitrary block sizes. For
// asymmetric blocks the simple shift is biased back toward the true
// mean using one of two fixed-point multipliers (8-bit profile).
func dcGen(topleft []uint8, tl, width, height int) int {
	const (
		multiplier1x2 = 0x5556
		multiplier1x4 = 0x3334
		baseShift     = 16
	)
	dc := (width + height) >> 1
	for i := 0; i < width; i++ {
		dc += int(topleft[tl+1+i])
	}
	for i := 0; i < height; i++ {
		dc += int(topleft[tl-1-i])
	}
	dc >>= bits.TrailingZeros(uint(width + height))
	if width != height {
		mult := multiplier1x2
		if width > height*2 || height > width*2 {
			mult = multiplier1x4
		}
		dc = (dc * mult) >> baseShift
	}
	return dc
}

// PredDC writes the DC mode prediction (average of both edges).
func PredDC(dst []uint8, stride int, topleft []uint8, tl, width, height int) {
	splatDC(dst, stride, width, height, dcGen(topleft, tl, width, height))
}

// PredDCTop writes the DC_TOP prediction (average of the top edge).
func PredDCTop(dst []uint8, stride int, topleft []uint8, tl, width, height int) {
	splatDC(dst, stride, width, height, dcGenTop(topleft, tl, width))
}

// PredDCLeft writes the DC_LEFT prediction (average of the left edge).
func PredDCLeft(dst []uint8, stride int, topleft []uint8, tl, width, height int) {
	splatDC(dst, stride, width, height, dcGenLeft(topleft, tl, height))
}

// PredDC128 writes the DC_128 prediction (constant 128, 8-bit profile).
func PredDC128(dst []uint8, stride int, width, height int) {
	splatDC(dst, stride, width, height, 128)
}

// PredV writes the V (vertical copy of top row).
func PredV(dst []uint8, stride int, topleft []uint8, tl, width, height int) {
	row := dst
	top := topleft[tl+1 : tl+1+width]
	for y := 0; y < height; y++ {
		copy(row[:width], top)
		row = row[stride:]
	}
}

// PredH writes the H (horizontal copy of left column).
func PredH(dst []uint8, stride int, topleft []uint8, tl, width, height int) {
	row := dst
	for y := 0; y < height; y++ {
		v := topleft[tl-1-y]
		for x := 0; x < width; x++ {
			row[x] = v
		}
		row = row[stride:]
	}
}

// PredFilter implements AV1 filter intra prediction for the five
// filter-intra variants signalled on DC-predicted luma blocks.
func PredFilter(dst []uint8, stride int, topleft []uint8, tl, width, height, mode int) {
	if mode < 0 || mode >= len(filterIntraTaps) {
		PredDC(dst, stride, topleft, tl, width, height)
		return
	}

	filter := filterIntraTaps[mode]
	for y := 0; y < height; y += 2 {
		topBase := tl + 1
		topSrc := topleft
		if y > 0 {
			topBase = (y - 1) * stride
			topSrc = dst
		}
		p0 := int(topleft[tl-y])
		leftBase := tl - y - 1
		leftFromDst := false
		for x := 0; x < width; x += 4 {
			p1 := int(topSrc[topBase+0])
			p2 := int(topSrc[topBase+1])
			p3 := int(topSrc[topBase+2])
			p4 := int(topSrc[topBase+3])
			p5, p6 := 0, 0
			if leftFromDst {
				p5 = int(dst[leftBase])
				p6 = int(dst[leftBase+stride])
			} else {
				p5 = int(topleft[leftBase])
				p6 = int(topleft[leftBase-1])
			}
			for yy := 0; yy < 2; yy++ {
				rowBase := (y+yy)*stride + x
				for xx := 0; xx < 4; xx++ {
					f := filter[yy*4+xx]
					acc := int(f[0])*p0 + int(f[1])*p1 + int(f[2])*p2 + int(f[3])*p3 +
						int(f[4])*p4 + int(f[5])*p5 + int(f[6])*p6
					dst[rowBase+xx] = clip8((acc + 8) >> 4)
				}
			}
			leftBase = y*stride + x + 3
			leftFromDst = true
			p0 = int(topSrc[topBase+3])
			topBase += 4
		}
	}
}

// PredPaeth writes the PAETH prediction.
//
// For each (x, y) the kernel picks whichever of left/top/topleft is
// closest (smallest absolute difference) to the planar predictor
// left + top - topleft, breaking ties in the dav1d order.
func PredPaeth(dst []uint8, stride int, topleft []uint8, tl, width, height int) {
	row := dst
	tlv := int(topleft[tl])
	for y := 0; y < height; y++ {
		left := int(topleft[tl-1-y])
		for x := 0; x < width; x++ {
			top := int(topleft[tl+1+x])
			base := left + top - tlv
			ldiff := absInt(left - base)
			tdiff := absInt(top - base)
			tldiff := absInt(tlv - base)
			switch {
			case ldiff <= tdiff && ldiff <= tldiff:
				row[x] = uint8(left)
			case tdiff <= tldiff:
				row[x] = uint8(top)
			default:
				row[x] = uint8(tlv)
			}
		}
		row = row[stride:]
	}
}

// PredSmooth writes the SMOOTH (bilinear) prediction. The caller must
// have pre-filled topleft[tl+width] ("right") and topleft[tl-height]
// ("bottom"); these are the corner extensions used as anchors for
// the bilinear blend.
func PredSmooth(dst []uint8, stride int, topleft []uint8, tl, width, height int) {
	row := dst
	wh := SmWeights[width:]
	wv := SmWeights[height:]
	right := int(topleft[tl+width])
	bottom := int(topleft[tl-height])
	for y := 0; y < height; y++ {
		wy := int(wv[y])
		left := int(topleft[tl-1-y])
		for x := 0; x < width; x++ {
			wx := int(wh[x])
			pred := wy*int(topleft[tl+1+x]) + (256-wy)*bottom +
				wx*left + (256-wx)*right
			row[x] = uint8((pred + 256) >> 9)
		}
		row = row[stride:]
	}
}

// PredSmoothV writes the SMOOTH_V (vertical-only bilinear) prediction.
// The caller must have pre-filled topleft[tl-height] ("bottom").
func PredSmoothV(dst []uint8, stride int, topleft []uint8, tl, width, height int) {
	row := dst
	wv := SmWeights[height:]
	bottom := int(topleft[tl-height])
	for y := 0; y < height; y++ {
		wy := int(wv[y])
		for x := 0; x < width; x++ {
			pred := wy*int(topleft[tl+1+x]) + (256-wy)*bottom
			row[x] = uint8((pred + 128) >> 8)
		}
		row = row[stride:]
	}
}

// PredSmoothH writes the SMOOTH_H (horizontal-only bilinear)
// prediction. The caller must have pre-filled topleft[tl+width]
// ("right").
func PredSmoothH(dst []uint8, stride int, topleft []uint8, tl, width, height int) {
	row := dst
	wh := SmWeights[width:]
	right := int(topleft[tl+width])
	for y := 0; y < height; y++ {
		left := int(topleft[tl-1-y])
		for x := 0; x < width; x++ {
			wx := int(wh[x])
			pred := wx*left + (256-wx)*right
			row[x] = uint8((pred + 128) >> 8)
		}
		row = row[stride:]
	}
}

// PredCFL writes a chroma-from-luma prediction over a DC base. `ac`
// is the AC residual buffer (zero-mean luma high-pass), laid out as
// height contiguous rows of `width` int16 samples. `alpha` is the
// signed CfL scale (range [-16, 16], with 0 meaning "DC-only").
func PredCFL(dst []uint8, stride int, ac []int16, width, height, dc, alpha int) {
	row := dst
	src := ac
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			diff := alpha * int(src[x])
			adj := (absInt(diff) + 32) >> 6
			if diff < 0 {
				adj = -adj
			}
			row[x] = clip8(dc + adj)
		}
		src = src[width:]
		row = row[stride:]
	}
}

// PredCFLTop runs PredCFL with the DC seeded from the top edge only.
func PredCFLTop(dst []uint8, stride int, topleft []uint8, tl int, ac []int16, width, height, alpha int) {
	PredCFL(dst, stride, ac, width, height, dcGenTop(topleft, tl, width), alpha)
}

// PredCFLLeft runs PredCFL with the DC seeded from the left edge only.
func PredCFLLeft(dst []uint8, stride int, topleft []uint8, tl int, ac []int16, width, height, alpha int) {
	PredCFL(dst, stride, ac, width, height, dcGenLeft(topleft, tl, height), alpha)
}

// PredCFLBoth runs PredCFL with the DC seeded from both edges.
func PredCFLBoth(dst []uint8, stride int, topleft []uint8, tl int, ac []int16, width, height, alpha int) {
	PredCFL(dst, stride, ac, width, height, dcGen(topleft, tl, width, height), alpha)
}

// PredCFL128 runs PredCFL with the DC fixed at the 8-bit midpoint.
func PredCFL128(dst []uint8, stride int, ac []int16, width, height, alpha int) {
	PredCFL(dst, stride, ac, width, height, 128, alpha)
}

// absInt is the obvious branchless |x| for ints; small enough to inline.
func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// clip8 clamps to [0, 255] (8-bit pixel range).
func clip8(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
