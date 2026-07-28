package tx

import (
	"testing"

	"github.com/zesun96/go-av1/internal/transform"
)

func TestQuantizeTX32AppliesDequantShift(t *testing.T) {
	small := []int32{1000}
	large := []int32{1000}
	QuantizeTx(small, 64, 0, transform.TX16x16)
	QuantizeTx(large, 64, 0, transform.TX32x32)
	if delta := int(large[0] - small[0]*2); delta < 0 || delta > 1 {
		t.Fatalf("TX32 level=%d, want twice TX16 level %d within rounding",
			large[0], small[0])
	}
	DequantizeTx(small, 64, 0, transform.TX16x16)
	DequantizeTx(large, 64, 0, transform.TX32x32)
	tolerance := int(transform.DqTbl[0][64][0])/2 + 1
	for name, got := range map[string]int32{"TX16": small[0], "TX32": large[0]} {
		if diff := int(got - 1000); diff < -tolerance || diff > tolerance {
			t.Fatalf("dequantized %s=%d, want 1000 within %d", name, got, tolerance)
		}
	}
}
