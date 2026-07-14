package tile

import (
	"testing"

	"github.com/zesun96/go-av1/internal/transform"
)

func TestRectTxSizeForLogs(t *testing.T) {
	cases := []struct {
		lw, lh uint8
		want   uint8
		ok     bool
	}{
		{0, 0, transform.TX4x4, true},
		{1, 1, transform.TX8x8, true},
		{2, 2, transform.TX16x16, true},
		{3, 3, transform.TX32x32, true},
		{4, 4, transform.TX64x64, true},
		{0, 1, transform.RTX4x8, true},
		{1, 0, transform.RTX8x4, true},
		{2, 4, transform.RTX16x64, true},
		{4, 2, transform.RTX64x16, true},
		{5, 5, 0, false},
	}
	for _, tc := range cases {
		got, ok := rectTxSizeForLogs(tc.lw, tc.lh)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("rectTxSizeForLogs(%d,%d) = (%d,%v), want (%d,%v)",
				tc.lw, tc.lh, got, ok, tc.want, tc.ok)
		}
	}
}

func TestGetScanUsesExact2DTables(t *testing.T) {
	scan := GetScan(1, 1, TxClass2D)
	if len(scan) != len(scan8x8) {
		t.Fatalf("len(scan8x8) = %d, want %d", len(scan), len(scan8x8))
	}
	for i, v := range scan8x8 {
		if scan[i] != v {
			t.Fatalf("scan8x8[%d] = %d, want %d", i, scan[i], v)
		}
	}
}

func TestGetScanFallsBackFor1D(t *testing.T) {
	scan := GetScan(1, 1, TxClassH)
	if len(scan) != 64 {
		t.Fatalf("len(1D scan) = %d, want 64", len(scan))
	}
	for i := range scan {
		if int(scan[i]) != i {
			t.Fatalf("1D scan[%d] = %d, want %d", i, scan[i], i)
		}
	}
}

func TestLastNonzeroColFromEOB(t *testing.T) {
	tests := []struct {
		tx   uint8
		eob  int
		want int
	}{
		{transform.TX4x4, 0, 0},
		{transform.TX4x4, 1, 0},
		{transform.TX4x4, 2, 1},
		{transform.TX4x4, 3, 2},
		{transform.TX4x4, 15, 3},
		{transform.TX64x64, 1023, 31},
	}

	for _, tc := range tests {
		got, ok := LastNonzeroColFromEOB(tc.tx, tc.eob)
		if !ok {
			t.Fatalf("LastNonzeroColFromEOB(%d, %d) unavailable", tc.tx, tc.eob)
		}
		if got != tc.want {
			t.Fatalf("LastNonzeroColFromEOB(%d, %d) = %d, want %d", tc.tx, tc.eob, got, tc.want)
		}
	}
}

func TestLastNonzeroColFromEOBMatchesScanPacking(t *testing.T) {
	for tx, scan := range Scans {
		if len(scan) == 0 {
			continue
		}
		height := int(transform.TxfmDimensions[tx].H) * 4
		if height > 32 {
			height = 32
		}
		maxCol := 0
		for eob, rc := range scan {
			if col := int(rc) & (height - 1); col > maxCol {
				maxCol = col
			}
			got, ok := LastNonzeroColFromEOB(uint8(tx), eob)
			if !ok || got != maxCol {
				t.Fatalf("tx=%d eob=%d: got (%d, %v), want (%d, true)", tx, eob, got, ok, maxCol)
			}
		}
	}
}
