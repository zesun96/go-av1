// Package benchmark contains encoder comparison metrics.
package benchmark

import (
	"errors"
	"math"
	"sort"
)

// Point is one bitrate-quality measurement.
type Point struct {
	RateKbps float64 `json:"rate_kbps"`
	PSNR     float64 `json:"psnr"`
}

// BDRate returns the average percentage rate difference of test relative to
// reference over their common PSNR interval.
func BDRate(reference, test []Point) (float64, error) {
	if len(reference) < 4 || len(test) < 4 {
		return 0, errors.New("benchmark: BD-rate needs at least four points per curve")
	}
	ref := append([]Point(nil), reference...)
	tst := append([]Point(nil), test...)
	sort.Slice(ref, func(i, j int) bool { return ref[i].PSNR < ref[j].PSNR })
	sort.Slice(tst, func(i, j int) bool { return tst[i].PSNR < tst[j].PSNR })
	lo := math.Max(ref[0].PSNR, tst[0].PSNR)
	hi := math.Min(ref[len(ref)-1].PSNR, tst[len(tst)-1].PSNR)
	if hi <= lo {
		return 0, errors.New("benchmark: curves have no common PSNR interval")
	}
	refPoly, err := fitLogRate(ref)
	if err != nil {
		return 0, err
	}
	testPoly, err := fitLogRate(tst)
	if err != nil {
		return 0, err
	}
	refAvg := integrate(refPoly, lo, hi) / (hi - lo)
	testAvg := integrate(testPoly, lo, hi) / (hi - lo)
	return (math.Exp(testAvg-refAvg) - 1) * 100, nil
}

func fitLogRate(points []Point) ([4]float64, error) {
	var matrix [4][5]float64
	for _, point := range points {
		if point.RateKbps <= 0 {
			return [4]float64{}, errors.New("benchmark: bitrate must be positive")
		}
		x := point.PSNR
		y := math.Log(point.RateKbps)
		powers := [7]float64{1}
		for i := 1; i < len(powers); i++ {
			powers[i] = powers[i-1] * x
		}
		for row := 0; row < 4; row++ {
			for col := 0; col < 4; col++ {
				matrix[row][col] += powers[row+col]
			}
			matrix[row][4] += y * powers[row]
		}
	}
	for pivot := 0; pivot < 4; pivot++ {
		best := pivot
		for row := pivot + 1; row < 4; row++ {
			if math.Abs(matrix[row][pivot]) > math.Abs(matrix[best][pivot]) {
				best = row
			}
		}
		if math.Abs(matrix[best][pivot]) < 1e-12 {
			return [4]float64{}, errors.New("benchmark: singular quality curve")
		}
		matrix[pivot], matrix[best] = matrix[best], matrix[pivot]
		scale := matrix[pivot][pivot]
		for col := pivot; col < 5; col++ {
			matrix[pivot][col] /= scale
		}
		for row := 0; row < 4; row++ {
			if row == pivot {
				continue
			}
			scale = matrix[row][pivot]
			for col := pivot; col < 5; col++ {
				matrix[row][col] -= scale * matrix[pivot][col]
			}
		}
	}
	return [4]float64{matrix[0][4], matrix[1][4], matrix[2][4], matrix[3][4]}, nil
}

func integrate(poly [4]float64, lo, hi float64) float64 {
	primitive := func(x float64) float64 {
		return poly[0]*x +
			poly[1]*x*x/2 +
			poly[2]*x*x*x/3 +
			poly[3]*x*x*x*x/4
	}
	return primitive(hi) - primitive(lo)
}
