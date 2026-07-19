package tile

import (
	"github.com/zesun96/go-av1/internal/transform"
)

// txfmShifts mirrors the shift argument in dav1d's inv_txfm_fn table.
var txfmShifts = [transform.NRectTxSizes]uint8{
	transform.TX4x4: 0, transform.TX8x8: 1, transform.TX16x16: 2,
	transform.TX32x32: 2, transform.TX64x64: 2,
	transform.RTX4x8: 0, transform.RTX8x4: 0,
	transform.RTX8x16: 1, transform.RTX16x8: 1,
	transform.RTX16x32: 1, transform.RTX32x16: 1,
	transform.RTX32x64: 1, transform.RTX64x32: 1,
	transform.RTX4x16: 1, transform.RTX16x4: 1,
	transform.RTX8x32: 2, transform.RTX32x8: 2,
	transform.RTX16x64: 2, transform.RTX64x16: 2,
}

// shiftFromTx returns the intermediate inverse-transform shift for tx.
func shiftFromTx(tx uint8) int {
	return int(txfmShifts[tx])
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
func applyResidualAdd(dst []uint8, stride int, coeff []int32,
	eob int, tx uint8, txtp uint8, bitDepth int) {
	shift := shiftFromTx(tx)
	exactLastNonzeroCol := lastNonzeroColForRecon(tx, txtp, eob)

	// Apply inverse transform and add residual to dst
	transform.InvTxfmAddWithLastNonzeroCol(dst, stride, coeff, eob, tx, shift, txtp, exactLastNonzeroCol, bitDepth)
}

func lastNonzeroColForRecon(tx uint8, txtp uint8, eob int) int {
	// WHT has its own inverse-transform path and is intentionally outside the
	// regular Tx1dTypes table.
	if txtp == transform.WHT_WHT {
		return -1
	}
	txtps := transform.Tx1dTypes[txtp]
	switch {
	case txtps[0] == transform.Tx1dIDENTITY && txtps[1] != transform.Tx1dIDENTITY:
		td := transform.TxfmDimensions[tx]
		sh := int(td.H) * 4
		if sh > 32 {
			sh = 32
		}
		if eob < 0 {
			return 0
		}
		if eob >= sh {
			return sh - 1
		}
		return eob
	case txtps[1] == transform.Tx1dIDENTITY && txtps[0] != transform.Tx1dIDENTITY:
		td := transform.TxfmDimensions[tx]
		if eob < 0 {
			return 0
		}
		return eob >> (td.Lw + 2)
	default:
		if col, ok := LastNonzeroColFromEOB(tx, eob); ok {
			return col
		}
		return -1
	}
}

func ReconBlock(dst []uint8, stride int, coeff []int32,
	eob int, tx uint8, txtp uint8,
	dq [2]uint16, bitDepth int) {

	// Dequantize coefficients in-place
	transform.Dequant(coeff, int(transform.TxfmDimensions[tx].W)*4, dq, int(tx), nil, eob, bitDepth)
	applyResidualAdd(dst, stride, coeff, eob, tx, txtp, bitDepth)
}

// ReconBlockDequantized mirrors dav1d's live decode path where residual
// consumption already applies dq/qm before the inverse transform stage.
func ReconBlockDequantized(dst []uint8, stride int, coeff []int32,
	eob int, tx uint8, txtp uint8, bitDepth int) {
	applyResidualAdd(dst, stride, coeff, eob, tx, txtp, bitDepth)
}

// ReconBlockDequantizedVisible applies residual add for a transform block whose
// visible area may be clipped by the right/bottom frame edge.
//
// dav1d can write full transform footprints because its frame buffers carry
// sufficient padding. The Go decoder keeps tightly-sized plane slices, so
// border tx blocks must reconstruct through a temporary full-size block and
// copy the visible rectangle back.
func ReconBlockDequantizedVisible(dst []uint8, stride int, coeff []int32,
	eob int, tx uint8, txtp uint8, bitDepth int, visW, visH int) {
	td := transform.TxfmDimensions[tx]
	fullW := int(td.W) * 4
	fullH := int(td.H) * 4
	if visW > fullW {
		visW = fullW
	}
	if visH > fullH {
		visH = fullH
	}
	if visW <= 0 || visH <= 0 || stride <= 0 || len(dst) < visW {
		return
	}
	// A slice starting near the bottom edge can contain fewer complete rows
	// than the coding-block dimensions suggest. Limit writes to rows for which
	// all visW samples are addressable.
	if maxRows := 1 + (len(dst)-visW)/stride; visH > maxRows {
		visH = maxRows
	}
	if visW >= fullW && visH >= fullH {
		applyResidualAdd(dst, stride, coeff, eob, tx, txtp, bitDepth)
		return
	}

	tmp := make([]uint8, fullW*fullH)
	for y := 0; y < visH; y++ {
		copy(tmp[y*fullW:y*fullW+visW], dst[y*stride:y*stride+visW])
	}
	applyResidualAdd(tmp, fullW, coeff, eob, tx, txtp, bitDepth)
	for y := 0; y < visH; y++ {
		copy(dst[y*stride:y*stride+visW], tmp[y*fullW:y*fullW+visW])
	}
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
