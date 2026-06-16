package intra

import (
	"math/rand"
	"testing"
)

// ---- Test helpers ----------------------------------------------------------

// makeEdge builds a topleft buffer that satisfies the SMOOTH layout:
// width+1 top samples (index tl..tl+width) and height+1 left samples
// (index tl-height..tl). The caller chooses values via genTop/genLeft;
// the TL sample lives at index tl.
func makeEdge(width, height int, tl uint8, genTop, genLeft func(i int) uint8) (buf []uint8, tlIdx int) {
	// Layout: [bottom-most left, ..., left[0], TL, top[0], ..., right]
	buf = make([]uint8, height+1+width+1)
	tlIdx = height + 1 // index of TL: 0..height are left+bottom, height = TL? Let's recompute.
	// buf[height-1-i] = left[i] for i in 0..height-1 (so left[i] at idx height-1-i)
	// buf[height] = bottom (topleft[tl-height])
	// We want topleft[tl-1-i] = left[i] and topleft[tl-height] = bottom.
	// Choose tl = height. Then tl-1-i = height-1-i, tl-height = 0.
	// Top: topleft[tl+x] for x in 1..width go to indices height+1..height+width.
	// Right: topleft[tl+width] at index height+width.
	tlIdx = height
	for i := 0; i < height; i++ {
		buf[tlIdx-1-i] = genLeft(i)
	}
	// "bottom" goes at index tl-height = 0 by default; caller can override
	// via a sentinel if it needs a distinct value. We seed it from left[h-1].
	buf[tlIdx-height] = genLeft(height - 1)
	buf[tlIdx] = tl
	for x := 0; x < width; x++ {
		buf[tlIdx+1+x] = genTop(x)
	}
	// "right" at index tl+width; seed from top[w-1] by default.
	buf[tlIdx+width] = genTop(width - 1)
	return buf, tlIdx
}

// blockEqual is a tiny helper that compares a width×height pixel block
// against the supplied flat expected slice (row-major, no padding).
func blockEqual(t *testing.T, got []uint8, stride int, want []uint8, width, height int, label string) {
	t.Helper()
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if got[y*stride+x] != want[y*width+x] {
				t.Fatalf("%s mismatch at (%d,%d): got %d want %d", label, x, y, got[y*stride+x], want[y*width+x])
			}
		}
	}
}

// allConst returns a (width*height) slice filled with value v.
func allConst(width, height int, v uint8) []uint8 {
	out := make([]uint8, width*height)
	for i := range out {
		out[i] = v
	}
	return out
}

// ---- DC family ------------------------------------------------------------

func TestPredDC_Constant(t *testing.T) {
	// All-c edges → DC = c for every block size.
	sizes := []struct{ w, h int }{{4, 4}, {4, 8}, {8, 4}, {8, 8}, {16, 16}, {4, 16}, {16, 4}, {32, 32}, {32, 8}}
	for _, s := range sizes {
		buf, tl := makeEdge(s.w, s.h, 77, func(int) uint8 { return 77 }, func(int) uint8 { return 77 })
		dst := make([]uint8, s.w*s.h)
		PredDC(dst, s.w, buf, tl, s.w, s.h)
		blockEqual(t, dst, s.w, allConst(s.w, s.h, 77), s.w, s.h, "DC")
	}
}

func TestPredDC_ManualSquare(t *testing.T) {
	// 4x4 with top=[10,20,30,40], left=[5,15,25,35].
	// dc_gen: (4+4)>>1 + 100 + 80 = 184; ctz(8)=3; 184>>3 = 23.
	top := []uint8{10, 20, 30, 40}
	left := []uint8{5, 15, 25, 35}
	buf, tl := makeEdge(4, 4, 0, func(i int) uint8 { return top[i] }, func(i int) uint8 { return left[i] })
	dst := make([]uint8, 16)
	PredDC(dst, 4, buf, tl, 4, 4)
	for i, v := range dst {
		if v != 23 {
			t.Fatalf("DC[%d]=%d want 23", i, v)
		}
	}
}

func TestPredDC_AsymmetricMultiplier(t *testing.T) {
	// 4x8: width!=height, width<=height*2 and height<=width*2 → multiplier=0x5556.
	// top=[10,10,10,10] sum=40; left=[20]*8 sum=160; (4+8)>>1=6; 6+40+160=206; ctz(12)=2 → 51.
	// 51 * 0x5556 = 51 * 21846 = 1114146; >> 16 = 17.
	buf, tl := makeEdge(4, 8, 0, func(int) uint8 { return 10 }, func(int) uint8 { return 20 })
	dst := make([]uint8, 4*8)
	PredDC(dst, 4, buf, tl, 4, 8)
	for i, v := range dst {
		if v != 17 {
			t.Fatalf("DC4x8[%d]=%d want 17", i, v)
		}
	}

	// 4x16: height > width*2 → multiplier=0x3334 (1x4 path).
	// top sum = 40; left sum = 320; (4+16)>>1 = 10; 10+40+320 = 370.
	// ctz(20) = 2 → 92. 92 * 0x3334 = 92 * 13108 = 1205936; >> 16 = 18.
	buf16, tl16 := makeEdge(4, 16, 0, func(int) uint8 { return 10 }, func(int) uint8 { return 20 })
	dst16 := make([]uint8, 4*16)
	PredDC(dst16, 4, buf16, tl16, 4, 16)
	for i, v := range dst16 {
		if v != 18 {
			t.Fatalf("DC4x16[%d]=%d want 18", i, v)
		}
	}
}

func TestPredDCTop_DCLeft(t *testing.T) {
	// DC_TOP: only top edge counts; DC_LEFT: only left edge counts.
	buf, tl := makeEdge(8, 8, 0,
		func(i int) uint8 { return uint8(i * 8) }, // 0..56 sum=224
		func(i int) uint8 { return 50 })           // sum=400
	dst := make([]uint8, 64)
	// DC_TOP: dc = 4 + 224 = 228; >> ctz(8)=3 → 28.
	PredDCTop(dst, 8, buf, tl, 8, 8)
	for _, v := range dst {
		if v != 28 {
			t.Fatalf("DC_TOP=%d want 28", v)
		}
	}
	// DC_LEFT: dc = 4 + 400 = 404; >> 3 → 50.
	PredDCLeft(dst, 8, buf, tl, 8, 8)
	for _, v := range dst {
		if v != 50 {
			t.Fatalf("DC_LEFT=%d want 50", v)
		}
	}
}

func TestPredDC128(t *testing.T) {
	dst := make([]uint8, 16*16)
	PredDC128(dst, 16, 16, 16)
	for i, v := range dst {
		if v != 128 {
			t.Fatalf("DC128[%d]=%d", i, v)
		}
	}
}

// ---- V / H ----------------------------------------------------------------

func TestPredV(t *testing.T) {
	top := []uint8{1, 2, 3, 4, 5, 6, 7, 8}
	buf, tl := makeEdge(8, 4, 0, func(i int) uint8 { return top[i] }, func(int) uint8 { return 99 })
	dst := make([]uint8, 8*4)
	PredV(dst, 8, buf, tl, 8, 4)
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			if dst[y*8+x] != top[x] {
				t.Fatalf("V[%d,%d]=%d want %d", x, y, dst[y*8+x], top[x])
			}
		}
	}
}

func TestPredH(t *testing.T) {
	left := []uint8{10, 20, 30, 40}
	buf, tl := makeEdge(8, 4, 0, func(int) uint8 { return 99 }, func(i int) uint8 { return left[i] })
	dst := make([]uint8, 8*4)
	PredH(dst, 8, buf, tl, 8, 4)
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			if dst[y*8+x] != left[y] {
				t.Fatalf("H[%d,%d]=%d want %d", x, y, dst[y*8+x], left[y])
			}
		}
	}
}

func TestPredFilter_ConstantEdge(t *testing.T) {
	for mode := 0; mode < 5; mode++ {
		buf, tl := makeEdge(8, 8, 73, func(int) uint8 { return 73 }, func(int) uint8 { return 73 })
		dst := make([]uint8, 8*8)
		PredFilter(dst, 8, buf, tl, 8, 8, mode)
		blockEqual(t, dst, 8, allConst(8, 8, 73), 8, 8, "FILTER")
	}
}

func TestPredFilter_Oracle4x4(t *testing.T) {
	buf, tl := makeEdge(4, 4, 23,
		func(i int) uint8 { return []uint8{31, 37, 41, 43}[i] },
		func(i int) uint8 { return []uint8{29, 19, 17, 13}[i] },
	)
	dst := make([]uint8, 4*4)
	PredFilter(dst, 4, buf, tl, 4, 4, 0)

	filter := filterIntraTaps[0]
	want := make([]uint8, 4*4)
	for y := 0; y < 4; y += 2 {
		topBase := tl + 1
		topSrc := buf
		if y > 0 {
			topBase = (y - 1) * 4
			topSrc = want
		}
		p0 := int(buf[tl-y])
		leftBase := tl - y - 1
		leftFromDst := false
		for x := 0; x < 4; x += 4 {
			p1 := int(topSrc[topBase+0])
			p2 := int(topSrc[topBase+1])
			p3 := int(topSrc[topBase+2])
			p4 := int(topSrc[topBase+3])
			p5, p6 := 0, 0
			if leftFromDst {
				p5 = int(want[leftBase])
				p6 = int(want[leftBase+4])
			} else {
				p5 = int(buf[leftBase])
				p6 = int(buf[leftBase-1])
			}
			for yy := 0; yy < 2; yy++ {
				for xx := 0; xx < 4; xx++ {
					f := filter[yy*4+xx]
					acc := int(f[0])*p0 + int(f[1])*p1 + int(f[2])*p2 + int(f[3])*p3 +
						int(f[4])*p4 + int(f[5])*p5 + int(f[6])*p6
					want[(y+yy)*4+x+xx] = clip8((acc + 8) >> 4)
				}
			}
			leftBase = y*4 + x + 3
			leftFromDst = true
			p0 = int(topSrc[topBase+3])
			topBase += 4
		}
	}
	blockEqual(t, dst, 4, want, 4, 4, "FILTER4x4")
}

// ---- PAETH ----------------------------------------------------------------

// paethRef is an independent oracle for the PAETH selection rule.
func paethRef(tl, top, left int) int {
	base := left + top - tl
	ldiff := absI(left - base)
	tdiff := absI(top - base)
	tldiff := absI(tl - base)
	switch {
	case ldiff <= tdiff && ldiff <= tldiff:
		return left
	case tdiff <= tldiff:
		return top
	default:
		return tl
	}
}

func absI(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func TestPredPaeth_Oracle(t *testing.T) {
	rng := rand.New(rand.NewSource(0xA1A1A1A1))
	for _, sz := range []struct{ w, h int }{{4, 4}, {8, 8}, {16, 8}, {8, 16}, {32, 32}} {
		buf, tl := makeEdge(sz.w, sz.h, uint8(rng.Intn(256)),
			func(int) uint8 { return uint8(rng.Intn(256)) },
			func(int) uint8 { return uint8(rng.Intn(256)) })
		dst := make([]uint8, sz.w*sz.h)
		PredPaeth(dst, sz.w, buf, tl, sz.w, sz.h)
		tlv := int(buf[tl])
		for y := 0; y < sz.h; y++ {
			for x := 0; x < sz.w; x++ {
				want := paethRef(tlv, int(buf[tl+1+x]), int(buf[tl-1-y]))
				if int(dst[y*sz.w+x]) != want {
					t.Fatalf("PAETH %dx%d at (%d,%d): got %d want %d",
						sz.w, sz.h, x, y, dst[y*sz.w+x], want)
				}
			}
		}
	}
}

func TestPredPaeth_Constant(t *testing.T) {
	// All-c edges: base = c+c-c = c → all diffs are 0; tie goes to left
	// (the first branch), still produces c.
	buf, tl := makeEdge(8, 8, 200, func(int) uint8 { return 200 }, func(int) uint8 { return 200 })
	dst := make([]uint8, 64)
	PredPaeth(dst, 8, buf, tl, 8, 8)
	for _, v := range dst {
		if v != 200 {
			t.Fatalf("paeth const = %d", v)
		}
	}
}

// ---- SMOOTH family --------------------------------------------------------

func TestPredSmooth_Constant(t *testing.T) {
	// All-c edges (including right=bottom=c) → blend = c.
	for _, sz := range []struct{ w, h int }{{4, 4}, {8, 8}, {16, 8}, {8, 16}, {32, 32}} {
		buf, tl := makeEdge(sz.w, sz.h, 123, func(int) uint8 { return 123 }, func(int) uint8 { return 123 })
		// Explicitly set bottom / right (they were seeded from edge values).
		buf[tl-sz.h] = 123
		buf[tl+sz.w] = 123
		dst := make([]uint8, sz.w*sz.h)
		PredSmooth(dst, sz.w, buf, tl, sz.w, sz.h)
		for _, v := range dst {
			if v != 123 {
				t.Fatalf("smooth const = %d", v)
			}
		}
	}
}

func TestPredSmoothV_H_Constant(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{4, 4}, {8, 8}, {16, 16}, {32, 16}} {
		buf, tl := makeEdge(sz.w, sz.h, 77, func(int) uint8 { return 77 }, func(int) uint8 { return 77 })
		buf[tl-sz.h] = 77
		buf[tl+sz.w] = 77
		dst := make([]uint8, sz.w*sz.h)
		PredSmoothV(dst, sz.w, buf, tl, sz.w, sz.h)
		for _, v := range dst {
			if v != 77 {
				t.Fatalf("smoothV const = %d", v)
			}
		}
		PredSmoothH(dst, sz.w, buf, tl, sz.w, sz.h)
		for _, v := range dst {
			if v != 77 {
				t.Fatalf("smoothH const = %d", v)
			}
		}
	}
}

func TestPredSmooth_OracleRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(0xBEEFCAFE))
	for _, sz := range []struct{ w, h int }{{4, 4}, {8, 4}, {4, 8}, {16, 8}, {32, 16}} {
		// Build the buffer manually so right/bottom get fresh random samples.
		buf := make([]uint8, sz.h+1+sz.w+1)
		tl := sz.h
		for i := 0; i < sz.h; i++ {
			buf[tl-1-i] = uint8(rng.Intn(256))
		}
		buf[tl-sz.h] = uint8(rng.Intn(256)) // bottom
		buf[tl] = uint8(rng.Intn(256))
		for x := 0; x < sz.w; x++ {
			buf[tl+1+x] = uint8(rng.Intn(256))
		}
		buf[tl+sz.w] = uint8(rng.Intn(256)) // right

		dst := make([]uint8, sz.w*sz.h)
		PredSmooth(dst, sz.w, buf, tl, sz.w, sz.h)
		right := int(buf[tl+sz.w])
		bottom := int(buf[tl-sz.h])
		wh := SmWeights[sz.w:]
		wv := SmWeights[sz.h:]
		for y := 0; y < sz.h; y++ {
			for x := 0; x < sz.w; x++ {
				wy := int(wv[y])
				wx := int(wh[x])
				pred := wy*int(buf[tl+1+x]) + (256-wy)*bottom +
					wx*int(buf[tl-1-y]) + (256-wx)*right
				want := uint8((pred + 256) >> 9)
				if dst[y*sz.w+x] != want {
					t.Fatalf("smooth %dx%d (%d,%d): got %d want %d",
						sz.w, sz.h, x, y, dst[y*sz.w+x], want)
				}
			}
		}

		PredSmoothV(dst, sz.w, buf, tl, sz.w, sz.h)
		for y := 0; y < sz.h; y++ {
			for x := 0; x < sz.w; x++ {
				wy := int(wv[y])
				pred := wy*int(buf[tl+1+x]) + (256-wy)*bottom
				want := uint8((pred + 128) >> 8)
				if dst[y*sz.w+x] != want {
					t.Fatalf("smoothV %dx%d (%d,%d): got %d want %d",
						sz.w, sz.h, x, y, dst[y*sz.w+x], want)
				}
			}
		}

		PredSmoothH(dst, sz.w, buf, tl, sz.w, sz.h)
		for y := 0; y < sz.h; y++ {
			for x := 0; x < sz.w; x++ {
				wx := int(wh[x])
				pred := wx*int(buf[tl-1-y]) + (256-wx)*right
				want := uint8((pred + 128) >> 8)
				if dst[y*sz.w+x] != want {
					t.Fatalf("smoothH %dx%d (%d,%d): got %d want %d",
						sz.w, sz.h, x, y, dst[y*sz.w+x], want)
				}
			}
		}
	}
}

// ---- CFL ------------------------------------------------------------------

func TestPredCFL_AlphaZero(t *testing.T) {
	// alpha=0 → all samples = clip8(dc) regardless of ac.
	ac := make([]int16, 4*4)
	for i := range ac {
		ac[i] = int16(i - 8) // negatives and positives
	}
	dst := make([]uint8, 4*4)
	PredCFL(dst, 4, ac, 4, 4, 200, 0)
	for _, v := range dst {
		if v != 200 {
			t.Fatalf("cfl alpha0 = %d", v)
		}
	}
}

func TestPredCFL_Symmetry(t *testing.T) {
	// Flipping the sign of alpha must flip the sign of the adjustment.
	ac := []int16{32, -32, 64, -64}
	a := make([]uint8, 4)
	b := make([]uint8, 4)
	PredCFL(a, 4, ac, 4, 1, 128, 4)
	PredCFL(b, 4, ac, 4, 1, 128, -4)
	for i := range a {
		// (a[i] - 128) should equal -(b[i] - 128).
		if int(a[i])-128 != -(int(b[i]) - 128) {
			t.Fatalf("cfl sym fail @%d a=%d b=%d", i, a[i], b[i])
		}
	}
}

func TestPredCFL_Clipping(t *testing.T) {
	// Force clamp at both ends.
	ac := []int16{2048, -2048}
	dst := make([]uint8, 2)
	PredCFL(dst, 2, ac, 2, 1, 128, 16)
	if dst[0] != 255 || dst[1] != 0 {
		t.Fatalf("cfl clip got %v want [255 0]", dst)
	}
}

func TestPredCFL_Variants(t *testing.T) {
	// Sanity-check that the *Top / *Left / *Both / *128 wrappers each
	// dispatch the matching DC generator. With identical edges all
	// variants must produce the same constant when alpha=0.
	buf, tl := makeEdge(8, 8, 64, func(int) uint8 { return 64 }, func(int) uint8 { return 64 })
	ac := make([]int16, 8*8)
	dst := make([]uint8, 8*8)
	for _, fn := range []func(){
		func() { PredCFLTop(dst, 8, buf, tl, ac, 8, 8, 0) },
		func() { PredCFLLeft(dst, 8, buf, tl, ac, 8, 8, 0) },
		func() { PredCFLBoth(dst, 8, buf, tl, ac, 8, 8, 0) },
	} {
		for i := range dst {
			dst[i] = 0
		}
		fn()
		for _, v := range dst {
			if v != 64 {
				t.Fatalf("cfl variant got %d", v)
			}
		}
	}
	PredCFL128(dst, 8, ac, 8, 8, 0)
	for _, v := range dst {
		if v != 128 {
			t.Fatalf("cfl 128 got %d", v)
		}
	}
}

// ---- SmWeights table integrity --------------------------------------------

func TestSmWeights_Anchors(t *testing.T) {
	// Spot-check a couple of well-known anchors from the AV1 spec.
	if SmWeights[2] != 255 || SmWeights[3] != 128 {
		t.Fatalf("bs=2 anchors wrong: %d %d", SmWeights[2], SmWeights[3])
	}
	if SmWeights[8] != 255 || SmWeights[15] != 32 {
		t.Fatalf("bs=8 anchors wrong: first=%d last=%d", SmWeights[8], SmWeights[15])
	}
	if SmWeights[64] != 255 || SmWeights[127] != 4 {
		t.Fatalf("bs=64 anchors wrong: first=%d last=%d", SmWeights[64], SmWeights[127])
	}
}
