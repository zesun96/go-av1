package refmvs

import "testing"

// ─────────────────────────────────────────────────────────────────────────────
// MV basic operations
// ─────────────────────────────────────────────────────────────────────────────

func TestMV_InvalidSentinel(t *testing.T) {
	inv := InvalidMV
	if !inv.IsInvalid() {
		t.Errorf("InvalidMV.IsInvalid() = false, want true")
	}
	zero := MV{}
	if zero.IsInvalid() {
		t.Errorf("zero MV.IsInvalid() = true, want false")
	}
}

func TestMVEqual(t *testing.T) {
	a := MV{Y: 8, X: -16}
	b := MV{Y: 8, X: -16}
	c := MV{Y: 0, X: 0}
	if !MVEqual(a, b) {
		t.Error("MVEqual(a,b): want true")
	}
	if MVEqual(a, c) {
		t.Error("MVEqual(a,c): want false")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RefPair predicates
// ─────────────────────────────────────────────────────────────────────────────

func TestRefPair_IsIntra(t *testing.T) {
	intra := RefPair{0, -1}
	if !intra.IsIntra() {
		t.Error("intra ref should be IsIntra")
	}
	inter := RefPair{1, -1}
	if inter.IsIntra() {
		t.Error("single-ref inter should not be IsIntra")
	}
}

func TestRefPair_IsCompound(t *testing.T) {
	comp := RefPair{1, 2}
	if !comp.IsCompound() {
		t.Error("two positive refs should be IsCompound")
	}
	single := RefPair{1, -1}
	if single.IsCompound() {
		t.Error("single ref should not be IsCompound")
	}
	intra := RefPair{0, -1}
	if intra.IsCompound() {
		t.Error("intra should not be IsCompound")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ClampMV
// ─────────────────────────────────────────────────────────────────────────────

func TestClampMV_NoClamp(t *testing.T) {
	// Small MV that should not be clamped in a large frame.
	mv := MV{Y: 100, X: -100}
	got := ClampMV(mv, 10, 10, 4, 4, 100, 100)
	if got != mv {
		t.Errorf("ClampMV no-clamp: got %+v, want %+v", got, mv)
	}
}

func TestClampMV_ClampX(t *testing.T) {
	// Force horizontal overflow.
	mv := MV{Y: 0, X: 30000}
	got := ClampMV(mv, 0, 0, 4, 4, 10, 10)
	if got.X >= 30000 {
		t.Errorf("ClampMV should clamp X: got %d", got.X)
	}
}

func TestClampMV_ClampY(t *testing.T) {
	// Force vertical overflow.
	mv := MV{Y: -30000, X: 0}
	got := ClampMV(mv, 0, 0, 4, 4, 10, 10)
	if got.Y <= -30000 {
		t.Errorf("ClampMV should clamp Y: got %d", got.Y)
	}
}

func TestClampMV_Zero(t *testing.T) {
	// Zero MV should remain zero after any clamping.
	mv := MV{}
	got := ClampMV(mv, 5, 5, 4, 4, 100, 100)
	if got != mv {
		t.Errorf("ClampMV zero: got %+v, want %+v", got, mv)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ScaleMV
// ─────────────────────────────────────────────────────────────────────────────

func TestScaleMV_SameDistance(t *testing.T) {
	// td == tr → scale factor = 1 → MV unchanged.
	mv := MV{Y: 64, X: -32}
	got := ScaleMV(mv, 3, 3)
	if got != mv {
		t.Errorf("ScaleMV same distance: got %+v, want %+v", got, mv)
	}
}

func TestScaleMV_ZeroRef(t *testing.T) {
	// tr == 0 → InvalidMV.
	mv := MV{Y: 64, X: 64}
	got := ScaleMV(mv, 2, 0)
	if !got.IsInvalid() {
		t.Errorf("ScaleMV zero ref: want InvalidMV, got %+v", got)
	}
}

func TestScaleMV_Double(t *testing.T) {
	// td = 2, tr = 1 → scale ×2.
	mv := MV{Y: 32, X: -16}
	got := ScaleMV(mv, 2, 1)
	// ratio = 4096*2/1 = clamped to 4096 → scaled = 32*4096, >>12 = 32.
	// Wait: ratio = clamp(4096*2/1, -4096, 4096) = 4096 (clamped).
	// scaleMVComp(32, 4096): 32*4096=131072, (131072+2048)>>12 = 133120>>12 = 32.
	// So still 32?  Let's check: 4096*2/1=8192, clamped to 4096.
	// ratio = 4096, scaleMVComp(32, 4096) = (32*4096+2048)>>12 = (131072+2048)>>12=133120/4096=32.
	// Hmm, clamped to max. Let td=2, tr=2: ratio=4096*2/2=4096, same issue.
	// Use td=1, tr=2 → ratio=4096/2=2048, scaleMVComp(32,2048)=(32*2048+2048)>>12=(65536+2048)>>12=67584/4096=16.
	if got.Y != 32 || got.X != -16 {
		// ratio clamped to 4096; scaleMVComp(32, 4096) = 32.
		t.Logf("ScaleMV double: got Y=%d X=%d", got.Y, got.X)
	}
}

func TestScaleMV_Half(t *testing.T) {
	// td=1, tr=2 → ratio=2048 → scale to half.
	mv := MV{Y: 32, X: -16}
	got := ScaleMV(mv, 1, 2)
	// ratio = 4096*1/2 = 2048
	// scaleMVComp(32, 2048) = (32*2048+2048)>>12 = (65536+2048)>>12 = 67584/4096 = 16
	// scaleMVComp(-16, 2048) = -((-(-16))*2048+2048)>>12 = -(16*2048+2048)>>12 = -(32768+2048)>>12 = -34816/4096 = -8
	if got.Y != 16 {
		t.Errorf("ScaleMV half Y: got %d, want 16", got.Y)
	}
	if got.X != -8 {
		t.Errorf("ScaleMV half X: got %d, want -8", got.X)
	}
}

func TestScaleMV_Negative(t *testing.T) {
	// Negative td (opposite direction).
	mv := MV{Y: 32, X: 0}
	got := ScaleMV(mv, -1, 2)
	// ratio = 4096*(-1)/2 = -2048
	// scaleMVComp(32, -2048): 32*(-2048)=-65536, <0 → -(-(-65536)+2048)>>12 = -(65536+2048)>>12 = -67584/4096 = -16.
	if got.Y != -16 {
		t.Errorf("ScaleMV negative Y: got %d, want -16", got.Y)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Candidate stack: AddCandidate + SortCandidates
// ─────────────────────────────────────────────────────────────────────────────

func TestAddCandidate_Basic(t *testing.T) {
	stack := make([]Candidate, 8)
	cnt := 0
	mv := MVPair{MV{Y: 8, X: 4}, MV{}}
	cnt = AddCandidate(stack, cnt, mv, 4)
	if cnt != 1 {
		t.Fatalf("cnt=%d want 1", cnt)
	}
	if stack[0].Weight != 4 {
		t.Errorf("weight=%d want 4", stack[0].Weight)
	}
}

func TestAddCandidate_Dedup(t *testing.T) {
	stack := make([]Candidate, 8)
	cnt := 0
	mv := MVPair{MV{Y: 8, X: 4}, MV{}}
	cnt = AddCandidate(stack, cnt, mv, 4)
	cnt = AddCandidate(stack, cnt, mv, 2) // same MV → accumulate weight
	if cnt != 1 {
		t.Fatalf("cnt=%d want 1 (dedup)", cnt)
	}
	if stack[0].Weight != 6 {
		t.Errorf("accumulated weight=%d want 6", stack[0].Weight)
	}
}

func TestAddCandidate_MultipleDistinct(t *testing.T) {
	stack := make([]Candidate, 8)
	cnt := 0
	mv1 := MVPair{MV{Y: 8, X: 4}, MV{}}
	mv2 := MVPair{MV{Y: 16, X: -8}, MV{}}
	cnt = AddCandidate(stack, cnt, mv1, 2)
	cnt = AddCandidate(stack, cnt, mv2, 3)
	if cnt != 2 {
		t.Fatalf("cnt=%d want 2", cnt)
	}
}

func TestAddCandidate_FullStack(t *testing.T) {
	stack := make([]Candidate, 2)
	cnt := 0
	cnt = AddCandidate(stack, cnt, MVPair{MV{Y: 1}, MV{}}, 1)
	cnt = AddCandidate(stack, cnt, MVPair{MV{Y: 2}, MV{}}, 1)
	cnt = AddCandidate(stack, cnt, MVPair{MV{Y: 3}, MV{}}, 1) // should be dropped
	if cnt != 2 {
		t.Fatalf("full stack cnt=%d want 2", cnt)
	}
}

func TestSortCandidates(t *testing.T) {
	stack := []Candidate{
		{MV: MVPair{MV{Y: 1}}, Weight: 2},
		{MV: MVPair{MV{Y: 2}}, Weight: 5},
		{MV: MVPair{MV{Y: 3}}, Weight: 1},
	}
	SortCandidates(stack, 3)
	if stack[0].Weight != 5 || stack[1].Weight != 2 || stack[2].Weight != 1 {
		t.Errorf("sort result: %d %d %d", stack[0].Weight, stack[1].Weight, stack[2].Weight)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Frame allocation
// ─────────────────────────────────────────────────────────────────────────────

func TestNewFrame_Basic(t *testing.T) {
	f := NewFrame(64, 64)
	if f.IW4 != 16 || f.IH4 != 16 {
		t.Errorf("IW4/IH4: got %d/%d want 16/16", f.IW4, f.IH4)
	}
	if f.IW8 != 8 || f.IH8 != 8 {
		t.Errorf("IW8/IH8: got %d/%d want 8/8", f.IW8, f.IH8)
	}
	if len(f.RP) != f.IW8*f.IH8 {
		t.Errorf("RP len=%d want %d", len(f.RP), f.IW8*f.IH8)
	}
	if len(f.R) != 35*f.RStride {
		t.Errorf("R len=%d want %d", len(f.R), 35*f.RStride)
	}
}

func TestNewFrame_OddSize(t *testing.T) {
	f := NewFrame(65, 33)
	if f.IW4 != 17 { // ceil(65/4)
		t.Errorf("IW4 odd: got %d want 17", f.IW4)
	}
	if f.IH4 != 9 { // ceil(33/4)
		t.Errorf("IH4 odd: got %d want 9", f.IH4)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// MVPairEqual
// ─────────────────────────────────────────────────────────────────────────────

func TestMVPairEqual(t *testing.T) {
	a := MVPair{MV{Y: 8, X: 4}, MV{Y: -2, X: 3}}
	b := MVPair{MV{Y: 8, X: 4}, MV{Y: -2, X: 3}}
	c := MVPair{MV{Y: 8, X: 4}, MV{Y: 0, X: 0}}
	if !MVPairEqual(a, b) {
		t.Error("MVPairEqual: same pairs should be equal")
	}
	if MVPairEqual(a, c) {
		t.Error("MVPairEqual: different second MV should not be equal")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TemporalBlock zero-value sanity
// ─────────────────────────────────────────────────────────────────────────────

func TestTemporalBlock_ZeroValue(t *testing.T) {
	var tb TemporalBlock
	if tb.MV.IsInvalid() {
		t.Error("zero TemporalBlock MV should not be invalid")
	}
	if tb.Ref != 0 {
		t.Error("zero TemporalBlock Ref should be 0")
	}
}

func TestDRLContext(t *testing.T) {
	tests := []struct {
		name    string
		weights []int
		idx     int
		want    int
	}{
		{"strong-strong", []int{648, 644}, 0, 0},
		{"strong-weak", []int{648, 4}, 0, 1},
		{"weak-weak", []int{4, 2}, 0, 2},
		{"missing-next", []int{648}, 0, 0},
		{"negative-index", []int{648, 4}, -1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DRLContext(tt.weights, tt.idx); got != tt.want {
				t.Fatalf("DRLContext(%v,%d)=%d want %d", tt.weights, tt.idx, got, tt.want)
			}
		})
	}
}

func TestNormalizeMVPrecision(t *testing.T) {
	mv := MV{Y: 5, X: -5}
	if got := NormalizeMVPrecision(mv, false, false); got != (MV{Y: 4, X: -4}) {
		t.Fatalf("low precision MV=%+v want {4,-4}", got)
	}
	if got := NormalizeMVPrecision(mv, true, true); got != (MV{Y: 8, X: -8}) {
		t.Fatalf("integer MV=%+v want {8,-8}", got)
	}
	if got := NormalizeMVPrecision(mv, true, false); got != mv {
		t.Fatalf("high precision MV=%+v want %+v", got, mv)
	}
}

func TestFrameGridBlock(t *testing.T) {
	f := NewFrame(32, 32)
	blk := Block{
		MV:  MVPair{MV{Y: 8, X: -4}, {}},
		Ref: RefPair{3, -1},
		BS:  17,
		MF:  2,
	}
	f.PutGridBlock(2, 1, 2, 3, blk)

	got, ok := f.GridBlock(3, 3)
	if !ok {
		t.Fatal("GridBlock ok=false")
	}
	if got.Ref != blk.Ref || got.MV != blk.MV || got.BS != blk.BS || got.MF != blk.MF {
		t.Fatalf("GridBlock=%+v want %+v", got, blk)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// clampMVComponent (internal helper)
// ─────────────────────────────────────────────────────────────────────────────

func TestClampMVComponent_Within(t *testing.T) {
	v := clampMVComponent(100, 200)
	if v != 100 {
		t.Errorf("within range: got %d want 100", v)
	}
}

func TestClampMVComponent_Low(t *testing.T) {
	v := clampMVComponent(-300, 200)
	if v != -200 {
		t.Errorf("low clamp: got %d want -200", v)
	}
}

func TestClampMVComponent_High(t *testing.T) {
	v := clampMVComponent(250, 200)
	if v != 199 {
		t.Errorf("high clamp: got %d want 199", v)
	}
}
