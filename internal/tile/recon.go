package tile

import (
	"github.com/zesun96/go-av1/internal/transform"
)

// shiftFromTx returns the intermediate shift value for a given transform
// size, matching dav1d's convention:
//
//	shift = max(lw, lh) clamped to [0, 2]
func shiftFromTx(tx uint8) int {
	max := int(transform.TxfmDimensions[tx].Max)
	if max > 2 {
		return 2
	}
	return max
}

// ReconBlock adds the dequantized + inverse-transformed residual to
// dst, which must already contain the intra prediction pixels.
//
// Parameters:
//   - dst:      pixel buffer (row-major, already holds prediction)
//   - stride:   row stride of dst in pixels
//   - coeff:    coefficient block in column-major layout;
//     zeroed on return
//   - eob:      end-of-block index (last non-zero coefficient position)
//   - tx:       transform size (RectTxfmSize constant)
//   - txtp:     2D transform type (DCT_DCT, ADST_DCT, etc.)
//   - dq:       [DC, AC] dequant values
//   - bitDepth: pixel bit depth (8, 10, or 12)
func ReconBlock(dst []uint8, stride int, coeff []int32,
	eob int, tx uint8, txtp uint8,
	dq [2]uint16, bitDepth int) {

	shift := shiftFromTx(tx)

	// Dequantize coefficients in-place
	transform.Dequant(coeff, int(transform.TxfmDimensions[tx].W)*4, dq, int(tx), nil, eob)

	// Apply inverse transform and add residual to dst
	transform.InvTxfmAdd(dst, stride, coeff, eob, tx, shift, txtp, bitDepth)
}

// ReconBIntra reconstructs an intra-coded block by iterating over its
// transform blocks. For each transform block:
//
//  1. Calls predFn to fill a temporary prediction buffer
//  2. Copies prediction to the correct position in dst
//  3. If coeffFn returns skip=false, dequantizes coefficients and
//     applies inverse transform, adding residual to dst
//
// Parameters:
//   - dst:       destination pixel buffer for the full block
//   - stride:    row stride of dst in pixels
//   - bw4, bh4:  block width/height in 4px units
//   - tx:        transform size (RectTxfmSize constant)
//   - predBuf:   scratch buffer for prediction (must be at least 32×32)
//   - predFn:    fills predBuf with prediction for current transform block
//   - coeffFn:   provides coefficients for current transform block
//   - bitDepth:  pixel bit depth
func ReconBIntra(dst []uint8, stride int,
	bw4, bh4 int,
	tx uint8,
	predBuf []uint8,
	predFn func(pred []uint8, tbx, tby, tw, th int),
	coeffFn func(tbx, tby int) (coeff []int32, eob int, txtp uint8, dq [2]uint16, skip bool),
	bitDepth int) {

	td := transform.TxfmDimensions[tx]
	tw := int(td.W) * 4 // transform block width in pixels
	th := int(td.H) * 4 // transform block height in pixels

	// Iterate over transform blocks within the block
	for by := 0; by < bh4*4; by += th {
		for bx := 0; bx < bw4*4; bx += tw {
			// Get prediction for this transform block
			predFn(predBuf, bx, by, tw, th)

			// Copy prediction to dst
			for y := 0; y < th; y++ {
				dstRow := dst[(by+y)*stride+bx:]
				copy(dstRow[:tw], predBuf[y*tw:(y+1)*tw])
			}

			// Get coefficients
			coeff, eob, txtp, dq, skip := coeffFn(bx, by)
			if !skip && coeff != nil {
				ReconBlock(dst[by*stride+bx:], stride, coeff, eob, tx, txtp, dq, bitDepth)
			}
		}
	}
}
