package bitstream

import (
	"math/rand"
	"testing"
)

// ---------------------------------------------------------------------------
// Bit / F / SU / refill
// ---------------------------------------------------------------------------

func TestGetBits_BitMatchesByteOrder(t *testing.T) {
	// 0xA5 = 1010 0101 -> consecutive Bit() reads MSB-first.
	g := NewGetBits([]byte{0xA5})
	want := []uint32{1, 0, 1, 0, 0, 1, 0, 1}
	for i, w := range want {
		if got := g.Bit(); got != w {
			t.Fatalf("Bit[%d] = %d, want %d", i, got, w)
		}
	}
	if g.Err() {
		t.Fatalf("unexpected sticky error after fully consuming buffer")
	}
	// Reading one more bit must trigger the sticky error and return 0.
	if got := g.Bit(); got != 0 || !g.Err() {
		t.Fatalf("Bit past end: got=%d err=%v, want 0/true", got, g.Err())
	}
}

func TestGetBits_FRoundTrip(t *testing.T) {
	type slot struct {
		n int
		v uint32
	}
	cases := [][]slot{
		{{1, 1}, {7, 0x55}, {16, 0xBEEF}, {8, 0xAB}},
		{{32, 0xDEADBEEF}, {1, 0}, {31, 0x7FFFFFFF}},
		{{4, 0xC}, {12, 0x123}, {16, 0x4567}, {1, 0}, {3, 0x5}, {16, 0xFFFF}},
		{{1, 1}, {1, 0}, {1, 1}, {1, 1}, {1, 0}, {1, 0}},
	}
	for ci, c := range cases {
		w := newBitWriter()
		for _, s := range c {
			w.f(s.n, s.v)
		}
		buf := w.bytes()
		g := NewGetBits(buf)
		for si, s := range c {
			got := g.F(s.n)
			if got != s.v {
				t.Fatalf("case %d slot %d: F(%d) = %#x, want %#x", ci, si, s.n, got, s.v)
			}
		}
		if g.Err() {
			t.Fatalf("case %d: unexpected sticky error", ci)
		}
	}
}

func TestGetBits_SUSignedRoundTrip(t *testing.T) {
	values := []struct {
		n int
		v int32
	}{
		{1, -1}, {1, 0},
		{4, -8}, {4, 7}, {4, -1}, {4, 0},
		{8, -128}, {8, 127},
		{16, -32768}, {16, 32767},
		{32, -1}, {32, 0x7FFFFFFF}, {32, -2147483648},
	}
	w := newBitWriter()
	for _, e := range values {
		w.su(e.n, e.v)
	}
	g := NewGetBits(w.bytes())
	for i, e := range values {
		got := g.SU(e.n)
		if got != e.v {
			t.Fatalf("slot %d: SU(%d) = %d, want %d", i, e.n, got, e.v)
		}
	}
}

func TestGetBits_FOnEmptyBufferSetsErr(t *testing.T) {
	g := NewGetBits(nil)
	if g.F(8) != 0 || !g.Err() {
		t.Fatalf("F on empty buffer must produce 0 and set err")
	}
	// All subsequent reads return 0.
	if g.F(1) != 0 {
		t.Fatalf("subsequent F must keep returning 0")
	}
	if g.Bit() != 0 {
		t.Fatalf("subsequent Bit must keep returning 0")
	}
}

func TestGetBits_FRefillSpanningBytes(t *testing.T) {
	// 4 bytes, request a 32-bit value: forces several refill iterations.
	g := NewGetBits([]byte{0x12, 0x34, 0x56, 0x78})
	if got := g.F(32); got != 0x12345678 {
		t.Fatalf("F(32) = %#x, want 0x12345678", got)
	}
}

func TestGetBits_FRefillRunsOutMidWindow(t *testing.T) {
	// Ask for a wider field than the buffer holds: refill must record
	// what it managed to load and still flag a sticky error. This
	// exercises the "state != 0 break" path.
	g := NewGetBits([]byte{0xAB})
	got := g.F(16)
	if !g.Err() {
		t.Fatalf("sticky error must be set after F(16) on 1-byte buffer")
	}
	// Top byte of the result must be the byte we did manage to read.
	if got>>8 != 0xAB {
		t.Fatalf("F(16) high byte = %#x, want 0xAB", got>>8)
	}
}

func TestGetBits_FPanicsOnOutOfRange(t *testing.T) {
	for _, n := range []int{0, -1, 33} {
		func(width int) {
			defer func() {
				if recover() == nil {
					t.Fatalf("F(%d) must panic", width)
				}
			}()
			NewGetBits([]byte{0}).F(width)
		}(n)
	}
}

func TestGetBits_SUPanicsOnOutOfRange(t *testing.T) {
	for _, n := range []int{0, -1, 33} {
		func(width int) {
			defer func() {
				if recover() == nil {
					t.Fatalf("SU(%d) must panic", width)
				}
			}()
			NewGetBits([]byte{0}).SU(width)
		}(n)
	}
}

// ---------------------------------------------------------------------------
// Leb128
// ---------------------------------------------------------------------------

func TestGetBits_Leb128(t *testing.T) {
	cases := []struct {
		v  uint32
		ok bool
	}{
		{0, true}, {1, true}, {127, true}, {128, true}, {16383, true},
		{16384, true}, {0xFFFFFFFF, true},
	}
	for _, c := range cases {
		w := newBitWriter()
		w.leb128(c.v)
		g := NewGetBits(w.bytes())
		got, ok := g.Leb128()
		if ok != c.ok || got != c.v {
			t.Fatalf("Leb128(%d) = (%d, %v), want (%d, %v)", c.v, got, ok, c.v, c.ok)
		}
	}
}

func TestGetBits_Leb128Overflow(t *testing.T) {
	// 8 bytes of 0xFF => continuation bit set on the last byte and the
	// accumulated value is far larger than 2^32-1.
	g := NewGetBits([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF})
	v, ok := g.Leb128()
	if ok || v != 0 {
		t.Fatalf("Leb128 overflow must return (0,false); got (%d,%v)", v, ok)
	}
	if !g.Err() {
		t.Fatalf("Leb128 overflow must set err")
	}
}

// ---------------------------------------------------------------------------
// Uniform
// ---------------------------------------------------------------------------

func TestGetBits_UniformRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 256; i++ {
		max := uint32(rng.Intn(254) + 2) // [2, 255]
		v := uint32(rng.Intn(int(max)))
		w := newBitWriter()
		w.uniform(max, v)
		g := NewGetBits(w.bytes())
		got := g.Uniform(max)
		if got != v {
			t.Fatalf("Uniform(max=%d) = %d, want %d", max, got, v)
		}
	}
}

func TestGetBits_UniformPanicsOnSmallMax(t *testing.T) {
	for _, m := range []uint32{0, 1} {
		func(max uint32) {
			defer func() {
				if recover() == nil {
					t.Fatalf("Uniform(%d) must panic", max)
				}
			}()
			NewGetBits([]byte{0xFF}).Uniform(max)
		}(m)
	}
}

// ---------------------------------------------------------------------------
// VLC
// ---------------------------------------------------------------------------

func TestGetBits_VLCRoundTrip(t *testing.T) {
	values := []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 100, 1000, 65535, 0xFFFFFE}
	for _, v := range values {
		w := newBitWriter()
		w.vlc(v)
		g := NewGetBits(w.bytes())
		got := g.VLC()
		if got != v {
			t.Fatalf("VLC roundtrip for %d returned %d", v, got)
		}
	}
}

func TestGetBits_VLCOverflow(t *testing.T) {
	// 32 leading zero bits cause the helper to bail out with the overflow
	// sentinel.
	g := NewGetBits([]byte{0, 0, 0, 0, 0xFF})
	if got := g.VLC(); got != 0xFFFFFFFF {
		t.Fatalf("VLC overflow = %#x, want 0xFFFFFFFF", got)
	}
}

// ---------------------------------------------------------------------------
// BitsSubexp
// ---------------------------------------------------------------------------

func TestGetBits_BitsSubexpDeterministicByteStreams(t *testing.T) {
	// BitsSubexp's encoder is non-trivial; rather than duplicate it we
	// pin the decoded value for a handful of fixed byte streams. Any
	// regression in the decoder will move these results.
	cases := []struct {
		data []byte
		ref  int32
		n    uint32
		want int32
	}{
		// All-zero buffer: forces the small-uniform path with v == 0
		// and exercises the "ref*2 > n" centring branch on negative ref.
		{[]byte{0x00, 0x00, 0x00, 0x00}, 0, 1, 0},
		{[]byte{0x00, 0x00, 0x00, 0x00}, -3, 1, -3},
		{[]byte{0x00, 0x00, 0x00, 0x00}, 3, 1, 3},
		// Patterned input drives the iterative growth path with n=2,3.
		{[]byte{0xAA, 0x55, 0xC3, 0x3C}, 0, 2, -3},
		{[]byte{0xAA, 0x55, 0xC3, 0x3C}, 7, 3, -2},
	}
	for i, c := range cases {
		g := NewGetBits(c.data)
		got := g.BitsSubexp(c.ref, c.n)
		if got != c.want {
			t.Fatalf("case %d BitsSubexp(ref=%d, n=%d) = %d, want %d", i, c.ref, c.n, got, c.want)
		}
	}
}

func TestGetBits_BitsSubexpDoesNotLoop(t *testing.T) {
	// Larger n_param exercises the iterative growth path inside the
	// helper. The exact value is opaque but must remain finite and signed.
	g := NewGetBits([]byte{0xAA, 0x55, 0xC3, 0x3C, 0x12, 0x34, 0x56, 0x78})
	for _, n := range []uint32{1, 2, 3, 4, 5} {
		_ = g.BitsSubexp(0, n)
	}
}

func TestInvRecenter(t *testing.T) {
	cases := []struct {
		r, v, want uint32
	}{
		// v > r<<1 branch.
		{0, 5, 5},
		{2, 7, 7},
		// v even branch: (v>>1) + r.
		{3, 0, 3},
		{3, 2, 4},
		{5, 4, 7},
		// v odd branch: r - ((v+1)>>1).
		//   invRecenter(3, 3) = 3 - 2 = 1.
		//   invRecenter(5, 1) = 5 - 1 = 4.
		//   invRecenter(5, 5) = 5 - 3 = 2.
		{3, 3, 1},
		{5, 1, 4},
		{5, 5, 2},
	}
	for _, c := range cases {
		if got := invRecenter(c.r, c.v); got != c.want {
			t.Fatalf("invRecenter(%d, %d) = %d, want %d", c.r, c.v, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// ByteAlign / BitPos / BytePos
// ---------------------------------------------------------------------------

func TestGetBits_ByteAlignAndPositions(t *testing.T) {
	g := NewGetBits([]byte{0xFF, 0x55, 0xAA})
	if g.BitPos() != 0 || g.BytePos() != 0 {
		t.Fatalf("initial positions must be zero")
	}
	g.F(3)
	if g.BitPos() != 3 {
		t.Fatalf("BitPos after F(3) = %d, want 3", g.BitPos())
	}
	g.ByteAlign()
	if g.BitPos()%8 != 0 {
		t.Fatalf("BitPos after ByteAlign must be byte-aligned, got %d", g.BitPos())
	}
	pos := g.BytePos()
	if g.F(8) != 0x55 {
		t.Fatalf("F(8) at aligned position must read 0x55")
	}
	if g.BytePos() != pos+1 {
		t.Fatalf("BytePos must advance by one after F(8)")
	}
}

func TestGetBits_BytePosPanicsOffBoundary(t *testing.T) {
	g := NewGetBits([]byte{0xFF})
	g.F(3)
	defer func() {
		if recover() == nil {
			t.Fatal("BytePos off boundary must panic")
		}
	}()
	g.BytePos()
}

// ---------------------------------------------------------------------------
// Fuzz: randomised round-trip
// ---------------------------------------------------------------------------

func FuzzGetBits_FRoundTrip(f *testing.F) {
	f.Add(uint64(0xDEADBEEFCAFEBABE))
	f.Add(uint64(0))
	f.Add(uint64(^uint64(0)))
	f.Fuzz(func(t *testing.T, seed uint64) {
		rng := rand.New(rand.NewSource(int64(seed)))
		const n = 32
		widths := make([]int, n)
		values := make([]uint32, n)
		w := newBitWriter()
		for i := 0; i < n; i++ {
			widths[i] = rng.Intn(32) + 1
			mask := uint32((uint64(1) << uint(widths[i])) - 1)
			if widths[i] == 32 {
				mask = 0xFFFFFFFF
			}
			values[i] = rng.Uint32() & mask
			w.f(widths[i], values[i])
		}
		g := NewGetBits(w.bytes())
		for i := 0; i < n; i++ {
			if got := g.F(widths[i]); got != values[i] {
				t.Fatalf("F[%d](%d) = %#x, want %#x", i, widths[i], got, values[i])
			}
		}
	})
}
