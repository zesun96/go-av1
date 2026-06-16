package tile

import (
	"testing"

	"github.com/zesun96/go-av1/internal/header"
	predinter "github.com/zesun96/go-av1/internal/predict/inter"
	"github.com/zesun96/go-av1/internal/refmvs"
)

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
		mode header.FilterMode
		want predinter.Filter2D
	}{
		{header.FilterMode8TapRegular, predinter.Filter2D8TapRegular},
		{header.FilterMode8TapSmooth, predinter.Filter2D8TapSmooth},
		{header.FilterMode8TapSharp, predinter.Filter2D8TapSharp},
		{header.FilterModeBilinear, predinter.Filter2DBilinear},
		{header.FilterModeSwitchable, predinter.Filter2D8TapRegular},
	}
	for _, tc := range tests {
		if got := interFilter2D(tc.mode); got != tc.want {
			t.Fatalf("interFilter2D(%d)=%d want %d", tc.mode, got, tc.want)
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
	fs.SetInterBlock(0, 0, 8, 8, false, 0, 2, 5, uint8(header.FilterMode8TapRegular), InterModeNearestMV, refmvs.MV{Y: 12, X: -20})

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
	fs.MVFrame.RP[1*fs.MVFrame.RPStride+1] = refmvs.TemporalBlock{
		MV:  refmvs.MV{Y: 18, X: -10},
		Ref: 4,
	}

	refSlot, refFrame, refOrder, mv, _, interMode, skipMode, ref := deriveInterFallback(fs, fb, fhdr, 0, false, 8, 8)
	if ref == nil {
		t.Fatal("ref_frame_mvs derive ref=nil")
	}
	if refSlot != 3 || refOrder != 3 || skipMode {
		t.Fatalf("ref_frame_mvs derive=(%d,%d,%v) want (3,3,false)", refSlot, refOrder, skipMode)
	}
	if refFrame != 4 {
		t.Fatalf("ref_frame_mvs refFrame=%d want 4", refFrame)
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
		Ref: refmvs.RefPair{5, -1},
	})

	cnt, stack := singleRefInterCandidates(fs, fhdr, fb, 8, 8)
	if cnt != 2 {
		t.Fatalf("candidate cnt=%d want 2", cnt)
	}
	if stack[0].MV[0].Y != 10 || stack[1].MV[0].Y != 4 {
		t.Fatalf("candidate order=(%d,%d) want (10,4)", stack[0].MV[0].Y, stack[1].MV[0].Y)
	}
}

func TestDecodeInterBlockFallbackRecordsState(t *testing.T) {
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
	decodeInterBlockFallback(fs, fhdr, seq, fb, 0, false, 0, 0, 8, 8)

	if got := fb.Y[0]; got != 77 {
		t.Fatalf("decoded pixel=%d want 77", got)
	}
	if got := fs.AboveRef[0]; got != 0 {
		t.Fatalf("AboveRef=%d want 0", got)
	}
	if got := fs.LeftFilter[0]; got != uint8(header.FilterMode8TapSharp) {
		t.Fatalf("LeftFilter=%d want %d", got, header.FilterMode8TapSharp)
	}
}
