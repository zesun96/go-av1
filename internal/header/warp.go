package header

import "math/bits"

var warpDivLUT = [257]uint16{
	16384, 16320, 16257, 16194, 16132, 16070, 16009, 15948, 15888, 15828, 15768, 15709,
	15650, 15592, 15534, 15477, 15420, 15364, 15308, 15252, 15197, 15142, 15087, 15033,
	14980, 14926, 14873, 14821, 14769, 14717, 14665, 14614, 14564, 14513, 14463, 14413,
	14364, 14315, 14266, 14218, 14170, 14122, 14075, 14028, 13981, 13935, 13888, 13843,
	13797, 13752, 13707, 13662, 13618, 13574, 13530, 13487, 13443, 13400, 13358, 13315,
	13273, 13231, 13190, 13148, 13107, 13066, 13026, 12985, 12945, 12906, 12866, 12827,
	12788, 12749, 12710, 12672, 12633, 12596, 12558, 12520, 12483, 12446, 12409, 12373,
	12336, 12300, 12264, 12228, 12193, 12157, 12122, 12087, 12053, 12018, 11984, 11950,
	11916, 11882, 11848, 11815, 11782, 11749, 11716, 11683, 11651, 11619, 11586, 11555,
	11523, 11491, 11460, 11429, 11398, 11367, 11336, 11305, 11275, 11245, 11215, 11185,
	11155, 11125, 11096, 11067, 11038, 11009, 10980, 10951, 10923, 10894, 10866, 10838,
	10810, 10782, 10755, 10727, 10700, 10673, 10645, 10618, 10592, 10565, 10538, 10512,
	10486, 10460, 10434, 10408, 10382, 10356, 10331, 10305, 10280, 10255, 10230, 10205,
	10180, 10156, 10131, 10107, 10082, 10058, 10034, 10010, 9986, 9963, 9939, 9916,
	9892, 9869, 9846, 9823, 9800, 9777, 9754, 9732, 9709, 9687, 9664, 9642,
	9620, 9598, 9576, 9554, 9533, 9511, 9489, 9468, 9447, 9425, 9404, 9383,
	9362, 9341, 9321, 9300, 9279, 9259, 9239, 9218, 9198, 9178, 9158, 9138,
	9118, 9098, 9079, 9059, 9039, 9020, 9001, 8981, 8962, 8943, 8924, 8905,
	8886, 8867, 8849, 8830, 8812, 8793, 8775, 8756, 8738, 8720, 8702, 8684,
	8666, 8648, 8630, 8613, 8595, 8577, 8560, 8542, 8525, 8508, 8490, 8473,
	8456, 8439, 8422, 8405, 8389, 8372, 8355, 8339, 8322, 8306, 8289, 8273,
	8257, 8240, 8224, 8208, 8192,
}

func clipWarpParam(v int64) int16 {
	if v < -32768 {
		v = -32768
	} else if v > 32767 {
		v = 32767
	}
	if v < 0 {
		return int16(-(((-v + 32) >> 6) << 6))
	}
	return int16(((v + 32) >> 6) << 6)
}

func signedRoundShift(v int64, shift int) int64 {
	if v < 0 {
		return -((-v + (int64(1) << (shift - 1))) >> shift)
	}
	return (v + (int64(1) << (shift - 1))) >> shift
}

// DeriveShear derives the quantized affine factors used by warped prediction.
// It returns false when the matrix violates AV1's warped-motion constraints.
func (w *WarpedMotionParams) DeriveShear() bool {
	mat := &w.Matrix
	if mat[2] <= 0 {
		return false
	}
	w.Alpha = clipWarpParam(int64(mat[2]) - 0x10000)
	w.Beta = clipWarpParam(int64(mat[3]))
	d := uint32(mat[2])
	log := bits.Len32(d) - 1
	e := int(d) - (1 << log)
	f := 0
	if log > 8 {
		f = (e + (1 << (log - 9))) >> (log - 8)
	} else {
		f = e << (8 - log)
	}
	shift := log + 14
	recip := int64(warpDivLUT[f])
	w.Gamma = clipWarpParam(signedRoundShift(int64(mat[4])*0x10000*recip, shift))
	w.Delta = clipWarpParam(int64(mat[5]) - signedRoundShift(int64(mat[3])*int64(mat[4])*recip, shift) - 0x10000)
	a, b, c, dlt := int(w.Alpha), int(w.Beta), int(w.Gamma), int(w.Delta)
	return 4*absWarp(a)+7*absWarp(b) < 0x10000 && 4*absWarp(c)+4*absWarp(dlt) < 0x10000
}

func absWarp(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// WarpPoint is one local warped-motion correspondence in 1/8-pel units.
type WarpPoint struct {
	InX, InY   int
	OutX, OutY int
}

// FindAffine fits AV1's integer affine model to projected neighbour samples.
func FindAffine(points []WarpPoint, bw4, bh4 int, mvX, mvY int16, bx4, by4 int) (WarpedMotionParams, bool) {
	var w WarpedMotionParams
	if len(points) == 0 {
		return w, false
	}
	var a00, a01, a11, bx0, bx1, by0, by1 int64
	rsuy, rsux := 2*bh4-1, 2*bw4-1
	suy, sux := rsuy*8, rsux*8
	duy, dux := suy+int(mvY), sux+int(mvX)
	for _, p := range points {
		dx, dy := p.OutX-dux, p.OutY-duy
		sx, sy := p.InX-sux, p.InY-suy
		if absWarp(sx-dx) >= 256 || absWarp(sy-dy) >= 256 {
			continue
		}
		a00 += int64((sx*sx)>>2 + sx*2 + 8)
		a01 += int64((sx*sy)>>2 + sx + sy + 4)
		a11 += int64((sy*sy)>>2 + sy*2 + 8)
		bx0 += int64((sx*dx)>>2 + sx + dx + 8)
		bx1 += int64((sy*dx)>>2 + sy + dx + 4)
		by0 += int64((sx*dy)>>2 + sx + dy + 4)
		by1 += int64((sy*dy)>>2 + sy + dy + 8)
	}
	det := a00*a11 - a01*a01
	if det == 0 {
		return w, false
	}
	adet := uint64(det)
	if det < 0 {
		adet = uint64(-det)
	}
	log := bits.Len64(adet) - 1
	e := adet - (uint64(1) << log)
	var f uint64
	if log > 8 {
		f = (e + (uint64(1) << (log - 9))) >> (log - 8)
	} else {
		f = e << (8 - log)
	}
	shift := log + 14 - 16
	idet := int64(warpDivLUT[f])
	if det < 0 {
		idet = -idet
	}
	if shift < 0 {
		idet <<= -shift
		shift = 0
	}
	roundMul := func(v int64) int64 { return signedRoundShift(v*idet, shift) }
	clip := func(v, lo, hi int64) int32 {
		if v < lo {
			v = lo
		} else if v > hi {
			v = hi
		}
		return int32(v)
	}
	w.Matrix[2] = clip(roundMul(a11*bx0-a01*bx1), 0xe001, 0x11fff)
	w.Matrix[3] = clip(roundMul(a00*bx1-a01*bx0), -0x1fff, 0x1fff)
	w.Matrix[4] = clip(roundMul(a11*by0-a01*by1), -0x1fff, 0x1fff)
	w.Matrix[5] = clip(roundMul(a00*by1-a01*by0), 0xe001, 0x11fff)
	isuy, isux := by4*4+rsuy, bx4*4+rsux
	w.Matrix[0] = clip(int64(mvX)*0x2000-int64(isux)*(int64(w.Matrix[2])-0x10000)-int64(isuy)*int64(w.Matrix[3]), -0x800000, 0x7fffff)
	w.Matrix[1] = clip(int64(mvY)*0x2000-int64(isux)*int64(w.Matrix[4])-int64(isuy)*(int64(w.Matrix[5])-0x10000), -0x800000, 0x7fffff)
	w.Type = WMTypeAffine
	if !w.DeriveShear() {
		return WarpedMotionParams{}, false
	}
	return w, true
}
