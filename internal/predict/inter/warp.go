package inter

// PutWarpAffine predicts a block using AV1 affine warped motion. Matrix uses
// 16.16 fixed point in luma coordinates; ssHor/ssVer select plane subsampling.
func PutWarpAffine(dst []uint8, dstStride int, src []uint8, srcStride, srcW, srcH int,
	bx, by, bw, bh, ssHor, ssVer int, matrix [6]int32, abcd [4]int16) {
	for y := 0; y < bh; y += 8 {
		srcY := (by << ssVer) + ((y + 4) << ssVer)
		mat3Y := int64(matrix[3])*int64(srcY) + int64(matrix[0])
		mat5Y := int64(matrix[5])*int64(srcY) + int64(matrix[1])
		for x := 0; x < bw; x += 8 {
			srcX := (bx << ssHor) + ((x + 4) << ssHor)
			mvx := (int64(matrix[2])*int64(srcX) + mat3Y) >> ssHor
			mvy := (int64(matrix[4])*int64(srcX) + mat5Y) >> ssVer
			dx := int(mvx>>16) - 4
			dy := int(mvy>>16) - 4
			mx := ((int(mvx) & 0xffff) - int(abcd[0])*4 - int(abcd[1])*7) &^ 0x3f
			my := ((int(mvy) & 0xffff) - int(abcd[2])*4 - int(abcd[3])*4) &^ 0x3f
			putWarp8x8(dst[y*dstStride+x:], dstStride, src, srcStride, srcW, srcH,
				dx, dy, minWarp(8, bw-x), minWarp(8, bh-y), abcd, mx, my)
		}
	}
}

func putWarp8x8(dst []uint8, dstStride int, src []uint8, srcStride, srcW, srcH,
	dx, dy, outW, outH int, abcd [4]int16, mx, my int) {
	var mid [15 * 8]int16
	for y := 0; y < 15; y++ {
		tmx := mx
		for x := 0; x < 8; x++ {
			filter := WarpFilter[warpPhase(tmx)]
			sum := 0
			for k := 0; k < 8; k++ {
				sx := clampWarp(dx+x-3+k, 0, srcW-1)
				sy := clampWarp(dy-3+y, 0, srcH-1)
				sum += int(filter[k]) * int(src[sy*srcStride+sx])
			}
			mid[y*8+x] = int16((sum + 4) >> 3)
			tmx += int(abcd[0])
		}
		mx += int(abcd[1])
	}
	for y := 0; y < outH; y++ {
		tmy := my
		for x := 0; x < outW; x++ {
			filter := WarpFilter[warpPhase(tmy)]
			sum := 0
			for k := 0; k < 8; k++ {
				sum += int(filter[k]) * int(mid[(y+k)*8+x])
			}
			dst[y*dstStride+x] = clampPixel((sum + 1024) >> 11)
			tmy += int(abcd[2])
		}
		my += int(abcd[3])
	}
}

func warpPhase(v int) int { return clampWarp(64+((v+512)>>10), 0, len(WarpFilter)-1) }
func clampWarp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
func minWarp(a, b int) int {
	if a < b {
		return a
	}
	return b
}
