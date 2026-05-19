package transform

import (
	"math/rand"
	"testing"
)

// AV1 8-bit profile 0 stage clip range.
const (
	itxMin = -(1 << 15)
	itxMax = (1 << 15) - 1
)

// equalSlice32 reports whether two int32 slices are equal element-wise.
func equalSlice32(t *testing.T, got, want []int32, tag string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: len mismatch got=%d want=%d", tag, len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: idx=%d got=%d want=%d (full got=%v)", tag, i, got[i], want[i], got)
		}
	}
}

// ---- clip ----------------------------------------------------------------

func TestClip(t *testing.T) {
	cases := []struct {
		v, lo, hi, want int
	}{
		{0, -10, 10, 0},
		{-100, -10, 10, -10},
		{100, -10, 10, 10},
		{5, -10, 10, 5},
		{-10, -10, 10, -10},
		{10, -10, 10, 10},
	}
	for _, c := range cases {
		if got := clip(c.v, c.lo, c.hi); got != c.want {
			t.Fatalf("clip(%d,%d,%d)=%d want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

// ---- DCT family ----------------------------------------------------------

func TestInvDCT4_Zero(t *testing.T) {
	c := make([]int32, 4)
	InvDCT4(c, 1, itxMin, itxMax)
	equalSlice32(t, c, []int32{0, 0, 0, 0}, "DCT4 zero")
}

func TestInvDCT4_DCOnly(t *testing.T) {
	// X = 256: t0 = t1 = (256*181+128)>>8 = 181, t2=t3=0
	// c[0]=t0+t3=181, c[1]=t1+t2=181, c[2]=t1-t2=181, c[3]=t0-t3=181.
	c := []int32{256, 0, 0, 0}
	InvDCT4(c, 1, itxMin, itxMax)
	equalSlice32(t, c, []int32{181, 181, 181, 181}, "DCT4 DC")
}

func TestInvDCT4_AC1Only(t *testing.T) {
	// in1=4096: t0=t1=0, t2 = (4096*1567 - 0 + 2048)>>12 - 0 = 1567,
	// t3 = (4096*(3784-4096) + 0 + 2048)>>12 + 4096 = (-1277952+2048)/4096 + 4096
	// arithmetic shift floor(-1275904/4096) = -312, so t3 = -312 + 4096 = 3784.
	// Out: [t3, t2, -t2, -t3] = [3784, 1567, -1567, -3784]
	c := []int32{0, 4096, 0, 0}
	InvDCT4(c, 1, itxMin, itxMax)
	equalSlice32(t, c, []int32{3784, 1567, -1567, -3784}, "DCT4 AC1")
}

func TestInvDCT4_TX64Path(t *testing.T) {
	// Drive the tx64 branch through the internal entrypoint.
	c := []int32{256, 0, 0, 0}
	invDCT4Internal(c, 1, itxMin, itxMax, true)
	// tx64: t0=t1=(256*181+128)>>8=181, t2=t3=0 (in1=0)
	equalSlice32(t, c, []int32{181, 181, 181, 181}, "DCT4 tx64 dc")

	c2 := []int32{0, 4096, 0, 0}
	invDCT4Internal(c2, 1, itxMin, itxMax, true)
	// tx64 path: t0=t1=0 (in0=0); t2=(4096*1567+2048)>>12=1567;
	// t3=(4096*3784+2048)>>12=3784. Out: [t3,t2,-t2,-t3].
	equalSlice32(t, c2, []int32{3784, 1567, -1567, -3784}, "DCT4 tx64 ac1")
}

func TestInvDCT8_DCOnly(t *testing.T) {
	c := make([]int32, 8)
	c[0] = 256
	InvDCT8(c, 1, itxMin, itxMax)
	want := int32((256*181 + 128) >> 8) // 181
	for i, v := range c {
		if v != want {
			t.Fatalf("DCT8 DC: c[%d]=%d want %d", i, v, want)
		}
	}
}

func TestInvDCT8_TX64Path(t *testing.T) {
	c := make([]int32, 8)
	c[0] = 4096
	invDCT8Internal(c, 1, itxMin, itxMax, true)
	// tx64 only uses inputs c[0] and c[1]; both code paths must run.
	for _, v := range c {
		_ = v
	}
}

func TestInvDCT16_DCOnly(t *testing.T) {
	c := make([]int32, 16)
	c[0] = 256
	InvDCT16(c, 1, itxMin, itxMax)
	want := int32((256*181 + 128) >> 8)
	for i, v := range c {
		if v != want {
			t.Fatalf("DCT16 DC: c[%d]=%d want %d", i, v, want)
		}
	}
}

func TestInvDCT16_TX64Path(t *testing.T) {
	c := make([]int32, 16)
	c[0] = 4096
	c[1] = 4096
	c[2] = 4096
	c[3] = 4096
	c[5] = 4096
	c[7] = 4096
	invDCT16Internal(c, 1, itxMin, itxMax, true)
	// Only validates that the tx64 branch executes without OOB or panic;
	// stride+slice math ensures it touches the same lane subset as the
	// non-tx64 path.
}

func TestInvDCT32_DCOnly(t *testing.T) {
	c := make([]int32, 32)
	c[0] = 256
	InvDCT32(c, 1, itxMin, itxMax)
	want := int32((256*181 + 128) >> 8) // 181
	for i, v := range c {
		if v != want {
			t.Fatalf("DCT32 DC: c[%d]=%d want %d", i, v, want)
		}
	}
}

func TestInvDCT32_TX64Path(t *testing.T) {
	c := make([]int32, 32)
	c[0] = 4096
	c[1] = 4096
	invDCT32Internal(c, 1, itxMin, itxMax, true)
	// Validates tx64 branch runs without panic.
}

func TestInvDCT64_DCOnly(t *testing.T) {
	c := make([]int32, 64)
	c[0] = 256
	InvDCT64(c, 1, itxMin, itxMax)
	// DCT64 calls invDCT32Internal(c, 2, ...) for the even half,
	// then the odd half with all zeros. DC input should produce a
	// near-constant output scaled by the 64-point butterfly chain.
	// Validate non-zero output and no panics.
	nonzero := 0
	for _, v := range c {
		if v != 0 {
			nonzero++
		}
	}
	if nonzero == 0 {
		t.Fatal("DCT64 DC: all outputs zero")
	}
}

func TestInvDCT64_RandomSmoke(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	c := make([]int32, 64)
	for i := range c {
		c[i] = int32(rng.Intn(8192) - 4096)
	}
	InvDCT64(c, 1, itxMin, itxMax)
	// Just ensure no panic / OOB.
}

// Round-trip via stride: verify that strided storage works correctly.
func TestInvDCT4_Stride2(t *testing.T) {
	// Place 4 inputs at stride=2 (so c has length 8 with garbage in the
	// odd slots) and ensure the kernel only touches the requested ones.
	c := make([]int32, 8)
	c[0] = 256
	c[2] = 0
	c[4] = 0
	c[6] = 0
	// Garbage in odd slots that must remain untouched.
	c[1] = 999
	c[3] = -999
	c[5] = 12345
	c[7] = -12345

	InvDCT4(c, 2, itxMin, itxMax)
	if c[0] != 181 || c[2] != 181 || c[4] != 181 || c[6] != 181 {
		t.Fatalf("stride: got even=%v", []int32{c[0], c[2], c[4], c[6]})
	}
	if c[1] != 999 || c[3] != -999 || c[5] != 12345 || c[7] != -12345 {
		t.Fatalf("stride: odd lanes were modified: %v", c)
	}
}

// ---- ADST / FLIPADST -----------------------------------------------------

func TestInvADST4_DC(t *testing.T) {
	// in0 = 4096; in1=in2=in3=0.
	// out[0] = (1321*4096+2048)>>12 = 1321
	// out[1] = ((2482-4096)*4096+2048)>>12 + 4096 = -1614+4096 = 2482
	// out[2] = (209*4096+128)>>8 = 3344
	// out[3] = ((3803-4096)*4096+2048)>>12 + 4096 = -293+4096 = 3803
	c := []int32{4096, 0, 0, 0}
	InvADST4(c, 1, itxMin, itxMax)
	equalSlice32(t, c, []int32{1321, 2482, 3344, 3803}, "ADST4 DC")
}

func TestInvFlipADST4_DC(t *testing.T) {
	// FLIPADST4 = reverse(ADST4): swapped output indices.
	c := []int32{4096, 0, 0, 0}
	InvFlipADST4(c, 1, itxMin, itxMax)
	equalSlice32(t, c, []int32{3803, 3344, 2482, 1321}, "FlipADST4 DC")
}

func TestInvADST4_FlipReverseInvariant(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 32; trial++ {
		in := make([]int32, 4)
		for i := range in {
			in[i] = int32(rng.Intn(2048) - 1024)
		}
		a := append([]int32(nil), in...)
		f := append([]int32(nil), in...)
		InvADST4(a, 1, itxMin, itxMax)
		InvFlipADST4(f, 1, itxMin, itxMax)
		for i := 0; i < 4; i++ {
			if f[i] != a[3-i] {
				t.Fatalf("trial=%d i=%d flip=%d adst[%d]=%d", trial, i, f[i], 3-i, a[3-i])
			}
		}
	}
}

func TestInvADST8_FlipReverseInvariant(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for trial := 0; trial < 32; trial++ {
		in := make([]int32, 8)
		for i := range in {
			in[i] = int32(rng.Intn(2048) - 1024)
		}
		a := append([]int32(nil), in...)
		f := append([]int32(nil), in...)
		InvADST8(a, 1, itxMin, itxMax)
		InvFlipADST8(f, 1, itxMin, itxMax)
		for i := 0; i < 8; i++ {
			if f[i] != a[7-i] {
				t.Fatalf("trial=%d i=%d flip=%d adst[%d]=%d", trial, i, f[i], 7-i, a[7-i])
			}
		}
	}
}

func TestInvADST16_FlipReverseInvariant(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for trial := 0; trial < 32; trial++ {
		in := make([]int32, 16)
		for i := range in {
			in[i] = int32(rng.Intn(2048) - 1024)
		}
		a := append([]int32(nil), in...)
		f := append([]int32(nil), in...)
		InvADST16(a, 1, itxMin, itxMax)
		InvFlipADST16(f, 1, itxMin, itxMax)
		for i := 0; i < 16; i++ {
			if f[i] != a[15-i] {
				t.Fatalf("trial=%d i=%d flip=%d adst[%d]=%d", trial, i, f[i], 15-i, a[15-i])
			}
		}
	}
}

func TestInvADST8_RandomSmoke(t *testing.T) {
	// Just exercise the kernel at full width.
	rng := rand.New(rand.NewSource(4))
	c := make([]int32, 8)
	for i := range c {
		c[i] = int32(rng.Intn(8192) - 4096)
	}
	InvADST8(c, 1, itxMin, itxMax)
	// Sanity: at least one sample has changed.
	allZero := true
	for _, v := range c {
		if v != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatalf("ADST8 random should not produce all zeros")
	}
}

func TestInvADST16_RandomSmoke(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	c := make([]int32, 16)
	for i := range c {
		c[i] = int32(rng.Intn(8192) - 4096)
	}
	InvADST16(c, 1, itxMin, itxMax)
	allZero := true
	for _, v := range c {
		if v != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatalf("ADST16 random should not produce all zeros")
	}
}

// ---- IDENTITY ------------------------------------------------------------

func TestInvIdentity4(t *testing.T) {
	c := []int32{4096, -4096, 0, 1024}
	InvIdentity4(c, 1, itxMin, itxMax)
	// in + ((in*1697+2048)>>12)
	want := func(in int32) int32 {
		return in + int32((int(in)*1697+2048)>>12)
	}
	wantOut := []int32{want(4096), want(-4096), want(0), want(1024)}
	equalSlice32(t, c, wantOut, "Identity4")
}

func TestInvIdentity8(t *testing.T) {
	c := []int32{1, 2, -3, 4, -5, 6, -7, 8}
	InvIdentity8(c, 1, itxMin, itxMax)
	equalSlice32(t, c, []int32{2, 4, -6, 8, -10, 12, -14, 16}, "Identity8")
}

func TestInvIdentity16(t *testing.T) {
	c := make([]int32, 16)
	for i := range c {
		c[i] = int32(i - 8)
	}
	want := make([]int32, 16)
	for i := range c {
		in := i - 8
		want[i] = int32(2*in + ((in*1697 + 1024) >> 11))
	}
	InvIdentity16(c, 1, itxMin, itxMax)
	equalSlice32(t, c, want, "Identity16")
}

func TestInvIdentity32(t *testing.T) {
	c := make([]int32, 32)
	for i := range c {
		c[i] = int32(i)
	}
	InvIdentity32(c, 1, itxMin, itxMax)
	for i, v := range c {
		if v != int32(4*i) {
			t.Fatalf("Identity32: c[%d]=%d want %d", i, v, 4*i)
		}
	}
}

// ---- WHT4 ----------------------------------------------------------------

func TestInvWHT4(t *testing.T) {
	// Algorithm by hand:
	//   in0=8, in1=4, in2=2, in3=1
	//   t0 = 12, t2 = 1, t4 = (12-1)>>1 = 5
	//   t3 = 5-1 = 4, t1 = 5-4 = 1
	//   c0 = 12-4 = 8, c1 = 4, c2 = 1, c3 = 1+1 = 2
	c := []int32{8, 4, 2, 1}
	InvWHT4(c, 1)
	equalSlice32(t, c, []int32{8, 4, 1, 2}, "WHT4 manual")
}

func TestInvWHT4_Zero(t *testing.T) {
	c := []int32{0, 0, 0, 0}
	InvWHT4(c, 1)
	equalSlice32(t, c, []int32{0, 0, 0, 0}, "WHT4 zero")
}

// ---- Dispatch tables -----------------------------------------------------

func TestTx1dFnsDispatch(t *testing.T) {
	cases := []struct {
		size, ty int
		fn       Itx1dFn
		nilOK    bool
	}{
		{TX4x4, Tx1dDCT, InvDCT4, false},
		{TX4x4, Tx1dADST, InvADST4, false},
		{TX4x4, Tx1dFLIPADST, InvFlipADST4, false},
		{TX4x4, Tx1dIDENTITY, InvIdentity4, false},
		{TX8x8, Tx1dDCT, InvDCT8, false},
		{TX16x16, Tx1dADST, InvADST16, false},
		{TX32x32, Tx1dDCT, InvDCT32, false},
		{TX32x32, Tx1dIDENTITY, InvIdentity32, false},
		{TX64x64, Tx1dDCT, InvDCT64, false},
	}
	for _, c := range cases {
		got := Tx1dFns[c.size][c.ty]
		if c.nilOK {
			if got != nil {
				t.Fatalf("[%d][%d] expected nil, got non-nil", c.size, c.ty)
			}
			continue
		}
		if got == nil {
			t.Fatalf("[%d][%d] expected non-nil", c.size, c.ty)
		}
		// Run a smoke sample through the dispatched fn.
		bufSize := 4
		if c.size >= TX32x32 {
			bufSize = 64
		} else if c.size >= TX8x8 {
			bufSize = 16
		}
		buf := make([]int32, bufSize)
		buf[0] = 256
		got(buf, 1, itxMin, itxMax)
	}
}

func TestTx1dTypesDispatch(t *testing.T) {
	cases := []struct {
		ty       int
		col, row uint8
	}{
		{DCT_DCT, Tx1dDCT, Tx1dDCT},
		{ADST_DCT, Tx1dADST, Tx1dDCT},
		{FLIPADST_FLIPADST, Tx1dFLIPADST, Tx1dFLIPADST},
		{IDTX, Tx1dIDENTITY, Tx1dIDENTITY},
		{V_DCT, Tx1dDCT, Tx1dIDENTITY},
		{H_FLIPADST, Tx1dIDENTITY, Tx1dFLIPADST},
	}
	for _, c := range cases {
		gotCol := Tx1dTypes[c.ty][0]
		gotRow := Tx1dTypes[c.ty][1]
		if gotCol != c.col || gotRow != c.row {
			t.Fatalf("ty=%d got=(%d,%d) want=(%d,%d)", c.ty, gotCol, gotRow, c.col, c.row)
		}
	}
}
