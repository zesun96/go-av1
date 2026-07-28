// Package tx implements AV1 forward transforms and quantization.
package tx

import "math"

// These kernels mirror SVT-AV1's svt_av1_fdct{4,8}_new and the 2-D
// fwd_txfm shifts. The initial x4 shift is essential: AV1's quantizers
// expect small-transform coefficients in Q3 (eight times pixel scale).

const (
	cos8  = 8035
	cos16 = 7568
	cos24 = 6811
	cos32 = 5793
	cos40 = 4551
	cos48 = 3135
	cos56 = 1598
)

func halfBTF(a int32, x int32, b int32, y int32) int32 {
	return int32((int64(a)*int64(x) + int64(b)*int64(y) + 4096) >> 13)
}

func fdct4(input [4]int32) (output [4]int32) {
	s0 := input[0] + input[3]
	s1 := input[1] + input[2]
	s2 := input[1] - input[2]
	s3 := input[0] - input[3]
	output[0] = halfBTF(cos32, s0, cos32, s1)
	output[1] = halfBTF(cos48, s2, cos16, s3)
	output[2] = halfBTF(-cos32, s1, cos32, s0)
	output[3] = halfBTF(cos48, s3, -cos16, s2)
	return output
}

func fdct8(input [8]int32) (output [8]int32) {
	// Stage 1.
	var s [8]int32
	s[0] = input[0] + input[7]
	s[1] = input[1] + input[6]
	s[2] = input[2] + input[5]
	s[3] = input[3] + input[4]
	s[4] = input[3] - input[4]
	s[5] = input[2] - input[5]
	s[6] = input[1] - input[6]
	s[7] = input[0] - input[7]

	// Stage 2.
	var t [8]int32
	t[0] = s[0] + s[3]
	t[1] = s[1] + s[2]
	t[2] = s[1] - s[2]
	t[3] = s[0] - s[3]
	t[4] = s[4]
	t[5] = halfBTF(-cos32, s[5], cos32, s[6])
	t[6] = halfBTF(cos32, s[6], cos32, s[5])
	t[7] = s[7]

	// Stage 3.
	s[0] = halfBTF(cos32, t[0], cos32, t[1])
	s[1] = halfBTF(-cos32, t[1], cos32, t[0])
	s[2] = halfBTF(cos48, t[2], cos16, t[3])
	s[3] = halfBTF(cos48, t[3], -cos16, t[2])
	s[4] = t[4] + t[5]
	s[5] = t[4] - t[5]
	s[6] = t[7] - t[6]
	s[7] = t[7] + t[6]

	// Stage 4 and output permutation.
	t[4] = halfBTF(cos56, s[4], cos8, s[7])
	t[5] = halfBTF(cos24, s[5], cos40, s[6])
	t[6] = halfBTF(cos24, s[6], -cos40, s[5])
	t[7] = halfBTF(cos56, s[7], -cos8, s[4])
	output[0] = s[0]
	output[1] = t[4]
	output[2] = s[2]
	output[3] = t[6]
	output[4] = s[1]
	output[5] = t[5]
	output[6] = s[3]
	output[7] = t[7]
	return output
}

// FwdDCT8 applies the AV1 8-point forward DCT in place.
func FwdDCT8(c []int32, stride int) {
	var input [8]int32
	for i := range input {
		input[i] = c[i*stride]
	}
	output := fdct8(input)
	for i := range output {
		c[i*stride] = output[i]
	}
}

// FwdDCT8x8 computes SVT-AV1's TX_8X8 DCT_DCT forward transform.
func FwdDCT8x8(dst []int32, src []int16, srcStride int) {
	var tmp [64]int32
	for x := 0; x < 8; x++ {
		var input [8]int32
		for y := 0; y < 8; y++ {
			input[y] = int32(src[y*srcStride+x]) << 2
		}
		output := fdct8(input)
		for y := 0; y < 8; y++ {
			// TX8 shift[1] is -1: round right by one after columns.
			tmp[y*8+x] = (output[y] + 1) >> 1
		}
	}
	for y := 0; y < 8; y++ {
		var input [8]int32
		copy(input[:], tmp[y*8:y*8+8])
		output := fdct8(input)
		copy(dst[y*8:y*8+8], output[:])
	}
}

// FwdDCT4x4 computes SVT-AV1's TX_4X4 DCT_DCT forward transform.
func FwdDCT4x4(dst []int32, src []int16, srcStride int) {
	var tmp [16]int32
	for x := 0; x < 4; x++ {
		var input [4]int32
		for y := 0; y < 4; y++ {
			input[y] = int32(src[y*srcStride+x]) << 2
		}
		output := fdct4(input)
		for y := 0; y < 4; y++ {
			tmp[y*4+x] = output[y]
		}
	}
	for y := 0; y < 4; y++ {
		var input [4]int32
		copy(input[:], tmp[y*4:y*4+4])
		output := fdct4(input)
		copy(dst[y*4:y*4+4], output[:])
	}
}

// FwdDCT16x16 computes a TX_16X16 DCT_DCT in the coefficient scale consumed
// by AV1 quantization. The orthonormal DCT is scaled by eight, matching the
// existing integer 4x4 and 8x8 kernels.
func FwdDCT16x16(dst []int32, src []int16, srcStride int) {
	fwdDCTSquare(dst, src, srcStride, 16)
}

// FwdDCT32x32 computes a TX_32X32 DCT_DCT in the coefficient scale consumed
// by AV1 quantization.
func FwdDCT32x32(dst []int32, src []int16, srcStride int) {
	fwdDCTSquareScaled(dst, src, srcStride, 32, 4)
}

func fwdDCTSquare(dst []int32, src []int16, srcStride, n int) {
	fwdDCTSquareScaled(dst, src, srcStride, n, 8)
}

func fwdDCTSquareScaled(dst []int32, src []int16, srcStride, n int, scaleFactor float64) {
	basis := make([]float64, n*n)
	for k := 0; k < n; k++ {
		scale := math.Sqrt(2.0 / float64(n))
		if k == 0 {
			scale = math.Sqrt(1.0 / float64(n))
		}
		for x := 0; x < n; x++ {
			basis[k*n+x] = scale * math.Cos(
				math.Pi*float64((2*x+1)*k)/float64(2*n))
		}
	}
	tmp := make([]float64, n*n)
	for y := 0; y < n; y++ {
		for u := 0; u < n; u++ {
			var sum float64
			for x := 0; x < n; x++ {
				sum += float64(src[y*srcStride+x]) * basis[u*n+x]
			}
			tmp[y*n+u] = sum
		}
	}
	for v := 0; v < n; v++ {
		for u := 0; u < n; u++ {
			var sum float64
			for y := 0; y < n; y++ {
				sum += tmp[y*n+u] * basis[v*n+y]
			}
			dst[v*n+u] = int32(math.Round(sum * scaleFactor))
		}
	}
}
