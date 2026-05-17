package bitstream

import (
	"math/rand"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeUniformDav1dCDF returns a uniform dav1d-style inverse CDF for n
// symbols. Slot n holds the per-context counter and starts at zero.
func makeUniformDav1dCDF(n int) []uint16 {
	cdf := make([]uint16, n+1)
	for i := 0; i < n-1; i++ {
		// dav1d cdf[i] holds 32768 - (i+1) * 32768 / n.
		cdf[i] = uint16(32768 - (i+1)*32768/n)
	}
	cdf[n-1] = 0
	cdf[n] = 0
	return cdf
}

// dav1dToICDF converts a dav1d "inverse" CDF prefix of length n into the
// SVT/aom encoder layout (which is identical: cdf[n-1] == 0, monotonically
// decreasing). The function exists to make round-trip test code read
// symmetrically with the decoder side.
func dav1dToICDF(cdf []uint16, n int) []uint16 {
	icdf := make([]uint16, n)
	copy(icdf, cdf[:n])
	return icdf
}

// ---------------------------------------------------------------------------
// BoolEqui / Bool
// ---------------------------------------------------------------------------

func TestMSAC_BoolEquiRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	const n = 4096
	bits := make([]uint32, n)
	enc := newMSACEncoder()
	for i := range bits {
		bits[i] = uint32(rng.Intn(2))
		enc.encodeBoolEqui(bits[i])
	}
	dec := NewMSAC(enc.done(), false)
	for i, want := range bits {
		got := dec.BoolEqui()
		if got != want {
			t.Fatalf("BoolEqui[%d] = %d, want %d", i, got, want)
		}
	}
}

func TestMSAC_BoolRoundTripAcrossProbabilities(t *testing.T) {
	probs := []uint32{1 << 7, 1 << 10, 16384, 24576, 32000, 32767}
	for _, f := range probs {
		rng := rand.New(rand.NewSource(int64(f)))
		const n = 1024
		bits := make([]uint32, n)
		enc := newMSACEncoder()
		for i := range bits {
			bits[i] = uint32(rng.Intn(2))
			enc.encodeBoolQ15(bits[i], f)
		}
		dec := NewMSAC(enc.done(), false)
		for i, want := range bits {
			got := dec.Bool(f)
			if got != want {
				t.Fatalf("f=%d Bool[%d] = %d, want %d", f, i, got, want)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Symbol / SymbolAdapt
// ---------------------------------------------------------------------------

func TestMSAC_SymbolRoundTrip(t *testing.T) {
	for _, n := range []int{2, 3, 4, 5, 8, 12, 15} {
		rng := rand.New(rand.NewSource(int64(n)))
		const samples = 256
		seq := make([]uint32, samples)
		enc := newMSACEncoder()
		cdfDec := makeUniformDav1dCDF(n)
		icdf := dav1dToICDF(cdfDec, n)
		for i := range seq {
			seq[i] = uint32(rng.Intn(n))
			enc.encodeCDFQ15(int(seq[i]), icdf, n)
		}
		dec := NewMSAC(enc.done(), true) // CDF updates disabled to match
		for i, want := range seq {
			got := dec.Symbol(cdfDec, n)
			if got != want {
				t.Fatalf("n=%d Symbol[%d] = %d, want %d", n, i, got, want)
			}
		}
	}
}

func TestMSAC_SymbolAdaptKeepsParity(t *testing.T) {
	// The encoder side does NOT model adaptation; so to test SymbolAdapt
	// we only verify it returns the same sequence as Symbol when
	// allowUpdateCDF is forced off after construction. This guards the
	// "skip adaptation" path explicitly.
	const n = 4
	cdf := makeUniformDav1dCDF(n)
	icdf := dav1dToICDF(cdf, n)
	enc := newMSACEncoder()
	for _, s := range []int{0, 1, 2, 3, 1, 2, 0, 3} {
		enc.encodeCDFQ15(s, icdf, n)
	}
	dec := NewMSAC(enc.done(), false)
	dec.SetAllowUpdateCDF(false)
	if dec.AllowUpdateCDF() {
		t.Fatal("SetAllowUpdateCDF(false) ignored")
	}
	want := []uint32{0, 1, 2, 3, 1, 2, 0, 3}
	for i, w := range want {
		got := dec.SymbolAdapt(cdf, n)
		if got != w {
			t.Fatalf("SymbolAdapt[%d] = %d, want %d", i, got, w)
		}
	}
}

func TestMSAC_SymbolAdaptUpdatesCDF(t *testing.T) {
	// Drive SymbolAdapt with adaptation on and check that the CDF table
	// actually changes (we don't assert exact values, just that the
	// counter increments and the probabilities shift after several reads).
	const n = 4
	cdf := makeUniformDav1dCDF(n)
	icdf := dav1dToICDF(cdf, n)
	enc := newMSACEncoder()
	for i := 0; i < 16; i++ {
		enc.encodeCDFQ15(0, icdf, n) // bias toward symbol 0
	}
	dec := NewMSAC(enc.done(), false)
	for i := 0; i < 16; i++ {
		if dec.SymbolAdapt(cdf, n) != 0 {
			t.Fatalf("expected biased decode of symbol 0 at i=%d", i)
		}
	}
	if cdf[n] == 0 {
		t.Fatal("counter slot must increment after adaptive decodes")
	}
	// Counter is capped at 32.
	if cdf[n] > 32 {
		t.Fatalf("counter overran cap: %d", cdf[n])
	}
}

func TestMSAC_SymbolPanicsOnBadN(t *testing.T) {
	for _, n := range []int{0, 16} {
		func(nn int) {
			defer func() {
				if recover() == nil {
					t.Fatalf("Symbol n=%d must panic", nn)
				}
			}()
			NewMSAC([]byte{0, 0, 0, 0}, true).Symbol(make([]uint16, nn+1), nn)
		}(n)
	}
}

// ---------------------------------------------------------------------------
// BoolAdapt
// ---------------------------------------------------------------------------

func TestMSAC_BoolAdaptUpdatesCounter(t *testing.T) {
	cdf := []uint16{16384, 0}
	enc := newMSACEncoder()
	for i := 0; i < 32; i++ {
		enc.encodeBoolQ15(1, 16384)
	}
	dec := NewMSAC(enc.done(), false)
	for i := 0; i < 32; i++ {
		if dec.BoolAdapt(cdf) != 1 {
			t.Fatalf("BoolAdapt[%d] expected 1", i)
		}
	}
	if cdf[1] == 0 {
		t.Fatal("BoolAdapt must update its counter")
	}
}

func TestMSAC_BoolAdaptDisabled(t *testing.T) {
	cdf := []uint16{16384, 0}
	enc := newMSACEncoder()
	enc.encodeBoolQ15(0, 16384)
	dec := NewMSAC(enc.done(), true) // disable updates
	if dec.BoolAdapt(cdf) != 0 {
		t.Fatal("BoolAdapt with disabled updates should still decode correctly")
	}
	if cdf[0] != 16384 || cdf[1] != 0 {
		t.Fatal("BoolAdapt must not modify CDF when updates are disabled")
	}
}

// ---------------------------------------------------------------------------
// Bools / Uniform / Subexp
// ---------------------------------------------------------------------------

func TestMSAC_BoolsRoundTrip(t *testing.T) {
	enc := newMSACEncoder()
	enc.encodeBools(0xA5, 8)
	enc.encodeBools(0x12345, 17)
	dec := NewMSAC(enc.done(), true)
	if got := dec.Bools(8); got != 0xA5 {
		t.Fatalf("Bools(8) = %#x", got)
	}
	if got := dec.Bools(17); got != 0x12345 {
		t.Fatalf("Bools(17) = %#x", got)
	}
}

func TestMSAC_UniformRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(2026))
	enc := newMSACEncoder()
	type sample struct{ n, v uint32 }
	const count = 64
	samples := make([]sample, count)
	for i := range samples {
		samples[i].n = uint32(rng.Intn(254) + 2) // [2, 255]
		samples[i].v = uint32(rng.Intn(int(samples[i].n)))
		enc.encodeUniform(samples[i].v, samples[i].n)
	}
	dec := NewMSAC(enc.done(), true)
	for i, s := range samples {
		got := dec.Uniform(s.n)
		if got != s.v {
			t.Fatalf("Uniform(n=%d)[%d] = %d, want %d", s.n, i, got, s.v)
		}
	}
}

func TestMSAC_UniformSingleton(t *testing.T) {
	dec := NewMSAC([]byte{0xFF, 0xFF}, true)
	if dec.Uniform(1) != 0 {
		t.Fatal("Uniform(1) must return 0 without consuming bits")
	}
}

func TestMSAC_UniformPanicsOnZero(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Uniform(0) must panic")
		}
	}()
	NewMSAC([]byte{0xFF}, true).Uniform(0)
}

func TestMSAC_SubexpPanicsOnBadN(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Subexp with n != 8<<k must panic")
		}
	}()
	NewMSAC([]byte{0xFF, 0xFF}, true).Subexp(0, 7, 0)
}

func TestMSAC_SubexpDoesNotPanic(t *testing.T) {
	// Validate every legal (k, n=8<<k) pairing executes without diverging.
	for k := uint32(0); k <= 4; k++ {
		n := int32(8) << k
		dec := NewMSAC([]byte{0x12, 0x34, 0x56, 0x78, 0x9A, 0xBC, 0xDE, 0xF0}, true)
		v := dec.Subexp(n/2, n, k)
		if v < 0 || v >= n {
			t.Fatalf("Subexp(k=%d) = %d, must be in [0, %d)", k, v, n)
		}
	}
	// Also exercise the high-ref branch (ref*2 > n).
	dec := NewMSAC([]byte{0x12, 0x34, 0x56, 0x78}, true)
	v := dec.Subexp(7, 8, 0)
	if v < 0 || v >= 8 {
		t.Fatalf("Subexp high-ref returned %d", v)
	}
}

// ---------------------------------------------------------------------------
// HiTok
// ---------------------------------------------------------------------------

func TestMSAC_HiTokWalksBranches(t *testing.T) {
	const branches = 4
	const samples = 32
	cdfRef := makeUniformDav1dCDF(branches)
	icdf := dav1dToICDF(cdfRef, branches)
	rng := rand.New(rand.NewSource(33))
	enc := newMSACEncoder()
	expectedToks := make([]uint32, 0, samples)
	for i := 0; i < samples; i++ {
		// Each Hi-Tok decode performs up to four 4-symbol Symbol reads.
		// The reference walks the same decision tree, so we mirror it.
		var tok uint32
		var sub uint32
		sub = uint32(rng.Intn(4))
		enc.encodeCDFQ15(int(sub), icdf, branches)
		tok = 3 + sub
		if sub == 3 {
			sub = uint32(rng.Intn(4))
			enc.encodeCDFQ15(int(sub), icdf, branches)
			tok = 6 + sub
			if sub == 3 {
				sub = uint32(rng.Intn(4))
				enc.encodeCDFQ15(int(sub), icdf, branches)
				tok = 9 + sub
				if sub == 3 {
					sub = uint32(rng.Intn(4))
					enc.encodeCDFQ15(int(sub), icdf, branches)
					tok = 12 + sub
				}
			}
		}
		expectedToks = append(expectedToks, tok)
	}
	dec := NewMSAC(enc.done(), true)
	cdf := makeUniformDav1dCDF(branches)
	for i, want := range expectedToks {
		got := dec.HiTok(cdf)
		if got != want {
			t.Fatalf("HiTok[%d] = %d, want %d", i, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// AllowUpdateCDF toggle and end-of-buffer behaviour
// ---------------------------------------------------------------------------

func TestMSAC_AllowUpdateCDFToggle(t *testing.T) {
	dec := NewMSAC([]byte{0x80}, false)
	if !dec.AllowUpdateCDF() {
		t.Fatal("expected adaptation enabled by default")
	}
	dec.SetAllowUpdateCDF(false)
	if dec.AllowUpdateCDF() {
		t.Fatal("AllowUpdateCDF flag did not flip off")
	}
	dec.SetAllowUpdateCDF(true)
	if !dec.AllowUpdateCDF() {
		t.Fatal("AllowUpdateCDF flag did not flip back on")
	}
}

func TestMSAC_EndOfBufferReturnsValidValues(t *testing.T) {
	// Decode well past the end of a tiny buffer; the decoder must not
	// panic and must keep returning legal values.
	dec := NewMSAC([]byte{0xAA}, true)
	for i := 0; i < 64; i++ {
		_ = dec.BoolEqui()
	}
}

// ---------------------------------------------------------------------------
// Edge / branch coverage top-ups
// ---------------------------------------------------------------------------

func TestMSAC_SymbolAdaptCounterClamps(t *testing.T) {
	// Push the per-context counter past 32 so the increment branch is
	// skipped on later calls; this is the only path that exercises the
	// "counter saturated" arm of SymbolAdapt.
	const n = 4
	cdf := makeUniformDav1dCDF(n)
	icdf := dav1dToICDF(cdf, n)
	enc := newMSACEncoder()
	const rounds = 64
	for i := 0; i < rounds; i++ {
		enc.encodeCDFQ15(0, icdf, n)
	}
	dec := NewMSAC(enc.done(), false)
	for i := 0; i < rounds; i++ {
		if got := dec.SymbolAdapt(cdf, n); got != 0 {
			t.Fatalf("adaptive decode[%d] = %d, want 0", i, got)
		}
	}
	if cdf[n] != 32 {
		t.Fatalf("counter must clamp at 32, got %d", cdf[n])
	}
}

func TestMSAC_SymbolAdaptDisabledShortCircuits(t *testing.T) {
	// Disable adaptation up front so SymbolAdapt's early-return path is
	// covered without any CDF mutation.
	const n = 4
	cdf := makeUniformDav1dCDF(n)
	icdf := dav1dToICDF(cdf, n)
	enc := newMSACEncoder()
	enc.encodeCDFQ15(2, icdf, n)
	dec := NewMSAC(enc.done(), true)
	snapshot := append([]uint16(nil), cdf...)
	if dec.SymbolAdapt(cdf, n) != 2 {
		t.Fatal("disabled SymbolAdapt must still decode")
	}
	for i, v := range cdf {
		if v != snapshot[i] {
			t.Fatalf("cdf[%d] mutated while updates disabled", i)
		}
	}
}

func TestMSAC_BoolAdaptCounterClamps(t *testing.T) {
	// Run more decodes than the counter cap so the "count >= 32"
	// branch fires.
	cdf := []uint16{16384, 0}
	enc := newMSACEncoder()
	const rounds = 64
	for i := 0; i < rounds; i++ {
		enc.encodeBoolQ15(0, 16384)
	}
	dec := NewMSAC(enc.done(), false)
	for i := 0; i < rounds; i++ {
		if dec.BoolAdapt(cdf) != 0 {
			t.Fatalf("BoolAdapt[%d] expected 0", i)
		}
	}
	if cdf[1] != 32 {
		t.Fatalf("BoolAdapt counter must clamp at 32, got %d", cdf[1])
	}
}

func TestMSAC_HiTokDeepBranches(t *testing.T) {
	// Construct a token sequence that hits the deepest branch (tok_br == 3
	// in the third nested decode), so HiTok's final "tok = 12 + ..." line
	// runs and the function-level coverage hits 100%.
	const branches = 4
	cdfRef := makeUniformDav1dCDF(branches)
	icdf := dav1dToICDF(cdfRef, branches)
	enc := newMSACEncoder()
	// Emit the canonical "escape three times then payload 2" sequence.
	enc.encodeCDFQ15(3, icdf, branches)
	enc.encodeCDFQ15(3, icdf, branches)
	enc.encodeCDFQ15(3, icdf, branches)
	enc.encodeCDFQ15(2, icdf, branches)
	// Plus one shallow tok to exercise the early-return path.
	enc.encodeCDFQ15(1, icdf, branches)
	dec := NewMSAC(enc.done(), true)
	cdf := makeUniformDav1dCDF(branches)
	if got := dec.HiTok(cdf); got != 14 { // 12 + 2
		t.Fatalf("deep HiTok = %d, want 14", got)
	}
	if got := dec.HiTok(cdf); got != 4 { // 3 + 1
		t.Fatalf("shallow HiTok = %d, want 4", got)
	}
}

func TestMSAC_SubexpCoversAllInternalBranches(t *testing.T) {
	// Force every internal branch of Subexp by sweeping byte streams that
	// drive different BoolEqui outcomes for the (a, k++) decisions, plus
	// a low-ref and high-ref centring path for each k.
	inputs := [][]byte{
		{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		{0xA5, 0x5A, 0x33, 0xCC, 0x12, 0x34, 0x56, 0x78},
		{0x80, 0x40, 0x20, 0x10, 0x08, 0x04, 0x02, 0x01},
	}
	for _, data := range inputs {
		for k := uint32(0); k <= 3; k++ {
			n := int32(8) << k
			// low-ref branch (ref*2 <= n)
			dec := NewMSAC(data, true)
			v := dec.Subexp(0, n, k)
			if v < 0 || v >= n {
				t.Fatalf("low-ref Subexp(k=%d) = %d, must be in [0, %d)", k, v, n)
			}
			// mid-ref branch
			dec = NewMSAC(data, true)
			v = dec.Subexp(n/2, n, k)
			if v < 0 || v >= n {
				t.Fatalf("mid-ref Subexp(k=%d) = %d, must be in [0, %d)", k, v, n)
			}
			// high-ref branch (ref*2 > n)
			dec = NewMSAC(data, true)
			v = dec.Subexp(n-1, n, k)
			if v < 0 || v >= n {
				t.Fatalf("high-ref Subexp(k=%d) = %d, must be in [0, %d)", k, v, n)
			}
		}
	}
}

func TestMSAC_RefillStopsAtBufferEdge(t *testing.T) {
	// Cover the early-out path inside refill() where the underlying buffer
	// runs out mid-window: a 1-byte buffer plus heavy decoding forces the
	// padding branch to execute.
	dec := NewMSAC([]byte{0x55}, true)
	for i := 0; i < 256; i++ {
		_ = dec.BoolEqui()
		_ = dec.Bool(20000)
	}
}

func TestMSAC_SymbolAdaptUpdatesNonZeroVal(t *testing.T) {
	// SymbolAdapt has two adaptation loops: i<val and i>=val. The other
	// adaptive tests bias the encoder to symbol 0 so the i<val loop
	// never executes. The encoder side does not model adaptation, so we
	// only need a single decode of a non-zero symbol and then assert that
	// (a) the symbol came back correctly and (b) cdf entries strictly
	// before val moved upward, exercising the i<val arm.
	const n = 4
	cdf := makeUniformDav1dCDF(n)
	icdf := dav1dToICDF(cdf, n)
	prior := append([]uint16(nil), cdf...)
	enc := newMSACEncoder()
	enc.encodeCDFQ15(2, icdf, n)
	dec := NewMSAC(enc.done(), false)
	if got := dec.SymbolAdapt(cdf, n); got != 2 {
		t.Fatalf("SymbolAdapt = %d, want 2", got)
	}
	// i<val loop: cdf[0] and cdf[1] must have strictly increased toward 32768.
	for i := 0; i < 2; i++ {
		if cdf[i] <= prior[i] {
			t.Fatalf("cdf[%d] = %d, expected to increase past prior=%d", i, cdf[i], prior[i])
		}
	}
	// Counter slot must have advanced once.
	if cdf[n] != 1 {
		t.Fatalf("counter = %d, want 1", cdf[n])
	}
}
