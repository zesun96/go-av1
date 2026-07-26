// Package rdo contains rate-distortion cost helpers for encoder mode search.
package rdo

import (
	"math"

	"github.com/zesun96/go-av1/internal/transform"
)

// Lambda returns an 8-bit distortion-domain multiplier calibrated from AV1's
// AC dequantizer. Squaring qstep follows the usual SSE-domain RDO model.
func Lambda(qindex, bitDepth int) float64 {
	qindex = clamp(qindex, 1, 255)
	bitDepth = clamp(bitDepth, 0, len(transform.DqTbl)-1)
	qstep := float64(transform.DqTbl[bitDepth][qindex][1])
	return 0.85 * qstep * qstep / 256
}

// EstimateCoeffBits estimates entropy cost from quantized coefficient levels.
func EstimateCoeffBits(coeff []int32) float64 {
	bits := 0.0
	for _, level := range coeff {
		if level == 0 {
			bits += 0.04
			continue
		}
		mag := math.Abs(float64(level))
		bits += 2.0 + math.Log2(mag+1)
	}
	return bits
}

// Cost combines prediction distortion and estimated syntax/coefficient rate.
func Cost(distortion int64, coeff []int32, syntaxBits float64, qindex, bitDepth int) float64 {
	return float64(distortion) +
		Lambda(qindex, bitDepth)*(syntaxBits+EstimateCoeffBits(coeff))
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
