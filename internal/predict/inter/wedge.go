package inter

type wedgeCode struct {
	direction int
	xOffset   int
	yOffset   int
}

const (
	wedgeHorizontal = iota
	wedgeVertical
	wedgeOblique27
	wedgeOblique63
	wedgeOblique117
	wedgeOblique153
)

var wedgeCodebookHGTW = [16]wedgeCode{
	{2, 4, 4}, {3, 4, 4}, {4, 4, 4}, {5, 4, 4},
	{0, 4, 2}, {0, 4, 4}, {0, 4, 6}, {1, 4, 4},
	{2, 4, 2}, {2, 4, 6}, {5, 4, 2}, {5, 4, 6},
	{3, 2, 4}, {3, 6, 4}, {4, 2, 4}, {4, 6, 4},
}

var wedgeCodebookHLTW = [16]wedgeCode{
	{2, 4, 4}, {3, 4, 4}, {4, 4, 4}, {5, 4, 4},
	{1, 2, 4}, {1, 4, 4}, {1, 6, 4}, {0, 4, 4},
	{2, 4, 2}, {2, 4, 6}, {5, 4, 2}, {5, 4, 6},
	{3, 2, 4}, {3, 6, 4}, {4, 2, 4}, {4, 6, 4},
}

var wedgeCodebookHEQW = [16]wedgeCode{
	{2, 4, 4}, {3, 4, 4}, {4, 4, 4}, {5, 4, 4},
	{0, 4, 2}, {0, 4, 6}, {1, 2, 4}, {1, 6, 4},
	{2, 4, 2}, {2, 4, 6}, {5, 4, 2}, {5, 4, 6},
	{3, 2, 4}, {3, 6, 4}, {4, 2, 4}, {4, 6, 4},
}

var wedgeSigns = map[[2]int]uint16{
	{32, 32}: 0x7bfb, {32, 16}: 0x7beb, {32, 8}: 0x6beb,
	{16, 32}: 0x7beb, {16, 16}: 0x7bfb, {16, 8}: 0x7beb,
	{8, 32}: 0x7aeb, {8, 16}: 0x7beb, {8, 8}: 0x7bfb,
}

func insertWedgeBorder(dst []uint8, src [8]uint8, center int) {
	for i := range dst {
		switch {
		case i < center-4:
			dst[i] = 0
		case i >= center+4:
			dst[i] = 64
		default:
			dst[i] = src[i-(center-4)]
		}
	}
}

func wedgeMasters() [6][64 * 64]uint8 {
	var master [6][64 * 64]uint8
	vert := [8]uint8{0, 2, 7, 21, 43, 57, 62, 64}
	even := [8]uint8{1, 4, 11, 27, 46, 58, 62, 63}
	odd := [8]uint8{1, 2, 6, 18, 37, 53, 60, 63}
	for y := 0; y < 64; y++ {
		insertWedgeBorder(master[wedgeVertical][y*64:(y+1)*64], vert, 32)
		center := 48 - y/2
		border := even
		if y&1 != 0 {
			border = odd
			center--
		}
		insertWedgeBorder(master[wedgeOblique63][y*64:(y+1)*64], border, center)
	}
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			master[wedgeOblique27][x*64+y] = master[wedgeOblique63][y*64+x]
			master[wedgeHorizontal][x*64+y] = master[wedgeVertical][y*64+x]
		}
	}
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			master[wedgeOblique117][y*64+63-x] = master[wedgeOblique63][y*64+x]
			master[wedgeOblique153][y*64+63-x] = master[wedgeOblique27][y*64+x]
		}
	}
	return master
}

// WedgeMask returns the luma inter-intra wedge mask for an AV1 block.
func WedgeMask(w, h, index int) []uint8 {
	if index < 0 || index >= 16 {
		return nil
	}
	var codebook *[16]wedgeCode
	switch {
	case h > w:
		codebook = &wedgeCodebookHGTW
	case h < w:
		codebook = &wedgeCodebookHLTW
	default:
		codebook = &wedgeCodebookHEQW
	}
	signs, ok := wedgeSigns[[2]int{w, h}]
	if !ok {
		return nil
	}
	code := codebook[index]
	master := wedgeMasters()
	x0 := 32 - (w * code.xOffset >> 3)
	y0 := 32 - (h * code.yOffset >> 3)
	sign := (signs >> index) & 1
	mask := make([]uint8, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := master[code.direction][(y0+y)*64+x0+x]
			if sign != 0 {
				v = 64 - v
			}
			mask[y*w+x] = v
		}
	}
	return mask
}

var interIntraWeights = [32]uint8{
	60, 52, 45, 39, 34, 30, 26, 22, 19, 17, 15, 13, 11, 10, 8, 7,
	6, 6, 5, 4, 4, 3, 3, 2, 2, 2, 2, 1, 1, 1, 1, 1,
}

// InterIntraMask returns the non-wedge mask for DC, vertical, horizontal, or
// smooth inter-intra prediction modes.
func InterIntraMask(w, h, mode int) []uint8 {
	mask := make([]uint8, w*h)
	if mode == 0 {
		for i := range mask {
			mask[i] = 32
		}
		return mask
	}
	step := 1
	maxDim := w
	if h > maxDim {
		maxDim = h
	}
	if maxDim == 16 {
		step = 2
	} else if maxDim <= 4 {
		step = 8
	} else if maxDim <= 8 {
		step = 4
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			pos := y
			switch mode {
			case 2:
				pos = x
			case 3:
				if x < pos {
					pos = x
				}
			}
			if pos*step > 31 {
				pos = 31 / step
			}
			mask[y*w+x] = interIntraWeights[pos*step]
		}
	}
	return mask
}

// SubsampleMask averages a luma mask for chroma subsampling.
func SubsampleMask(mask []uint8, w, h, ssHor, ssVer int) ([]uint8, int, int) {
	cw, ch := (w+(1<<ssHor)-1)>>ssHor, (h+(1<<ssVer)-1)>>ssVer
	out := make([]uint8, cw*ch)
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			sum, count := 0, 0
			for yy := 0; yy < 1<<ssVer; yy++ {
				for xx := 0; xx < 1<<ssHor; xx++ {
					sx, sy := x<<ssHor|xx, y<<ssVer|yy
					if sx < w && sy < h {
						sum += int(mask[sy*w+sx])
						count++
					}
				}
			}
			out[y*cw+x] = uint8((sum + count/2) / count)
		}
	}
	return out, cw, ch
}

// BlendMask blends intra into the existing inter prediction using a Q6 mask.
func BlendMask(dst []uint8, stride int, intra, mask []uint8, w, h int) {
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m := int(mask[y*w+x])
			d := int(dst[y*stride+x])
			i := int(intra[y*w+x])
			dst[y*stride+x] = uint8((d*(64-m) + i*m + 32) >> 6)
		}
	}
}
