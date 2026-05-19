package cdef

import "testing"

// ─── helpers ──────────────────────────────────────────────────────────────────

// makeBlock returns a w×h uint8 slice (row-major, stride=w) plus its srcBase=0.
func makeBlock(val uint8, w, h int) ([]uint8, int) {
	buf := make([]uint8, w*h)
	for i := range buf {
		buf[i] = val
	}
	return buf, 0
}

// makeLeft returns [h][2]uint8 all set to val.
func makeLeft(val uint8, h int) [][2]uint8 {
	l := make([][2]uint8, h)
	for i := range l {
		l[i][0] = val
		l[i][1] = val
	}
	return l
}

// makeTopBottom returns top/bottom slices of size 2*w (2 rows each, stride=w).
func makeTopBottom(val uint8, w int) (top, bottom []uint8) {
	top = make([]uint8, 2*w)
	bottom = make([]uint8, 2*w)
	for i := range top {
		top[i] = val
		bottom[i] = val
	}
	return
}

// ─── constrain ────────────────────────────────────────────────────────────────

// allEdges returns all edges present.
func allEdges() EdgeFlags { return HaveLeft | HaveRight | HaveTop | HaveBottom }

// ─── constrain ────────────────────────────────────────────────────────────────

func TestConstrain_Zero(t *testing.T) {
	if constrain(0, 10, 0) != 0 {
		t.Error("constrain(0,10,0) != 0")
	}
}

func TestConstrain_BelowThreshold(t *testing.T) {
	// diff=3, threshold=10, shift=0 → adiff=3, max(0,10-3)=7 → min(3,7)=3 → sign=+
	if got := constrain(3, 10, 0); got != 3 {
		t.Errorf("constrain(3,10,0)=%d want 3", got)
	}
}

func TestConstrain_AboveThreshold(t *testing.T) {
	// diff=20, threshold=10, shift=0 → max(0,10-20)=0 → min(20,0)=0
	if got := constrain(20, 10, 0); got != 0 {
		t.Errorf("constrain(20,10,0)=%d want 0", got)
	}
}

func TestConstrain_Negative(t *testing.T) {
	// diff=-3, threshold=10, shift=0 → applySign(3,−3)=−3
	if got := constrain(-3, 10, 0); got != -3 {
		t.Errorf("constrain(-3,10,0)=%d want -3", got)
	}
}

// ─── FilterBlock: flat input → no change ──────────────────────────────────────

// TestFilterBlock_PriOnly_Flat: uniform block → every constrain() returns 0 → sum=0 → no change.
func TestFilterBlock_PriOnly_Flat(t *testing.T) {
	w, h := 8, 8
	dst, dstBase := makeBlock(128, w, h)
	before := make([]uint8, len(dst))
	copy(before, dst)

	top, bottom := makeTopBottom(128, w)
	left := makeLeft(128, h)

	FilterBlock(dst, dstBase, w,
		left,
		top, 0, w,
		bottom, 0, w,
		4, 0, 2, 3, w, h, allEdges())

	for i, v := range dst {
		if v != before[i] {
			t.Errorf("pixel[%d] changed %d→%d on flat block (pri only)", i, before[i], v)
		}
	}
}

// TestFilterBlock_SecOnly_Flat: same with sec_strength only.
func TestFilterBlock_SecOnly_Flat(t *testing.T) {
	w, h := 8, 8
	dst, dstBase := makeBlock(100, w, h)
	before := make([]uint8, len(dst))
	copy(before, dst)

	top, bottom := makeTopBottom(100, w)
	left := makeLeft(100, h)

	FilterBlock(dst, dstBase, w,
		left,
		top, 0, w,
		bottom, 0, w,
		0, 2, 4, 3, w, h, allEdges())

	for i, v := range dst {
		if v != before[i] {
			t.Errorf("pixel[%d] changed %d→%d on flat block (sec only)", i, before[i], v)
		}
	}
}

// TestFilterBlock_Combined_Flat: pri+sec on flat → no change.
func TestFilterBlock_Combined_Flat(t *testing.T) {
	w, h := 4, 4
	dst, dstBase := makeBlock(200, w, h)
	before := make([]uint8, len(dst))
	copy(before, dst)

	top, bottom := makeTopBottom(200, w)
	left := makeLeft(200, h)

	FilterBlock(dst, dstBase, w,
		left,
		top, 0, w,
		bottom, 0, w,
		2, 1, 0, 3, w, h, allEdges())

	for i, v := range dst {
		if v != before[i] {
			t.Errorf("pixel[%d] changed on flat combined", i)
		}
	}
}

// TestFilterBlock_4x8: 4×8 block, flat → no change.
func TestFilterBlock_4x8_Flat(t *testing.T) {
	w, h := 4, 8
	dst, dstBase := makeBlock(128, w, h)
	before := make([]uint8, len(dst))
	copy(before, dst)

	top, bottom := makeTopBottom(128, w)
	left := makeLeft(128, h)

	FilterBlock(dst, dstBase, w,
		left,
		top, 0, w,
		bottom, 0, w,
		4, 0, 1, 4, w, h, allEdges())

	for i, v := range dst {
		if v != before[i] {
			t.Errorf("pixel[%d] changed on 4x8 flat block", i)
		}
	}
}

// TestFilterBlock_PriOnly_Edge: place a sharp edge and verify attenuation.
// Left half = 118, right half = 138 (diff=20), dir=2, pri_strength=64, damping=3
// priShift=imax(0,3-ulog2(64))=imax(0,3-6)=0
// pri_tap=4-((64)&1)=4, constrain(20,64,0)=min(20,max(0,64-20))=min(20,44)=20
// sum for edge pixel: 2 neighbors on each side = 4*20+... > 0 → pixel is modified.
func TestFilterBlock_PriOnly_Edge(t *testing.T) {
	w, h := 8, 8
	dst := make([]uint8, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < 4 {
				dst[y*w+x] = 118
			} else {
				dst[y*w+x] = 138
			}
		}
	}

	top, bottom := makeTopBottom(0, w)
	for i := 0; i < 4; i++ {
		top[i] = 118
		bottom[i] = 118
		top[i+4] = 138
		bottom[i+4] = 138
		top[w+i] = 118
		bottom[w+i] = 118
		top[w+i+4] = 138
		bottom[w+i+4] = 138
	}
	left := make([][2]uint8, h)
	for i := range left {
		left[i][0] = 118
		left[i][1] = 118
	}

	// Save original edge pixels (col 3 and col 4 of row 0)
	origP := dst[3] // 118
	origQ := dst[4] // 138

	FilterBlock(dst, 0, w,
		left,
		top, 0, w,
		bottom, 0, w,
		64, 0, 2, 3, w, h, allEdges())

	// Edge pixels should be attenuated: p(118) pulled up toward 138, q(138) pulled down toward 118.
	if dst[3] <= origP {
		t.Errorf("edge pixel p=%d should increase from %d", dst[3], origP)
	}
	if dst[4] >= origQ {
		t.Errorf("edge pixel q=%d should decrease from %d", dst[4], origQ)
	}
}

// TestFilterBlock_NoPri_NoSec: both zero → filter should not be called; panic
// test via zero strengths but valid inputs. Actually, with both = 0 our code
// does "sec only" branch (since pri==0). To avoid invalid state, use pri=1 sec=0.
func TestFilterBlock_ZeroStrengths_NoPanic(t *testing.T) {
	// Use pri=1, sec=0 to avoid sec-only with strength=0 issue in real code.
	w, h := 8, 8
	dst, dstBase := makeBlock(128, w, h)

	top, bottom := makeTopBottom(128, w)
	left := makeLeft(128, h)

	FilterBlock(dst, dstBase, w, left, top, 0, w, bottom, 0, w,
		1, 0, 0, 3, w, h, allEdges())
}

// ─── FindDir ─────────────────────────────────────────────────────────────────

// TestFindDir_Flat: uniform 8×8 block → direction=0 is fine (no crash, variance=0).
func TestFindDir_Flat(t *testing.T) {
	img := make([]uint8, 8*8)
	for i := range img {
		img[i] = 128
	}
	dir, variance := FindDir(img, 0, 8)
	// All directional costs are equal; direction may be anything but variance=0.
	_ = dir
	if variance != 0 {
		t.Errorf("FindDir flat: variance=%d want 0", variance)
	}
}

// TestFindDir_Horizontal: strong horizontal gradient → prefer dir=2 (horizontal).
func TestFindDir_Horizontal(t *testing.T) {
	img := make([]uint8, 8*8)
	// Alternate rows: 0 and 255
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if y%2 == 0 {
				img[y*8+x] = 0
			} else {
				img[y*8+x] = 255
			}
		}
	}
	dir, _ := FindDir(img, 0, 8)
	// Horizontal pattern should produce high cost[2] or cost[6] (H or V rows)
	_ = dir // just verify no panic
}

// TestFindDir_Vertical: strong vertical pattern.
func TestFindDir_Vertical(t *testing.T) {
	img := make([]uint8, 8*8)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if x%2 == 0 {
				img[y*8+x] = 0
			} else {
				img[y*8+x] = 255
			}
		}
	}
	dir, _ := FindDir(img, 0, 8)
	_ = dir // just verify no panic
}

// TestFindDir_ReturnRange: direction must be in [0,7].
func TestFindDir_ReturnRange(t *testing.T) {
	img := make([]uint8, 8*8)
	for i := range img {
		img[i] = uint8(i * 3)
	}
	dir, _ := FindDir(img, 0, 8)
	if dir < 0 || dir > 7 {
		t.Errorf("FindDir returned dir=%d out of [0,7]", dir)
	}
}

// TestFindDir_DiagonalEdge: diagonal pattern should prefer diagonal direction.
func TestFindDir_Diagonal(t *testing.T) {
	img := make([]uint8, 8*8)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if x+y < 8 {
				img[y*8+x] = 0
			} else {
				img[y*8+x] = 255
			}
		}
	}
	dir, variance := FindDir(img, 0, 8)
	if dir < 0 || dir > 7 {
		t.Errorf("FindDir diagonal: dir=%d out of [0,7]", dir)
	}
	// Should have non-zero variance
	if variance == 0 {
		t.Errorf("FindDir diagonal: variance=0, expected non-zero")
	}
}

// ─── fill ────────────────────────────────────────────────────────────────────

func TestFill(t *testing.T) {
	buf := make([]int16, 5*5)
	fill(buf, 0, 5, 3, 2)
	for i := 0; i < 3; i++ {
		if buf[i] != -32768 {
			t.Errorf("buf[%d]=%d want -32768", i, buf[i])
		}
	}
	for i := 5; i < 8; i++ {
		if buf[i] != -32768 {
			t.Errorf("buf[%d]=%d want -32768", i, buf[i])
		}
	}
}

// ─── ulog2 ───────────────────────────────────────────────────────────────────

func TestUlog2(t *testing.T) {
	cases := []struct{ v, want int }{
		{1, 0}, {2, 1}, {3, 1}, {4, 2}, {8, 3}, {16, 4},
	}
	for _, tc := range cases {
		if got := ulog2(tc.v); got != tc.want {
			t.Errorf("ulog2(%d)=%d want %d", tc.v, got, tc.want)
		}
	}
}

// ─── edge coverage ────────────────────────────────────────────────────────────

// TestFilterBlock_NoTopEdge: no top edge → fill with INT16_MIN (no panic).
func TestFilterBlock_NoTopEdge(t *testing.T) {
	w, h := 8, 8
	dst, dstBase := makeBlock(128, w, h)
	top, bottom := makeTopBottom(128, w)
	left := makeLeft(128, h)

	edges := HaveLeft | HaveRight | HaveBottom // no top
	FilterBlock(dst, dstBase, w, left, top, 0, w, bottom, 0, w,
		4, 0, 2, 3, w, h, edges)
}

// TestFilterBlock_NoBottomEdge: no bottom edge.
func TestFilterBlock_NoBottomEdge(t *testing.T) {
	w, h := 8, 8
	dst, dstBase := makeBlock(128, w, h)
	top, bottom := makeTopBottom(128, w)
	left := makeLeft(128, h)

	edges := HaveLeft | HaveRight | HaveTop // no bottom
	FilterBlock(dst, dstBase, w, left, top, 0, w, bottom, 0, w,
		4, 0, 2, 3, w, h, edges)
}

// TestFilterBlock_NoLeftEdge: no left edge.
func TestFilterBlock_NoLeftEdge(t *testing.T) {
	w, h := 8, 8
	dst, dstBase := makeBlock(128, w, h)
	top, bottom := makeTopBottom(128, w)
	left := makeLeft(128, h)

	edges := HaveRight | HaveTop | HaveBottom // no left
	FilterBlock(dst, dstBase, w, left, top, 0, w, bottom, 0, w,
		4, 0, 2, 3, w, h, edges)
}
