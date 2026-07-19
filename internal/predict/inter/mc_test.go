package inter

import (
	"slices"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helper utilities
// ─────────────────────────────────────────────────────────────────────────────

// makeSrc builds a w×h flat pixel buffer (all pixels = val) with:
//   - 4-row top/bottom padding
//   - 8-pixel left/right padding
//
// Returns the full buffer plus the srcBase index of (row=0, col=0).
// This guarantees the 3-pixel / 3-row padding required by 8-tap filters.
func makeSrc(val uint8, w, h int) (buf []uint8, stride, srcBase int) {
	hpad := 8 // ≥ 4 for V-tap overlap
	lpad := 8 // ≥ 3 for H-tap overlap + 1 extra
	stride = w + 2*lpad
	rows := h + 2*hpad
	buf = make([]uint8, stride*rows)
	for i := range buf {
		buf[i] = val
	}
	srcBase = hpad*stride + lpad
	return
}

// makeRampSrc builds a ramp buffer where pixel[row][col] = uint8(((hpad+row)*stride+(lpad+col))%200+30).
func makeRampSrc(w, h int) (buf []uint8, stride, srcBase int) {
	hpad, lpad := 8, 8
	stride = w + 2*lpad
	rows := h + 2*hpad
	buf = make([]uint8, stride*rows)
	for i := range buf {
		buf[i] = uint8(i%200 + 30)
	}
	srcBase = hpad*stride + lpad
	return
}

// newDst returns a zeroed dst slice; dst[0] is the top-left of the block.
func newDst(w, h, stride int) []uint8 {
	return make([]uint8, stride*(h-1)+w)
}

// ─────────────────────────────────────────────────────────────────────────────
// Filter table tests
// ─────────────────────────────────────────────────────────────────────────────

func TestMcSubpelFilters_Shape(t *testing.T) {
	if int(NFilterTypes) != 6 {
		t.Fatalf("NFilterTypes = %d, want 6", NFilterTypes)
	}
	for ft := FilterType(0); ft < NFilterTypes; ft++ {
		for phase := 0; phase < 15; phase++ {
			sum := 0
			for tap := 0; tap < 8; tap++ {
				sum += int(McSubpelFilters[ft][phase][tap])
			}
			if sum != 64 {
				t.Errorf("McSubpelFilters[%d][%d] tap-sum = %d, want 64", ft, phase, sum)
			}
		}
	}
}

func TestGetFilters_IntegerPosition(t *testing.T) {
	fh, fv := GetFilters(Filter2D8TapRegular, 8, 8, 0, 0)
	if fh != nil || fv != nil {
		t.Fatal("integer position should give nil,nil filters")
	}
}

func TestGetFilters_SmallWidth(t *testing.T) {
	fh4, _ := GetFilters(Filter2D8TapRegular, 4, 8, 8, 0)
	fh8, _ := GetFilters(Filter2D8TapRegular, 8, 8, 8, 0)
	if len(fh4) == 0 || len(fh8) == 0 {
		t.Fatal("expected non-nil filter slices")
	}
	if fh4[0] != 0 || fh4[1] != 0 {
		t.Errorf("small regular filter tap[0..1] should be 0,0 got %d,%d", fh4[0], fh4[1])
	}
	if fh8[1] != 1 {
		t.Errorf("large regular filter tap[1] at phase 8 should be 1, got %d", fh8[1])
	}
}

func TestGetFilters_SmallHeightUsesVerticalSmallFilter(t *testing.T) {
	_, fv := GetFilters(Filter2D8TapRegular, 8, 4, 0, 8)
	want := McSubpelFilters[FilterRegularSmall][7]
	if fv == nil || !slices.Equal(fv, want[:]) {
		t.Fatalf("vertical small filter=%v want %v", fv, want)
	}
}

func TestGetFilters_SmallAxisDowngradesSharpToRegularSmall(t *testing.T) {
	fh, fv := GetFilters(Filter2D8TapSharp, 4, 4, 8, 8)
	want := McSubpelFilters[FilterRegularSmall][7]
	if !slices.Equal(fh, want[:]) {
		t.Fatalf("small horizontal sharp filter=%v want regular-small %v", fh, want)
	}
	if !slices.Equal(fv, want[:]) {
		t.Fatalf("small vertical sharp filter=%v want regular-small %v", fv, want)
	}
}

func TestGetFilters_Bilinear(t *testing.T) {
	fh, fv := GetFilters(Filter2DBilinear, 8, 8, 8, 8)
	if fh != nil || fv != nil {
		t.Fatal("bilinear should return nil,nil from GetFilters")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PutCopy
// ─────────────────────────────────────────────────────────────────────────────

func TestPutCopy_Basic(t *testing.T) {
	// stride=3, srcBase=0 (row0=[10,20,30], row1=[40,50,60])
	src := []uint8{10, 20, 30, 40, 50, 60}
	dst := make([]uint8, 6)
	PutCopy(dst, 3, src, 0, 3, 3, 2)
	want := []uint8{10, 20, 30, 40, 50, 60}
	for i, v := range want {
		if dst[i] != v {
			t.Fatalf("PutCopy dst[%d]=%d want %d", i, dst[i], v)
		}
	}
}

func TestPut8TapQuantizer01HorizontalPhase2(t *testing.T) {
	row := []byte{81, 87, 96, 96, 97, 108, 110, 104, 101, 107, 111, 109, 104, 95, 89, 84}
	stride := len(row)
	src := make([]byte, stride*8)
	for y := 0; y < 8; y++ {
		copy(src[y*stride:], row)
	}
	dst := make([]byte, 8)
	Put8Tap(dst, 8, src, 3*stride+3, stride, 8, 1, 2, 0, Filter2D8TapRegular)
	want := []byte{96, 98, 109, 109, 103, 101, 108, 111}
	if !slices.Equal(dst, want) {
		t.Fatalf("horizontal phase-2 prediction=%v want %v", dst, want)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Put8Tap – integer position (no filtering)
// ─────────────────────────────────────────────────────────────────────────────

func TestPut8Tap_IntegerPos(t *testing.T) {
	const val = 77
	src, ss, sb := makeSrc(val, 8, 4)
	dst := newDst(8, 4, 8)
	Put8Tap(dst, 8, src, sb, ss, 8, 4, 0, 0, Filter2D8TapRegular)
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			if dst[y*8+x] != val {
				t.Fatalf("int-pos dst[%d][%d]=%d want %d", y, x, dst[y*8+x], val)
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Put8Tap – flat source: any filter on uniform src must reproduce the value
// ─────────────────────────────────────────────────────────────────────────────

func TestPut8Tap_FlatSrc_HOnly(t *testing.T) {
	const val = 100
	src, ss, sb := makeSrc(val, 8, 4)
	dst := newDst(8, 4, 8)
	Put8Tap(dst, 8, src, sb, ss, 8, 4, 8, 0, Filter2D8TapRegular)
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			if dst[y*8+x] != val {
				t.Fatalf("H-only flat dst[%d][%d]=%d want %d", y, x, dst[y*8+x], val)
			}
		}
	}
}

func TestPut8Tap_FlatSrc_VOnly(t *testing.T) {
	const val = 150
	src, ss, sb := makeSrc(val, 8, 8)
	dst := newDst(8, 8, 8)
	Put8Tap(dst, 8, src, sb, ss, 8, 8, 0, 4, Filter2D8TapSmooth)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if dst[y*8+x] != val {
				t.Fatalf("V-only flat dst[%d][%d]=%d want %d", y, x, dst[y*8+x], val)
			}
		}
	}
}

func TestPut8Tap_FlatSrc_HV(t *testing.T) {
	const val = 200
	src, ss, sb := makeSrc(val, 8, 8)
	dst := newDst(8, 8, 8)
	Put8Tap(dst, 8, src, sb, ss, 8, 8, 4, 4, Filter2D8TapSharp)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if dst[y*8+x] != val {
				t.Fatalf("HV flat dst[%d][%d]=%d want %d", y, x, dst[y*8+x], val)
			}
		}
	}
}

func TestPut8Tap_AllFilters(t *testing.T) {
	const val = 128
	src, ss, sb := makeSrc(val, 8, 8)
	for f := Filter2D(0); f < NFilter2D; f++ {
		if f == Filter2DBilinear {
			continue
		}
		dst := newDst(8, 8, 8)
		Put8Tap(dst, 8, src, sb, ss, 8, 8, 8, 8, f)
		for i, v := range dst {
			if v != val {
				t.Errorf("Filter2D=%d dst[%d]=%d want %d", f, i, v, val)
				break
			}
		}
	}
}

func TestPut8Tap_SmallWidth(t *testing.T) {
	const val = 90
	src, ss, sb := makeSrc(val, 4, 4)
	dst := newDst(4, 4, 4)
	Put8Tap(dst, 4, src, sb, ss, 4, 4, 8, 8, Filter2D8TapRegular)
	for i, v := range dst[:4*4] {
		if v != val {
			t.Errorf("small-width dst[%d]=%d want %d", i, v, val)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Prep8Tap – flat source
// ─────────────────────────────────────────────────────────────────────────────

func TestPrep8Tap_FlatSrc_IntPos(t *testing.T) {
	const val = 64
	src, ss, sb := makeSrc(val, 8, 8)
	tmp := make([]int16, 8*8)
	Prep8Tap(tmp, src, sb, ss, 8, 8, 0, 0, Filter2D8TapRegular)
	want := int16(val) << intermediateBits
	for i, v := range tmp {
		if v != want {
			t.Fatalf("tmp[%d]=%d want %d", i, v, want)
		}
	}
}

func TestPrep8Tap_FlatSrc_HOnly(t *testing.T) {
	const val = 80
	src, ss, sb := makeSrc(val, 8, 8)
	tmp := make([]int16, 8*8)
	Prep8Tap(tmp, src, sb, ss, 8, 8, 8, 0, Filter2D8TapRegular)
	want := int16(val) << intermediateBits
	for i, v := range tmp {
		if v != want {
			t.Fatalf("H-only tmp[%d]=%d want %d", i, v, want)
		}
	}
}

func TestPrep8Tap_FlatSrc_VOnly(t *testing.T) {
	const val = 120
	src, ss, sb := makeSrc(val, 8, 8)
	tmp := make([]int16, 8*8)
	Prep8Tap(tmp, src, sb, ss, 8, 8, 0, 8, Filter2D8TapSmooth)
	want := int16(val) << intermediateBits
	for i, v := range tmp {
		if v != want {
			t.Fatalf("V-only tmp[%d]=%d want %d", i, v, want)
		}
	}
}

func TestPrep8Tap_FlatSrc_HV(t *testing.T) {
	const val = 160
	src, ss, sb := makeSrc(val, 8, 8)
	tmp := make([]int16, 8*8)
	Prep8Tap(tmp, src, sb, ss, 8, 8, 4, 4, Filter2D8TapRegularSmooth)
	want := int16(val) << intermediateBits
	for i, v := range tmp {
		if v != want {
			t.Fatalf("HV tmp[%d]=%d want %d", i, v, want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PutBilin
// ─────────────────────────────────────────────────────────────────────────────

func TestPutBilin_IntPos(t *testing.T) {
	const val = 55
	src, ss, sb := makeSrc(val, 8, 4)
	dst := newDst(8, 4, 8)
	PutBilin(dst, 8, src, sb, ss, 8, 4, 0, 0)
	for i, v := range dst[:8*4] {
		if v != val {
			t.Fatalf("bilin int-pos dst[%d]=%d want %d", i, v, val)
		}
	}
}

func TestPutBilin_FlatSrc_HOnly(t *testing.T) {
	const val = 200
	src, ss, sb := makeSrc(val, 8, 4)
	dst := newDst(8, 4, 8)
	PutBilin(dst, 8, src, sb, ss, 8, 4, 8, 0)
	for i, v := range dst[:8*4] {
		if v != val {
			t.Fatalf("bilin H-flat dst[%d]=%d want %d", i, v, val)
		}
	}
}

func TestPutBilin_FlatSrc_VOnly(t *testing.T) {
	const val = 100
	src, ss, sb := makeSrc(val, 8, 4)
	dst := newDst(8, 4, 8)
	PutBilin(dst, 8, src, sb, ss, 8, 4, 0, 8)
	for i, v := range dst[:8*4] {
		if v != val {
			t.Fatalf("bilin V-flat dst[%d]=%d want %d", i, v, val)
		}
	}
}

func TestPutBilin_FlatSrc_HV(t *testing.T) {
	const val = 60
	src, ss, sb := makeSrc(val, 4, 4)
	dst := newDst(4, 4, 4)
	PutBilin(dst, 4, src, sb, ss, 4, 4, 4, 4)
	for i, v := range dst[:4*4] {
		if v != val {
			t.Fatalf("bilin HV flat dst[%d]=%d want %d", i, v, val)
		}
	}
}

// TestPutBilin_HalfPel_H: alternating src[x]=100, src[x+1]=200 → half-pel H → 150.
func TestPutBilin_HalfPel_H(t *testing.T) {
	// Build a 4-row buffer where even cols=100, odd cols=200.
	// Use stride=16, 4 rows of active data + ample padding.
	const stride = 16
	buf := make([]uint8, stride*8)
	for r := 0; r < 8; r++ {
		for x := 0; x < stride; x++ {
			if x%2 == 0 {
				buf[r*stride+x] = 100
			} else {
				buf[r*stride+x] = 200
			}
		}
	}
	srcBase := 0 // (row=0,col=0) → even → 100; col+1 → 200
	const w, h = 2, 4
	dst := make([]uint8, stride*h)
	PutBilin(dst, stride, buf, srcBase, stride, w, h, 8, 0)
	// dst[y][x] = (100*(16-8)+200*8+8)>>4 = (800+1600+8)>>4 = 150
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			got := int(dst[y*stride+x])
			if got != 150 {
				t.Errorf("bilin half-pel H dst[%d][%d]=%d want 150", y, x, got)
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PrepBilin
// ─────────────────────────────────────────────────────────────────────────────

func TestPrepBilin_FlatSrc_IntPos(t *testing.T) {
	const val = 80
	src, ss, sb := makeSrc(val, 8, 4)
	tmp := make([]int16, 8*4)
	PrepBilin(tmp, src, sb, ss, 8, 4, 0, 0)
	want := int16(val) << intermediateBits
	for i, v := range tmp {
		if v != want {
			t.Fatalf("PrepBilin int-pos tmp[%d]=%d want %d", i, v, want)
		}
	}
}

func TestPrepBilin_FlatSrc_HOnly(t *testing.T) {
	const val = 90
	src, ss, sb := makeSrc(val, 8, 4)
	tmp := make([]int16, 8*4)
	PrepBilin(tmp, src, sb, ss, 8, 4, 8, 0)
	want := int16(val) << intermediateBits
	for i, v := range tmp {
		if v != want {
			t.Fatalf("PrepBilin H tmp[%d]=%d want %d", i, v, want)
		}
	}
}

func TestPrepBilin_FlatSrc_VOnly(t *testing.T) {
	const val = 110
	src, ss, sb := makeSrc(val, 8, 4)
	tmp := make([]int16, 8*4)
	PrepBilin(tmp, src, sb, ss, 8, 4, 0, 8)
	want := int16(val) << intermediateBits
	for i, v := range tmp {
		if v != want {
			t.Fatalf("PrepBilin V tmp[%d]=%d want %d", i, v, want)
		}
	}
}

func TestPrepBilin_FlatSrc_HV(t *testing.T) {
	// HV bilinear on flat src: result should equal pixel value (not Q4),
	// because the two-pass normalizes back to pixel range.
	const val = 130
	src, ss, sb := makeSrc(val, 8, 4)
	tmp := make([]int16, 8*4)
	PrepBilin(tmp, src, sb, ss, 8, 4, 4, 4)
	want := int16(val) // HV bilinear normalizes to pixel value
	for i, v := range tmp {
		if v != want {
			t.Fatalf("PrepBilin HV tmp[%d]=%d want %d", i, v, want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Avg
// ─────────────────────────────────────────────────────────────────────────────

func TestAvg_EqualBuffers(t *testing.T) {
	const val = 100
	n := 8 * 4
	tmp1 := make([]int16, n)
	tmp2 := make([]int16, n)
	for i := range tmp1 {
		tmp1[i] = int16(val) << intermediateBits
		tmp2[i] = int16(val) << intermediateBits
	}
	dst := make([]uint8, n)
	Avg(dst, 8, tmp1, tmp2, 8, 4)
	for i, v := range dst {
		if v != val {
			t.Fatalf("Avg dst[%d]=%d want %d", i, v, val)
		}
	}
}

func TestAvg_DifferentBuffers(t *testing.T) {
	n := 8 * 4
	tmp1 := make([]int16, n)
	tmp2 := make([]int16, n)
	for i := range tmp1 {
		tmp1[i] = 80 << intermediateBits
		tmp2[i] = 120 << intermediateBits
	}
	dst := make([]uint8, n)
	Avg(dst, 8, tmp1, tmp2, 8, 4)
	for i, v := range dst {
		if v != 100 {
			t.Fatalf("Avg dst[%d]=%d want 100", i, v)
		}
	}
}

func TestAvg_Clamp(t *testing.T) {
	n := 4
	tmp1 := make([]int16, n)
	tmp2 := make([]int16, n)
	for i := range tmp1 {
		// Overflow: 300 << 4 doesn't fit int16, use max-safe value
		tmp1[i] = 127 << 4 // ~127 pixels, doubles to ~254 → clamp 255
		tmp2[i] = 127 << 4
	}
	dst := make([]uint8, n)
	Avg(dst, 4, tmp1, tmp2, 4, 1)
	for i, v := range dst {
		if v != 127 {
			t.Fatalf("Avg dst[%d]=%d want 127", i, v)
		}
	}
}

func TestAvg_NegClamp(t *testing.T) {
	n := 4
	tmp1 := make([]int16, n)
	tmp2 := make([]int16, n)
	for i := range tmp1 {
		tmp1[i] = -100 << 4
		tmp2[i] = -100 << 4
	}
	dst := make([]uint8, n)
	Avg(dst, 4, tmp1, tmp2, 4, 1)
	for i, v := range dst {
		if v != 0 {
			t.Fatalf("Avg neg clamp dst[%d]=%d want 0", i, v)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// WAvg
// ─────────────────────────────────────────────────────────────────────────────

func TestWAvg_EqualWeight(t *testing.T) {
	// weight=8: blend 80 and 120 → 100.
	n := 8
	tmp1 := make([]int16, n)
	tmp2 := make([]int16, n)
	for i := range tmp1 {
		tmp1[i] = 80 << intermediateBits
		tmp2[i] = 120 << intermediateBits
	}
	dst := make([]uint8, n)
	WAvg(dst, 8, tmp1, tmp2, 8, 1, 8)
	for i, v := range dst {
		if v != 100 {
			t.Errorf("WAvg equal dst[%d]=%d want 100", i, v)
		}
	}
}

func TestWAvg_FullWeight1(t *testing.T) {
	// weight=16: output = tmp1 = 80.
	n := 8
	tmp1 := make([]int16, n)
	tmp2 := make([]int16, n)
	for i := range tmp1 {
		tmp1[i] = 80 << intermediateBits
		tmp2[i] = 120 << intermediateBits
	}
	dst := make([]uint8, n)
	WAvg(dst, 8, tmp1, tmp2, 8, 1, 16)
	for i, v := range dst {
		if v != 80 {
			t.Errorf("WAvg full-1 dst[%d]=%d want 80", i, v)
		}
	}
}

func TestWAvg_FullWeight2(t *testing.T) {
	// weight=0: output = tmp2 = 120.
	n := 8
	tmp1 := make([]int16, n)
	tmp2 := make([]int16, n)
	for i := range tmp1 {
		tmp1[i] = 80 << intermediateBits
		tmp2[i] = 120 << intermediateBits
	}
	dst := make([]uint8, n)
	WAvg(dst, 8, tmp1, tmp2, 8, 1, 0)
	for i, v := range dst {
		if v != 120 {
			t.Errorf("WAvg full-2 dst[%d]=%d want 120", i, v)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Compound round-trip: Prep8Tap → Avg
// ─────────────────────────────────────────────────────────────────────────────

func TestCompound_Prep8Tap_Avg(t *testing.T) {
	const val1, val2 = 80, 120
	src1, ss1, sb1 := makeSrc(val1, 8, 8)
	src2, ss2, sb2 := makeSrc(val2, 8, 8)
	tmp1 := make([]int16, 8*8)
	tmp2 := make([]int16, 8*8)
	Prep8Tap(tmp1, src1, sb1, ss1, 8, 8, 0, 0, Filter2D8TapRegular)
	Prep8Tap(tmp2, src2, sb2, ss2, 8, 8, 0, 0, Filter2D8TapRegular)
	dst := make([]uint8, 8*8)
	Avg(dst, 8, tmp1, tmp2, 8, 8)
	want := uint8((val1 + val2) / 2)
	for i, v := range dst {
		if v != want {
			t.Fatalf("compound avg dst[%d]=%d want %d", i, v, want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Ramp source smoke tests
// ─────────────────────────────────────────────────────────────────────────────

func TestPut8Tap_Ramp_IntPos_MatchesCopy(t *testing.T) {
	src, ss, sb := makeRampSrc(8, 4)
	dst1 := newDst(8, 4, 8)
	dst2 := newDst(8, 4, 8)
	Put8Tap(dst1, 8, src, sb, ss, 8, 4, 0, 0, Filter2D8TapRegular)
	PutCopy(dst2, 8, src, sb, ss, 8, 4)
	for i := range dst1[:8*4] {
		if dst1[i] != dst2[i] {
			t.Fatalf("ramp int-pos mismatch at %d: %d vs %d", i, dst1[i], dst2[i])
		}
	}
}

func TestPut8Tap_Ramp_HV_NoOutOfRange(t *testing.T) {
	src, ss, sb := makeRampSrc(16, 16)
	dst := newDst(16, 16, 16)
	// Should not panic; clampPixel guarantees [0,255].
	Put8Tap(dst, 16, src, sb, ss, 16, 16, 8, 8, Filter2D8TapRegular)
	// uint8 always in [0,255]; just verify no panic occurred.
}
