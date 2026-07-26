package benchmark

import (
	"math"
	"testing"
)

func TestBDRateIdenticalAndDoubleRateCurves(t *testing.T) {
	ref := []Point{
		{100, 30}, {180, 32}, {320, 34}, {560, 36},
	}
	if got, err := BDRate(ref, ref); err != nil || math.Abs(got) > 1e-6 {
		t.Fatalf("identical BD-rate=%f err=%v", got, err)
	}
	double := append([]Point(nil), ref...)
	for i := range double {
		double[i].RateKbps *= 2
	}
	got, err := BDRate(ref, double)
	if err != nil || math.Abs(got-100) > 1e-5 {
		t.Fatalf("double-rate BD-rate=%f err=%v", got, err)
	}
}
