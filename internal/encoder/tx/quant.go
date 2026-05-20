package tx

import "github.com/zesun96/go-av1/internal/transform"

// Quantize applies forward quantization to an 8x8 coefficient block.
//
// For each coefficient: qcoeff = sign(coeff) * floor((abs(coeff) * qval + round) >> shift)
// where qval is DqTbl[hbd][qindex][isAC] and shift = dqShift derived from transform size.
//
// Parameters:
//   - coeffs: 64 transform coefficients (8x8, raster order)
//   - qindex: quantization parameter index [0, 255]
//   - hbd: bit depth index (0=8bit, 1=10bit, 2=12bit)
//
// Returns the index of the last non-zero coefficient (eob), or -1 if all zero.
func Quantize(coeffs []int32, qindex int, hbd int) int {
	dcDequant := int(transform.DqTbl[hbd][qindex][0])
	acDequant := int(transform.DqTbl[hbd][qindex][1])

	// dqShift for TX_8X8 (ctx=2 in TxfmDimensions): max(0, 2-2) = 0
	const dqShift = 0

	eob := -1
	for i := 0; i < 64; i++ {
		if coeffs[i] == 0 {
			continue
		}

		dq := acDequant
		if i == 0 {
			dq = dcDequant
		}

		sign := int32(1)
		level := int(coeffs[i])
		if level < 0 {
			sign = -1
			level = -level
		}

		// Forward quantization: qcoeff = (level + dq/2) / dq
		// This is a simple round-to-nearest division.
		qcoeff := (level + dq/2) / dq
		if qcoeff > 0 {
			coeffs[i] = sign * int32(qcoeff)
			eob = i
		} else {
			coeffs[i] = 0
		}
	}
	return eob
}

// Dequantize applies inverse quantization (for reconstruction loop).
// This reconstructs the approximate coefficients from quantized levels.
func Dequantize(coeffs []int32, qindex int, hbd int) {
	dcDequant := int(transform.DqTbl[hbd][qindex][0])
	acDequant := int(transform.DqTbl[hbd][qindex][1])

	const dqShift = 0

	for i := 0; i < 64; i++ {
		if coeffs[i] == 0 {
			continue
		}

		dq := acDequant
		if i == 0 {
			dq = dcDequant
		}

		sign := int32(1)
		level := int(coeffs[i])
		if level < 0 {
			sign = -1
			level = -level
		}

		coeffs[i] = sign * int32((level*dq)>>dqShift)
	}
}
