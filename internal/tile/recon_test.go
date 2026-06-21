package tile

import (
	"testing"

	"github.com/zesun96/go-av1/internal/transform"
)

func TestShiftFromTx(t *testing.T) {
	cases := []struct {
		tx   uint8
		want int
	}{
		{transform.TX4x4, 0},
		{transform.TX8x8, 1},
		{transform.TX16x16, 2},
		{transform.TX32x32, 2},
		{transform.TX64x64, 2},
		{transform.RTX4x8, 1},
		{transform.RTX8x4, 1},
		{transform.RTX8x16, 2},
		{transform.RTX16x8, 2},
		{transform.RTX16x32, 2},
		{transform.RTX32x16, 2},
	}
	for _, c := range cases {
		if got := shiftFromTx(c.tx); got != c.want {
			t.Fatalf("shiftFromTx(TxfmSize=%d) = %d want %d", c.tx, got, c.want)
		}
	}
}

func TestReconBlock_DCSkip(t *testing.T) {
	// When skip=true (no coefficients), dst should remain unchanged
	w, h := 4, 4
	dst := make([]uint8, w*h)
	for i := range dst {
		dst[i] = 100
	}

	coeff := make([]int32, w*h)
	dq := [2]uint16{4, 4}

	ReconBlock(dst, w, coeff, -1, transform.TX4x4, transform.DCT_DCT, dq, 8)

	for i, v := range dst {
		if v != 100 {
			t.Fatalf("skip: dst[%d]=%d want 100", i, v)
		}
	}
}

func TestReconBlock_DCOnly(t *testing.T) {
	// DC-only: single non-zero DC coefficient
	w, h := 4, 4
	dst := make([]uint8, w*h)
	for i := range dst {
		dst[i] = 100
	}

	coeff := make([]int32, w*h)
	coeff[0] = 256 // DC coefficient

	dq := [2]uint16{4, 4}

	ReconBlock(dst, w, coeff, 0, transform.TX4x4, transform.DCT_DCT, dq, 8)

	// After dequant: coeffs[0] = 256 * 4 = 1024
	// After InvTxfmAdd DC-only:
	//   dc = 1024
	//   dc = (1024*181+128)>>8 = 724
	//   dc = (724*181+128+2048)>>12 = 32
	// Expected: each pixel = 100 + 32 = 132
	for i, v := range dst {
		if v != 132 {
			t.Fatalf("DC-only: dst[%d]=%d want 132", i, v)
		}
	}
}

func TestReconBIntra_MultiBlock(t *testing.T) {
	// 8x8 block with 2x2 grid of 4x4 transform blocks
	bw4, bh4 := 2, 2 // 8x8 block
	tx := uint8(transform.TX4x4)

	dst := make([]uint8, 8*8)
	predBuf := make([]uint8, 32*32)

	// Prediction: constant 50
	predFn := func(pred []uint8, tbx, tby, tw_, th_ int) {
		for i := range pred {
			pred[i] = 50
		}
	}

	// Coefficients: only top-left block has DC residual
	coeffTL := make([]int32, 16)
	coeffTL[0] = 256
	var coeffNil []int32

	callCount := 0
	coeffFn := func(tbx, tby int) (coeff []int32, eob int, txtp uint8, dq [2]uint16, skip bool) {
		callCount++
		dq = [2]uint16{4, 4}
		if tbx == 0 && tby == 0 {
			return coeffTL, 0, transform.DCT_DCT, dq, false
		}
		return coeffNil, -1, transform.DCT_DCT, dq, true
	}

	ReconBIntra(dst, 8, bw4, bh4, tx, predBuf, predFn, coeffFn, 8)

	// Top-left 4x4 should have DC residual added
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			got := dst[y*8+x]
			if got != 82 { // 50 + 32 = 82
				t.Fatalf("multi-block TL[%d,%d]=%d want 82", y, x, got)
			}
		}
	}

	// Other blocks should remain at prediction value
	for y := 0; y < 4; y++ {
		for x := 4; x < 8; x++ {
			if dst[y*8+x] != 50 {
				t.Fatalf("multi-block TR[%d,%d]=%d want 50", y, x, dst[y*8+x])
			}
		}
	}
	for y := 4; y < 8; y++ {
		for x := 0; x < 4; x++ {
			if dst[y*8+x] != 50 {
				t.Fatalf("multi-block BL[%d,%d]=%d want 50", y, x, dst[y*8+x])
			}
		}
		for x := 4; x < 8; x++ {
			if dst[y*8+x] != 50 {
				t.Fatalf("multi-block BR[%d,%d]=%d want 50", y, x, dst[y*8+x])
			}
		}
	}

	// coeffFn should be called 4 times (2x2 grid)
	if callCount != 4 {
		t.Fatalf("coeffFn called %d times, want 4", callCount)
	}
}

func TestReconBIntra_16x16_With_8x8_Tx(t *testing.T) {
	// 16x16 block with 2x2 grid of 8x8 transform blocks
	bw4, bh4 := 4, 4 // 16x16 block
	tx := uint8(transform.TX8x8)

	dst := make([]uint8, 16*16)
	predBuf := make([]uint8, 32*32)

	// Prediction: constant 60
	predFn := func(pred []uint8, tbx, tby, tw, th int) {
		for i := range pred {
			pred[i] = 60
		}
	}

	// All blocks have DC-only residual
	coeff := make([]int32, 64)
	coeff[0] = 128

	coeffFn := func(tbx, tby int) (coeff []int32, eob int, txtp uint8, dq [2]uint16, skip bool) {
		return nil, -1, transform.DCT_DCT, [2]uint16{4, 4}, true
	}

	ReconBIntra(dst, 16, bw4, bh4, tx, predBuf, predFn, coeffFn, 8)

	// All pixels should be 60 (skip=true for all)
	for i, v := range dst {
		if v != 60 {
			t.Fatalf("skip-all: dst[%d]=%d want 60", i, v)
		}
	}
}

// ---- BlockDimensions & Scans table sanity -----------------------------------

func TestBlockDimensions_Spot(t *testing.T) {
	// BS4x4: w=1, h=1, lw=0, lh=0
	d := BlockDimensions[BS4x4]
	if d[0] != 1 || d[1] != 1 || d[2] != 0 || d[3] != 0 {
		t.Fatalf("BS4x4 dims=%v want {1,1,0,0}", d)
	}
	// BS64x64: w=16, h=16, lw=4, lh=4
	d = BlockDimensions[BS64x64]
	if d[0] != 16 || d[1] != 16 || d[2] != 4 || d[3] != 4 {
		t.Fatalf("BS64x64 dims=%v want {16,16,4,4}", d)
	}
	// BS8x4: w=2, h=1, lw=1, lh=0
	d = BlockDimensions[BS8x4]
	if d[0] != 2 || d[1] != 1 || d[2] != 1 || d[3] != 0 {
		t.Fatalf("BS8x4 dims=%v want {2,1,1,0}", d)
	}
}

func TestScans_LengthAndFirstLast(t *testing.T) {
	// scan4x4: 16 entries, first=0, last=15
	if len(Scans[transform.TX4x4]) != 16 {
		t.Fatalf("TX4x4 scan len=%d want 16", len(Scans[transform.TX4x4]))
	}
	if Scans[transform.TX4x4][0] != 0 {
		t.Fatalf("TX4x4 scan[0]=%d want 0", Scans[transform.TX4x4][0])
	}
	if Scans[transform.TX4x4][15] != 15 {
		t.Fatalf("TX4x4 scan[15]=%d want 15", Scans[transform.TX4x4][15])
	}
	// scan8x8: 64 entries
	if len(Scans[transform.TX8x8]) != 64 {
		t.Fatalf("TX8x8 scan len=%d want 64", len(Scans[transform.TX8x8]))
	}
	// TX64x64 shares scan32x32: 1024 entries
	if len(Scans[transform.TX64x64]) != 1024 {
		t.Fatalf("TX64x64 scan len=%d want 1024", len(Scans[transform.TX64x64]))
	}
	// RTX4x8: 32 entries
	if len(Scans[transform.RTX4x8]) != 32 {
		t.Fatalf("RTX4x8 scan len=%d want 32", len(Scans[transform.RTX4x8]))
	}
	// Verify scan8x8 is a permutation of 0..63
	seen := make([]bool, 64)
	for _, v := range Scans[transform.TX8x8] {
		if int(v) >= 64 {
			t.Fatalf("TX8x8 scan entry %d out of range [0,64)", v)
		}
		seen[v] = true
	}
	for i, ok := range seen {
		if !ok {
			t.Fatalf("TX8x8 scan missing entry %d", i)
		}
	}
}

func TestScans_4x4_FullPermutation(t *testing.T) {
	// scan4x4 must be a permutation of 0..15
	seen := make([]bool, 16)
	for _, v := range Scans[transform.TX4x4] {
		if int(v) >= 16 {
			t.Fatalf("TX4x4 scan entry %d out of range", v)
		}
		seen[v] = true
	}
	for i, ok := range seen {
		if !ok {
			t.Fatalf("TX4x4 scan missing entry %d", i)
		}
	}
}

// ---- End-to-end integration: pred + dequant + itx -------------------------

func TestE2E_PredPlusDCResidue(t *testing.T) {
	// Simulate DC_PRED on a 4x4 block with a DC-only residual.
	// Topleft=[100, 100, ...] → DC pred = 100. Residual DC coeff = 64.
	// Expected: every pixel = 100 + dequant_and_itx(64, dq=8)
	//   After dequant: 64 * 8 = 512
	//   InvTxfmAdd DC-only:
	//     dc = 512 * 181 + 128 >> 8 = 362
	//     dc = 362 * 181 + 128 + 2048 >> 12 = 16
	//   Final pixel: 100 + 16 = 116

	dst := make([]uint8, 4*4)

	// Simple DC prediction at 100
	pred := [4 * 4]uint8{}
	for i := range pred {
		pred[i] = 100
	}
	copy(dst, pred[:])

	coeff := make([]int32, 4*4)
	coeff[0] = 64

	dq := [2]uint16{8, 8}
	ReconBlock(dst, 4, coeff, 0, transform.TX4x4, transform.DCT_DCT, dq, 8)

	for i, v := range dst {
		if v != 116 {
			t.Fatalf("e2e: dst[%d]=%d want 116", i, v)
		}
	}
}

func TestReconBlock_UsesExactLastNonzeroColPath(t *testing.T) {
	dstA := make([]uint8, 8*8)
	dstB := make([]uint8, 8*8)
	for i := range dstA {
		dstA[i] = 90
		dstB[i] = 90
	}

	coeffA := make([]int32, 8*8)
	coeffB := make([]int32, 8*8)
	coeffA[0] = 5
	coeffA[1] = -3
	coeffA[8] = 2
	copy(coeffB, coeffA)

	dq := [2]uint16{8, 8}
	eob := 3

	ReconBlock(dstA, 8, coeffA, eob, transform.TX8x8, transform.DCT_DCT, dq, 8)

	transform.Dequant(coeffB, int(transform.TxfmDimensions[transform.TX8x8].W)*4, dq, int(transform.TX8x8), nil, eob, 8)
	exact, ok := LastNonzeroColFromEOB(transform.TX8x8, eob)
	if !ok {
		t.Fatal("expected exact last-nonzero-col for TX8x8")
	}
	transform.InvTxfmAddWithLastNonzeroCol(dstB, 8, coeffB, eob, transform.TX8x8, shiftFromTx(transform.TX8x8), transform.DCT_DCT, exact, 8)

	for i := range dstA {
		if dstA[i] != dstB[i] {
			t.Fatalf("dst[%d]=%d want %d", i, dstA[i], dstB[i])
		}
	}
	for i := range coeffA {
		if coeffA[i] != coeffB[i] {
			t.Fatalf("coeff[%d]=%d want %d", i, coeffA[i], coeffB[i])
		}
	}
}
