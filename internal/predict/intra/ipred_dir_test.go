package intra

import (
	"math/rand"
	"testing"
)

// ---- Helpers ---------------------------------------------------------------

// makeDirEdge builds a topleft buffer that exposes up to `bottomPad`
// extra samples below the left column and up to `rightPad` extra
// samples right of the top row. Returns the buffer and the index of
// the TL sample.
func makeDirEdge(width, height, bottomPad, rightPad int,
	tlVal uint8, genTop, genLeft func(i int) uint8) (buf []uint8, tl int) {
	leftN := height + bottomPad
	topN := width + rightPad
	buf = make([]uint8, leftN+1+topN)
	tl = leftN
	buf[tl] = tlVal
	for i := 0; i < topN; i++ {
		buf[tl+1+i] = genTop(i)
	}
	for i := 0; i < leftN; i++ {
		// left[i] lives at index tl-1-i (mirror).
		buf[tl-1-i] = genLeft(i)
	}
	return
}

func constU8(v uint8) func(int) uint8 { return func(int) uint8 { return v } }

// ---- getFilterStrength / getUpsample ---------------------------------------

func TestGetUpsample_TableSpot(t *testing.T) {
	cases := []struct {
		wh, angle, isSm, want int
	}{
		{8, 20, 0, 1},  // angle<40 && wh<=16 → upsample
		{16, 39, 0, 1}, // boundary in
		{16, 40, 0, 0}, // angle out
		{17, 20, 0, 0}, // wh too big
		{8, 20, 1, 1},  // isSm: wh<=16>>1=8 → upsample
		{16, 20, 1, 0}, // isSm shrinks limit
	}
	for _, c := range cases {
		if g := getUpsample(c.wh, c.angle, c.isSm); g != c.want {
			t.Errorf("getUpsample(%d,%d,%d) = %d, want %d", c.wh, c.angle, c.isSm, g, c.want)
		}
	}
}

func TestGetFilterStrength_TableMatrix(t *testing.T) {
	cases := []struct {
		wh, angle, isSm, want int
	}{
		// isSm=0
		{8, 56, 0, 1},
		{8, 55, 0, 0},
		{16, 40, 0, 1},
		{16, 39, 0, 0},
		{24, 32, 0, 3},
		{24, 16, 0, 2},
		{24, 8, 0, 1},
		{24, 7, 0, 0},
		{32, 32, 0, 3},
		{32, 4, 0, 2},
		{32, 3, 0, 1},
		{40, 0, 0, 3}, // wh>32 → 3
		// isSm=1
		{8, 64, 1, 2},
		{8, 40, 1, 1},
		{8, 39, 1, 0},
		{16, 48, 1, 2},
		{16, 20, 1, 1},
		{16, 19, 1, 0},
		{24, 4, 1, 3},
		{24, 3, 1, 0},
		{40, 0, 1, 3},
	}
	for _, c := range cases {
		if g := getFilterStrength(c.wh, c.angle, c.isSm); g != c.want {
			t.Errorf("getFilterStrength(%d,%d,%d) = %d, want %d",
				c.wh, c.angle, c.isSm, g, c.want)
		}
	}
}

// ---- filterEdge / upsampleEdge ---------------------------------------------

func TestFilterEdge_ConstantPreserved(t *testing.T) {
	in := make([]uint8, 32)
	for i := range in {
		in[i] = 123
	}
	out := make([]uint8, 16)
	// origin=0, from=0, to=16, lim covers entire range
	filterEdge(out, 16, 0, 16, in, 0, 0, 16, 2)
	for i, v := range out {
		if v != 123 {
			t.Fatalf("out[%d] = %d, want 123 (constant in → constant out)", i, v)
		}
	}
}

func TestFilterEdge_RampedAndClipped(t *testing.T) {
	// Strength-1 kernel {0,5,6,5,0}/16. Use a step input to verify
	// both the boundary clipping (lim regions) and the central filter.
	in := []uint8{0, 0, 0, 0, 0, 32, 32, 32, 32, 32, 32, 32, 32, 32, 32, 32}
	out := make([]uint8, 16)
	filterEdge(out, 16, 0, 16, in, 0, 0, 16, 1)
	// Middle of constant 32-region should stay 32.
	if out[10] != 32 {
		t.Fatalf("middle should remain 32, got %d", out[10])
	}
	// out[0..1] live in the clamped low region.
	if out[0] != 0 {
		t.Fatalf("out[0] should clamp to 0, got %d", out[0])
	}
}

func TestFilterEdge_LimWindowSkipsTail(t *testing.T) {
	in := make([]uint8, 32)
	for i := range in {
		in[i] = uint8(i * 4)
	}
	out := make([]uint8, 16)
	// lim 4..10 — values outside should be raw clipped reads.
	filterEdge(out, 16, 4, 10, in, 0, 0, 16, 3)
	// Outside-lim cells must equal raw clipped input.
	for _, i := range []int{0, 1, 2, 3, 10, 11, 12, 13, 14, 15} {
		want := in[clipInt(i, 0, 15)]
		if out[i] != want {
			t.Fatalf("out[%d]=%d (raw), want %d", i, out[i], want)
		}
	}
}

func TestFilterEdge_PanicsOnZeroStrength(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic with strength=0")
		}
	}()
	in := make([]uint8, 4)
	out := make([]uint8, 4)
	filterEdge(out, 4, 0, 4, in, 0, 0, 4, 0)
}

func TestFilterEdge_SmallSzClipsBothLoops(t *testing.T) {
	// sz < limFrom and sz < limTo: cover the `if sz < upper { upper = sz }`
	// short-circuits at both filterEdge loops.
	in := make([]uint8, 16)
	for i := range in {
		in[i] = uint8(i)
	}
	out := make([]uint8, 4)
	filterEdge(out, 2, 8, 12, in, 0, 0, 16, 2)
	for i := 0; i < 2; i++ {
		if out[i] != in[i] {
			t.Fatalf("out[%d]=%d, want %d (raw)", i, out[i], in[i])
		}
	}
}

func TestUpsampleEdge_ConstantInputProducesConstant(t *testing.T) {
	in := make([]uint8, 16)
	for i := range in {
		in[i] = 200
	}
	out := make([]uint8, 31)
	upsampleEdge(out, 16, in, 0, 0, 16)
	for i, v := range out {
		if v != 200 {
			// Filtered midpoints: 200*(-1+9+9-1)/16 + round = 200. Should hold.
			t.Fatalf("upsample[%d]=%d, want 200", i, v)
		}
	}
}

func TestUpsampleEdge_InterleavesOriginalSamples(t *testing.T) {
	in := []uint8{10, 30, 60, 100, 150, 200, 240, 250}
	out := make([]uint8, 2*8-1)
	upsampleEdge(out, 8, in, 0, 0, 8)
	for i := 0; i < 8; i++ {
		if out[i*2] != in[i] {
			t.Fatalf("out[%d]=%d, expected original in[%d]=%d", i*2, out[i*2], i, in[i])
		}
	}
}

// ---- PredZ1 ----------------------------------------------------------------

func TestPredZ1_ConstantEdge(t *testing.T) {
	const w, h = 4, 4
	buf, tl := makeDirEdge(w, h, 0, w+h, 77, constU8(77), constU8(77))
	dst := make([]uint8, w*h)
	for _, ang := range []int{15, 45, 85} {
		PredZ1(dst, w, buf, tl, w, h, ang)
		for i, v := range dst {
			if v != 77 {
				t.Fatalf("angle=%d dst[%d]=%d, want 77 (constant edge)", ang, i, v)
			}
		}
	}
}

func TestPredZ1_AllPaths(t *testing.T) {
	const w, h = 8, 8
	buf, tl := makeDirEdge(w, h, 0, w+h, 50,
		func(i int) uint8 { return uint8(50 + i) },
		constU8(0))
	dst := make([]uint8, w*h)
	// 1. Raw path: enable_edge_filter=0
	PredZ1(dst, w, buf, tl, w, h, 45)
	// 2. Filter-strength path: enable_edge_filter=1, isSm=0, wh=16, angle=40
	PredZ1(dst, w, buf, tl, w, h, 40|(1<<10))
	// 3. Upsample path: enable_edge_filter=1, angle<40, wh<=16
	PredZ1(dst, w, buf, tl, w, h, 20|(1<<10))
	// 4. Upsample path with isSm=1, wh<=8
	buf2, tl2 := makeDirEdge(4, 4, 0, 8, 50, constU8(50), constU8(0))
	dst2 := make([]uint8, 16)
	PredZ1(dst2, 4, buf2, tl2, 4, 4, 20|(1<<9)|(1<<10))
}

func TestPredZ1_PadAtMaxBaseX(t *testing.T) {
	// Use a very steep angle (close to 90°) so projection runs off the
	// edge and triggers the pad branch.
	const w, h = 4, 4
	buf, tl := makeDirEdge(w, h, 0, w+h, 99,
		func(i int) uint8 { return uint8(10 + i*5) }, constU8(0))
	dst := make([]uint8, w*h)
	PredZ1(dst, w, buf, tl, w, h, 3)
	// Raw path max_base_x = w+min(w,h)-1 = 7
	pad := buf[tl+1+(w+h-1)]
	found := false
	for _, v := range dst {
		if v == pad {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected at least one padded sample (=%d) in dst=%v", pad, dst)
	}
}

func TestPredZ1_PanicAngle(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for angle out of range")
		}
	}()
	buf, tl := makeDirEdge(4, 4, 0, 8, 0, constU8(0), constU8(0))
	dst := make([]uint8, 16)
	PredZ1(dst, 4, buf, tl, 4, 4, 0)
}

// ---- PredZ3 ----------------------------------------------------------------

func TestPredZ3_ConstantEdge(t *testing.T) {
	const w, h = 4, 4
	buf, tl := makeDirEdge(w, h, w+h, 0, 88, constU8(0), constU8(88))
	dst := make([]uint8, w*h)
	for _, ang := range []int{200, 225, 260} {
		PredZ3(dst, w, buf, tl, w, h, ang)
		for i, v := range dst {
			if v != 88 {
				t.Fatalf("angle=%d dst[%d]=%d, want 88", ang, i, v)
			}
		}
	}
}

func TestPredZ3_AllPaths(t *testing.T) {
	const w, h = 8, 8
	buf, tl := makeDirEdge(w, h, w+h, 0, 50,
		constU8(0),
		func(i int) uint8 { return uint8(50 + i) })
	dst := make([]uint8, w*h)
	// Raw / filter / upsample paths, each with valid angles.
	PredZ3(dst, w, buf, tl, w, h, 225)
	PredZ3(dst, w, buf, tl, w, h, 220|(1<<10)) // filter
	PredZ3(dst, w, buf, tl, w, h, 200|(1<<10)) // upsample (angle-180=20)
	buf2, tl2 := makeDirEdge(4, 4, 8, 0, 50, constU8(0), constU8(50))
	dst2 := make([]uint8, 16)
	PredZ3(dst2, 4, buf2, tl2, 4, 4, 200|(1<<9)|(1<<10)) // isSm + upsample
}

func TestPredZ3_PadAtMaxBaseY(t *testing.T) {
	const w, h = 4, 4
	buf, tl := makeDirEdge(w, h, w+h, 0, 99,
		constU8(0), func(i int) uint8 { return uint8(10 + i*5) })
	dst := make([]uint8, w*h)
	PredZ3(dst, w, buf, tl, w, h, 267)
	pad := buf[tl-1-(h+minInt(w, h)-1)]
	found := false
	for _, v := range dst {
		if v == pad {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected at least one padded sample (=%d), got dst=%v", pad, dst)
	}
}

func TestPredZ3_PanicAngle(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for angle out of range")
		}
	}()
	buf, tl := makeDirEdge(4, 4, 8, 0, 0, constU8(0), constU8(0))
	dst := make([]uint8, 16)
	PredZ3(dst, 4, buf, tl, 4, 4, 180)
}

// ---- PredZ2 ----------------------------------------------------------------

func TestPredZ2_ConstantEdge(t *testing.T) {
	const w, h = 4, 4
	buf, tl := makeDirEdge(w, h, h, w, 55, constU8(55), constU8(55))
	dst := make([]uint8, w*h)
	for _, ang := range []int{120, 135, 170} {
		PredZ2(dst, w, buf, tl, w, h, ang, w, h)
		for i, v := range dst {
			if v != 55 {
				t.Fatalf("angle=%d dst[%d]=%d, want 55", ang, i, v)
			}
		}
	}
}

func TestPredZ2_AllPaths(t *testing.T) {
	const w, h = 8, 8
	buf, tl := makeDirEdge(w, h, h, w, 50,
		func(i int) uint8 { return uint8(60 + i) },
		func(i int) uint8 { return uint8(60 + i) })
	dst := make([]uint8, w*h)
	// Raw path
	PredZ2(dst, w, buf, tl, w, h, 135, w, h)
	// Filter-strength path on both axes
	PredZ2(dst, w, buf, tl, w, h, 135|(1<<10), w, h)
	// Upsample-above only (angle-90 small): try angle=110 with wh<=16
	PredZ2(dst, w, buf, tl, w, h, 110|(1<<10), w, h)
	// Upsample-left only: 180-angle small → angle close to 180
	PredZ2(dst, w, buf, tl, w, h, 170|(1<<10), w, h)
	// isSm both sides
	buf2, tl2 := makeDirEdge(4, 4, 4, 4, 50,
		func(i int) uint8 { return uint8(60 + i) },
		func(i int) uint8 { return uint8(60 + i) })
	dst2 := make([]uint8, 16)
	PredZ2(dst2, 4, buf2, tl2, 4, 4, 110|(1<<9)|(1<<10), 4, 4)
	PredZ2(dst2, 4, buf2, tl2, 4, 4, 170|(1<<9)|(1<<10), 4, 4)
	// maxWidth/maxHeight smaller than w/h to exercise lim window
	PredZ2(dst2, 4, buf2, tl2, 4, 4, 135|(1<<10), 2, 2)
}

func TestPredZ2_PanicAngle(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for angle out of range")
		}
	}()
	buf, tl := makeDirEdge(4, 4, 4, 4, 0, constU8(0), constU8(0))
	dst := make([]uint8, 16)
	PredZ2(dst, 4, buf, tl, 4, 4, 90, 4, 4)
}

// ---- Cross-mode sanity -----------------------------------------------------

func TestPredZ_RandomNoCrashAllPaths(t *testing.T) {
	rng := rand.New(rand.NewSource(0xA51))
	for trial := 0; trial < 16; trial++ {
		w := 1 << uint(2+rng.Intn(2)) // 4 or 8
		h := w
		// z1
		buf, tl := makeDirEdge(w, h, h, w+h, uint8(rng.Intn(256)),
			func(int) uint8 { return uint8(rng.Intn(256)) },
			func(int) uint8 { return uint8(rng.Intn(256)) })
		dst := make([]uint8, w*h)
		for _, ang := range []int{15, 45, 80} {
			PredZ1(dst, w, buf, tl, w, h, ang|(1<<10))
		}
		for _, ang := range []int{200, 225, 260} {
			PredZ3(dst, w, buf, tl, w, h, ang|(1<<10))
		}
		for _, ang := range []int{105, 135, 170} {
			PredZ2(dst, w, buf, tl, w, h, ang|(1<<10), w, h)
		}
	}
}

// ---- Misc clip helpers ------------------------------------------------------

func TestClipPixel(t *testing.T) {
	cases := []struct{ in, want int }{{-5, 0}, {0, 0}, {123, 123}, {255, 255}, {300, 255}}
	for _, c := range cases {
		if g := int(clipPixel(c.in)); g != c.want {
			t.Errorf("clipPixel(%d)=%d, want %d", c.in, g, c.want)
		}
	}
}

func TestClipIntAndMinMax(t *testing.T) {
	if clipInt(5, 0, 3) != 3 {
		t.Fatal("clipInt high")
	}
	if clipInt(-1, 0, 3) != 0 {
		t.Fatal("clipInt low")
	}
	if clipInt(2, 0, 3) != 2 {
		t.Fatal("clipInt mid")
	}
	if minInt(1, 2) != 1 || minInt(2, 1) != 1 {
		t.Fatal("minInt")
	}
	if maxInt(1, 2) != 2 || maxInt(2, 1) != 2 {
		t.Fatal("maxInt")
	}
}
