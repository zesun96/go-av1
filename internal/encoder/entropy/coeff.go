// Package entropy implements AV1 coefficient entropy coding for the encoder.
//
// For the minimum viable encoder (M10), we use:
//   - disable_frame_end_update_cdf = 1 (fixed CDFs, no adaptation)
//   - TX_8X8 blocks only
//   - DCT_DCT transform type only
//   - Simplified residual_coding syntax
package entropy

import (
	"github.com/zesun96/go-av1/internal/encoder/bitwriter"
)

// Default CDF tables for coefficient coding (AV1 spec Table 9-25 and beyond).
// These are the initial CDFs used when disable_frame_end_update_cdf=1.

// txbSkipCDF is the initial CDF for txb_skip (all_zero flag).
// Context 0 (TX_8X8, DC_PRED, no above/left context for simplicity).
var txbSkipCDF = [2]uint16{24576, 0} // ~75% probability of non-zero

// eobPt512CDF is the initial CDF for end_of_block position (TX_8X8 has max 64 coeffs).
// We use eob_pt_64 (7 symbols: 0..6 mapping to bit prefix groups).
var eobPt64CDF = [7]uint16{
	28672, 25600, 22528, 19456, 14336, 8192, 0,
}

// eobExtraCDF for extra bits after EOB position.
var eobExtraCDF = [2]uint16{16384, 0}

// coeffBaseEobCDF is the CDF for the base level of the EOB coefficient.
// 3 symbols: 0, 1, 2+ (but since it's EOB it's always >= 1, so really 1 or 2+)
var coeffBaseEobCDF = [3]uint16{24576, 16384, 0}

// coeffBaseCDF is for non-EOB coefficient base level (4 symbols: 0,1,2,3+).
var coeffBaseCDF = [4]uint16{24576, 16384, 8192, 0}

// coeffBrCDF is for coefficient remainder (bypass levels above base).
// 4 symbols representing ranges.
var coeffBrCDF = [4]uint16{20480, 12288, 6144, 0}

// dcSignCDF is the CDF for DC coefficient sign.
var dcSignCDF = [2]uint16{16384, 0}

// ScanOrder8x8 is the default scan order for 8x8 DCT_DCT (diagonal scan).
var ScanOrder8x8 = [64]int{
	0, 1, 8, 16, 9, 2, 3, 10,
	17, 24, 32, 25, 18, 11, 4, 5,
	12, 19, 26, 33, 40, 48, 41, 34,
	27, 20, 13, 6, 7, 14, 21, 28,
	35, 42, 49, 56, 57, 50, 43, 36,
	29, 22, 15, 23, 30, 37, 44, 51,
	58, 59, 52, 45, 38, 31, 39, 46,
	53, 60, 61, 54, 47, 55, 62, 63,
}

// EncodeCoefficients encodes one 8x8 block's quantized coefficients using
// the AV1 residual_coding syntax with MSAC arithmetic coding.
//
// Parameters:
//   - ec: MSAC encoder to write to
//   - qcoeffs: 64 quantized coefficients in raster scan order
//   - eob: last non-zero coefficient index in raster order (from Quantize), or -1 if all zero
//
// The encoder writes:
//  1. txb_skip (all_zero flag)
//  2. If not all zero: eob position, coefficient levels, signs
func EncodeCoefficients(ec *bitwriter.MSACEncoder, qcoeffs []int32, eob int) {
	// 1. txb_skip: 1 means all-zero block
	cdf := make([]uint16, 2)
	copy(cdf, txbSkipCDF[:])
	if eob < 0 {
		ec.BoolAdapt(1, cdf) // all zero
		return
	}
	ec.BoolAdapt(0, cdf) // has non-zero coefficients

	// Find EOB in scan order (scan position of last non-zero coeff)
	eobScan := 0
	for si := 63; si >= 0; si-- {
		if qcoeffs[ScanOrder8x8[si]] != 0 {
			eobScan = si
			break
		}
	}

	// 2. Encode EOB position using the prefix/suffix scheme.
	// eobPlus1 = scan_eob + 1 (range 1..64)
	eobPlus1 := eobScan + 1
	encodeEOB(ec, eobPlus1)

	// 3. Encode coefficient levels in reverse scan order (from eobScan down to 0).
	for ci := eobScan; ci >= 0; ci-- {
		scanIdx := ScanOrder8x8[ci]
		level := int(qcoeffs[scanIdx])
		if level < 0 {
			level = -level
		}

		// Encode base level
		if ci == eobScan {
			// EOB position: coefficient must be non-zero (min level = 1)
			encodeCoeffBaseEOB(ec, level)
		} else {
			encodeCoeffBase(ec, level)
		}

		// Encode remainder (levels above the base threshold)
		var threshold int
		if ci == eobScan {
			threshold = 3 // after sym 2 (= level 3+), remainder = level - 3
			if level <= 2 {
				threshold = level
			}
		} else {
			threshold = 3 // after sym 3 (= level 3+), remainder = level - 3
			if level < 3 {
				threshold = level
			}
		}
		remainder := level - threshold
		if remainder > 0 {
			encodeCoeffBr(ec, remainder)
		}
	}

	// 4. Encode signs
	for ci := eobScan; ci >= 0; ci-- {
		scanIdx := ScanOrder8x8[ci]
		if qcoeffs[scanIdx] == 0 {
			continue
		}
		if scanIdx == 0 {
			// DC sign uses special context
			signCdf := make([]uint16, 2)
			copy(signCdf, dcSignCDF[:])
			if qcoeffs[scanIdx] < 0 {
				ec.BoolAdapt(1, signCdf)
			} else {
				ec.BoolAdapt(0, signCdf)
			}
		} else {
			// AC signs are bypass-coded (equi-probable)
			if qcoeffs[scanIdx] < 0 {
				ec.BoolEqui(1)
			} else {
				ec.BoolEqui(0)
			}
		}
	}

	// 5. Encode remaining coefficient values (golomb-rice for large values)
	for ci := eobScan; ci >= 0; ci-- {
		scanIdx := ScanOrder8x8[ci]
		level := int(qcoeffs[scanIdx])
		if level < 0 {
			level = -level
		}
		threshold := 3
		if ci == eobScan && level > 2 {
			threshold = 3
		}
		// Golomb escape for levels that exceed the BR coding range (threshold + 4*3 = threshold + 12)
		if level > threshold+14 {
			encodeGolomb(ec, level-threshold-15)
		}
	}
}

// encodeEOB encodes the end-of-block position (1..64) for TX_8X8.
func encodeEOB(ec *bitwriter.MSACEncoder, eobPlus1 int) {
	// Map eobPlus1 to prefix group:
	// 1     -> pt=0
	// 2     -> pt=1
	// 3-4   -> pt=2, extra=1bit
	// 5-8   -> pt=3, extra=2bits
	// 9-16  -> pt=4, extra=3bits
	// 17-32 -> pt=5, extra=4bits
	// 33-64 -> pt=6, extra=5bits
	var pt, extra, nExtra int
	switch {
	case eobPlus1 == 1:
		pt = 0
	case eobPlus1 == 2:
		pt = 1
	case eobPlus1 <= 4:
		pt = 2
		extra = eobPlus1 - 3
		nExtra = 1
	case eobPlus1 <= 8:
		pt = 3
		extra = eobPlus1 - 5
		nExtra = 2
	case eobPlus1 <= 16:
		pt = 4
		extra = eobPlus1 - 9
		nExtra = 3
	case eobPlus1 <= 32:
		pt = 5
		extra = eobPlus1 - 17
		nExtra = 4
	default: // 33-64
		pt = 6
		extra = eobPlus1 - 33
		nExtra = 5
	}

	// Encode prefix using CDF (7 symbols for eob_pt_64)
	cdf := make([]uint16, len(eobPt64CDF))
	copy(cdf, eobPt64CDF[:])
	ec.Symbol(uint32(pt), cdf, len(cdf))

	// Encode extra bits if any
	if nExtra > 0 {
		extraCdf := make([]uint16, 2)
		copy(extraCdf, eobExtraCDF[:])
		for i := nExtra - 1; i >= 0; i-- {
			bit := uint32((extra >> uint(i)) & 1)
			ec.BoolAdapt(bit, extraCdf)
		}
	}
}

// encodeCoeffBaseEOB encodes the base level for the EOB coefficient (level >= 1).
// 3 symbols: 0=level_1, 1=level_2, 2=level_3+
func encodeCoeffBaseEOB(ec *bitwriter.MSACEncoder, level int) {
	cdf := make([]uint16, len(coeffBaseEobCDF))
	copy(cdf, coeffBaseEobCDF[:])
	sym := level - 1 // level 1->0, level 2->1, level 3+->2
	if sym > 2 {
		sym = 2
	}
	ec.Symbol(uint32(sym), cdf, len(cdf))
}

// encodeCoeffBase encodes the base level for non-EOB coefficients.
// 4 symbols: 0=zero, 1=level_1, 2=level_2, 3=level_3+
func encodeCoeffBase(ec *bitwriter.MSACEncoder, level int) {
	cdf := make([]uint16, len(coeffBaseCDF))
	copy(cdf, coeffBaseCDF[:])
	sym := level
	if sym > 3 {
		sym = 3
	}
	ec.Symbol(uint32(sym), cdf, len(cdf))
}

// encodeCoeffBr encodes the coefficient remainder (levels above base threshold).
// 4 symbols: 0, 1, 2, 3+ (escape to next iteration).
func encodeCoeffBr(ec *bitwriter.MSACEncoder, remainder int) {
	for remainder > 0 {
		cdf := make([]uint16, len(coeffBrCDF))
		copy(cdf, coeffBrCDF[:])
		sym := remainder
		if sym > 3 {
			sym = 3
		}
		ec.Symbol(uint32(sym), cdf, len(cdf))
		if sym < 3 {
			break
		}
		remainder -= 3
	}
}

// encodeGolomb encodes a value using exponential Golomb coding (bypass).
func encodeGolomb(ec *bitwriter.MSACEncoder, v int) {
	// Exponential Golomb order 0: write (length-1) zeros, then 1, then (length-1) bits
	length := 0
	tmp := v + 1
	for tmp > 0 {
		length++
		tmp >>= 1
	}
	for i := 0; i < length-1; i++ {
		ec.BoolEqui(0)
	}
	for i := length - 1; i >= 0; i-- {
		ec.BoolEqui(uint32((v + 1) >> uint(i) & 1))
	}
}
