package tile

import (
	"testing"

	"github.com/zesun96/go-av1/internal/bitstream"
	"github.com/zesun96/go-av1/internal/header"
	predinter "github.com/zesun96/go-av1/internal/predict/inter"
	"github.com/zesun96/go-av1/internal/refmvs"
)

func TestCompoundFlagPresent(t *testing.T) {
	fh := &header.FrameHeader{SwitchableCompRefs: 1}
	if !compoundFlagPresent(fh, 0, 64, 64) {
		t.Fatal("64x64 switchable inter block should code compflag")
	}
	if compoundFlagPresent(fh, 0, 8, 64) {
		t.Fatal("8xN block must not code compflag")
	}
	fh.Segmentation.Enabled = 1
	fh.Segmentation.SegData.D[2].Ref = 1
	if compoundFlagPresent(fh, 2, 64, 64) {
		t.Fatal("segment-forced reference must not code compflag")
	}
}

func TestCompoundFlagContextWithoutNeighbours(t *testing.T) {
	if got := compoundFlagContext(NewFrameState(64, 64), 0, 0); got != 1 {
		t.Fatalf("compound context=%d want 1", got)
	}
}

func TestSplitMV8(t *testing.T) {
	tests := []struct {
		mv       int
		wantPix  int
		wantFrac int
	}{
		{0, 0, 0},
		{8, 1, 0},
		{5, 0, 10},
		{-1, -1, 14},
		{-8, -1, 0},
		{-9, -2, 14},
	}
	for _, tc := range tests {
		pix, frac := splitMV8(tc.mv)
		if pix != tc.wantPix || frac != tc.wantFrac {
			t.Fatalf("splitMV8(%d)=(%d,%d) want (%d,%d)", tc.mv, pix, frac, tc.wantPix, tc.wantFrac)
		}
	}
}

func TestInterFilter2D(t *testing.T) {
	tests := []struct {
		modeH header.FilterMode
		modeV header.FilterMode
		want  predinter.Filter2D
	}{
		{header.FilterMode8TapRegular, header.FilterMode8TapRegular, predinter.Filter2D8TapRegular},
		{header.FilterMode8TapRegular, header.FilterMode8TapSmooth, predinter.Filter2D8TapRegularSmooth},
		{header.FilterMode8TapSharp, header.FilterMode8TapRegular, predinter.Filter2D8TapSharpRegular},
		{header.FilterMode8TapSmooth, header.FilterMode8TapSharp, predinter.Filter2D8TapSmoothSharp},
		{header.FilterModeBilinear, header.FilterMode8TapRegular, predinter.Filter2DBilinear},
		{header.FilterModeSwitchable, header.FilterModeSwitchable, predinter.Filter2D8TapRegular},
	}
	for _, tc := range tests {
		if got := interFilter2D(tc.modeH, tc.modeV); got != tc.want {
			t.Fatalf("interFilter2D(%d,%d)=%d want %d", tc.modeH, tc.modeV, got, tc.want)
		}
	}
}

func TestFrameRefSlot(t *testing.T) {
	fhdr := &header.FrameHeader{}
	fhdr.Refidx = [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0}

	tests := []struct {
		refFrame int
		wantSlot int
		wantOK   bool
	}{
		{1, 6, true},
		{4, 3, true},
		{7, 0, true},
		{0, -1, false},
		{8, -1, false},
	}
	for _, tc := range tests {
		gotSlot, gotOK := frameRefSlot(fhdr, tc.refFrame)
		if gotSlot != tc.wantSlot || gotOK != tc.wantOK {
			t.Fatalf("frameRefSlot(%d)=(%d,%v) want (%d,%v)", tc.refFrame, gotSlot, gotOK, tc.wantSlot, tc.wantOK)
		}
	}
}

func TestSlotRefFrame(t *testing.T) {
	fhdr := &header.FrameHeader{}
	fhdr.Refidx = [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0}
	got, ok := slotRefFrame(fhdr, 4)
	if !ok || got != 3 {
		t.Fatalf("slotRefFrame(4)=(%d,%v) want (3,true)", got, ok)
	}
}

func TestDeriveInterFallback(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx:           [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
		SubpelFilterMode: header.FilterModeBilinear,
		HP:               1,
	}
	fhdr.Segmentation.Enabled = 1
	fhdr.Segmentation.SegData.D[2].Ref = 3 // third ref-frame enum -> Refidx[2] -> slot 4
	fhdr.GMV[2] = header.WarpedMotionParams{
		Type:   header.WMTypeTranslation,
		Matrix: [6]int32{16 << 13, -8 << 13, 1 << 16, 0, 0, 1 << 16},
	}
	fb := &FrameBuf{}
	fb.Refs[4] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}

	refSlot, refFrame, refOrder, mv, filterMode, interMode, skipMode, ref := deriveInterFallback(nil, fb, fhdr, 2, false, 0, 0)
	if ref == nil {
		t.Fatal("deriveInterFallback ref=nil")
	}
	if refSlot != 4 || refOrder != 2 {
		t.Fatalf("deriveInterFallback ref=(%d,%d) want (4,2)", refSlot, refOrder)
	}
	if refFrame != 3 {
		t.Fatalf("refFrame=%d want 3", refFrame)
	}
	if filterMode != header.FilterModeBilinear {
		t.Fatalf("filterMode=%d want bilinear", filterMode)
	}
	if interMode != InterModeGlobalMV || skipMode {
		t.Fatalf("interMode/skipMode=(%d,%v) want global,false", interMode, skipMode)
	}
	if mv.X != 16 || mv.Y != -8 {
		t.Fatalf("mv=(%d,%d) want (-8,16) in Y,X order", mv.Y, mv.X)
	}
}

func TestDeriveInterFallbackSkipModeAndNeighbour(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx:           [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
		SubpelFilterMode: header.FilterMode8TapRegular,
		SkipModeEnabled:  1,
		SkipModeRefs:     [2]int8{1, 4},
	}
	fb := &FrameBuf{}
	fb.Refs[5] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fb.Refs[2] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}

	fs := NewFrameState(32, 32)
	fs.SetInterBlock(0, 0, 8, 8, false, 0, 2, 5, uint8(header.FilterMode8TapRegular), uint8(header.FilterMode8TapRegular), InterModeNearestMV, refmvs.MV{Y: 12, X: -20})

	refSlot, refFrame, refOrder, mv, _, interMode, skipMode, ref := deriveInterFallback(fs, fb, fhdr, 0, true, 8, 0)
	if ref == nil {
		t.Fatal("skip-mode derive ref=nil")
	}
	if refSlot != 5 || refOrder != 1 || !skipMode {
		t.Fatalf("skip-mode derive=(%d,%d,%v) want (5,1,true)", refSlot, refOrder, skipMode)
	}
	if refFrame != 2 {
		t.Fatalf("skip-mode refFrame=%d want 2", refFrame)
	}
	if interMode != InterModeZeroMV || mv.X != 0 || mv.Y != 0 {
		t.Fatalf("skip-mode inter=(%d,%d,%d) want zero/0/0", interMode, mv.Y, mv.X)
	}

	refSlot, refFrame, refOrder, mv, _, interMode, skipMode, ref = deriveInterFallback(fs, fb, fhdr, 0, false, 8, 0)
	if ref == nil {
		t.Fatal("neighbour derive ref=nil")
	}
	if refSlot != 2 || refOrder != 4 || skipMode {
		t.Fatalf("neighbour derive=(%d,%d,%v) want (2,4,false)", refSlot, refOrder, skipMode)
	}
	if refFrame != 5 {
		t.Fatalf("neighbour refFrame=%d want 5", refFrame)
	}
	if interMode != InterModeNearestMV || mv.Y != 12 || mv.X != -20 {
		t.Fatalf("neighbour mv=(mode=%d y=%d x=%d) want nearest/12/-20", interMode, mv.Y, mv.X)
	}
}

func TestDeriveInterFallbackForceIntegerMV(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx:           [header.RefsPerFrame]int8{0, 1, 2, 3, 4, 5, 6},
		SubpelFilterMode: header.FilterMode8TapRegular,
		ForceIntegerMV:   1,
		HP:               1,
	}
	fhdr.GMV[0] = header.WarpedMotionParams{
		Type:   header.WMTypeTranslation,
		Matrix: [6]int32{13 << 13, -11 << 13, 1 << 16, 0, 0, 1 << 16},
	}
	fb := &FrameBuf{}
	fb.Refs[0] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}

	_, _, _, mv, _, interMode, _, ref := deriveInterFallback(nil, fb, fhdr, 0, false, 0, 0)
	if ref == nil {
		t.Fatal("force-int derive ref=nil")
	}
	if interMode != InterModeGlobalMV {
		t.Fatalf("interMode=%d want global", interMode)
	}
	if mv.X != 8 || mv.Y != -8 {
		t.Fatalf("integer mv=(%d,%d) want (-8,8) in Y,X order", mv.Y, mv.X)
	}
}

func TestDeriveInterFallbackUseRefFrameMVs(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx:           [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
		SubpelFilterMode: header.FilterMode8TapRegular,
		UseRefFrameMVs:   1,
	}
	fb := &FrameBuf{}
	fb.Refs[3] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fb.Refs[6] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}

	fs := NewFrameState(32, 32)
	fs.MVFrame = refmvs.NewFrame(32, 32)
	fs.MVFrame.OrderHint, fs.MVFrame.OrderBits = 10, 5
	fs.MVFrame.RefOrderHints[6] = 8
	source := refmvs.NewFrame(32, 32)
	source.OrderHint, source.OrderBits = 8, 5
	source.RefFrameOrderHints[3] = 6
	source.RP[1*source.RPStride+1] = refmvs.TemporalBlock{
		MV:  refmvs.MV{Y: 18, X: -10},
		Ref: 4,
	}
	fb.RefMVs[6] = source
	fs.MVFrame.RefSlots[1] = 6
	refmvs.BuildTemporalProjection(fs.MVFrame, fb.RefMVs)

	refSlot, refFrame, refOrder, mv, _, interMode, skipMode, ref := deriveInterFallback(fs, fb, fhdr, 0, false, 8, 8)
	if ref == nil {
		t.Fatal("ref_frame_mvs derive ref=nil")
	}
	if refSlot != 6 || refOrder != 0 || skipMode {
		t.Fatalf("ref_frame_mvs derive=(%d,%d,%v) want (6,0,false)", refSlot, refOrder, skipMode)
	}
	if refFrame != 1 {
		t.Fatalf("ref_frame_mvs refFrame=%d want 1", refFrame)
	}
	if interMode != InterModeZeroMV || mv.Y != 18 || mv.X != -10 {
		t.Fatalf("temporal mv=(mode=%d y=%d x=%d) want zero/18/-10", interMode, mv.Y, mv.X)
	}
}

func TestDeriveInterFallbackUsesGridCandidate(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx:           [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
		SubpelFilterMode: header.FilterMode8TapRegular,
	}
	fb := &FrameBuf{}
	fb.Refs[4] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}

	fs := NewFrameState(32, 32)
	fs.MVFrame = refmvs.NewFrame(32, 32)
	fs.MVFrame.PutGridBlock(2, 1, 2, 2, refmvs.Block{
		MV:  refmvs.MVPair{refmvs.MV{Y: -14, X: 22}, {}},
		Ref: refmvs.RefPair{3, -1},
	})

	refSlot, refFrame, refOrder, mv, _, interMode, skipMode, ref := deriveInterFallback(fs, fb, fhdr, 0, false, 8, 8)
	if ref == nil {
		t.Fatal("grid derive ref=nil")
	}
	if refSlot != 4 || refOrder != 2 || skipMode {
		t.Fatalf("grid derive=(%d,%d,%v) want (4,2,false)", refSlot, refOrder, skipMode)
	}
	if refFrame != 3 {
		t.Fatalf("grid refFrame=%d want 3", refFrame)
	}
	if interMode != InterModeNearestMV || mv.Y != -14 || mv.X != 22 {
		t.Fatalf("grid mv=(mode=%d y=%d x=%d) want nearest/-14/22", interMode, mv.Y, mv.X)
	}
}

func TestSingleRefInterCandidatesSorted(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx: [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
	}
	fb := &FrameBuf{}
	fb.Refs[4] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fb.Refs[2] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}

	fs := NewFrameState(32, 32)
	fs.MVFrame = refmvs.NewFrame(32, 32)
	fs.MVFrame.PutGridBlock(2, 1, 2, 2, refmvs.Block{
		MV:  refmvs.MVPair{refmvs.MV{Y: 10, X: 4}, {}},
		Ref: refmvs.RefPair{3, -1},
	})
	fs.MVFrame.PutGridBlock(1, 2, 2, 2, refmvs.Block{
		MV:  refmvs.MVPair{refmvs.MV{Y: 4, X: 2}, {}},
		Ref: refmvs.RefPair{3, -1},
	})

	cnt, stack := singleRefInterCandidates(fs, fhdr, fb, 4, 3, 8, 8, 8, 8)
	if cnt != 2 {
		t.Fatalf("candidate cnt=%d want 2", cnt)
	}
	if stack[0].mv.Y != 10 || stack[1].mv.Y != 4 {
		t.Fatalf("candidate order=(%d,%d) want (10,4)", stack[0].mv.Y, stack[1].mv.Y)
	}
	if stack[0].refSlot != 4 || stack[0].refFrame != 3 {
		t.Fatalf("top candidate ref=(slot=%d frame=%d) want (4,3)", stack[0].refSlot, stack[0].refFrame)
	}
}

func TestSingleRefInterCandidatesIncludeTopRight(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx: [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
	}
	fb := &FrameBuf{}
	fb.Refs[4] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fb.Refs[2] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}

	fs := NewFrameState(32, 32)
	fs.MVFrame = refmvs.NewFrame(32, 32)
	fs.MVFrame.PutGridBlock(4, 1, 1, 1, refmvs.Block{
		MV:  refmvs.MVPair{refmvs.MV{Y: 9, X: 1}, {}},
		Ref: refmvs.RefPair{3, -1},
	})
	fs.MVFrame.PutGridBlock(1, 2, 1, 1, refmvs.Block{
		MV:  refmvs.MVPair{refmvs.MV{Y: 4, X: 2}, {}},
		Ref: refmvs.RefPair{3, -1},
	})

	cnt, stack := singleRefInterCandidates(fs, fhdr, fb, 4, 3, 8, 8, 8, 8)
	if cnt != 2 {
		t.Fatalf("candidate cnt=%d want 2", cnt)
	}
	if stack[0].mv.Y != 4 || stack[1].mv.Y != 9 {
		t.Fatalf("candidate order=(%d,%d) want (4,9)", stack[0].mv.Y, stack[1].mv.Y)
	}
}

func TestSingleRefInterState(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx:           [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
		SubpelFilterMode: header.FilterMode8TapRegular,
	}
	fb := &FrameBuf{}
	fb.Refs[4] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}

	fs := NewFrameState(32, 32)
	fs.MVFrame = refmvs.NewFrame(32, 32)
	fs.MVFrame.PutGridBlock(2, 1, 2, 2, refmvs.Block{
		MV:  refmvs.MVPair{refmvs.MV{Y: 10, X: 4}, {}},
		Ref: refmvs.RefPair{3, -1},
	})

	st := singleRefInterState(fs, fb, fhdr, 0, false, 8, 8)
	if st.ref == nil {
		t.Fatal("singleRefInterState ref=nil")
	}
	if st.refSlot != 4 || st.refFrame != 3 || st.refOrder != 2 {
		t.Fatalf("state ref=(slot=%d frame=%d order=%d) want (4,3,2)", st.refSlot, st.refFrame, st.refOrder)
	}
	if st.interMode != InterModeNearestMV || st.mv.Y != 10 || st.mv.X != 4 {
		t.Fatalf("state mv=(mode=%d y=%d x=%d) want nearest/10/4", st.interMode, st.mv.Y, st.mv.X)
	}
	if st.candCnt != 1 {
		t.Fatalf("state candCnt=%d want 1", st.candCnt)
	}
}

func TestChooseSkipModeRef(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx:          [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
		SkipModeEnabled: 1,
		SkipModeRefs:    [2]int8{1, 4},
	}
	fb := &FrameBuf{}
	fb.Refs[5] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}

	refSlot, refFrame, refOrder, ref, ok := chooseSkipModeRef(fhdr, fb)
	if !ok || refSlot != 5 || refFrame != 2 || refOrder != 1 || ref != fb.Refs[5] {
		t.Fatalf("skipmode ref=(slot=%d frame=%d order=%d ok=%v ref=%p)", refSlot, refFrame, refOrder, ok, ref)
	}
}

func TestChooseSegmentRef(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx: [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
	}
	fhdr.Segmentation.Enabled = 1
	fhdr.Segmentation.SegData.D[2].Ref = 3
	fb := &FrameBuf{}
	fb.Refs[4] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}

	refSlot, refFrame, refOrder, ref, ok := chooseSegmentRef(fhdr, fb, 2)
	if !ok || refSlot != 4 || refFrame != 3 || refOrder != 2 || ref != fb.Refs[4] {
		t.Fatalf("seg ref=(slot=%d frame=%d order=%d ok=%v ref=%p)", refSlot, refFrame, refOrder, ok, ref)
	}
}

func TestResolveSingleRefReference_SyntaxRefBeatsNeighbourFallback(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx: [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
	}
	fb := &FrameBuf{}
	fb.Refs[4] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fb.Refs[2] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}

	fs := NewFrameState(32, 32)
	fs.SetInterBlock(0, 0, 8, 8, false, 0, 2, 5, uint8(header.FilterMode8TapRegular), uint8(header.FilterMode8TapRegular), InterModeNearestMV, refmvs.MV{Y: 12, X: -20})

	st := interState{}
	syntax := singleRefInterSyntax{
		refSlot: 4,
		hasRef:  true,
	}
	resolveSingleRefReference(&st, fs, fb, fhdr, 0, false, 8, 0, syntax)

	if st.refSlot != 4 || st.refFrame != 3 || st.ref != fb.Refs[4] {
		t.Fatalf("state ref=(slot=%d frame=%d ref=%p) want (4,3,%p)", st.refSlot, st.refFrame, st.ref, fb.Refs[4])
	}
}

func TestApplyTemporalInterMV(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx:         [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
		UseRefFrameMVs: 1,
	}
	fb := &FrameBuf{}
	fb.Refs[3] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fs := NewFrameState(32, 32)
	fs.MVFrame = refmvs.NewFrame(32, 32)
	fs.MVFrame.OrderHint, fs.MVFrame.OrderBits = 10, 5
	fs.MVFrame.RefOrderHints[3] = 8
	source := refmvs.NewFrame(32, 32)
	source.OrderHint, source.OrderBits = 8, 5
	source.RefFrameOrderHints[2] = 6
	source.RP[1*source.RPStride+1] = refmvs.TemporalBlock{
		MV:  refmvs.MV{Y: 18, X: -10},
		Ref: 3,
	}
	fb.RefMVs[3] = source
	fs.MVFrame.RefSlots[1] = 3
	refmvs.BuildTemporalProjection(fs.MVFrame, fb.RefMVs)
	st := interState{refSlot: 3}

	if !applyTemporalInterMV(&st, fs, fb, fhdr, 8, 8) {
		t.Fatal("applyTemporalInterMV returned false")
	}
	if st.mv != (refmvs.MV{Y: 18, X: -10}) || st.refSlot != 3 || st.refFrame != 4 || st.ref != fb.Refs[3] {
		t.Fatalf("state=%+v", st)
	}
}

func TestApplyGlobalInterMV(t *testing.T) {
	fhdr := &header.FrameHeader{HP: 1}
	fhdr.GMV[2] = header.WarpedMotionParams{
		Type:   header.WMTypeTranslation,
		Matrix: [6]int32{16 << 13, -8 << 13, 1 << 16, 0, 0, 1 << 16},
	}
	st := interState{refOrder: 2}

	if !applyGlobalInterMV(&st, fhdr, 0) {
		t.Fatal("applyGlobalInterMV returned false")
	}
	if st.interMode != InterModeGlobalMV || st.mv != (refmvs.MV{Y: -8, X: 16}) {
		t.Fatalf("state=%+v", st)
	}
}

func TestDeriveInterFallbackMatchesSingleRefInterState(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx:           [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
		SubpelFilterMode: header.FilterMode8TapRegular,
	}
	fb := &FrameBuf{}
	fb.Refs[4] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fs := NewFrameState(32, 32)
	fs.MVFrame = refmvs.NewFrame(32, 32)
	fs.MVFrame.PutGridBlock(2, 1, 2, 2, refmvs.Block{
		MV:  refmvs.MVPair{refmvs.MV{Y: 10, X: 4}, {}},
		Ref: refmvs.RefPair{3, -1},
	})

	st := singleRefInterState(fs, fb, fhdr, 0, false, 8, 8)
	refSlot, refFrame, refOrder, mv, filterMode, interMode, skipMode, ref := deriveInterFallback(fs, fb, fhdr, 0, false, 8, 8)
	if refSlot != st.refSlot || refFrame != st.refFrame || refOrder != st.refOrder ||
		mv != st.mv || filterMode != st.filterMode || interMode != st.interMode ||
		skipMode != st.skipMode || ref != st.ref {
		t.Fatal("deriveInterFallback no longer matches singleRefInterState")
	}
}

func TestSingleRefInterStateMultipleCandidatesUsesNearest(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx:           [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
		SubpelFilterMode: header.FilterMode8TapRegular,
	}
	fb := &FrameBuf{}
	fb.Refs[4] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fb.Refs[2] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fs := NewFrameState(32, 32)
	fs.MVFrame = refmvs.NewFrame(32, 32)
	fs.MVFrame.PutGridBlock(2, 1, 2, 2, refmvs.Block{
		MV:  refmvs.MVPair{refmvs.MV{Y: 10, X: 4}, {}},
		Ref: refmvs.RefPair{3, -1},
	})
	fs.MVFrame.PutGridBlock(1, 2, 2, 2, refmvs.Block{
		MV:  refmvs.MVPair{refmvs.MV{Y: 4, X: 2}, {}},
		Ref: refmvs.RefPair{3, -1},
	})

	st := singleRefInterState(fs, fb, fhdr, 0, false, 8, 8)
	if st.interMode != InterModeNearestMV || st.mv.Y != 10 || st.mv.X != 4 {
		t.Fatalf("state mv=(mode=%d y=%d x=%d) want nearest/10/4", st.interMode, st.mv.Y, st.mv.X)
	}
	if st.candCnt != 2 {
		t.Fatalf("state candCnt=%d want 2", st.candCnt)
	}
}

func TestSingleRefInterStateHints(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx:           [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
		SubpelFilterMode: header.FilterMode8TapRegular,
	}
	fb := &FrameBuf{}
	fb.Refs[4] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fb.Refs[2] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fs := NewFrameState(32, 32)
	fs.MVFrame = refmvs.NewFrame(32, 32)
	fs.MVFrame.PutGridBlock(2, 1, 2, 2, refmvs.Block{
		MV:  refmvs.MVPair{refmvs.MV{Y: 10, X: 4}, {}},
		Ref: refmvs.RefPair{3, -1},
	})
	fs.MVFrame.PutGridBlock(1, 2, 2, 2, refmvs.Block{
		MV:  refmvs.MVPair{refmvs.MV{Y: 4, X: 2}, {}},
		Ref: refmvs.RefPair{3, -1},
	})

	near := singleRefInterStateWithHint(fs, fb, fhdr, 0, false, 8, 8, singleRefInterSyntax{modeHint: interModeHintNear, refSlot: -1})
	if near.interMode != InterModeNearMV || near.mv.Y != 4 || near.mv.X != 2 {
		t.Fatalf("near=(mode=%d y=%d x=%d) want near/4/2", near.interMode, near.mv.Y, near.mv.X)
	}

	refmv := singleRefInterStateWithHint(fs, fb, fhdr, 0, false, 8, 8, singleRefInterSyntax{modeHint: interModeHintRef, refSlot: -1})
	if refmv.interMode != InterModeRefMV || refmv.baseMV.Y != 10 || refmv.baseMV.X != 4 || refmv.mv != refmv.baseMV {
		t.Fatalf("refmv state mismatch: mode=%d base=%+v mv=%+v", refmv.interMode, refmv.baseMV, refmv.mv)
	}

	newmv := singleRefInterStateWithHint(fs, fb, fhdr, 0, false, 8, 8, singleRefInterSyntax{modeHint: interModeHintNew, refSlot: -1})
	if newmv.interMode != InterModeNewMV || newmv.baseMV.Y != 10 || newmv.deltaMV != (refmvs.MV{}) || newmv.mv != newmv.baseMV {
		t.Fatalf("newmv state mismatch: mode=%d base=%+v delta=%+v mv=%+v", newmv.interMode, newmv.baseMV, newmv.deltaMV, newmv.mv)
	}
}

func TestDecodeInterBlockRecordsState(t *testing.T) {
	seq := &header.SequenceHeader{}
	fhdr := &header.FrameHeader{
		Refidx:           [header.RefsPerFrame]int8{0, 1, 2, 3, 4, 5, 6},
		SubpelFilterMode: header.FilterMode8TapSharp,
	}
	fb := &FrameBuf{
		Y:       make([]byte, 16*16),
		StrideY: 16,
		Width:   16,
		Height:  16,
	}
	ref := &PlaneBuf{
		Y:       make([]byte, 16*16),
		StrideY: 16,
		Width:   16,
		Height:  16,
	}
	for i := range ref.Y {
		ref.Y[i] = 77
	}
	fb.Refs[0] = ref

	fs := NewFrameState(16, 16)
	decodeInterBlock(nil, nil, fs, fhdr, seq, fb, blockSyntaxState{segID: 0, skip: false}, 0, 0, 8, 8)

	if got := fb.Y[0]; got != 77 {
		t.Fatalf("decoded pixel=%d want 77", got)
	}
	if got := fs.AboveRef[0]; got != 0 {
		t.Fatalf("AboveRef=%d want 0", got)
	}
	if got := fs.LeftFilter[0]; got != uint8(header.FilterMode8TapSharp) {
		t.Fatalf("LeftFilter=%d want %d", got, header.FilterMode8TapSharp)
	}
	blk, ok := fs.BlockState(0, 0)
	if !ok {
		t.Fatal("BlockState missing")
	}
	if blk.Intra || blk.InterMode != InterModeZeroMV || blk.RefSlot != 0 || blk.Filter != uint8(header.FilterMode8TapSharp) {
		t.Fatalf("block state=%+v", blk)
	}
}

func TestDecodeInterBlockSwitchableFilterConsumesLiveSyntax(t *testing.T) {
	seq := &header.SequenceHeader{}
	fhdr := &header.FrameHeader{
		Refidx:           [header.RefsPerFrame]int8{0, 1, 2, 3, 4, 5, 6},
		SubpelFilterMode: header.FilterModeSwitchable,
	}
	fb := &FrameBuf{
		Y:       make([]byte, 16*16),
		StrideY: 16,
		Width:   16,
		Height:  16,
	}
	ref := &PlaneBuf{
		Y:       make([]byte, 16*16),
		StrideY: 16,
		Width:   16,
		Height:  16,
	}
	for i := range ref.Y {
		ref.Y[i] = 91
	}
	fb.Refs[0] = ref

	fs := NewFrameState(16, 16)
	ctx := NewTileCtx()
	m := bitstream.NewMSAC([]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, true)
	decodeInterBlock(m, ctx, fs, fhdr, seq, fb, blockSyntaxState{segID: 0, skip: false}, 0, 0, 8, 8)

	blk, ok := fs.BlockState(0, 0)
	if !ok {
		t.Fatal("BlockState missing")
	}
	if blk.Filter > uint8(header.FilterModeBilinear) {
		t.Fatalf("filter=%d out of switchable range", blk.Filter)
	}
	if blk.Filter != fs.AboveFilter[0] || blk.Filter != fs.LeftFilter[0] {
		t.Fatalf("neighbour filter mismatch: blk=%d above=%d left=%d", blk.Filter, fs.AboveFilter[0], fs.LeftFilter[0])
	}
}

func TestDecodeSingleRefInterBlockHintRecordsState(t *testing.T) {
	seq := &header.SequenceHeader{}
	fhdr := &header.FrameHeader{
		Refidx:           [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
		SubpelFilterMode: header.FilterMode8TapRegular,
	}
	fb := &FrameBuf{
		Y:       make([]byte, 16*16),
		StrideY: 16,
		Width:   16,
		Height:  16,
	}
	fb.Refs[4] = &PlaneBuf{Y: make([]byte, 16*16), StrideY: 16, Width: 16, Height: 16}
	fb.Refs[2] = &PlaneBuf{Y: make([]byte, 16*16), StrideY: 16, Width: 16, Height: 16}

	fs := NewFrameState(16, 16)
	fs.MVFrame = refmvs.NewFrame(16, 16)
	fs.MVFrame.PutGridBlock(2, 1, 1, 1, refmvs.Block{
		MV:  refmvs.MVPair{refmvs.MV{Y: 10, X: 4}, {}},
		Ref: refmvs.RefPair{3, -1},
	})
	fs.MVFrame.PutGridBlock(1, 2, 1, 1, refmvs.Block{
		MV:  refmvs.MVPair{refmvs.MV{Y: 4, X: 2}, {}},
		Ref: refmvs.RefPair{3, -1},
	})

	st := decodeSingleRefInterBlock(fs, fhdr, seq, fb, 0, false, 8, 8, 8, 8, interModeHintNear)
	if st.interMode != InterModeNearMV || st.mv.Y != 4 || st.mv.X != 2 {
		t.Fatalf("state=(mode=%d y=%d x=%d) want near/4/2", st.interMode, st.mv.Y, st.mv.X)
	}
	blk, ok := fs.BlockState(8, 8)
	if !ok {
		t.Fatal("BlockState missing")
	}
	if blk.Intra || blk.InterMode != InterModeNearMV || blk.RefSlot != 4 || blk.BaseMV != [2]int16{4, 2} || blk.MV != [2]int16{4, 2} {
		t.Fatalf("block state=%+v", blk)
	}
}

func TestSingleRefInterStateFromSyntaxNewMV(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx:           [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
		SubpelFilterMode: header.FilterMode8TapRegular,
	}
	fb := &FrameBuf{}
	fb.Refs[4] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fs := NewFrameState(32, 32)
	fs.MVFrame = refmvs.NewFrame(32, 32)
	fs.MVFrame.PutGridBlock(2, 1, 2, 2, refmvs.Block{
		MV:  refmvs.MVPair{refmvs.MV{Y: 10, X: 4}, {}},
		Ref: refmvs.RefPair{3, -1},
	})

	st := singleRefInterStateFromSyntax(fs, fb, fhdr, 0, false, 8, 8, singleRefInterSyntax{
		modeHint: interModeHintNew,
		deltaMV:  refmvs.MV{Y: 2, X: -6},
	})
	if st.interMode != InterModeNewMV {
		t.Fatalf("mode=%d want newmv", st.interMode)
	}
	if st.baseMV != (refmvs.MV{Y: 10, X: 4}) || st.deltaMV != (refmvs.MV{Y: 2, X: -6}) || st.mv != (refmvs.MV{Y: 12, X: -2}) {
		t.Fatalf("state mismatch: base=%+v delta=%+v mv=%+v", st.baseMV, st.deltaMV, st.mv)
	}
}

func TestSingleRefInterStateFromSyntaxNewMVWithoutCandidate(t *testing.T) {
	fhdr := &header.FrameHeader{Refidx: [header.RefsPerFrame]int8{0, 1, 2, 3, 4, 5, 6}}
	fb := &FrameBuf{}
	fb.Refs[0] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fs := NewFrameState(32, 32)
	syntax := singleRefInterSyntax{
		modeHint:     interModeHintNew,
		motionSource: interMotionSourceCandidate,
		refSlot:      0,
		hasRef:       true,
		drlIdx:       0,
		deltaMV:      refmvs.MV{Y: -8, X: 32},
	}

	st := singleRefInterStateFromSyntax(fs, fb, fhdr, 0, false, 0, 0, syntax)
	if st.interMode != InterModeNewMV || st.mv != syntax.deltaMV {
		t.Fatalf("NEWMV fallback mode=%d mv=%+v, want mode=%d mv=%+v", st.interMode, st.mv, InterModeNewMV, syntax.deltaMV)
	}
}

func TestNearMVUsesGlobalFallbackWhenSecondCandidateMissing(t *testing.T) {
	fhdr := &header.FrameHeader{Refidx: [header.RefsPerFrame]int8{0, 1, 2, 3, 4, 5, 6}}
	fb := &FrameBuf{}
	fb.Refs[0] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fs := NewFrameState(64, 64)
	fs.MVFrame = refmvs.NewFrame(64, 64)
	fs.SetBlockState(0, 0, 16, 16, Av1Block{
		Intra: false, RefSlot: 0, InterMode: InterModeNearestMV, MV: [2]int16{0, 32},
	})
	fs.MVFrame.PutGridBlock(0, 0, 4, 4, refmvs.Block{
		Ref: refmvs.RefPair{1, -1}, MV: refmvs.MVPair{{X: 32}}, BS: BS16x16,
	})

	st := singleRefInterStateFromSyntax(fs, fb, fhdr, 0, false, 16, 0, singleRefInterSyntax{
		modeHint: interModeHintNear, motionSource: interMotionSourceCandidate,
		refSlot: 0, hasRef: true, drlIdx: 1,
	})
	if st.interMode != InterModeNearMV || st.mv != (refmvs.MV{}) {
		t.Fatalf("NEAR fallback mode=%d mv=%+v, want near/zero", st.interMode, st.mv)
	}
}

func TestGlobalSyntaxIdentityUsesZeroMV(t *testing.T) {
	fhdr := &header.FrameHeader{Refidx: [header.RefsPerFrame]int8{0, 1, 2, 3, 4, 5, 6}}
	fhdr.GMV[0].Type = header.WMTypeIdentity
	fb := &FrameBuf{}
	fb.Refs[0] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	st := singleRefInterStateFromSyntax(NewFrameState(32, 32), fb, fhdr, 0, false, 0, 0, singleRefInterSyntax{
		motionSource: interMotionSourceGlobal, refSlot: 0, hasRef: true,
	})
	if st.interMode != InterModeGlobalMV || st.mv != (refmvs.MV{}) {
		t.Fatalf("identity global state mode=%d mv=%+v", st.interMode, st.mv)
	}
}

func TestNewMVDRL0PrefersTopDirectCandidate(t *testing.T) {
	fhdr := &header.FrameHeader{Refidx: [header.RefsPerFrame]int8{0, 1, 2, 3, 4, 5, 6}}
	fb := &FrameBuf{}
	fb.Refs[0] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fs := NewFrameState(64, 64)
	fs.SetBlockState(16, 0, 16, 16, Av1Block{Intra: false, RefSlot: 0, MV: [2]int16{0, 0}})
	fs.SetBlockState(0, 16, 16, 16, Av1Block{Intra: false, RefSlot: 0, MV: [2]int16{0, 32}})

	st := singleRefInterStateFromSyntax(fs, fb, fhdr, 0, false, 16, 16, singleRefInterSyntax{
		modeHint: interModeHintNew, motionSource: interMotionSourceCandidate,
		refSlot: 0, hasRef: true, drlIdx: 0, deltaMV: refmvs.MV{X: -2},
	})
	if st.baseMV != (refmvs.MV{}) || st.mv != (refmvs.MV{X: -2}) {
		t.Fatalf("NEWMV top candidate base=%+v mv=%+v", st.baseMV, st.mv)
	}
}

func TestApplySyntaxInterRef(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx: [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
	}
	fb := &FrameBuf{}
	fb.Refs[2] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	st := interState{refSlot: 0}

	ok := applySyntaxInterRef(&st, fb, fhdr, singleRefInterSyntax{refSlot: 2, hasRef: true})
	if !ok {
		t.Fatal("applySyntaxInterRef returned false")
	}
	if st.refSlot != 2 || st.refFrame != 5 || st.refOrder != 4 || st.ref != fb.Refs[2] {
		t.Fatalf("state=%+v", st)
	}
}

func TestSelectInterCandidateMode(t *testing.T) {
	mode, pick := selectInterCandidateMode(interModeHintAuto, 2)
	if mode != InterModeNearestMV || pick != 0 {
		t.Fatalf("auto=(mode=%d pick=%d)", mode, pick)
	}
	mode, pick = selectInterCandidateMode(interModeHintNear, 2)
	if mode != InterModeNearMV || pick != 1 {
		t.Fatalf("near=(mode=%d pick=%d)", mode, pick)
	}
	mode, pick = selectInterCandidateMode(interModeHintNew, 2)
	if mode != InterModeNewMV || pick != 0 {
		t.Fatalf("new=(mode=%d pick=%d)", mode, pick)
	}
}

func TestApplyNeighbourInterSyntaxSetsMotionSource(t *testing.T) {
	syntax := singleRefInterSyntax{modeHint: interModeHintAuto, motionSource: interMotionSourceAuto, refSlot: -1}
	ok := applyNeighbourInterSyntax(&syntax, Av1Block{
		Intra:     false,
		InterMode: InterModeGlobalMV,
		RefSlot:   3,
	})
	if !ok || syntax.motionSource != interMotionSourceGlobal || !syntax.hasRef || syntax.refSlot != 3 {
		t.Fatalf("syntax=%+v", syntax)
	}
}

func TestApplyInterCandidate(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx: [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
	}
	fb := &FrameBuf{}
	fb.Refs[4] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	st := interState{refSlot: 4}
	cand := interCandidate{
		mv:       refmvs.MV{Y: 10, X: 4},
		refSlot:  4,
		refFrame: 3,
	}

	applyInterCandidate(&st, fhdr, fb, cand, InterModeRefMV)
	if st.interMode != InterModeRefMV || st.baseMV != cand.mv || st.mv != cand.mv || st.ref != fb.Refs[4] || st.refOrder != 2 {
		t.Fatalf("state=%+v", st)
	}
}

func TestResolveSingleRefMotionPrefersCandidateSyntax(t *testing.T) {
	fhdr := &header.FrameHeader{
		Refidx:         [header.RefsPerFrame]int8{6, 5, 4, 3, 2, 1, 0},
		UseRefFrameMVs: 1,
	}
	fb := &FrameBuf{}
	fb.Refs[3] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fb.Refs[4] = &PlaneBuf{Y: make([]byte, 16), Width: 4, Height: 4}
	fs := NewFrameState(32, 32)
	fs.MVFrame = refmvs.NewFrame(32, 32)
	fs.MVFrame.RP[1*fs.MVFrame.RPStride+1] = refmvs.TemporalBlock{
		MV:  refmvs.MV{Y: 18, X: -10},
		Ref: 4,
	}
	fs.MVFrame.PutGridBlock(2, 1, 2, 2, refmvs.Block{
		MV:  refmvs.MVPair{refmvs.MV{Y: 10, X: 4}, {}},
		Ref: refmvs.RefPair{3, -1},
	})
	st := interState{refSlot: 4}
	updateInterRefState(&st, fhdr, fb)

	resolveSingleRefMotion(&st, fs, fb, fhdr, 0, 8, 8, singleRefInterSyntax{
		modeHint:     interModeHintNearest,
		motionSource: interMotionSourceCandidate,
		refSlot:      4,
		hasRef:       true,
		bw:           8,
		bh:           8,
	})
	if st.interMode != InterModeNearestMV || st.mv != (refmvs.MV{Y: 10, X: 4}) {
		t.Fatalf("state=%+v", st)
	}
}

func TestDeriveSingleRefInterSyntaxFromNeighbourBlockState(t *testing.T) {
	fs := NewFrameState(32, 32)
	fs.SetBlockState(8, 4, 8, 8, Av1Block{
		Intra:     false,
		InterMode: InterModeNewMV,
		RefSlot:   2,
		DeltaMV:   [2]int16{3, -5},
	})

	syntax := deriveSingleRefInterSyntax(fs, 8, 8)
	if syntax.modeHint != interModeHintNew || syntax.refSlot != 2 || !syntax.hasRef || syntax.deltaMV != (refmvs.MV{Y: 3, X: -5}) {
		t.Fatalf("syntax=%+v", syntax)
	}
}

func TestDeriveSingleRefInterSyntaxPrefersTopThenLeft(t *testing.T) {
	fs := NewFrameState(32, 32)
	fs.SetBlockState(8, 4, 8, 8, Av1Block{
		Intra:     false,
		InterMode: InterModeRefMV,
	})
	fs.SetBlockState(4, 8, 8, 8, Av1Block{
		Intra:     false,
		InterMode: InterModeNearMV,
	})

	syntax := deriveSingleRefInterSyntax(fs, 8, 8)
	if syntax.modeHint != interModeHintRef {
		t.Fatalf("syntax.modeHint=%d want ref", syntax.modeHint)
	}
}
