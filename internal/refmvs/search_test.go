package refmvs

import "testing"

func TestFindSpatialRowFirstTie(t *testing.T) {
	f := NewFrame(64, 64)
	dims := make([][2]uint8, 1)
	dims[0] = [2]uint8{4, 4}
	top := Block{MV: MVPair{{X: 148}, {}}, Ref: RefPair{1, -1}, BS: 0}
	left := Block{MV: MVPair{{}, {}}, Ref: RefPair{1, -1}, BS: 0}
	f.PutGridBlock(4, 0, 4, 4, top)
	f.PutGridBlock(0, 4, 4, 4, left)

	r := FindSpatial(SearchConfig{Frame: f, Ref: 1, Bx4: 4, By4: 4, Bw4: 4, Bh4: 4, BlockDims: dims})
	if r.Count != 2 || r.Candidates[0].MV[0].X != 148 {
		t.Fatalf("candidates=%+v count=%d", r.Candidates, r.Count)
	}
}

func TestFindSpatialDeduplicatesEdges(t *testing.T) {
	f := NewFrame(64, 64)
	dims := [][2]uint8{{4, 4}}
	blk := Block{MV: MVPair{{X: 8}, {}}, Ref: RefPair{1, -1}, BS: 0, MF: 2}
	f.PutGridBlock(4, 0, 4, 4, blk)
	f.PutGridBlock(0, 4, 4, 4, blk)
	r := FindSpatial(SearchConfig{Frame: f, Ref: 1, Bx4: 4, By4: 4, Bw4: 4, Bh4: 4, BlockDims: dims})
	if r.Count != 1 || !r.RowMatch || !r.ColMatch || !r.HaveNewMV || r.Candidates[0].Weight <= 640 {
		t.Fatalf("result=%+v", r)
	}
}

func TestFindSpatialSortsSecondarySeparately(t *testing.T) {
	f := NewFrame(64, 64)
	dims := [][2]uint8{{2, 2}}
	nearest := Block{MV: MVPair{{X: 8}, {}}, Ref: RefPair{1, -1}, BS: 0}
	secondary := Block{MV: MVPair{{X: 16}, {}}, Ref: RefPair{1, -1}, BS: 0}
	f.PutGridBlock(4, 2, 2, 2, nearest)
	f.PutGridBlock(1, 5, 2, 2, secondary)
	r := FindSpatial(SearchConfig{Frame: f, Ref: 1, Bx4: 4, By4: 4, Bw4: 2, Bh4: 2, BlockDims: dims})
	if r.NearestCount != 2 || r.Candidates[0].MV[0].X != 8 {
		t.Fatalf("spatial range order mismatch: %+v", r)
	}
}

func TestFindSpatialSecondaryNewMVDoesNotSetNearestContext(t *testing.T) {
	f := NewFrame(64, 64)
	f.PutGridBlock(1, 5, 1, 1, Block{Ref: RefPair{1, -1}, MV: MVPair{{X: 8}}, BS: 0, MF: 2})
	r := FindSpatial(SearchConfig{Frame: f, Ref: 1, Bx4: 4, By4: 4, Bw4: 2, Bh4: 2, BlockDims: testBlockDims()})
	if r.Count == 0 || r.HaveNewMV || r.Candidates[0].Weight < 640 {
		t.Fatalf("secondary result count=%d haveNewMV=%v, want candidate without nearest NEWMV match", r.Count, r.HaveNewMV)
	}
}

func TestFindSpatialTopRightIsStrongNearestCandidate(t *testing.T) {
	f := NewFrame(64, 64)
	f.PutGridBlock(6, 3, 1, 1, Block{
		Ref: RefPair{1, -1}, MV: MVPair{{X: 16}}, BS: 0, MF: 2,
	})
	r := FindSpatial(SearchConfig{
		Frame: f, Ref: 1, Bx4: 4, By4: 4, Bw4: 2, Bh4: 2,
		BlockDims: testBlockDims(),
	})
	if r.Count != 1 || r.NearestCount != 1 || !r.RowMatch || !r.HaveNewMV || r.Candidates[0].Weight != 644 {
		t.Fatalf("top-right result=%+v", r)
	}
}

func TestFindTemporalScalesMotionField(t *testing.T) {
	current := NewFrame(32, 32)
	current.OrderHint, current.OrderBits = 9, 5
	current.HighPrecision = true
	current.RefOrderHints[3] = 8
	source := NewFrame(32, 32)
	source.OrderHint, source.OrderBits = 8, 5
	source.RefFrameOrderHints[2] = 6
	source.RP[source.RPStride+1] = TemporalBlock{MV: MV{Y: 6, X: -4}, Ref: 3}
	projectTestSource(current, source, 3)

	mv, ok := FindTemporal(current, source, 3, 2, 2)
	if !ok || mv != (MV{Y: 3, X: -2}) {
		t.Fatalf("FindTemporal = (%+v, %v), want {3,-2}, true", mv, ok)
	}
}

func TestRelativeDistWrapsOrderHint(t *testing.T) {
	if got := RelativeDist(1, 31, 5); got != 2 {
		t.Fatalf("RelativeDist(1,31,5)=%d want 2", got)
	}
	if got := RelativeDist(31, 1, 5); got != -2 {
		t.Fatalf("RelativeDist(31,1,5)=%d want -2", got)
	}
}

func TestFindAppendsTemporalCandidate(t *testing.T) {
	current := NewFrame(32, 32)
	current.OrderHint, current.OrderBits = 9, 5
	current.HighPrecision = true
	current.RefOrderHints[3] = 8
	source := NewFrame(32, 32)
	source.OrderHint, source.OrderBits = 8, 5
	source.RefFrameOrderHints[2] = 6
	source.RP[source.RPStride+1] = TemporalBlock{MV: MV{Y: 6, X: -4}, Ref: 3}
	projectTestSource(current, source, 3)

	r := Find(SearchConfig{Frame: current, TemporalSource: source, Ref: 4, TargetSlot: 3, Bx4: 2, By4: 2, Bw4: 2, Bh4: 2, BlockDims: testBlockDims()})
	if r.Count != 1 || r.NearestCount != 0 || r.Candidates[0].MV[0] != (MV{Y: 3, X: -2}) {
		t.Fatalf("Find temporal result = %+v", r)
	}
}

func TestFindMergesMatchingSpatialAndTemporal(t *testing.T) {
	current := NewFrame(32, 32)
	current.OrderHint, current.OrderBits = 9, 5
	current.HighPrecision = true
	current.RefOrderHints[3] = 8
	current.PutGridBlock(2, 1, 2, 1, Block{Ref: RefPair{4, -1}, MV: MVPair{{Y: 3, X: -2}}, BS: 1})
	source := NewFrame(32, 32)
	source.OrderHint, source.OrderBits = 8, 5
	source.RefFrameOrderHints[2] = 6
	source.RP[source.RPStride+1] = TemporalBlock{MV: MV{Y: 6, X: -4}, Ref: 3}
	projectTestSource(current, source, 3)

	r := Find(SearchConfig{Frame: current, TemporalSource: source, Ref: 4, TargetSlot: 3, Bx4: 2, By4: 2, Bw4: 2, Bh4: 2, BlockDims: testBlockDims()})
	if r.Count != 1 || r.NearestCount != 1 || r.Candidates[0].Weight <= 640 {
		t.Fatalf("Find merged result = %+v", r)
	}
}

func TestFindScansTemporalBlockArea(t *testing.T) {
	current := NewFrame(64, 64)
	current.OrderHint, current.OrderBits = 9, 5
	current.HighPrecision = true
	current.RefOrderHints[3] = 8
	source := NewFrame(64, 64)
	source.OrderHint, source.OrderBits = 8, 5
	source.RefFrameOrderHints[2] = 6
	source.RP[2*source.RPStride+2] = TemporalBlock{MV: MV{Y: 6}, Ref: 3}
	source.RP[2*source.RPStride+3] = TemporalBlock{MV: MV{X: 8}, Ref: 3}
	projectTestSource(current, source, 3)

	r := Find(SearchConfig{Frame: current, TemporalSource: source, Ref: 4, TargetSlot: 3, Bx4: 4, By4: 4, Bw4: 4, Bh4: 4, TileX1: 16, TileY1: 16, BlockDims: testBlockDims()})
	if r.Count != 2 {
		t.Fatalf("temporal area candidate count=%d want 2", r.Count)
	}
	if r.Candidates[0].MV[0] != (MV{Y: 3}) || r.Candidates[1].MV[0] != (MV{X: 4}) {
		t.Fatalf("temporal area candidates=%+v", r.Candidates[:r.Count])
	}
}

func TestFindTemporalGlobalMVContext(t *testing.T) {
	current := NewFrame(32, 32)
	current.OrderHint, current.OrderBits, current.HighPrecision = 9, 5, true
	current.RefOrderHints[3] = 8
	source := NewFrame(32, 32)
	source.OrderHint, source.OrderBits = 8, 5
	source.RefFrameOrderHints[2] = 6
	cfg := SearchConfig{Frame: current, TemporalSource: source, Ref: 4, TargetSlot: 3, Bx4: 2, By4: 2, Bw4: 2, Bh4: 2, BlockDims: testBlockDims()}

	if got := Find(cfg).GlobalMVContext; got != 1 {
		t.Fatalf("missing temporal global context=%d want 1", got)
	}
	source.RP[source.RPStride+1] = TemporalBlock{MV: MV{X: 30}, Ref: 3}
	projectTestSource(current, source, 3)
	if got := Find(cfg).GlobalMVContext; got != 0 {
		t.Fatalf("15-away temporal global context=%d want 0", got)
	}
	source.RP[source.RPStride+1] = TemporalBlock{MV: MV{X: 32}, Ref: 3}
	projectTestSource(current, source, 3)
	if got := Find(cfg).GlobalMVContext; got != 1 {
		t.Fatalf("16-away temporal global context=%d want 1", got)
	}
}

func testBlockDims() [][2]uint8 {
	return [][2]uint8{{1, 1}, {2, 1}}
}

func projectTestSource(current, source *Frame, slot int) {
	var refs [8]*Frame
	refs[slot] = source
	current.RefSlots[1] = int8(slot)
	BuildTemporalProjection(current, refs)
}
