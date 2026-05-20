// Package core implements the main encoding loop for the minimum viable AV1 encoder.
//
// Pipeline per frame:
//  1. DC prediction (predict each 8x8 block from left/above DC average)
//  2. Compute residual = source - prediction
//  3. Forward DCT 8x8
//  4. Quantize
//  5. Entropy encode (MSAC)
//  6. Produce OBU bitstream
package core

import (
	"github.com/zesun96/go-av1/internal/encoder/bitwriter"
	"github.com/zesun96/go-av1/internal/encoder/entropy"
	"github.com/zesun96/go-av1/internal/encoder/obuwriter"
	"github.com/zesun96/go-av1/internal/encoder/tx"
)

// FrameEncoder encodes a single frame.
type FrameEncoder struct {
	Width    int
	Height   int
	QIndex   int
	BitDepth int // 0=8bit index for DqTbl
}

// EncodeFrame encodes one Y4M frame (Y plane only for M10, chroma uses same
// path with chroma dimensions) and returns the complete AV1 temporal unit bytes.
//
// yPlane is the luma plane (width x height bytes for 8-bit).
// cbPlane, crPlane are the chroma planes (width/2 x height/2 for 4:2:0).
func (fe *FrameEncoder) EncodeFrame(yPlane, cbPlane, crPlane []byte, frameNum int) []byte {
	// Encode each plane's tile data using MSAC
	ec := bitwriter.NewMSACEncoder(fe.Width * fe.Height / 4)

	// Encode luma
	fe.encodePlane(ec, yPlane, fe.Width, fe.Height)

	// Encode chroma (4:2:0: half dimensions)
	chromaW := fe.Width / 2
	chromaH := fe.Height / 2
	if cbPlane != nil && len(cbPlane) > 0 {
		fe.encodePlane(ec, cbPlane, chromaW, chromaH)
	}
	if crPlane != nil && len(crPlane) > 0 {
		fe.encodePlane(ec, crPlane, chromaW, chromaH)
	}

	tileData := ec.Flush()

	// Build the complete temporal unit
	seqParams := &obuwriter.SeqParams{
		Width:    fe.Width,
		Height:   fe.Height,
		BitDepth: 8,
		ChromaSS: 1, // 4:2:0
	}

	isKeyFrame := true // M10: all frames are key frames
	obuData := obuwriter.BuildTemporalUnit(seqParams, fe.QIndex, tileData, isKeyFrame)
	return obuData
}

// encodePlane encodes a single plane by splitting into 8x8 blocks.
func (fe *FrameEncoder) encodePlane(ec *bitwriter.MSACEncoder, plane []byte, width, height int) {
	// Process superblocks (64x64) split into 8x8 blocks via PARTITION_SPLIT chain.
	// For M10: fixed partition all the way to 8x8.
	blocksW := (width + 7) / 8
	blocksH := (height + 7) / 8

	// Reconstructed frame buffer for DC prediction reference
	recon := make([]byte, width*height)
	copy(recon, plane) // Initialize with source (will be overwritten)

	for by := 0; by < blocksH; by++ {
		for bx := 0; bx < blocksW; bx++ {
			fe.encodeBlock8x8(ec, plane, recon, width, height, bx, by)
		}
	}
}

// encodeBlock8x8 encodes a single 8x8 block using DC prediction + DCT + quantize.
func (fe *FrameEncoder) encodeBlock8x8(
	ec *bitwriter.MSACEncoder,
	src []byte,
	recon []byte,
	planeW, planeH int,
	bx, by int,
) {
	x0 := bx * 8
	y0 := by * 8

	// Compute DC prediction value from above row and left column of reconstructed frame
	dcPred := fe.computeDCPred(recon, planeW, planeH, x0, y0)

	// Compute residual
	var residual [64]int16
	for row := 0; row < 8; row++ {
		for col := 0; col < 8; col++ {
			px := x0 + col
			py := y0 + row
			var srcVal byte
			if px < planeW && py < planeH {
				srcVal = src[py*planeW+px]
			}
			residual[row*8+col] = int16(srcVal) - int16(dcPred)
		}
	}

	// Forward DCT 8x8
	var coeffs [64]int32
	tx.FwdDCT8x8(coeffs[:], residual[:], 8)

	// Quantize
	eob := tx.Quantize(coeffs[:], fe.QIndex, fe.BitDepth)

	// Entropy encode coefficients
	entropy.EncodeCoefficients(ec, coeffs[:], eob)

	// Reconstruct for prediction reference (dequant + inverse DCT + add pred)
	fe.reconstructBlock(recon, planeW, planeH, x0, y0, coeffs[:], eob, dcPred)
}

// computeDCPred computes DC_PRED value for an 8x8 block.
// Uses average of above row (8 pixels) and left column (8 pixels).
func (fe *FrameEncoder) computeDCPred(recon []byte, w, h, x0, y0 int) byte {
	sum := 0
	count := 0

	// Above row
	if y0 > 0 {
		for col := 0; col < 8; col++ {
			px := x0 + col
			if px < w {
				sum += int(recon[(y0-1)*w+px])
				count++
			}
		}
	}

	// Left column
	if x0 > 0 {
		for row := 0; row < 8; row++ {
			py := y0 + row
			if py < h {
				sum += int(recon[py*w+(x0-1)])
				count++
			}
		}
	}

	if count == 0 {
		return 128 // No neighbors: use mid-gray
	}
	return byte((sum + count/2) / count)
}

// reconstructBlock applies inverse quantization and inverse transform to update
// the reconstructed frame buffer.
func (fe *FrameEncoder) reconstructBlock(
	recon []byte, w, h, x0, y0 int,
	qcoeffs []int32, eob int, dcPred byte,
) {
	if eob < 0 {
		// All zero block: just fill with DC prediction
		for row := 0; row < 8; row++ {
			for col := 0; col < 8; col++ {
				px := x0 + col
				py := y0 + row
				if px < w && py < h {
					recon[py*w+px] = dcPred
				}
			}
		}
		return
	}

	// Make a copy for dequantization
	var dqCoeffs [64]int32
	copy(dqCoeffs[:], qcoeffs[:])
	tx.Dequantize(dqCoeffs[:], fe.QIndex, fe.BitDepth)

	// Inverse DCT 8x8 (simplified: reuse column/row structure)
	var reconBlock [64]int32
	invDCT8x8(dqCoeffs[:], reconBlock[:])

	// Add prediction and clip to [0, 255]
	for row := 0; row < 8; row++ {
		for col := 0; col < 8; col++ {
			px := x0 + col
			py := y0 + row
			if px < w && py < h {
				val := int(reconBlock[row*8+col]) + int(dcPred)
				if val < 0 {
					val = 0
				}
				if val > 255 {
					val = 255
				}
				recon[py*w+px] = byte(val)
			}
		}
	}
}

// invDCT8x8 performs a simplified inverse 8x8 DCT for reconstruction.
// Uses the same butterfly structure as the forward transform but in reverse.
func invDCT8x8(coeffs []int32, out []int32) {
	// Row transforms first
	var tmp [64]int32
	for row := 0; row < 8; row++ {
		invDCT8Row(coeffs[row*8:], tmp[row*8:])
	}

	// Intermediate round
	for i := range tmp {
		tmp[i] = (tmp[i] + 1) >> 1
	}

	// Column transforms
	for col := 0; col < 8; col++ {
		invDCT8Col(tmp[:], col, out)
	}
}

func invDCT8Row(in []int32, out []int32) {
	// Even part (4-point inverse DCT)
	t0 := (int(in[0]) + int(in[4])) * 181 >> 8
	t1 := (int(in[0]) - int(in[4])) * 181 >> 8
	t2 := (int(in[2])*1567 - int(in[6])*3784 + 2048) >> 12
	t3 := (int(in[2])*3784 + int(in[6])*1567 + 2048) >> 12

	e0 := t0 + t3
	e1 := t1 + t2
	e2 := t1 - t2
	e3 := t0 - t3

	// Odd part
	o0 := (int(in[1])*4017 + int(in[7])*799 + 2048) >> 12
	o1 := (int(in[3])*1703 + int(in[5])*1138 + 1024) >> 11
	o2 := (int(in[3])*1138 - int(in[5])*1703 + 1024) >> 11
	o3 := (int(in[1])*799 - int(in[7])*4017 + 2048) >> 12

	out[0] = int32(e0 + o0)
	out[1] = int32(e1 + o1)
	out[2] = int32(e2 + o2)
	out[3] = int32(e3 + o3)
	out[4] = int32(e3 - o3)
	out[5] = int32(e2 - o2)
	out[6] = int32(e1 - o1)
	out[7] = int32(e0 - o0)
}

func invDCT8Col(buf []int32, col int, out []int32) {
	t0 := (int(buf[0*8+col]) + int(buf[4*8+col])) * 181 >> 8
	t1 := (int(buf[0*8+col]) - int(buf[4*8+col])) * 181 >> 8
	t2 := (int(buf[2*8+col])*1567 - int(buf[6*8+col])*3784 + 2048) >> 12
	t3 := (int(buf[2*8+col])*3784 + int(buf[6*8+col])*1567 + 2048) >> 12

	e0 := t0 + t3
	e1 := t1 + t2
	e2 := t1 - t2
	e3 := t0 - t3

	o0 := (int(buf[1*8+col])*4017 + int(buf[7*8+col])*799 + 2048) >> 12
	o1 := (int(buf[3*8+col])*1703 + int(buf[5*8+col])*1138 + 1024) >> 11
	o2 := (int(buf[3*8+col])*1138 - int(buf[5*8+col])*1703 + 1024) >> 11
	o3 := (int(buf[1*8+col])*799 - int(buf[7*8+col])*4017 + 2048) >> 12

	out[0*8+col] = int32(e0 + o0)
	out[1*8+col] = int32(e1 + o1)
	out[2*8+col] = int32(e2 + o2)
	out[3*8+col] = int32(e3 + o3)
	out[4*8+col] = int32(e3 - o3)
	out[5*8+col] = int32(e2 - o2)
	out[6*8+col] = int32(e1 - o1)
	out[7*8+col] = int32(e0 - o0)
}
