package tx

import "github.com/zesun96/go-av1/internal/transform"

// Quantize applies forward quantization to a small-transform coefficient block.
//
// For each coefficient: qcoeff = sign(coeff) * floor((abs(coeff) * qval + round) >> shift)
// where qval is DqTbl[hbd][qindex][isAC] and shift = dqShift derived from transform size.
//
// Parameters:
//   - coeffs: transform coefficients in raster order
//   - qindex: quantization parameter index [0, 255]
//   - hbd: bit depth index (0=8bit, 1=10bit, 2=12bit)
//
// Returns the index of the last non-zero coefficient (eob), or -1 if all zero.
func Quantize(coeffs []int32, qindex int, hbd int) int {
	return QuantizeTx(coeffs, qindex, hbd, transform.TX8x8)
}

// QuantizeTx applies the transform-size dequant shift used by AV1 for
// TX32-and-larger coefficient domains.
func QuantizeTx(coeffs []int32, qindex int, hbd int, tx uint8) int {
	dcDequant := int(transform.DqTbl[hbd][qindex][0])
	acDequant := int(transform.DqTbl[hbd][qindex][1])
	dqShift := max(0, int(transform.TxfmDimensions[tx].Ctx)-2)

	eob := -1
	for i := range coeffs {
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
		qcoeff := (level*(1<<dqShift) + dq/2) / dq
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
	DequantizeTx(coeffs, qindex, hbd, transform.TX8x8)
}

// DequantizeTx mirrors the transform-size shift in the decoder.
func DequantizeTx(coeffs []int32, qindex int, hbd int, tx uint8) {
	dcDequant := int(transform.DqTbl[hbd][qindex][0])
	acDequant := int(transform.DqTbl[hbd][qindex][1])
	dqShift := max(0, int(transform.TxfmDimensions[tx].Ctx)-2)

	for i := range coeffs {
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
