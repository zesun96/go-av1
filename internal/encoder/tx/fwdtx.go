// Package tx implements forward transform and quantization for the AV1 encoder.
package tx

// FwdDCT8 computes the 8-point forward Type-II DCT.
//
// This is the exact inverse of InvDCT8 in internal/transform/itx1d.go,
// using the same fixed-point constants (cosine approximations) so that
// forward(inverse(x)) == x within rounding.
//
// Input/output convention: c[i*stride] for i = 0..7.
func FwdDCT8(c []int32, stride int) {
	// Stage 1: butterfly (even/odd decomposition)
	s0 := int(c[0]) + int(c[7*stride])
	s7 := int(c[0]) - int(c[7*stride])
	s1 := int(c[stride]) + int(c[6*stride])
	s6 := int(c[stride]) - int(c[6*stride])
	s2 := int(c[2*stride]) + int(c[5*stride])
	s5 := int(c[2*stride]) - int(c[5*stride])
	s3 := int(c[3*stride]) + int(c[4*stride])
	s4 := int(c[3*stride]) - int(c[4*stride])

	// Stage 2: even part → 4-point forward DCT
	e0 := s0 + s3
	e3 := s0 - s3
	e1 := s1 + s2
	e2 := s1 - s2

	// 4-pt DCT outputs (c[0], c[2], c[4], c[6])
	c[0] = int32((e0 + e1) * 181 >> 8)        // cos(0) * sqrt(2)/2
	c[4*stride] = int32((e0 - e1) * 181 >> 8) // cos(pi/2)
	c[2*stride] = int32((e2*1567 + e3*3784 + 2048) >> 12)
	c[6*stride] = int32((e3*1567 - e2*3784 + 2048) >> 12)

	// Odd part
	c[stride] = int32((s7*4017 + s4*799 + 2048) >> 12)
	c[3*stride] = int32((s6*1138 + s5*1703 + 1024) >> 11)
	c[5*stride] = int32((s6*1703 - s5*1138 + 1024) >> 11)
	c[7*stride] = int32((s7*799 - s4*4017 + 2048) >> 12)
}

// FwdDCT8x8 computes the 2D forward DCT on an 8x8 block.
//
// The input block is stored row-major in src (8 values per row, stride srcStride).
// The output coefficients are stored in dst (8 values per row, stride 8).
//
// Process: column transforms first, then row transforms (mirroring the
// decoder's row-first-then-column inverse).
func FwdDCT8x8(dst []int32, src []int16, srcStride int) {
	// Temporary buffer for intermediate results.
	var tmp [64]int32

	// Column transforms (process each column)
	for col := 0; col < 8; col++ {
		// Load column from src into contiguous temp
		for row := 0; row < 8; row++ {
			tmp[row*8+col] = int32(src[row*srcStride+col])
		}
	}

	// Apply 1D DCT to each column (stride = 8, since tmp is row-major)
	for col := 0; col < 8; col++ {
		fwdDCT8Col(tmp[:], col)
	}

	// Intermediate rounding shift (matches AV1 spec intermediate transform shift)
	for i := range tmp {
		tmp[i] = (tmp[i] + 1) >> 1
	}

	// Row transforms
	for row := 0; row < 8; row++ {
		fwdDCT8Row(tmp[:], row, dst)
	}
}

// fwdDCT8Col performs forward DCT8 on column col of buf (row-major, 8-wide).
func fwdDCT8Col(buf []int32, col int) {
	s0 := int(buf[0*8+col]) + int(buf[7*8+col])
	s7 := int(buf[0*8+col]) - int(buf[7*8+col])
	s1 := int(buf[1*8+col]) + int(buf[6*8+col])
	s6 := int(buf[1*8+col]) - int(buf[6*8+col])
	s2 := int(buf[2*8+col]) + int(buf[5*8+col])
	s5 := int(buf[2*8+col]) - int(buf[5*8+col])
	s3 := int(buf[3*8+col]) + int(buf[4*8+col])
	s4 := int(buf[3*8+col]) - int(buf[4*8+col])

	// Even part: 4-point DCT
	e0 := s0 + s3
	e3 := s0 - s3
	e1 := s1 + s2
	e2 := s1 - s2

	buf[0*8+col] = int32((e0+e1)*181+128) >> 8
	buf[4*8+col] = int32((e0-e1)*181+128) >> 8
	buf[2*8+col] = int32((e2*1567 + e3*3784 + 2048) >> 12)
	buf[6*8+col] = int32((e3*1567 - e2*3784 + 2048) >> 12)

	// Odd part
	buf[1*8+col] = int32((s7*4017 + s4*799 + 2048) >> 12)
	buf[5*8+col] = int32((s6*1703 - s5*1138 + 1024) >> 11)
	buf[3*8+col] = int32((s6*1138 + s5*1703 + 1024) >> 11)
	buf[7*8+col] = int32((s7*799 - s4*4017 + 2048) >> 12)
}

// fwdDCT8Row performs forward DCT8 on row of buf (row-major, 8-wide) and
// writes output to dst.
func fwdDCT8Row(buf []int32, row int, dst []int32) {
	off := row * 8
	s0 := int(buf[off+0]) + int(buf[off+7])
	s7 := int(buf[off+0]) - int(buf[off+7])
	s1 := int(buf[off+1]) + int(buf[off+6])
	s6 := int(buf[off+1]) - int(buf[off+6])
	s2 := int(buf[off+2]) + int(buf[off+5])
	s5 := int(buf[off+2]) - int(buf[off+5])
	s3 := int(buf[off+3]) + int(buf[off+4])
	s4 := int(buf[off+3]) - int(buf[off+4])

	// Even part
	e0 := s0 + s3
	e3 := s0 - s3
	e1 := s1 + s2
	e2 := s1 - s2

	dstOff := row * 8
	dst[dstOff+0] = int32((e0+e1)*181+128) >> 8
	dst[dstOff+4] = int32((e0-e1)*181+128) >> 8
	dst[dstOff+2] = int32((e2*1567 + e3*3784 + 2048) >> 12)
	dst[dstOff+6] = int32((e3*1567 - e2*3784 + 2048) >> 12)

	// Odd part
	dst[dstOff+1] = int32((s7*4017 + s4*799 + 2048) >> 12)
	dst[dstOff+5] = int32((s6*1703 - s5*1138 + 1024) >> 11)
	dst[dstOff+3] = int32((s6*1138 + s5*1703 + 1024) >> 11)
	dst[dstOff+7] = int32((s7*799 - s4*4017 + 2048) >> 12)
}
