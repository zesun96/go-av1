package transform

// Inverse 1D transform kernels for AV1, faithfully ported from
// dav1d/src/itx_1d.c (lines 65-1081). Supports the small/medium sizes
// (4/8/16) used by the M3 intra-only key frame path plus the WHT4
// kernel used by lossless coding.
//
// All kernels operate in-place on int32 coefficients with the
// dav1d convention:
//
//   c[i * stride] = i-th sample, i = 0..(size-1)
//
// `min`/`max` clip intermediate results to the spec-allowed range
// (typically [-(1<<(7+bitdepth)), (1<<(7+bitdepth))-1]). The kernels
// never read or write outside the requested `size * stride` window.

// clip clamps v into [min, max]. Hot path inlined manually by the
// surrounding kernels via the local helper below.
func clip(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// --- DCT family -----------------------------------------------------------

// invDCT4Internal implements dav1d inv_dct4_1d_internal_c. tx64 selects
// the reduced-bandwidth path used when this kernel feeds the 64-point
// DCT (only inputs 0 and 1 are non-zero).
func invDCT4Internal(c []int32, stride, min, max int, tx64 bool) {
	in0 := int(c[0])
	in1 := int(c[stride])

	var t0, t1, t2, t3 int
	if tx64 {
		t0 = (in0*181 + 128) >> 8
		t1 = t0
		t2 = (in1*1567 + 2048) >> 12
		t3 = (in1*3784 + 2048) >> 12
	} else {
		in2 := int(c[2*stride])
		in3 := int(c[3*stride])
		t0 = ((in0+in2)*181 + 128) >> 8
		t1 = ((in0-in2)*181 + 128) >> 8
		t2 = ((in1*1567 - in3*(3784-4096) + 2048) >> 12) - in3
		t3 = ((in1*(3784-4096) + in3*1567 + 2048) >> 12) + in1
	}

	c[0] = int32(clip(t0+t3, min, max))
	c[stride] = int32(clip(t1+t2, min, max))
	c[2*stride] = int32(clip(t1-t2, min, max))
	c[3*stride] = int32(clip(t0-t3, min, max))
}

// InvDCT4 is the public entry point for the 4-point inverse DCT.
func InvDCT4(c []int32, stride, min, max int) {
	invDCT4Internal(c, stride, min, max, false)
}

// invDCT8Internal implements dav1d inv_dct8_1d_internal_c.
func invDCT8Internal(c []int32, stride, min, max int, tx64 bool) {
	invDCT4Internal(c, stride<<1, min, max, tx64)

	in1 := int(c[stride])
	in3 := int(c[3*stride])

	var t4a, t5a, t6a, t7a int
	if tx64 {
		t4a = (in1*799 + 2048) >> 12
		t5a = (in3*-2276 + 2048) >> 12
		t6a = (in3*3406 + 2048) >> 12
		t7a = (in1*4017 + 2048) >> 12
	} else {
		in5 := int(c[5*stride])
		in7 := int(c[7*stride])
		t4a = ((in1*799 - in7*(4017-4096) + 2048) >> 12) - in7
		t5a = (in5*1703 - in3*1138 + 1024) >> 11
		t6a = (in5*1138 + in3*1703 + 1024) >> 11
		t7a = ((in1*(4017-4096) + in7*799 + 2048) >> 12) + in1
	}

	t4 := clip(t4a+t5a, min, max)
	t5a = clip(t4a-t5a, min, max)
	t7 := clip(t7a+t6a, min, max)
	t6a = clip(t7a-t6a, min, max)

	t5 := ((t6a-t5a)*181 + 128) >> 8
	t6 := ((t6a+t5a)*181 + 128) >> 8

	t0 := int(c[0])
	t1 := int(c[2*stride])
	t2 := int(c[4*stride])
	t3 := int(c[6*stride])

	c[0] = int32(clip(t0+t7, min, max))
	c[stride] = int32(clip(t1+t6, min, max))
	c[2*stride] = int32(clip(t2+t5, min, max))
	c[3*stride] = int32(clip(t3+t4, min, max))
	c[4*stride] = int32(clip(t3-t4, min, max))
	c[5*stride] = int32(clip(t2-t5, min, max))
	c[6*stride] = int32(clip(t1-t6, min, max))
	c[7*stride] = int32(clip(t0-t7, min, max))
}

// InvDCT8 is the public entry point for the 8-point inverse DCT.
func InvDCT8(c []int32, stride, min, max int) {
	invDCT8Internal(c, stride, min, max, false)
}

// invDCT16Internal implements dav1d inv_dct16_1d_internal_c.
func invDCT16Internal(c []int32, stride, min, max int, tx64 bool) {
	invDCT8Internal(c, stride<<1, min, max, tx64)

	in1 := int(c[stride])
	in3 := int(c[3*stride])
	in5 := int(c[5*stride])
	in7 := int(c[7*stride])

	var t8a, t9a, t10a, t11a, t12a, t13a, t14a, t15a int
	if tx64 {
		t8a = (in1*401 + 2048) >> 12
		t9a = (in7*-2598 + 2048) >> 12
		t10a = (in5*1931 + 2048) >> 12
		t11a = (in3*-1189 + 2048) >> 12
		t12a = (in3*3920 + 2048) >> 12
		t13a = (in5*3612 + 2048) >> 12
		t14a = (in7*3166 + 2048) >> 12
		t15a = (in1*4076 + 2048) >> 12
	} else {
		in9 := int(c[9*stride])
		in11 := int(c[11*stride])
		in13 := int(c[13*stride])
		in15 := int(c[15*stride])
		t8a = ((in1*401 - in15*(4076-4096) + 2048) >> 12) - in15
		t9a = (in9*1583 - in7*1299 + 1024) >> 11
		t10a = ((in5*1931 - in11*(3612-4096) + 2048) >> 12) - in11
		t11a = ((in13*(3920-4096) - in3*1189 + 2048) >> 12) + in13
		t12a = ((in13*1189 + in3*(3920-4096) + 2048) >> 12) + in3
		t13a = ((in5*(3612-4096) + in11*1931 + 2048) >> 12) + in5
		t14a = (in9*1299 + in7*1583 + 1024) >> 11
		t15a = ((in1*(4076-4096) + in15*401 + 2048) >> 12) + in1
	}

	t8 := clip(t8a+t9a, min, max)
	t9 := clip(t8a-t9a, min, max)
	t10 := clip(t11a-t10a, min, max)
	t11 := clip(t11a+t10a, min, max)
	t12 := clip(t12a+t13a, min, max)
	t13 := clip(t12a-t13a, min, max)
	t14 := clip(t15a-t14a, min, max)
	t15 := clip(t15a+t14a, min, max)

	t9a = ((t14*1567 - t9*(3784-4096) + 2048) >> 12) - t9
	t14a = ((t14*(3784-4096) + t9*1567 + 2048) >> 12) + t14
	t10a = ((-(t13*(3784-4096) + t10*1567) + 2048) >> 12) - t13
	t13a = ((t13*1567 - t10*(3784-4096) + 2048) >> 12) - t10

	t8a = clip(t8+t11, min, max)
	t9 = clip(t9a+t10a, min, max)
	t10 = clip(t9a-t10a, min, max)
	t11a = clip(t8-t11, min, max)
	t12a = clip(t15-t12, min, max)
	t13 = clip(t14a-t13a, min, max)
	t14 = clip(t14a+t13a, min, max)
	t15a = clip(t15+t12, min, max)

	t10a = ((t13-t10)*181 + 128) >> 8
	t13a = ((t13+t10)*181 + 128) >> 8
	t11 = ((t12a-t11a)*181 + 128) >> 8
	t12 = ((t12a+t11a)*181 + 128) >> 8

	t0 := int(c[0])
	t1 := int(c[2*stride])
	t2 := int(c[4*stride])
	t3 := int(c[6*stride])
	t4 := int(c[8*stride])
	t5 := int(c[10*stride])
	t6 := int(c[12*stride])
	t7 := int(c[14*stride])

	c[0] = int32(clip(t0+t15a, min, max))
	c[stride] = int32(clip(t1+t14, min, max))
	c[2*stride] = int32(clip(t2+t13a, min, max))
	c[3*stride] = int32(clip(t3+t12, min, max))
	c[4*stride] = int32(clip(t4+t11, min, max))
	c[5*stride] = int32(clip(t5+t10a, min, max))
	c[6*stride] = int32(clip(t6+t9, min, max))
	c[7*stride] = int32(clip(t7+t8a, min, max))
	c[8*stride] = int32(clip(t7-t8a, min, max))
	c[9*stride] = int32(clip(t6-t9, min, max))
	c[10*stride] = int32(clip(t5-t10a, min, max))
	c[11*stride] = int32(clip(t4-t11, min, max))
	c[12*stride] = int32(clip(t3-t12, min, max))
	c[13*stride] = int32(clip(t2-t13a, min, max))
	c[14*stride] = int32(clip(t1-t14, min, max))
	c[15*stride] = int32(clip(t0-t15a, min, max))
}

// InvDCT16 is the public entry point for the 16-point inverse DCT.
func InvDCT16(c []int32, stride, min, max int) {
	invDCT16Internal(c, stride, min, max, false)
}

// --- ADST / FLIPADST family ----------------------------------------------

// invADST4Internal implements dav1d inv_adst4_1d_internal_c. The
// `outStride` parameter may be negative to realise flip-adst by writing
// the output in reverse order; `outBase` is the index of out[0].
func invADST4Internal(in []int32, inStride int, _, _ int, out []int32, outBase, outStride int) {
	in0 := int(in[0])
	in1 := int(in[inStride])
	in2 := int(in[2*inStride])
	in3 := int(in[3*inStride])

	out[outBase] = int32(
		((1321*in0+(3803-4096)*in2+
			(2482-4096)*in3+(3344-4096)*in1+2048)>>12 +
			in2 + in3 + in1),
	)
	out[outBase+outStride] = int32(
		(((2482-4096)*in0-1321*in2-
			(3803-4096)*in3+(3344-4096)*in1+2048)>>12 +
			in0 - in3 + in1),
	)
	out[outBase+2*outStride] = int32((209*(in0-in2+in3) + 128) >> 8)
	out[outBase+3*outStride] = int32(
		(((3803-4096)*in0+(2482-4096)*in2-
			1321*in3-(3344-4096)*in1+2048)>>12 +
			in0 + in2 - in1),
	)
}

// InvADST4 applies the 4-point inverse ADST in place.
func InvADST4(c []int32, stride, min, max int) {
	invADST4Internal(c, stride, min, max, c, 0, stride)
}

// InvFlipADST4 applies the flipped 4-point inverse ADST in place,
// writing samples in reverse order.
func InvFlipADST4(c []int32, stride, min, max int) {
	invADST4Internal(c, stride, min, max, c, 3*stride, -stride)
}

// invADST8Internal implements dav1d inv_adst8_1d_internal_c.
func invADST8Internal(in []int32, inStride, min, max int, out []int32, outBase, outStride int) {
	in0 := int(in[0])
	in1 := int(in[inStride])
	in2 := int(in[2*inStride])
	in3 := int(in[3*inStride])
	in4 := int(in[4*inStride])
	in5 := int(in[5*inStride])
	in6 := int(in[6*inStride])
	in7 := int(in[7*inStride])

	t0a := (((4076-4096)*in7+401*in0+2048)>>12 + in7)
	t1a := ((401*in7-(4076-4096)*in0+2048)>>12 - in0)
	t2a := (((3612-4096)*in5+1931*in2+2048)>>12 + in5)
	t3a := ((1931*in5-(3612-4096)*in2+2048)>>12 - in2)
	t4a := (1299*in3 + 1583*in4 + 1024) >> 11
	t5a := (1583*in3 - 1299*in4 + 1024) >> 11
	t6a := ((1189*in1+(3920-4096)*in6+2048)>>12 + in6)
	t7a := (((3920-4096)*in1-1189*in6+2048)>>12 + in1)

	t0 := clip(t0a+t4a, min, max)
	t1 := clip(t1a+t5a, min, max)
	t2 := clip(t2a+t6a, min, max)
	t3 := clip(t3a+t7a, min, max)
	t4 := clip(t0a-t4a, min, max)
	t5 := clip(t1a-t5a, min, max)
	t6 := clip(t2a-t6a, min, max)
	t7 := clip(t3a-t7a, min, max)

	t4a = (((3784-4096)*t4+1567*t5+2048)>>12 + t4)
	t5a = ((1567*t4-(3784-4096)*t5+2048)>>12 - t5)
	t6a = (((3784-4096)*t7-1567*t6+2048)>>12 + t7)
	t7a = ((1567*t7+(3784-4096)*t6+2048)>>12 + t6)

	out[outBase] = int32(clip(t0+t2, min, max))
	out[outBase+7*outStride] = int32(-clip(t1+t3, min, max))
	t2 = clip(t0-t2, min, max)
	t3 = clip(t1-t3, min, max)
	out[outBase+outStride] = int32(-clip(t4a+t6a, min, max))
	out[outBase+6*outStride] = int32(clip(t5a+t7a, min, max))
	t6 = clip(t4a-t6a, min, max)
	t7 = clip(t5a-t7a, min, max)

	out[outBase+3*outStride] = int32(-(((t2+t3)*181 + 128) >> 8))
	out[outBase+4*outStride] = int32(((t2-t3)*181 + 128) >> 8)
	out[outBase+2*outStride] = int32(((t6+t7)*181 + 128) >> 8)
	out[outBase+5*outStride] = int32(-(((t6-t7)*181 + 128) >> 8))
}

// InvADST8 applies the 8-point inverse ADST in place.
func InvADST8(c []int32, stride, min, max int) {
	invADST8Internal(c, stride, min, max, c, 0, stride)
}

// InvFlipADST8 applies the flipped 8-point inverse ADST in place.
func InvFlipADST8(c []int32, stride, min, max int) {
	invADST8Internal(c, stride, min, max, c, 7*stride, -stride)
}

// invADST16Internal implements dav1d inv_adst16_1d_internal_c.
func invADST16Internal(in []int32, inStride, min, max int, out []int32, outBase, outStride int) {
	in0 := int(in[0])
	in1 := int(in[inStride])
	in2 := int(in[2*inStride])
	in3 := int(in[3*inStride])
	in4 := int(in[4*inStride])
	in5 := int(in[5*inStride])
	in6 := int(in[6*inStride])
	in7 := int(in[7*inStride])
	in8 := int(in[8*inStride])
	in9 := int(in[9*inStride])
	in10 := int(in[10*inStride])
	in11 := int(in[11*inStride])
	in12 := int(in[12*inStride])
	in13 := int(in[13*inStride])
	in14 := int(in[14*inStride])
	in15 := int(in[15*inStride])

	t0 := ((in15*(4091-4096) + in0*201 + 2048) >> 12) + in15
	t1 := ((in15*201 - in0*(4091-4096) + 2048) >> 12) - in0
	t2 := ((in13*(3973-4096) + in2*995 + 2048) >> 12) + in13
	t3 := ((in13*995 - in2*(3973-4096) + 2048) >> 12) - in2
	t4 := ((in11*(3703-4096) + in4*1751 + 2048) >> 12) + in11
	t5 := ((in11*1751 - in4*(3703-4096) + 2048) >> 12) - in4
	t6 := (in9*1645 + in6*1220 + 1024) >> 11
	t7 := (in9*1220 - in6*1645 + 1024) >> 11
	t8 := ((in7*2751 + in8*(3035-4096) + 2048) >> 12) + in8
	t9 := ((in7*(3035-4096) - in8*2751 + 2048) >> 12) + in7
	t10 := ((in5*2106 + in10*(3513-4096) + 2048) >> 12) + in10
	t11 := ((in5*(3513-4096) - in10*2106 + 2048) >> 12) + in5
	t12 := ((in3*1380 + in12*(3857-4096) + 2048) >> 12) + in12
	t13 := ((in3*(3857-4096) - in12*1380 + 2048) >> 12) + in3
	t14 := ((in1*601 + in14*(4052-4096) + 2048) >> 12) + in14
	t15 := ((in1*(4052-4096) - in14*601 + 2048) >> 12) + in1

	t0a := clip(t0+t8, min, max)
	t1a := clip(t1+t9, min, max)
	t2a := clip(t2+t10, min, max)
	t3a := clip(t3+t11, min, max)
	t4a := clip(t4+t12, min, max)
	t5a := clip(t5+t13, min, max)
	t6a := clip(t6+t14, min, max)
	t7a := clip(t7+t15, min, max)
	t8a := clip(t0-t8, min, max)
	t9a := clip(t1-t9, min, max)
	t10a := clip(t2-t10, min, max)
	t11a := clip(t3-t11, min, max)
	t12a := clip(t4-t12, min, max)
	t13a := clip(t5-t13, min, max)
	t14a := clip(t6-t14, min, max)
	t15a := clip(t7-t15, min, max)

	t8 = ((t8a*(4017-4096) + t9a*799 + 2048) >> 12) + t8a
	t9 = ((t8a*799 - t9a*(4017-4096) + 2048) >> 12) - t9a
	t10 = ((t10a*2276 + t11a*(3406-4096) + 2048) >> 12) + t11a
	t11 = ((t10a*(3406-4096) - t11a*2276 + 2048) >> 12) + t10a
	t12 = ((t13a*(4017-4096) - t12a*799 + 2048) >> 12) + t13a
	t13 = ((t13a*799 + t12a*(4017-4096) + 2048) >> 12) + t12a
	t14 = ((t15a*2276 - t14a*(3406-4096) + 2048) >> 12) - t14a
	t15 = ((t15a*(3406-4096) + t14a*2276 + 2048) >> 12) + t15a

	t0 = clip(t0a+t4a, min, max)
	t1 = clip(t1a+t5a, min, max)
	t2 = clip(t2a+t6a, min, max)
	t3 = clip(t3a+t7a, min, max)
	t4 = clip(t0a-t4a, min, max)
	t5 = clip(t1a-t5a, min, max)
	t6 = clip(t2a-t6a, min, max)
	t7 = clip(t3a-t7a, min, max)
	t8a = clip(t8+t12, min, max)
	t9a = clip(t9+t13, min, max)
	t10a = clip(t10+t14, min, max)
	t11a = clip(t11+t15, min, max)
	t12a = clip(t8-t12, min, max)
	t13a = clip(t9-t13, min, max)
	t14a = clip(t10-t14, min, max)
	t15a = clip(t11-t15, min, max)

	t4a = ((t4*(3784-4096) + t5*1567 + 2048) >> 12) + t4
	t5a = ((t4*1567 - t5*(3784-4096) + 2048) >> 12) - t5
	t6a = ((t7*(3784-4096) - t6*1567 + 2048) >> 12) + t7
	t7a = ((t7*1567 + t6*(3784-4096) + 2048) >> 12) + t6
	t12 = ((t12a*(3784-4096) + t13a*1567 + 2048) >> 12) + t12a
	t13 = ((t12a*1567 - t13a*(3784-4096) + 2048) >> 12) - t13a
	t14 = ((t15a*(3784-4096) - t14a*1567 + 2048) >> 12) + t15a
	t15 = ((t15a*1567 + t14a*(3784-4096) + 2048) >> 12) + t14a

	out[outBase] = int32(clip(t0+t2, min, max))
	out[outBase+15*outStride] = int32(-clip(t1+t3, min, max))
	t2aOut := clip(t0-t2, min, max)
	t3aOut := clip(t1-t3, min, max)
	out[outBase+3*outStride] = int32(-clip(t4a+t6a, min, max))
	out[outBase+12*outStride] = int32(clip(t5a+t7a, min, max))
	t6 = clip(t4a-t6a, min, max)
	t7 = clip(t5a-t7a, min, max)
	out[outBase+outStride] = int32(-clip(t8a+t10a, min, max))
	out[outBase+14*outStride] = int32(clip(t9a+t11a, min, max))
	t10 = clip(t8a-t10a, min, max)
	t11 = clip(t9a-t11a, min, max)
	out[outBase+2*outStride] = int32(clip(t12+t14, min, max))
	out[outBase+13*outStride] = int32(-clip(t13+t15, min, max))
	t14aOut := clip(t12-t14, min, max)
	t15aOut := clip(t13-t15, min, max)

	out[outBase+7*outStride] = int32(-(((t2aOut+t3aOut)*181 + 128) >> 8))
	out[outBase+8*outStride] = int32(((t2aOut-t3aOut)*181 + 128) >> 8)
	out[outBase+4*outStride] = int32(((t6+t7)*181 + 128) >> 8)
	out[outBase+11*outStride] = int32(-(((t6-t7)*181 + 128) >> 8))
	out[outBase+6*outStride] = int32(((t10+t11)*181 + 128) >> 8)
	out[outBase+9*outStride] = int32(-(((t10-t11)*181 + 128) >> 8))
	out[outBase+5*outStride] = int32(-(((t14aOut+t15aOut)*181 + 128) >> 8))
	out[outBase+10*outStride] = int32(((t14aOut-t15aOut)*181 + 128) >> 8)
}

// InvADST16 applies the 16-point inverse ADST in place.
func InvADST16(c []int32, stride, min, max int) {
	invADST16Internal(c, stride, min, max, c, 0, stride)
}

// InvFlipADST16 applies the flipped 16-point inverse ADST in place.
func InvFlipADST16(c []int32, stride, min, max int) {
	invADST16Internal(c, stride, min, max, c, 15*stride, -stride)
}

// --- IDENTITY family ------------------------------------------------------

// InvIdentity4 implements dav1d inv_identity4_1d_c.
func InvIdentity4(c []int32, stride, _, _ int) {
	for i := 0; i < 4; i++ {
		in := int(c[i*stride])
		c[i*stride] = int32(in + ((in*1697 + 2048) >> 12))
	}
}

// InvIdentity8 implements dav1d inv_identity8_1d_c.
func InvIdentity8(c []int32, stride, _, _ int) {
	for i := 0; i < 8; i++ {
		c[i*stride] *= 2
	}
}

// InvIdentity16 implements dav1d inv_identity16_1d_c.
func InvIdentity16(c []int32, stride, _, _ int) {
	for i := 0; i < 16; i++ {
		in := int(c[i*stride])
		c[i*stride] = int32(2*in + ((in*1697 + 1024) >> 11))
	}
}

// InvIdentity32 implements dav1d inv_identity32_1d_c.
func InvIdentity32(c []int32, stride, _, _ int) {
	for i := 0; i < 32; i++ {
		c[i*stride] *= 4
	}
}

// --- WHT (lossless) ------------------------------------------------------

// InvWHT4 implements dav1d dav1d_inv_wht4_1d_c. Used by lossless coding
// (the 4×4 Walsh-Hadamard transform).
func InvWHT4(c []int32, stride int) {
	in0 := int(c[0])
	in1 := int(c[stride])
	in2 := int(c[2*stride])
	in3 := int(c[3*stride])

	t0 := in0 + in1
	t2 := in2 - in3
	t4 := (t0 - t2) >> 1
	t3 := t4 - in3
	t1 := t4 - in1

	c[0] = int32(t0 - t3)
	c[stride] = int32(t3)
	c[2*stride] = int32(t1)
	c[3*stride] = int32(t2 + t1)
}
