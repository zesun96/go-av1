package tile

import (
	"reflect"
	"testing"

	"github.com/zesun96/go-av1/internal/header"
	"github.com/zesun96/go-av1/internal/refmvs"
)

func TestCompoundJointWeightMatchesDav1dDistanceQuantization(t *testing.T) {
	fb := &FrameBuf{}
	fb.RefOrderHints[0], fb.RefOrderHintValid[0] = 0, true
	fb.RefOrderHints[6], fb.RefOrderHintValid[6] = 3, true
	fhdr := &header.FrameHeader{FrameOffset: 1}
	seq := &header.SequenceHeader{OrderHintNBits: 5}
	if got, want := compoundJointWeight(fhdr, seq, fb, 0, 6), 11; got != want {
		t.Fatalf("joint compound weight = %d, want %d", got, want)
	}
	if got, want := compoundJointWeight(fhdr, seq, fb, 6, 0), 5; got != want {
		t.Fatalf("reversed joint compound weight = %d, want %d", got, want)
	}
}

func TestTPartitionBlocksMatchAV1Geometry(t *testing.T) {
	tests := []struct {
		part int
		want [3]partitionBlock
	}{
		{PartitionTTopSplit, [3]partitionBlock{{8, 12, 8, 8}, {16, 12, 8, 8}, {8, 20, 16, 8}}},
		{PartitionTBottomSplit, [3]partitionBlock{{8, 12, 16, 8}, {8, 20, 8, 8}, {16, 20, 8, 8}}},
		{PartitionTLeftSplit, [3]partitionBlock{{8, 12, 8, 8}, {8, 20, 8, 8}, {16, 12, 8, 16}}},
		{PartitionTRightSplit, [3]partitionBlock{{8, 12, 8, 16}, {16, 12, 8, 8}, {16, 20, 8, 8}}},
	}
	for _, test := range tests {
		if got := tPartitionBlocks(test.part, 8, 12, 16); got != test.want {
			t.Fatalf("partition %d blocks = %v, want %v", test.part, got, test.want)
		}
	}
}

func TestIntraEdgeFlagsMatchDav1dLayoutRules(t *testing.T) {
	node := intraEdgeNode{topHasRight: true}
	if got, want := node.hFlags(BL8X8, 1), edgeI420TopHasRight; got != want {
		t.Fatalf("8x8 h[1] flags = %d, want %d", got, want)
	}
	if got, want := node.vFlags(BL8X8, 1), edgeAllTopHasRight; got != want {
		t.Fatalf("8x8 v[1] flags = %d, want %d", got, want)
	}
	seq420 := &header.SequenceHeader{SsHor: 1, SsVer: 1}
	if got, want := planeIntraEdgeFlags(edgeI420TopHasRight|edgeI420LeftHasBottom, 1, seq420), edgeTopHasRight|edgeLeftHasBottom; got != want {
		t.Fatalf("4:2:0 plane flags = %d, want %d", got, want)
	}
}

func TestTransformIntraEdgeFlagsUseDecodedTransformNeighbors(t *testing.T) {
	if got, want := transformIntraEdgeFlags(8, 8, 0, 0, 4, 4, 0), edgeTopHasRight|edgeLeftHasBottom; got != want {
		t.Fatalf("top-left transform flags = %d, want %d", got, want)
	}
	if got, want := transformIntraEdgeFlags(8, 8, 4, 4, 4, 4, edgeTopHasRight|edgeLeftHasBottom), intraEdgeFlags(0); got != want {
		t.Fatalf("bottom-right transform flags = %d, want %d", got, want)
	}
	if got, want := transformIntraEdgeFlags(8, 8, 4, 0, 4, 4, edgeTopHasRight|edgeLeftHasBottom), edgeTopHasRight; got != want {
		t.Fatalf("top-right transform flags = %d, want %d", got, want)
	}
}

func TestCflLumaRectCoversSub8x8ChromaOwner(t *testing.T) {
	seq := &header.SequenceHeader{SsHor: 1, SsVer: 1}
	x, y, w, h := cflLumaRect(seq, 168, 0, 4, 4)
	if x != 336 || y != 0 || w != 8 || h != 8 {
		t.Fatalf("CFL luma rect = (%d,%d %dx%d), want (336,0 8x8)", x, y, w, h)
	}
}

func TestKFYModeCDFsAreMonotonic(t *testing.T) {
	for top := range KFYMCDFDefault {
		for left := range KFYMCDFDefault[top] {
			cdf := KFYMCDFDefault[top][left]
			for i := 1; i < NIntraPredModes; i++ {
				if cdf[i] > cdf[i-1] {
					t.Fatalf("KFY[%d][%d] rises at %d: %d > %d", top, left, i, cdf[i], cdf[i-1])
				}
			}
		}
	}
}

func TestKFYModeCDFTop3RowsMatchDav1d(t *testing.T) {
	wantLeft3 := [NIntraPredModes + 1]uint16{
		25054, 23720, 23252, 16101, 15951, 15774, 15615, 14001,
		6025, 2379, 1232, 240, 0, 0,
	}
	wantLeft4 := [NIntraPredModes + 1]uint16{
		23925, 22488, 21272, 17451, 16116, 14825, 13660, 10050,
		6999, 2815, 1785, 283, 0, 0,
	}
	if got := KFYMCDFDefault[3][3]; got != wantLeft3 {
		t.Fatalf("KFY[3][3] = %v, want %v", got, wantLeft3)
	}
	if got := KFYMCDFDefault[3][4]; got != wantLeft4 {
		t.Fatalf("KFY[3][4] = %v, want %v", got, wantLeft4)
	}
}

func TestUVModeCDFKeepsDav1dYMode10Probability(t *testing.T) {
	// dav1d CDF13(..., 22696, ...) is stored as an inverse CDF.
	if got, want := UVModeCDFDefault[1][10][10], uint16(32768-22696); got != want {
		t.Fatalf("UV mode CDF = %d, want %d", got, want)
	}
}

func TestCFLAllowedForBlock(t *testing.T) {
	seq420 := &header.SequenceHeader{SsHor: 1, SsVer: 1}
	seq444 := &header.SequenceHeader{}

	if !cflAllowedForBlock(seq420, 32, 32, false) {
		t.Fatal("32x32 4:2:0 block should allow CFL")
	}
	if cflAllowedForBlock(seq420, 64, 64, false) {
		t.Fatal("64x64 4:2:0 block should not allow CFL")
	}
	if !cflAllowedForBlock(seq420, 4, 4, true) {
		t.Fatal("lossless 4:4 luma block should allow CFL on 4:2:0 chroma")
	}
	if !cflAllowedForBlock(seq420, 8, 8, true) {
		t.Fatal("lossless 8:8 luma block should allow CFL on 4:2:0 chroma")
	}
	if cflAllowedForBlock(seq420, 16, 16, true) {
		t.Fatal("lossless 16:16 luma block should not allow CFL on 4:2:0 chroma")
	}
	if !cflAllowedForBlock(seq444, 4, 4, true) {
		t.Fatal("lossless 4:4:4 4x4 block should allow CFL")
	}
	if cflAllowedForBlock(seq444, 8, 8, true) {
		t.Fatal("lossless 4:4:4 8x8 block should not allow CFL")
	}
}

func TestPrepareFilterIntraEdgesKeepsTopLeftRequirement(t *testing.T) {
	plane := make([]byte, 8*8)
	for y := 0; y < 8; y++ {
		plane[y*8+3] = byte(40 + y)
	}
	topLeft, tl := newIntraEdgeBuffer(4, 4)
	mode, _ := prepareIntraPrediction(
		plane, 8, 8, 8, 4, 0, 4, 4,
		topLeft, tl, DCPred, 0, 0, false, 0, false, true,
	)
	if mode != intraPredFilter {
		t.Fatalf("dispatch mode = %d, want filter", mode)
	}
	if topLeft[tl] != 40 || topLeft[tl+1] != 40 || topLeft[tl-1] != 40 {
		t.Fatalf("filter edges tl/top/left = %d/%d/%d, want 40/40/40", topLeft[tl], topLeft[tl+1], topLeft[tl-1])
	}
}

func TestPrepareZone2EdgesIncludesBottomLeftExtension(t *testing.T) {
	plane := make([]byte, 8*8)
	for y := 0; y < 8; y++ {
		plane[y*8+3] = byte(40 + y)
	}
	topLeft, tl := newIntraEdgeBuffer(4, 4)
	mode, _ := prepareIntraPrediction(
		plane, 8, 8, 8, 4, 0, 4, 4,
		topLeft, tl, DiagDownRightPred, 0, -1, false, 0, false, true,
	)
	if mode != intraPredZ2 {
		t.Fatalf("dispatch mode = %d, want Zone 2", mode)
	}
	for i := 0; i < 4; i++ {
		if got, want := topLeft[tl-5-i], byte(44+i); got != want {
			t.Fatalf("bottom-left edge %d = %d, want %d", i, got, want)
		}
	}
}

func TestPrepareZone2FiltersTopLeftForLargeTransform(t *testing.T) {
	plane := make([]byte, 32*32)
	plane[7*32+7] = 90
	plane[8*32+7] = 94
	plane[7*32+8] = 89
	topLeft, tl := newIntraEdgeBuffer(16, 8)
	mode, _ := prepareIntraPrediction(
		plane, 32, 32, 32, 8, 8, 16, 8,
		topLeft, tl, VertRightPred, -1, -1, true, 0, true, true,
	)
	if mode != intraPredZ2 {
		t.Fatalf("dispatch mode = %d, want Zone 2", mode)
	}
	if got := topLeft[tl]; got != 91 {
		t.Fatalf("filtered top-left = %d, want 91", got)
	}
}

func TestRestorationUnitExtentMergesShortTail(t *testing.T) {
	if got, want := restorationUnitExtent(0, 176, 128), 176; got != want {
		t.Fatalf("single restoration unit extent = %d, want %d", got, want)
	}
	if got, want := restorationUnitExtent(0, 300, 128), 128; got != want {
		t.Fatalf("first restoration unit extent = %d, want %d", got, want)
	}
	if got, want := restorationUnitExtent(128, 300, 128), 172; got != want {
		t.Fatalf("last restoration unit extent = %d, want %d", got, want)
	}
}

func TestRestorationUnitYExtentUsesStripeOffset(t *testing.T) {
	tests := []struct {
		pos, total, unitSize, ssV int
		wantStart, wantExtent     int
	}{
		{pos: 0, total: 288, unitSize: 128, wantStart: 0, wantExtent: 120},
		{pos: 128, total: 288, unitSize: 128, wantStart: 120, wantExtent: 168},
		{pos: 0, total: 144, unitSize: 64, ssV: 1, wantStart: 0, wantExtent: 60},
		{pos: 64, total: 144, unitSize: 64, ssV: 1, wantStart: 60, wantExtent: 84},
		{pos: 0, total: 100, unitSize: 128, wantStart: 0, wantExtent: 100},
	}
	for _, tc := range tests {
		gotStart, gotExtent := restorationUnitYExtent(tc.pos, tc.total, tc.unitSize, tc.ssV)
		if gotStart != tc.wantStart || gotExtent != tc.wantExtent {
			t.Fatalf("restorationUnitYExtent(%d, %d, %d, %d) = (%d, %d), want (%d, %d)",
				tc.pos, tc.total, tc.unitSize, tc.ssV, gotStart, gotExtent, tc.wantStart, tc.wantExtent)
		}
	}
}

func TestDeriveLocalWarpClipsBottomEdgeNeighbourScan(t *testing.T) {
	fs := NewFrameState(352, 288)
	fs.TileX1 = 352
	for i := range fs.BlockGrid {
		fs.BlockGrid[i] = Av1Block{RefFrame: 0, Bs: uint8(BS4x4)}
	}
	if _, ok := deriveLocalWarp(fs, 128, 256, 128, 64, interState{refFrame: 0}); !ok {
		t.Fatal("deriveLocalWarp found no matching edge samples")
	}
}

func TestWalk64x64RegionsUsesDav1dRasterOrder(t *testing.T) {
	type region struct{ x, y, w, h int }
	var got []region
	walk64x64Regions(130, 70, func(x, y, w, h int) {
		got = append(got, region{x, y, w, h})
	})
	want := []region{
		{0, 0, 64, 64}, {64, 0, 64, 64}, {128, 0, 2, 64},
		{0, 64, 64, 6}, {64, 64, 64, 6}, {128, 64, 2, 6},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("64x64 regions = %v, want %v", got, want)
	}
}

func TestRestorationCDFDefaultsMatchDav1d(t *testing.T) {
	ctx := NewTileCtx()
	if got, want := ctx.RestoreSwitchableCDF, [3]uint16{23355, 10187, 0}; got != want {
		t.Fatalf("switchable restoration CDF = %v, want %v", got, want)
	}
	if got, want := ctx.RestoreWienerCDF, [2]uint16{21198, 0}; got != want {
		t.Fatalf("Wiener restoration CDF = %v, want %v", got, want)
	}
	if got, want := ctx.RestoreSGRProjCDF, [2]uint16{15913, 0}; got != want {
		t.Fatalf("SGR restoration CDF = %v, want %v", got, want)
	}
}

func TestClampPreparedBottomLeftReplicatesLastAvailableSample(t *testing.T) {
	edge := make([]byte, 24)
	tl := 12
	edge[tl-4] = 73
	for i := 0; i < 4; i++ {
		edge[tl-5-i] = byte(100 + i)
	}
	clampPreparedBottomLeft(edge, tl, 4)
	for i := 0; i < 4; i++ {
		if got := edge[tl-5-i]; got != 73 {
			t.Fatalf("bottom-left edge %d = %d, want 73", i, got)
		}
	}
}

func TestBuildCflAc420MatchesDav1dShape(t *testing.T) {
	seq := &header.SequenceHeader{SsHor: 1, SsVer: 1}
	fb := &FrameBuf{
		Y: []byte{
			1, 2, 3, 4, 5, 6, 7, 8,
			9, 10, 11, 12, 13, 14, 15, 16,
			17, 18, 19, 20, 21, 22, 23, 24,
			25, 26, 27, 28, 29, 30, 31, 32,
			33, 34, 35, 36, 37, 38, 39, 40,
			41, 42, 43, 44, 45, 46, 47, 48,
			49, 50, 51, 52, 53, 54, 55, 56,
			57, 58, 59, 60, 61, 62, 63, 64,
		},
		StrideY: 8,
		Width:   8,
		Height:  8,
	}

	got := buildCflAc(fb, seq, 0, 0, 8, 8, 4, 4)
	want := buildCflAcRef(fb.Y, fb.StrideY, fb.Width, fb.Height, 0, 0, 8, 8, 4, 4, 1, 1)
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("420 ac[%d]=%d want %d", i, got[i], want[i])
		}
	}
}

func TestBuildCflAc444MatchesDav1dShape(t *testing.T) {
	seq := &header.SequenceHeader{}
	fb := &FrameBuf{
		Y: []byte{
			2, 4, 6, 8,
			10, 12, 14, 16,
			18, 20, 22, 24,
			26, 28, 30, 32,
		},
		StrideY: 4,
		Width:   4,
		Height:  4,
	}

	got := buildCflAc(fb, seq, 0, 0, 4, 4, 4, 4)
	want := buildCflAcRef(fb.Y, fb.StrideY, fb.Width, fb.Height, 0, 0, 4, 4, 4, 4, 0, 0)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("444 ac[%d]=%d want %d", i, got[i], want[i])
		}
	}
}

func TestBuildCflAc420PadsRightAndBottom(t *testing.T) {
	seq := &header.SequenceHeader{SsHor: 1, SsVer: 1}
	fb := &FrameBuf{
		Y: []byte{
			1, 2, 3, 4, 5, 6, 7, 8,
			9, 10, 11, 12, 13, 14, 15, 16,
			17, 18, 19, 20, 21, 22, 23, 24,
			25, 26, 27, 28, 29, 30, 31, 32,
			33, 34, 35, 36, 37, 38, 39, 40,
			41, 42, 43, 44, 45, 46, 47, 48,
			49, 50, 51, 52, 53, 54, 55, 56,
			57, 58, 59, 60, 61, 62, 63, 64,
		},
		StrideY: 8,
		Width:   8,
		Height:  8,
	}

	got := buildCflAc(fb, seq, 2, 2, 6, 6, 4, 4)
	want := buildCflAcRef(fb.Y, fb.StrideY, fb.Width, fb.Height, 2, 2, 6, 6, 4, 4, 1, 1)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("420-pad ac[%d]=%d want %d", i, got[i], want[i])
		}
	}
}

func TestBuildCflAc420UsesReconstructedPlanePadding(t *testing.T) {
	const stride, height = 16, 8
	y := make([]byte, stride*height)
	for row := 0; row < height; row++ {
		for x := 0; x < stride; x++ {
			y[row*stride+x] = byte(3*row + 5*x)
		}
	}
	fb := &FrameBuf{Y: y, StrideY: stride, Width: 10, Height: height}
	seq := &header.SequenceHeader{SsHor: 1, SsVer: 1}

	got := buildCflAc(fb, seq, 8, 0, 8, 8, 4, 4)
	want := buildCflAcRef(y, stride, stride, height, 8, 0, 8, 8, 4, 4, 1, 1)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("padding ac[%d]=%d want %d", i, got[i], want[i])
		}
	}
}

func TestBuildCflAc420AlignsOddOriginToChromaPhase(t *testing.T) {
	seq := &header.SequenceHeader{SsHor: 1, SsVer: 1}
	fb := &FrameBuf{
		Y: []byte{
			1, 2, 3, 4, 5, 6, 7, 8,
			9, 10, 11, 12, 13, 14, 15, 16,
			17, 18, 19, 20, 21, 22, 23, 24,
			25, 26, 27, 28, 29, 30, 31, 32,
			33, 34, 35, 36, 37, 38, 39, 40,
			41, 42, 43, 44, 45, 46, 47, 48,
			49, 50, 51, 52, 53, 54, 55, 56,
			57, 58, 59, 60, 61, 62, 63, 64,
		},
		StrideY: 8,
		Width:   8,
		Height:  8,
	}

	got := buildCflAc(fb, seq, 1, 1, 6, 6, 3, 3)
	want := buildCflAcRef(fb.Y, fb.StrideY, fb.Width, fb.Height, 1, 1, 6, 6, 3, 3, 1, 1)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("420-odd ac[%d]=%d want %d", i, got[i], want[i])
		}
	}
}

func buildCflAcRef(y []byte, stride, width, height, bx, by, bw, bh, cw, ch, ssHor, ssVer int) []int16 {
	ac := make([]int16, cw*ch)
	baseX := bx
	baseY := by
	if ssHor != 0 {
		baseX &^= (1 << ssHor) - 1
	}
	if ssVer != 0 {
		baseY &^= (1 << ssVer) - 1
	}
	validW := cw
	validH := ch
	if remW := width - baseX; remW >= 0 {
		maxW := (remW + (1 << ssHor) - 1) >> ssHor
		if validW > maxW {
			validW = maxW
		}
	} else {
		validW = 0
	}
	if remH := height - baseY; remH >= 0 {
		maxH := (remH + (1 << ssVer) - 1) >> ssVer
		if validH > maxH {
			validH = maxH
		}
	} else {
		validH = 0
	}
	if validW > cw {
		validW = cw
	}
	if validH > ch {
		validH = ch
	}
	for cy := 0; cy < validH; cy++ {
		srcY := baseY + (cy << ssVer)
		if srcY >= height {
			srcY = height - 1
		}
		srcY1 := srcY
		if ssVer != 0 && srcY1+1 < height {
			srcY1++
		}
		for cx := 0; cx < validW; cx++ {
			srcX := baseX + (cx << ssHor)
			if srcX >= width {
				srcX = width - 1
			}
			srcX1 := srcX
			if ssHor != 0 && srcX1+1 < width {
				srcX1++
			}
			acSum := int(y[srcY*stride+srcX])
			if ssHor != 0 {
				acSum += int(y[srcY*stride+srcX1])
			}
			if ssVer != 0 {
				acSum += int(y[srcY1*stride+srcX])
				if ssHor != 0 {
					acSum += int(y[srcY1*stride+srcX1])
				}
			}
			ac[cy*cw+cx] = int16(acSum << (1 + testBoolInt(ssVer == 0) + testBoolInt(ssHor == 0)))
		}
		for cx := validW; cx < cw; cx++ {
			ac[cy*cw+cx] = ac[cy*cw+cx-1]
		}
	}
	for cy := validH; cy < ch; cy++ {
		copy(ac[cy*cw:(cy+1)*cw], ac[(cy-1)*cw:cy*cw])
	}
	log2sz := ctzPow2(cw) + ctzPow2(ch)
	sum := (1 << log2sz) >> 1
	for _, v := range ac {
		sum += int(v)
	}
	sum >>= log2sz
	for i := range ac {
		ac[i] -= int16(sum)
	}
	return ac
}

func testBoolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestMotionModeNeighboursExcludesInterIntraFromWarpSamples(t *testing.T) {
	fs := NewFrameState(32, 32)
	fs.SetBlockState(0, 0, 8, 8, Av1Block{Intra: true})
	fs.SetBlockState(8, 0, 8, 8, Av1Block{Intra: true})
	fs.CommitInterBlock(0, 8, 8, 8, Av1Block{
		RefSlot:    0,
		RefFrame:   1,
		InterIntra: true,
	}, 1)

	overlap, matchingRef := motionModeNeighbours(fs, 8, 8, 8, 8, 0)
	if !overlap {
		t.Fatal("inter-intra neighbour should remain eligible for OBMC overlap")
	}
	if matchingRef {
		t.Fatal("inter-intra neighbour must not enable local warped motion")
	}

	fs.CommitInterBlock(0, 8, 8, 8, Av1Block{RefSlot: 0, RefFrame: 1}, 1)
	_, matchingRef = motionModeNeighbours(fs, 8, 8, 8, 8, 0)
	if !matchingRef {
		t.Fatal("ordinary inter neighbour should enable matching-reference warp")
	}
}

func TestMotionModeNeighboursExcludesCompoundFromWarpSamples(t *testing.T) {
	fs := NewFrameState(32, 32)
	fs.SetBlockState(0, 0, 8, 8, Av1Block{Intra: true})
	fs.SetBlockState(8, 0, 8, 8, Av1Block{Intra: true})
	fs.CommitInterBlock(0, 8, 8, 8, Av1Block{
		Compound: true,
		RefSlot:  0,
		RefSlot2: 1,
		RefFrame: 1,
	}, 1)

	overlap, matchingRef := motionModeNeighbours(fs, 8, 8, 8, 8, 0)
	if !overlap {
		t.Fatal("compound neighbour should remain eligible for OBMC overlap")
	}
	if matchingRef {
		t.Fatal("compound neighbour must not enable local warped motion")
	}
}

func TestMotionModeNeighboursExcludesTopRightFor128Block(t *testing.T) {
	fs := NewFrameState(256, 256)
	fs.TileX1, fs.TileY1 = 256, 256
	fs.RefMVTopRightKnown, fs.RefMVTopRightAvailable = true, true
	fs.SetBlockState(0, 124, 128, 4, Av1Block{RefSlot: 0, RefFrame: 1})
	fs.SetBlockState(124, 128, 4, 128, Av1Block{RefSlot: 0, RefFrame: 1})
	fs.CommitInterBlock(128, 124, 4, 4, Av1Block{RefSlot: 3, RefFrame: 4}, 4)

	overlap, matchingRef := motionModeNeighbours(fs, 0, 128, 128, 128, 3)
	if !overlap {
		t.Fatal("non-matching inter edge neighbours should still make OBMC available")
	}
	if matchingRef {
		t.Fatal("128-pixel block must not use its top-right block as a warp sample")
	}
}

func TestInterReferenceIsScaled(t *testing.T) {
	fhdr := &header.FrameHeader{Height: 720}
	fhdr.Width[0] = 1280

	if interReferenceIsScaled(fhdr, interState{ref: &PlaneBuf{Width: 1280, Height: 720}}) {
		t.Fatal("matching reference size reported as scaled")
	}
	if !interReferenceIsScaled(fhdr, interState{ref: &PlaneBuf{Width: 640, Height: 720}}) {
		t.Fatal("horizontally scaled reference was not detected")
	}
	if !interReferenceIsScaled(fhdr, interState{ref: &PlaneBuf{Width: 1280, Height: 360}}) {
		t.Fatal("vertically scaled reference was not detected")
	}
}

func TestMakeFrameInterPredictionUsesScaledReferenceCoordinates(t *testing.T) {
	ref := &PlaneBuf{
		Y:       make([]byte, 8*8),
		StrideY: 8,
		Width:   8,
		Height:  8,
	}
	for y := 0; y < ref.Height; y++ {
		for x := 0; x < ref.Width; x++ {
			ref.Y[y*ref.StrideY+x] = byte(10 + x)
		}
	}
	fb := &FrameBuf{Width: 16, Height: 16}
	got := makeFrameInterPredictionPlane(fb, ref, ref.Y, ref.StrideY, ref.Width, ref.Height,
		8, 0, 4, 4, refmvs.MV{}, header.FilterMode8TapRegular, header.FilterMode8TapRegular, 0, 0)
	if len(got) != 16 {
		t.Fatalf("prediction length=%d want 16", len(got))
	}
	if got[0] < 13 || got[0] > 15 {
		t.Fatalf("scaled prediction first sample=%d, want source near x=4", got[0])
	}
}

func TestInterFilterContextMatchesCompoundSecondReference(t *testing.T) {
	fs := NewFrameState(32, 32)
	fs.SetBlockState(8, 0, 8, 8, Av1Block{
		Compound: true,
		RefSlot:  0,
		RefSlot2: 6,
		Filter:   uint8(header.FilterMode8TapRegular),
		FilterV:  uint8(header.FilterMode8TapRegular),
	})

	if got := getInterFilterCtx(fs, 0, 6, 8, 8); got != 0 {
		t.Fatalf("compound second-reference filter context=%d want 0", got)
	}
}

func TestPredictCFLBlockUsesTileRelativeEdgeAvailability(t *testing.T) {
	const width, height = 4, 4
	tlBuf, tl := newIntraEdgeBuffer(width, height)
	for x := 0; x < width; x++ {
		tlBuf[tl+1+x] = 200
	}
	for y := 0; y < height; y++ {
		tlBuf[tl-1-y] = 40
	}
	dst := make([]byte, width*height)

	// At a non-zero absolute x coordinate that is also a tile boundary, the
	// left edge belongs to another tile and must not enter the CFL DC base.
	predictCFLBlock(dst, width, tlBuf, tl, width, height, 0, make([]int16, width*height), true, false)
	for i, value := range dst {
		if value != 200 {
			t.Fatalf("prediction[%d]=%d, want top-only DC 200", i, value)
		}
	}
}
