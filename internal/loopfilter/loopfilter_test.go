package loopfilter

import (
	"testing"
)

// makeLUT builds a FilterLUT with all E=e, I=i for every level.
func makeLUT(e, i uint8) *FilterLUT {
	var lut FilterLUT
	for k := 0; k < 64; k++ {
		lut.E[k] = e
		lut.I[k] = i
	}
	return &lut
}

func TestNewFilterLUTSharpness(t *testing.T) {
	lut0 := NewFilterLUT(0)
	if lut0.I[32] != 32 || lut0.E[32] != 100 {
		t.Fatalf("sharp=0 level=32 got I=%d E=%d", lut0.I[32], lut0.E[32])
	}
	lut7 := NewFilterLUT(7)
	if lut7.I[32] != 2 || lut7.E[32] != 70 {
		t.Fatalf("sharp=7 level=32 got I=%d E=%d", lut7.I[32], lut7.E[32])
	}
	if lut7.I[0] != 1 || lut7.E[0] != 5 {
		t.Fatalf("sharp=7 level=0 got I=%d E=%d", lut7.I[0], lut7.E[0])
	}
}

// flatBuf builds a flat pixel buffer (all pixels == val) with the given width,
// height and padding such that accesses dst[base ± 7*stride] are valid.
func flatBuf(val uint8, w, h, stride int) (buf []uint8, base int) {
	pad := 8
	rows := h + 2*pad
	buf = make([]uint8, rows*stride)
	for i := range buf {
		buf[i] = val
	}
	base = pad * stride
	return
}

// ─── loopFilter core ─────────────────────────────────────────────────────────

// TestLoopFilter_NotTriggered: E is very small so fm is never set → pixels unchanged.
func TestLoopFilter_NotTriggered(t *testing.T) {
	buf, base := flatBuf(100, 8, 4, 8)
	// introduce a sharp edge so |p0-q0| = 50, but set E=0 → fm=false
	for i := 0; i < 4; i++ {
		buf[base+i*8] = 50  // q0
		buf[base+i*8-1] = 0 // p0
	}
	loopFilter(buf, base, 0, 0, 0, 8, 1, 4)
	// values should remain unchanged
	for i := 0; i < 4; i++ {
		if buf[base+i*8] != 50 {
			t.Errorf("row %d: q0 changed unexpectedly", i)
		}
		if buf[base+i*8-1] != 0 {
			t.Errorf("row %d: p0 changed unexpectedly", i)
		}
	}
}

// TestLoopFilter_Wd4_Narrow: wd=4, HEV triggers simple 2-tap update.
func TestLoopFilter_Wd4_Narrow(t *testing.T) {
	// Flat source: all 128. The edge has p0=q0=128 → difference=0 → f=0 → no change.
	buf, base := flatBuf(128, 8, 4, 8)
	before := make([]uint8, len(buf))
	copy(before, buf)
	loopFilter(buf, base, 16, 4, 2, 8, 1, 4)
	for i, v := range buf {
		if v != before[i] {
			t.Errorf("pixel[%d] changed from %d to %d on flat source", i, before[i], v)
		}
	}
}

// TestLoopFilter_Wd4_HEV_update: build a controlled edge and check output.
// Layout: 4 consecutive pixels at base, each forms one p/q group.
// For each pixel i (0..3): p1=buf[base+i+stride*-2], p0=buf[base+i+stride*-1], etc.
// But with stridea=1, strideb=stride, the filter scans base+0..base+3 (4 positions along x),
// and for each position crosses the edge via stride.
// p1=100, p0=90, q0=110, q1=120, H=3 → hev triggers because |p1-p0|=10>H=3.
// f = clip(3*(q0-p0) + clip(p1-q1)) = clip(3*20 + clip(100-120)) = clip(60-20)=40
// f1=min(44,127)>>3=5, f2=min(43,127)>>3=5
// new p0=90+5=95, new q0=110-5=105
func TestLoopFilter_Wd4_HEV(t *testing.T) {
	stride := 20
	// place edge in the middle of rows; scan 4 consecutive cols at base..
	buf := make([]uint8, stride*10)
	base := 5 * stride // start of q0 row, col 0

	// Fill 4 consecutive columns at base (stridea=1 scans cols 0..3)
	for i := 0; i < 4; i++ {
		buf[base+i+stride*-2] = 100 // p1
		buf[base+i+stride*-1] = 90  // p0
		buf[base+i+stride*0] = 110  // q0
		buf[base+i+stride*1] = 120  // q1
	}

	// E must be large enough to trigger fm: |p0-q0|*2 + |p1-q1|/2 = 20*2+10/2 = 45
	// Set E=50, I=15, H=3 (→ hev=true since |p1-p0|=10>3)
	loopFilter(buf, base, 50, 15, 3, 1, stride, 4)

	for i := 0; i < 4; i++ {
		p0 := int(buf[base+i+stride*-1])
		q0 := int(buf[base+i+stride*0])
		if p0 != 95 {
			t.Errorf("col %d: p0 = %d, want 95", i, p0)
		}
		if q0 != 105 {
			t.Errorf("col %d: q0 = %d, want 105", i, q0)
		}
	}
}

// TestLoopFilter_Wd4_NoHEV: no HEV → also update p1, q1.
// 4 consecutive cols; for each: p1=128, p0=126, q0=130, q1=128
// H=4 (>|p1-p0|=2 and |q1-q0|=2 → hev=false)
// f = clip(3*(130-126)) = clip(12) = 12
// f1 = min(16,127)>>3=2, f2=min(15,127)>>3=1, f3=(2+1)>>1=1
// p0=126+1=127, q0=130-2=128, p1=128+1=129, q1=128-1=127
func TestLoopFilter_Wd4_NoHEV(t *testing.T) {
	stride := 20
	buf := make([]uint8, stride*10)
	base := 5 * stride

	for i := 0; i < 4; i++ {
		buf[base+i+stride*-2] = 128 // p1
		buf[base+i+stride*-1] = 126 // p0
		buf[base+i+stride*0] = 130  // q0
		buf[base+i+stride*1] = 128  // q1
	}
	// E: |p0-q0|*2+|p1-q1|/2 = 4*2+0/2=8; set E=10
	// I: must be >= |p1-p0|=2 and |q1-q0|=2; set I=5
	loopFilter(buf, base, 10, 5, 4, 1, stride, 4)

	for i := 0; i < 4; i++ {
		p1 := int(buf[base+i+stride*-2])
		p0 := int(buf[base+i+stride*-1])
		q0 := int(buf[base+i+stride*0])
		q1 := int(buf[base+i+stride*1])
		if p1 != 129 {
			t.Errorf("col %d: p1=%d want 129", i, p1)
		}
		if p0 != 127 {
			t.Errorf("col %d: p0=%d want 127", i, p0)
		}
		if q0 != 128 {
			t.Errorf("col %d: q0=%d want 128", i, q0)
		}
		if q1 != 127 {
			t.Errorf("col %d: q1=%d want 127", i, q1)
		}
	}
}

// TestLoopFilter_Wd8_Flat: all pixels flat at 128 → flat8in=true → pixels unchanged.
func TestLoopFilter_Wd8_Flat(t *testing.T) {
	buf, base := flatBuf(128, 8, 4, 8)
	before := make([]uint8, len(buf))
	copy(before, buf)
	// stridea=1 (scan along y), strideb=8 (cross edge)
	loopFilter(buf, base, 100, 10, 5, 1, 8, 8)
	for i, v := range buf {
		if v != before[i] {
			t.Errorf("pixel[%d] changed from %d to %d on flat source (wd=8)", i, before[i], v)
		}
	}
}

// TestLoopFilter_Wd8_Smooth: gradient input, verify no panic for wd=8 fallback.
// flat8in: |p2-p0|=4 > 1 → false → falls back to narrow filter.
func TestLoopFilter_Wd8_FallbackToNarrow(t *testing.T) {
	stride := 20
	buf := make([]uint8, stride*12) // extra room for q3=base+stride*3
	base := 5 * stride

	for i := 0; i < 4; i++ {
		buf[base+i+stride*-4] = 100
		buf[base+i+stride*-3] = 102
		buf[base+i+stride*-2] = 104
		buf[base+i+stride*-1] = 106
		buf[base+i+stride*0] = 150
		buf[base+i+stride*1] = 152
		buf[base+i+stride*2] = 154
		buf[base+i+stride*3] = 156
	}
	// should not panic
	loopFilter(buf, base, 200, 20, 5, 1, stride, 8)
}

// TestLoopFilter_Wd16_Flat: all 128, flat8in=flat8out=true, all pixels stay 128.
func TestLoopFilter_Wd16_Flat(t *testing.T) {
	buf, base := flatBuf(128, 16, 4, 16)
	before := make([]uint8, len(buf))
	copy(before, buf)
	loopFilter(buf, base, 200, 20, 10, 1, 16, 16)
	for i, v := range buf {
		if v != before[i] {
			t.Errorf("pixel[%d] changed %d→%d (wd=16 flat)", i, before[i], v)
		}
	}
}

// ─── LoopFilterH / LoopFilterV ──────────────────────────────────────────────

// makeFilterLevel encodes H into high nibble and level index into low nibble.
// For simplicity in tests we set L=level (0..63), H=L>>4 but at least 1.
func makeLevel(e, i, H uint8) uint8 {
	// Encode: high nibble = H, low 6 bits = level index.
	// lut.E[L&63] = e, lut.I[L&63] = i.
	// We just return H<<4 | 1 (some non-zero value).
	return H<<4 | 1
}

// TestLoopFilterH_NoEdge: vmask all zero → nothing touches the buffer.
func TestLoopFilterH_NoEdge(t *testing.T) {
	buf, base := flatBuf(128, 16, 8, 16)
	for i := 0; i < 4; i++ {
		buf[base+i*16-1] = 100 // p0 sharp
		buf[base+i*16] = 200   // q0 sharp
	}
	before := make([]uint8, len(buf))
	copy(before, buf)

	lut := makeLUT(0, 0)
	l := make([]uint8, 8)
	var vmask [3]uint32

	LoopFilterH(buf, base, 16, vmask, l, 1, lut, 2, false)
	for i, v := range buf {
		if v != before[i] {
			t.Errorf("pixel[%d] changed with vmask=0", i)
		}
	}
}

// TestLoopFilterV_NoEdge: same for vertical direction.
func TestLoopFilterV_NoEdge(t *testing.T) {
	buf, base := flatBuf(128, 16, 8, 16)
	before := make([]uint8, len(buf))
	copy(before, buf)

	lut := makeLUT(0, 0)
	l := make([]uint8, 8)
	var vmask [3]uint32

	LoopFilterV(buf, base, 16, vmask, l, 8, lut, 2, false)
	for i, v := range buf {
		if v != before[i] {
			t.Errorf("pixel[%d] changed with vmask=0", i)
		}
	}
}

// TestLoopFilterH_LevelZero: level=0 → filter skipped even if vmask set.
func TestLoopFilterH_LevelZero(t *testing.T) {
	buf, base := flatBuf(100, 8, 8, 8)
	for i := 0; i < 4; i++ {
		buf[base+i*8-1] = 0
		buf[base+i*8] = 200
	}
	before := make([]uint8, len(buf))
	copy(before, buf)

	lut := makeLUT(200, 20)
	// l[0]=0 and no row above → skipped
	l := make([]uint8, 4)
	vmask := [3]uint32{1, 0, 0}

	LoopFilterH(buf, base, 8, vmask, l, 1, lut, 2, false)
	for i, v := range buf {
		if v != before[i] {
			t.Errorf("pixel[%d] changed with l=0", i)
		}
	}
}

// TestLoopFilterH_Flat_Wd4: flat edge, level non-zero, wd=4. Since source is
// flat no change is expected.
func TestLoopFilterH_Flat_Wd4(t *testing.T) {
	buf, base := flatBuf(128, 8, 8, 8)
	before := make([]uint8, len(buf))
	copy(before, buf)

	lut := makeLUT(100, 10)
	l := []uint8{0x15} // non-zero, H=1
	vmask := [3]uint32{1, 0, 0}

	LoopFilterH(buf, base, 8, vmask, l, 1, lut, 2, false)
	for i, v := range buf {
		if v != before[i] {
			t.Errorf("pixel[%d] changed on flat wd=4", i)
		}
	}
}

// TestLoopFilterV_Flat_Wd4: same for vertical.
func TestLoopFilterV_Flat_Wd4(t *testing.T) {
	buf, base := flatBuf(128, 16, 8, 16)
	before := make([]uint8, len(buf))
	copy(before, buf)

	lut := makeLUT(100, 10)
	l := []uint8{0x15}
	vmask := [3]uint32{1, 0, 0}

	LoopFilterV(buf, base, 16, vmask, l, 1, lut, 2, false)
	for i, v := range buf {
		if v != before[i] {
			t.Errorf("pixel[%d] changed on flat wd=4 vertical", i)
		}
	}
}

// TestLoopFilterH_Chroma_Wd6: chroma path uses at most wd=6.
func TestLoopFilterH_Chroma_Flat_Wd6(t *testing.T) {
	buf, base := flatBuf(128, 8, 8, 8)
	before := make([]uint8, len(buf))
	copy(before, buf)

	lut := makeLUT(100, 10)
	l := []uint8{0x15}
	// vmask[1] set → idx=1 → wd = 4<<1 = 8, but chroma caps at 6
	// Actually chroma: wd = 4+2*idx = 4+2=6
	vmask := [3]uint32{1, 1, 0}

	LoopFilterH(buf, base, 8, vmask, l, 1, lut, 2, true)
	for i, v := range buf {
		if v != before[i] {
			t.Errorf("pixel[%d] changed on flat chroma wd=6", i)
		}
	}
}

// ─── iclip / iclipPixel helpers ──────────────────────────────────────────────

func TestIclip(t *testing.T) {
	tests := []struct{ v, lo, hi, want int }{
		{5, 0, 10, 5},
		{-1, 0, 10, 0},
		{11, 0, 10, 10},
	}
	for _, tc := range tests {
		if got := iclip(tc.v, tc.lo, tc.hi); got != tc.want {
			t.Errorf("iclip(%d,%d,%d)=%d want %d", tc.v, tc.lo, tc.hi, got, tc.want)
		}
	}
}

func TestIclipPixel(t *testing.T) {
	if iclipPixel(-1) != 0 {
		t.Error("iclipPixel(-1) != 0")
	}
	if iclipPixel(256) != 255 {
		t.Error("iclipPixel(256) != 255")
	}
	if iclipPixel(128) != 128 {
		t.Error("iclipPixel(128) != 128")
	}
}
