package rdo

import "testing"

func TestLambdaIncreasesWithQuantizer(t *testing.T) {
	if lo, hi := Lambda(40, 0), Lambda(180, 0); hi <= lo {
		t.Fatalf("lambda(40)=%f lambda(180)=%f", lo, hi)
	}
}

func TestCostPenalizesCoefficientRate(t *testing.T) {
	zero := Cost(100, make([]int32, 64), 1, 120, 0)
	denseCoeff := make([]int32, 64)
	for i := range denseCoeff {
		denseCoeff[i] = 3
	}
	dense := Cost(100, denseCoeff, 1, 120, 0)
	if dense <= zero {
		t.Fatalf("dense cost=%f zero cost=%f", dense, zero)
	}
}
