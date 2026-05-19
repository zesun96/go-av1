package transform

import (
	"math/rand"
	"testing"
)

// ---- IclipU8 ---------------------------------------------------------------

func TestIclipU8(t *testing.T) {
	cases := []struct{ v, want int }{
		{-1, 0}, {0, 0}, {1, 1}, {127, 127}, {255, 255}, {256, 255}, {1000, 255},
	}
	for _, c := range cases {
		if got := IclipU8(c.v); got != c.want {
			t.Fatalf("IclipU8(%d)=%d want %d", c.v, got, c.want)
		}
	}
}

// ---- DqTbl spot checks -----------------------------------------------------

func TestDqTbl_FirstLast(t *testing.T) {
	// First entry for 8-bit: {4, 4}
	if DqTbl[0][0][0] != 4 || DqTbl[0][0][1] != 4 {
		t.Fatalf("8-bit qidx=0: got {%d,%d} want {4,4}", DqTbl[0][0][0], DqTbl[0][0][1])
	}
	// Last entry for 8-bit: {1336, 1828}
	if DqTbl[0][255][0] != 1336 || DqTbl[0][255][1] != 1828 {
		t.Fatalf("8-bit qidx=255: got {%d,%d} want {1336,1828}", DqTbl[0][255][0], DqTbl[0][255][1])
	}
	// First entry for 10-bit: {4, 4}
	if DqTbl[1][0][0] != 4 || DqTbl[1][0][1] != 4 {
		t.Fatalf("10-bit qidx=0: got {%d,%d} want {4,4}", DqTbl[1][0][0], DqTbl[1][0][1])
	}
	// Last entry for 10-bit: {5347, 7312}
	if DqTbl[1][255][0] != 5347 || DqTbl[1][255][1] != 7312 {
		t.Fatalf("10-bit qidx=255: got {%d,%d} want {5347,7312}", DqTbl[1][255][0], DqTbl[1][255][1])
	}
	// Last entry for 12-bit: {21387, 29247}
	if DqTbl[2][255][0] != 21387 || DqTbl[2][255][1] != 29247 {
		t.Fatalf("12-bit qidx=255: got {%d,%d} want {21387,29247}", DqTbl[2][255][0], DqTbl[2][255][1])
	}
}

func TestDqTbl_SpotMiddle(t *testing.T) {
	// 8-bit qidx=128: verify from actual table data
	if DqTbl[0][128][0] != 140 || DqTbl[0][128][1] != 176 {
		t.Fatalf("8-bit qidx=128: got {%d,%d} want {140,176}", DqTbl[0][128][0], DqTbl[0][128][1])
	}
	// 10-bit qidx=100: verify from actual table data
	if DqTbl[1][100][0] != 369 || DqTbl[1][100][1] != 441 {
		t.Fatalf("10-bit qidx=100: got {%d,%d} want {369,441}", DqTbl[1][100][0], DqTbl[1][100][1])
	}
	// 12-bit qidx=200: verify from actual table data
	if DqTbl[2][200][0] != 6234 || DqTbl[2][200][1] != 10220 {
		t.Fatalf("12-bit qidx=200: got {%d,%d} want {6234,10220}", DqTbl[2][200][0], DqTbl[2][200][1])
	}
}

func TestDqTbl_Monotonic(t *testing.T) {
	// AC dequant values should be non-decreasing with qindex.
	for hbd := 0; hbd < 3; hbd++ {
		for i := 1; i < QindexRange; i++ {
			if DqTbl[hbd][i][1] < DqTbl[hbd][i-1][1] {
				t.Fatalf("hbd=%d AC not monotonic at qidx=%d: %d < %d",
					hbd, i, DqTbl[hbd][i][1], DqTbl[hbd][i-1][1])
			}
		}
	}
}

func TestDqTbl_ACgeDC(t *testing.T) {
	// AC dequant should be >= DC dequant for the same qindex (AV1 spec).
	for hbd := 0; hbd < 3; hbd++ {
		for i := 0; i < QindexRange; i++ {
			if DqTbl[hbd][i][1] < DqTbl[hbd][i][0] {
				t.Fatalf("hbd=%d qidx=%d: AC=%d < DC=%d",
					hbd, i, DqTbl[hbd][i][1], DqTbl[hbd][i][0])
			}
		}
	}
}

// ---- InitQuantTables -------------------------------------------------------

func TestInitQuantTables_NoSeg(t *testing.T) {
	// Without segmentation, only segment 0 is filled.
	dq := InitQuantTables(128, QuantDeltas{}, 0, nil)

	// Y: yac=128, ydc=128 → DqTbl[0][128][0/1]
	if dq[0][0][0] != DqTbl[0][128][0] {
		t.Fatalf("Y DC: got %d want %d", dq[0][0][0], DqTbl[0][128][0])
	}
	if dq[0][0][1] != DqTbl[0][128][1] {
		t.Fatalf("Y AC: got %d want %d", dq[0][0][1], DqTbl[0][128][1])
	}
	// U/V same as Y when deltas are zero
	if dq[0][1][0] != DqTbl[0][128][0] {
		t.Fatalf("U DC: got %d want %d", dq[0][1][0], DqTbl[0][128][0])
	}
	if dq[0][2][1] != DqTbl[0][128][1] {
		t.Fatalf("V AC: got %d want %d", dq[0][2][1], DqTbl[0][128][1])
	}
}

func TestInitQuantTables_WithDeltas(t *testing.T) {
	// qidx=100, Ydc_delta=5 → ydc = iclip_u8(100+5) = 105
	// Udc_delta=-10 → udc = iclip_u8(100-10) = 90
	d := QuantDeltas{YdcDelta: 5, UdcDelta: -10}
	dq := InitQuantTables(100, d, 0, nil)

	if dq[0][0][0] != DqTbl[0][105][0] {
		t.Fatalf("Y DC: got %d want %d", dq[0][0][0], DqTbl[0][105][0])
	}
	if dq[0][0][1] != DqTbl[0][100][1] {
		t.Fatalf("Y AC: got %d want %d", dq[0][0][1], DqTbl[0][100][1])
	}
	if dq[0][1][0] != DqTbl[0][90][0] {
		t.Fatalf("U DC: got %d want %d", dq[0][1][0], DqTbl[0][90][0])
	}
}

func TestInitQuantTables_DeltaClip(t *testing.T) {
	// qidx=250, Ydc_delta=20 → ydc = iclip_u8(270) = 255
	d := QuantDeltas{YdcDelta: 20}
	dq := InitQuantTables(250, d, 0, nil)
	if dq[0][0][0] != DqTbl[0][255][0] {
		t.Fatalf("Y DC clipped: got %d want %d", dq[0][0][0], DqTbl[0][255][0])
	}

	// qidx=5, Ydc_delta=-10 → ydc = iclip_u8(-5) = 0
	d2 := QuantDeltas{YdcDelta: -10}
	dq2 := InitQuantTables(5, d2, 0, nil)
	if dq2[0][0][0] != DqTbl[0][0][0] {
		t.Fatalf("Y DC clipped low: got %d want %d", dq2[0][0][0], DqTbl[0][0][0])
	}
}

func TestInitQuantTables_WithSegmentation(t *testing.T) {
	segDQ := make([]int, 8)
	segDQ[0] = 0
	segDQ[1] = 10
	segDQ[2] = -5
	dq := InitQuantTables(100, QuantDeltas{}, 0, segDQ)

	// seg 0: yac=100
	if dq[0][0][1] != DqTbl[0][100][1] {
		t.Fatalf("seg0 Y AC: got %d want %d", dq[0][0][1], DqTbl[0][100][1])
	}
	// seg 1: yac=iclip_u8(100+10)=110
	if dq[1][0][1] != DqTbl[0][110][1] {
		t.Fatalf("seg1 Y AC: got %d want %d", dq[1][0][1], DqTbl[0][110][1])
	}
	// seg 2: yac=iclip_u8(100-5)=95
	if dq[2][0][1] != DqTbl[0][95][1] {
		t.Fatalf("seg2 Y AC: got %d want %d", dq[2][0][1], DqTbl[0][95][1])
	}
}

func TestInitQuantTables_HBD(t *testing.T) {
	// 10-bit hbd=1
	dq := InitQuantTables(128, QuantDeltas{}, 1, nil)
	if dq[0][0][1] != DqTbl[1][128][1] {
		t.Fatalf("10-bit Y AC: got %d want %d", dq[0][0][1], DqTbl[1][128][1])
	}
	// 12-bit hbd=2
	dq2 := InitQuantTables(128, QuantDeltas{}, 2, nil)
	if dq2[0][0][1] != DqTbl[2][128][1] {
		t.Fatalf("12-bit Y AC: got %d want %d", dq2[0][0][1], DqTbl[2][128][1])
	}
}

// ---- Dequant ---------------------------------------------------------------

func TestDequant_DCOnly_4x4(t *testing.T) {
	// 4×4, ctx=0 → dq_shift=0
	coeffs := make([]int32, 16)
	coeffs[0] = 3 // DC level = 3
	dq := [2]uint16{100, 80}

	Dequant(coeffs, 4, dq, TX4x4, nil, -1)

	// DC: dc_dq * level = 100 * 3 = 300, dq_shift=0 → 300
	if coeffs[0] != 300 {
		t.Fatalf("DC: got %d want 300", coeffs[0])
	}
}

func TestDequant_DCOnly_32x32(t *testing.T) {
	// 32×32, ctx=3 → dq_shift=1
	coeffs := make([]int32, 32*32)
	coeffs[0] = 4 // DC level
	dq := [2]uint16{200, 150}

	Dequant(coeffs, 32, dq, TX32x32, nil, -1)

	// DC: 200 * 4 = 800, >> 1 = 400
	if coeffs[0] != 400 {
		t.Fatalf("DC 32x32: got %d want 400", coeffs[0])
	}
}

func TestDequant_DCOnly_64x64(t *testing.T) {
	// 64×64, ctx=4 → dq_shift=2
	coeffs := make([]int32, 64*64)
	coeffs[0] = 8 // DC level
	dq := [2]uint16{200, 150}

	Dequant(coeffs, 64, dq, TX64x64, nil, -1)

	// DC: 200 * 8 = 1600, >> 2 = 400
	if coeffs[0] != 400 {
		t.Fatalf("DC 64x64: got %d want 400", coeffs[0])
	}
}

func TestDequant_AC_4x4(t *testing.T) {
	// 4×4, dq_shift=0
	coeffs := make([]int32, 16)
	coeffs[1] = 2  // AC level
	coeffs[5] = -3 // negative AC
	dq := [2]uint16{100, 80}

	Dequant(coeffs, 4, dq, TX4x4, nil, -1)

	// DC: 100 * 0 = 0
	if coeffs[0] != 0 {
		t.Fatalf("DC: got %d want 0", coeffs[0])
	}
	// AC[1]: 80 * 2 = 160
	if coeffs[1] != 160 {
		t.Fatalf("AC[1]: got %d want 160", coeffs[1])
	}
	// AC[5]: sign=-1, 80 * 3 = 240 → -240
	if coeffs[5] != -240 {
		t.Fatalf("AC[5]: got %d want -240", coeffs[5])
	}
}

func TestDequant_AC_32x32_Shift(t *testing.T) {
	// 32×32, dq_shift=1
	coeffs := make([]int32, 32*32)
	coeffs[1] = 5 // AC level
	dq := [2]uint16{200, 150}

	Dequant(coeffs, 32, dq, TX32x32, nil, -1)

	// AC[1]: 150 * 5 = 750, >> 1 = 375
	if coeffs[1] != 375 {
		t.Fatalf("AC 32x32: got %d want 375", coeffs[1])
	}
}

func TestDequant_EOB(t *testing.T) {
	// eob=1 means only DC and first AC are valid
	coeffs := make([]int32, 16)
	coeffs[0] = 2
	coeffs[1] = 3
	coeffs[2] = 4 // should NOT be dequantized (beyond eob)
	dq := [2]uint16{100, 80}

	Dequant(coeffs, 4, dq, TX4x4, nil, 1)

	if coeffs[0] != 200 {
		t.Fatalf("DC: got %d want 200", coeffs[0])
	}
	if coeffs[1] != 240 { // 80*3
		t.Fatalf("AC[1]: got %d want 240", coeffs[1])
	}
	if coeffs[2] != 4 {
		t.Fatalf("AC[2] beyond eob: got %d want 4 (untouched)", coeffs[2])
	}
}

func TestDequant_ZeroCoeffs(t *testing.T) {
	coeffs := make([]int32, 16)
	dq := [2]uint16{100, 80}

	Dequant(coeffs, 4, dq, TX4x4, nil, -1)

	// All should remain zero
	for i, v := range coeffs {
		if v != 0 {
			t.Fatalf("coeffs[%d]=%d want 0", i, v)
		}
	}
}

func TestDequant_WithQM(t *testing.T) {
	// 4×4 with QM table
	coeffs := make([]int32, 16)
	coeffs[0] = 3 // DC
	coeffs[1] = 2 // AC
	dq := [2]uint16{100, 80}

	// QM values: qm[0]=32 (neutral), qm[1]=64 (double)
	qm := make([]uint8, 16)
	qm[0] = 32
	qm[1] = 64
	for i := 2; i < 16; i++ {
		qm[i] = 32
	}

	Dequant(coeffs, 4, dq, TX4x4, qm, -1)

	// DC: (100*32+16)>>5 = (3200+16)>>5 = 3216>>5 = 100, then 100*3=300
	if coeffs[0] != 300 {
		t.Fatalf("DC with QM: got %d want 300", coeffs[0])
	}
	// AC[1]: (80*64+16)>>5 = (5120+16)>>5 = 5136>>5 = 160, then 160*2=320
	if coeffs[1] != 320 {
		t.Fatalf("AC[1] with QM: got %d want 320", coeffs[1])
	}
}

func TestDequant_RandomSmoke(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for trial := 0; trial < 32; trial++ {
		txSz := TX4x4
		size := 16
		if trial%3 == 1 {
			txSz = TX8x8
			size = 64
		} else if trial%3 == 2 {
			txSz = TX16x16
			size = 256
		}

		coeffs := make([]int32, size)
		for i := range coeffs {
			coeffs[i] = int32(rng.Intn(21) - 10) // [-10, 10]
		}

		dq := [2]uint16{
			uint16(rng.Intn(256) + 10),
			uint16(rng.Intn(256) + 10),
		}

		Dequant(coeffs, int(TxfmDimensions[txSz].W)*4, dq, txSz, nil, -1)

		// Verify dequantized values are within reasonable bounds
		for i, v := range coeffs {
			if v > 0x7FFF || v < -0x7FFF {
				t.Fatalf("trial %d: coeffs[%d] = %d out of range", trial, i, v)
			}
		}
	}
}

func TestDequant_RectSize(t *testing.T) {
	// RTX16x32, ctx=3 → dq_shift=1
	coeffs := make([]int32, 16*32)
	coeffs[0] = 4
	coeffs[1] = 3
	dq := [2]uint16{200, 150}

	Dequant(coeffs, 16, dq, RTX16x32, nil, -1)

	// DC: 200*4=800, >>1=400
	if coeffs[0] != 400 {
		t.Fatalf("DC rect: got %d want 400", coeffs[0])
	}
	// AC: 150*3=450, >>1=225
	if coeffs[1] != 225 {
		t.Fatalf("AC rect: got %d want 225", coeffs[1])
	}
}
