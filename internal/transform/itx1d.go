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

// invDCT32Internal implements dav1d inv_dct32_1d_internal_c.
// tx64 selects the reduced-bandwidth path used when this kernel feeds
// the 64-point DCT (only even-indexed inputs are non-zero).
func invDCT32Internal(c []int32, stride, min, max int, tx64 bool) {
	invDCT16Internal(c, stride<<1, min, max, tx64)

	in1 := int(c[stride])
	in3 := int(c[3*stride])
	in5 := int(c[5*stride])
	in7 := int(c[7*stride])
	in9 := int(c[9*stride])
	in11 := int(c[11*stride])
	in13 := int(c[13*stride])
	in15 := int(c[15*stride])

	var t16a, t17a, t18a, t19a, t20a, t21a, t22a, t23a int
	var t24a, t25a, t26a, t27a, t28a, t29a, t30a, t31a int
	if tx64 {
		t16a = (in1*201 + 2048) >> 12
		t17a = (in15*-2751 + 2048) >> 12
		t18a = (in9*1751 + 2048) >> 12
		t19a = (in7*-1380 + 2048) >> 12
		t20a = (in5*995 + 2048) >> 12
		t21a = (in11*-2106 + 2048) >> 12
		t22a = (in13*2440 + 2048) >> 12
		t23a = (in3*-601 + 2048) >> 12
		t24a = (in3*4052 + 2048) >> 12
		t25a = (in13*3290 + 2048) >> 12
		t26a = (in11*3513 + 2048) >> 12
		t27a = (in5*3973 + 2048) >> 12
		t28a = (in7*3857 + 2048) >> 12
		t29a = (in9*3703 + 2048) >> 12
		t30a = (in15*3035 + 2048) >> 12
		t31a = (in1*4091 + 2048) >> 12
	} else {
		in17 := int(c[17*stride])
		in19 := int(c[19*stride])
		in21 := int(c[21*stride])
		in23 := int(c[23*stride])
		in25 := int(c[25*stride])
		in27 := int(c[27*stride])
		in29 := int(c[29*stride])
		in31 := int(c[31*stride])

		t16a = ((in1*201 - in31*(4091-4096) + 2048) >> 12) - in31
		t17a = ((in17*(3035-4096) - in15*2751 + 2048) >> 12) + in17
		t18a = ((in9*1751 - in23*(3703-4096) + 2048) >> 12) - in23
		t19a = ((in25*(3857-4096) - in7*1380 + 2048) >> 12) + in25
		t20a = ((in5*995 - in27*(3973-4096) + 2048) >> 12) - in27
		t21a = ((in21*(3513-4096) - in11*2106 + 2048) >> 12) + in21
		t22a = (in13*1220 - in19*1645 + 1024) >> 11
		t23a = ((in29*(4052-4096) - in3*601 + 2048) >> 12) + in29
		t24a = ((in29*601 + in3*(4052-4096) + 2048) >> 12) + in3
		t25a = (in13*1645 + in19*1220 + 1024) >> 11
		t26a = ((in21*2106 + in11*(3513-4096) + 2048) >> 12) + in11
		t27a = ((in5*(3973-4096) + in27*995 + 2048) >> 12) + in5
		t28a = ((in25*1380 + in7*(3857-4096) + 2048) >> 12) + in7
		t29a = ((in9*(3703-4096) + in23*1751 + 2048) >> 12) + in9
		t30a = ((in17*2751 + in15*(3035-4096) + 2048) >> 12) + in15
		t31a = ((in1*(4091-4096) + in31*201 + 2048) >> 12) + in1
	}

	t16 := clip(t16a+t17a, min, max)
	t17 := clip(t16a-t17a, min, max)
	t18 := clip(t19a-t18a, min, max)
	t19 := clip(t19a+t18a, min, max)
	t20 := clip(t20a+t21a, min, max)
	t21 := clip(t20a-t21a, min, max)
	t22 := clip(t23a-t22a, min, max)
	t23 := clip(t23a+t22a, min, max)
	t24 := clip(t24a+t25a, min, max)
	t25 := clip(t24a-t25a, min, max)
	t26 := clip(t27a-t26a, min, max)
	t27 := clip(t27a+t26a, min, max)
	t28 := clip(t28a+t29a, min, max)
	t29 := clip(t28a-t29a, min, max)
	t30 := clip(t31a-t30a, min, max)
	t31 := clip(t31a+t30a, min, max)

	t17a = ((t30*799 - t17*(4017-4096) + 2048) >> 12) - t17
	t30a = ((t30*(4017-4096) + t17*799 + 2048) >> 12) + t30
	t18a = ((-(t29*(4017-4096) + t18*799) + 2048) >> 12) - t29
	t29a = ((t29*799 - t18*(4017-4096) + 2048) >> 12) - t18
	t21a = (t26*1703 - t21*1138 + 1024) >> 11
	t26a = (t26*1138 + t21*1703 + 1024) >> 11
	t22a = (-(t25*1138 + t22*1703) + 1024) >> 11
	t25a = (t25*1703 - t22*1138 + 1024) >> 11

	t16a = clip(t16+t19, min, max)
	t17 = clip(t17a+t18a, min, max)
	t18 = clip(t17a-t18a, min, max)
	t19a = clip(t16-t19, min, max)
	t20a = clip(t23-t20, min, max)
	t21 = clip(t22a-t21a, min, max)
	t22 = clip(t22a+t21a, min, max)
	t23a = clip(t23+t20, min, max)
	t24a = clip(t24+t27, min, max)
	t25 = clip(t25a+t26a, min, max)
	t26 = clip(t25a-t26a, min, max)
	t27a = clip(t24-t27, min, max)
	t28a = clip(t31-t28, min, max)
	t29 = clip(t30a-t29a, min, max)
	t30 = clip(t30a+t29a, min, max)
	t31a = clip(t31+t28, min, max)

	t18a = ((t29*1567 - t18*(3784-4096) + 2048) >> 12) - t18
	t29a = ((t29*(3784-4096) + t18*1567 + 2048) >> 12) + t29
	t19 = ((t28a*1567 - t19a*(3784-4096) + 2048) >> 12) - t19a
	t28 = ((t28a*(3784-4096) + t19a*1567 + 2048) >> 12) + t28a
	t20 = ((-(t27a*(3784-4096) + t20a*1567) + 2048) >> 12) - t27a
	t27 = ((t27a*1567 - t20a*(3784-4096) + 2048) >> 12) - t20a
	t21a = ((-(t26*(3784-4096) + t21*1567) + 2048) >> 12) - t26
	t26a = ((t26*1567 - t21*(3784-4096) + 2048) >> 12) - t21

	t16 = clip(t16a+t23a, min, max)
	t17a = clip(t17+t22, min, max)
	t18 = clip(t18a+t21a, min, max)
	t19a = clip(t19+t20, min, max)
	t20a = clip(t19-t20, min, max)
	t21 = clip(t18a-t21a, min, max)
	t22a = clip(t17-t22, min, max)
	t23 = clip(t16a-t23a, min, max)
	t24 = clip(t31a-t24a, min, max)
	t25a = clip(t30-t25, min, max)
	t26 = clip(t29a-t26a, min, max)
	t27a = clip(t28-t27, min, max)
	t28a = clip(t28+t27, min, max)
	t29 = clip(t29a+t26a, min, max)
	t30a = clip(t30+t25, min, max)
	t31 = clip(t31a+t24a, min, max)

	t20 = ((t27a-t20a)*181 + 128) >> 8
	t27 = ((t27a+t20a)*181 + 128) >> 8
	t21a = ((t26-t21)*181 + 128) >> 8
	t26a = ((t26+t21)*181 + 128) >> 8
	t22 = ((t25a-t22a)*181 + 128) >> 8
	t25 = ((t25a+t22a)*181 + 128) >> 8
	t23a = ((t24-t23)*181 + 128) >> 8
	t24a = ((t24+t23)*181 + 128) >> 8

	t0 := int(c[0])
	t1 := int(c[2*stride])
	t2 := int(c[4*stride])
	t3 := int(c[6*stride])
	t4 := int(c[8*stride])
	t5 := int(c[10*stride])
	t6 := int(c[12*stride])
	t7 := int(c[14*stride])
	t8 := int(c[16*stride])
	t9 := int(c[18*stride])
	t10 := int(c[20*stride])
	t11 := int(c[22*stride])
	t12 := int(c[24*stride])
	t13 := int(c[26*stride])
	t14 := int(c[28*stride])
	t15 := int(c[30*stride])

	c[0] = int32(clip(t0+t31, min, max))
	c[stride] = int32(clip(t1+t30a, min, max))
	c[2*stride] = int32(clip(t2+t29, min, max))
	c[3*stride] = int32(clip(t3+t28a, min, max))
	c[4*stride] = int32(clip(t4+t27, min, max))
	c[5*stride] = int32(clip(t5+t26a, min, max))
	c[6*stride] = int32(clip(t6+t25, min, max))
	c[7*stride] = int32(clip(t7+t24a, min, max))
	c[8*stride] = int32(clip(t8+t23a, min, max))
	c[9*stride] = int32(clip(t9+t22, min, max))
	c[10*stride] = int32(clip(t10+t21a, min, max))
	c[11*stride] = int32(clip(t11+t20, min, max))
	c[12*stride] = int32(clip(t12+t19a, min, max))
	c[13*stride] = int32(clip(t13+t18, min, max))
	c[14*stride] = int32(clip(t14+t17a, min, max))
	c[15*stride] = int32(clip(t15+t16, min, max))
	c[16*stride] = int32(clip(t15-t16, min, max))
	c[17*stride] = int32(clip(t14-t17a, min, max))
	c[18*stride] = int32(clip(t13-t18, min, max))
	c[19*stride] = int32(clip(t12-t19a, min, max))
	c[20*stride] = int32(clip(t11-t20, min, max))
	c[21*stride] = int32(clip(t10-t21a, min, max))
	c[22*stride] = int32(clip(t9-t22, min, max))
	c[23*stride] = int32(clip(t8-t23a, min, max))
	c[24*stride] = int32(clip(t7-t24a, min, max))
	c[25*stride] = int32(clip(t6-t25, min, max))
	c[26*stride] = int32(clip(t5-t26a, min, max))
	c[27*stride] = int32(clip(t4-t27, min, max))
	c[28*stride] = int32(clip(t3-t28a, min, max))
	c[29*stride] = int32(clip(t2-t29, min, max))
	c[30*stride] = int32(clip(t1-t30a, min, max))
	c[31*stride] = int32(clip(t0-t31, min, max))
}

// InvDCT32 is the public entry point for the 32-point inverse DCT.
func InvDCT32(c []int32, stride, min, max int) {
	invDCT32Internal(c, stride, min, max, false)
}

// InvDCT64 implements dav1d inv_dct64_1d_c. It calls invDCT32Internal
// with tx64=true to process the even-indexed inputs, then performs
// additional butterfly stages for the odd-indexed inputs.
func InvDCT64(c []int32, stride, min, max int) {
	invDCT32Internal(c, stride<<1, min, max, true)

	in1 := int(c[stride])
	in3 := int(c[3*stride])
	in5 := int(c[5*stride])
	in7 := int(c[7*stride])
	in9 := int(c[9*stride])
	in11 := int(c[11*stride])
	in13 := int(c[13*stride])
	in15 := int(c[15*stride])
	in17 := int(c[17*stride])
	in19 := int(c[19*stride])
	in21 := int(c[21*stride])
	in23 := int(c[23*stride])
	in25 := int(c[25*stride])
	in27 := int(c[27*stride])
	in29 := int(c[29*stride])
	in31 := int(c[31*stride])

	t32a := (in1*101 + 2048) >> 12
	t33a := (in31*-2824 + 2048) >> 12
	t34a := (in17*1660 + 2048) >> 12
	t35a := (in15*-1474 + 2048) >> 12
	t36a := (in9*897 + 2048) >> 12
	t37a := (in23*-2191 + 2048) >> 12
	t38a := (in25*2359 + 2048) >> 12
	t39a := (in7*-700 + 2048) >> 12
	t40a := (in5*501 + 2048) >> 12
	t41a := (in27*-2520 + 2048) >> 12
	t42a := (in21*2019 + 2048) >> 12
	t43a := (in11*-1092 + 2048) >> 12
	t44a := (in13*1285 + 2048) >> 12
	t45a := (in19*-1842 + 2048) >> 12
	t46a := (in29*2675 + 2048) >> 12
	t47a := (in3*-301 + 2048) >> 12
	t48a := (in3*4085 + 2048) >> 12
	t49a := (in29*3102 + 2048) >> 12
	t50a := (in19*3659 + 2048) >> 12
	t51a := (in13*3889 + 2048) >> 12
	t52a := (in11*3948 + 2048) >> 12
	t53a := (in21*3564 + 2048) >> 12
	t54a := (in27*3229 + 2048) >> 12
	t55a := (in5*4065 + 2048) >> 12
	t56a := (in7*4036 + 2048) >> 12
	t57a := (in25*3349 + 2048) >> 12
	t58a := (in23*3461 + 2048) >> 12
	t59a := (in9*3996 + 2048) >> 12
	t60a := (in15*3822 + 2048) >> 12
	t61a := (in17*3745 + 2048) >> 12
	t62a := (in31*2967 + 2048) >> 12
	t63a := (in1*4095 + 2048) >> 12

	t32 := clip(t32a+t33a, min, max)
	t33 := clip(t32a-t33a, min, max)
	t34 := clip(t35a-t34a, min, max)
	t35 := clip(t35a+t34a, min, max)
	t36 := clip(t36a+t37a, min, max)
	t37 := clip(t36a-t37a, min, max)
	t38 := clip(t39a-t38a, min, max)
	t39 := clip(t39a+t38a, min, max)
	t40 := clip(t40a+t41a, min, max)
	t41 := clip(t40a-t41a, min, max)
	t42 := clip(t43a-t42a, min, max)
	t43 := clip(t43a+t42a, min, max)
	t44 := clip(t44a+t45a, min, max)
	t45 := clip(t44a-t45a, min, max)
	t46 := clip(t47a-t46a, min, max)
	t47 := clip(t47a+t46a, min, max)
	t48 := clip(t48a+t49a, min, max)
	t49 := clip(t48a-t49a, min, max)
	t50 := clip(t51a-t50a, min, max)
	t51 := clip(t51a+t50a, min, max)
	t52 := clip(t52a+t53a, min, max)
	t53 := clip(t52a-t53a, min, max)
	t54 := clip(t55a-t54a, min, max)
	t55 := clip(t55a+t54a, min, max)
	t56 := clip(t56a+t57a, min, max)
	t57 := clip(t56a-t57a, min, max)
	t58 := clip(t59a-t58a, min, max)
	t59 := clip(t59a+t58a, min, max)
	t60 := clip(t60a+t61a, min, max)
	t61 := clip(t60a-t61a, min, max)
	t62 := clip(t63a-t62a, min, max)
	t63 := clip(t63a+t62a, min, max)

	t33a = ((t33*(4096-4076) + t62*401 + 2048) >> 12) - t33
	t34a = ((t34*-401 + t61*(4096-4076) + 2048) >> 12) - t61
	t37a = (t37*-1299 + t58*1583 + 1024) >> 11
	t38a = (t38*-1583 + t57*-1299 + 1024) >> 11
	t41a = ((t41*(4096-3612) + t54*1931 + 2048) >> 12) - t41
	t42a = ((t42*-1931 + t53*(4096-3612) + 2048) >> 12) - t53
	t45a = ((t45*-1189 + t50*(3920-4096) + 2048) >> 12) + t50
	t46a = ((t46*(4096-3920) + t49*-1189 + 2048) >> 12) - t46
	t49a = ((t46*-1189 + t49*(3920-4096) + 2048) >> 12) + t49
	t50a = ((t45*(3920-4096) + t50*1189 + 2048) >> 12) + t45
	t53a = ((t42*(4096-3612) + t53*1931 + 2048) >> 12) - t42
	t54a = ((t41*1931 + t54*(3612-4096) + 2048) >> 12) + t54
	t57a = (t38*-1299 + t57*1583 + 1024) >> 11
	t58a = (t37*1583 + t58*1299 + 1024) >> 11
	t61a = ((t34*(4096-4076) + t61*401 + 2048) >> 12) - t34
	t62a = ((t33*401 + t62*(4076-4096) + 2048) >> 12) + t62

	t32a = clip(t32+t35, min, max)
	t33 = clip(t33a+t34a, min, max)
	t34 = clip(t33a-t34a, min, max)
	t35a = clip(t32-t35, min, max)
	t36a = clip(t39-t36, min, max)
	t37 = clip(t38a-t37a, min, max)
	t38 = clip(t38a+t37a, min, max)
	t39a = clip(t39+t36, min, max)
	t40a = clip(t40+t43, min, max)
	t41 = clip(t41a+t42a, min, max)
	t42 = clip(t41a-t42a, min, max)
	t43a = clip(t40-t43, min, max)
	t44a = clip(t47-t44, min, max)
	t45 = clip(t46a-t45a, min, max)
	t46 = clip(t46a+t45a, min, max)
	t47a = clip(t47+t44, min, max)
	t48a = clip(t48+t51, min, max)
	t49 = clip(t49a+t50a, min, max)
	t50 = clip(t49a-t50a, min, max)
	t51a = clip(t48-t51, min, max)
	t52a = clip(t55-t52, min, max)
	t53 = clip(t54a-t53a, min, max)
	t54 = clip(t54a+t53a, min, max)
	t55a = clip(t55+t52, min, max)
	t56a = clip(t56+t59, min, max)
	t57 = clip(t57a+t58a, min, max)
	t58 = clip(t57a-t58a, min, max)
	t59a = clip(t56-t59, min, max)
	t60a = clip(t63-t60, min, max)
	t61 = clip(t62a-t61a, min, max)
	t62 = clip(t62a+t61a, min, max)
	t63a = clip(t63+t60, min, max)

	t34a = ((t34*(4096-4017) + t61*799 + 2048) >> 12) - t34
	t35 = ((t35a*(4096-4017) + t60a*799 + 2048) >> 12) - t35a
	t36 = ((t36a*-799 + t59a*(4096-4017) + 2048) >> 12) - t59a
	t37a = ((t37*-799 + t58*(4096-4017) + 2048) >> 12) - t58
	t42a = (t42*-1138 + t53*1703 + 1024) >> 11
	t43 = (t43a*-1138 + t52a*1703 + 1024) >> 11
	t44 = (t44a*-1703 + t51a*-1138 + 1024) >> 11
	t45a = (t45*-1703 + t50*-1138 + 1024) >> 11
	t50a = (t45*-1138 + t50*1703 + 1024) >> 11
	t51 = (t44a*-1138 + t51a*1703 + 1024) >> 11
	t52 = (t43a*1703 + t52a*1138 + 1024) >> 11
	t53a = (t42*1703 + t53*1138 + 1024) >> 11
	t58a = ((t37*(4096-4017) + t58*799 + 2048) >> 12) - t37
	t59 = ((t36a*(4096-4017) + t59a*799 + 2048) >> 12) - t36a
	t60 = ((t35a*799 + t60a*(4017-4096) + 2048) >> 12) + t60a
	t61a = ((t34*799 + t61*(4017-4096) + 2048) >> 12) + t61

	t32 = clip(t32a+t39a, min, max)
	t33a = clip(t33+t38, min, max)
	t34 = clip(t34a+t37a, min, max)
	t35a = clip(t35+t36, min, max)
	t36a = clip(t35-t36, min, max)
	t37 = clip(t34a-t37a, min, max)
	t38a = clip(t33-t38, min, max)
	t39 = clip(t32a-t39a, min, max)
	t40 = clip(t47a-t40a, min, max)
	t41a = clip(t46-t41, min, max)
	t42 = clip(t45a-t42a, min, max)
	t43a = clip(t44-t43, min, max)
	t44a = clip(t44+t43, min, max)
	t45 = clip(t45a+t42a, min, max)
	t46a = clip(t46+t41, min, max)
	t47 = clip(t47a+t40a, min, max)
	t48 = clip(t48a+t55a, min, max)
	t49a = clip(t49+t54, min, max)
	t50 = clip(t50a+t53a, min, max)
	t51a = clip(t51+t52, min, max)
	t52a = clip(t51-t52, min, max)
	t53 = clip(t50a-t53a, min, max)
	t54a = clip(t49-t54, min, max)
	t55 = clip(t48a-t55a, min, max)
	t56 = clip(t63a-t56a, min, max)
	t57a = clip(t62-t57, min, max)
	t58 = clip(t61a-t58a, min, max)
	t59a = clip(t60-t59, min, max)
	t60a = clip(t60+t59, min, max)
	t61 = clip(t61a+t58a, min, max)
	t62a = clip(t62+t57, min, max)
	t63 = clip(t63a+t56a, min, max)

	t36 = ((t36a*(4096-3784) + t59a*1567 + 2048) >> 12) - t36a
	t37a = ((t37*(4096-3784) + t58*1567 + 2048) >> 12) - t37
	t38 = ((t38a*(4096-3784) + t57a*1567 + 2048) >> 12) - t38a
	t39a = ((t39*(4096-3784) + t56*1567 + 2048) >> 12) - t39
	t40a = ((t40*-1567 + t55*(4096-3784) + 2048) >> 12) - t55
	t41 = ((t41a*-1567 + t54a*(4096-3784) + 2048) >> 12) - t54a
	t42a = ((t42*-1567 + t53*(4096-3784) + 2048) >> 12) - t53
	t43 = ((t43a*-1567 + t52a*(4096-3784) + 2048) >> 12) - t52a
	t52 = ((t43a*(4096-3784) + t52a*1567 + 2048) >> 12) - t43a
	t53a = ((t42*(4096-3784) + t53*1567 + 2048) >> 12) - t42
	t54 = ((t41a*(4096-3784) + t54a*1567 + 2048) >> 12) - t41a
	t55a = ((t40*(4096-3784) + t55*1567 + 2048) >> 12) - t40
	t56a = ((t39*1567 + t56*(3784-4096) + 2048) >> 12) + t56
	t57 = ((t38a*1567 + t57a*(3784-4096) + 2048) >> 12) + t57a
	t58a = ((t37*1567 + t58*(3784-4096) + 2048) >> 12) + t58
	t59 = ((t36a*1567 + t59a*(3784-4096) + 2048) >> 12) + t59a

	t32a = clip(t32+t47, min, max)
	t33 = clip(t33a+t46a, min, max)
	t34a = clip(t34+t45, min, max)
	t35 = clip(t35a+t44a, min, max)
	t36a = clip(t36+t43, min, max)
	t37 = clip(t37a+t42a, min, max)
	t38a = clip(t38+t41, min, max)
	t39 = clip(t39a+t40a, min, max)
	t40 = clip(t39a-t40a, min, max)
	t41a = clip(t38-t41, min, max)
	t42 = clip(t37a-t42a, min, max)
	t43a = clip(t36-t43, min, max)
	t44 = clip(t35a-t44a, min, max)
	t45a = clip(t34-t45, min, max)
	t46 = clip(t33a-t46a, min, max)
	t47a = clip(t32-t47, min, max)
	t48a = clip(t63-t48, min, max)
	t49 = clip(t62a-t49a, min, max)
	t50a = clip(t61-t50, min, max)
	t51 = clip(t60a-t51a, min, max)
	t52a = clip(t59-t52, min, max)
	t53 = clip(t58a-t53a, min, max)
	t54a = clip(t57-t54, min, max)
	t55 = clip(t56a-t55a, min, max)
	t56 = clip(t56a+t55a, min, max)
	t57a = clip(t57+t54, min, max)
	t58 = clip(t58a+t53a, min, max)
	t59a = clip(t59+t52, min, max)
	t60 = clip(t60a+t51a, min, max)
	t61a = clip(t61+t50, min, max)
	t62 = clip(t62a+t49a, min, max)
	t63a = clip(t63+t48, min, max)

	t40a = ((t55-t40)*181 + 128) >> 8
	t41 = ((t54a-t41a)*181 + 128) >> 8
	t42a = ((t53-t42)*181 + 128) >> 8
	t43 = ((t52a-t43a)*181 + 128) >> 8
	t44a = ((t51-t44)*181 + 128) >> 8
	t45 = ((t50a-t45a)*181 + 128) >> 8
	t46a = ((t49-t46)*181 + 128) >> 8
	t47 = ((t48a-t47a)*181 + 128) >> 8
	t48 = ((t47a+t48a)*181 + 128) >> 8
	t49a = ((t46+t49)*181 + 128) >> 8
	t50 = ((t45a+t50a)*181 + 128) >> 8
	t51a = ((t44+t51)*181 + 128) >> 8
	t52 = ((t43a+t52a)*181 + 128) >> 8
	t53a = ((t42+t53)*181 + 128) >> 8
	t54 = ((t41a+t54a)*181 + 128) >> 8
	t55a = ((t40+t55)*181 + 128) >> 8

	t0 := int(c[0])
	t1 := int(c[2*stride])
	t2 := int(c[4*stride])
	t3 := int(c[6*stride])
	t4 := int(c[8*stride])
	t5 := int(c[10*stride])
	t6 := int(c[12*stride])
	t7 := int(c[14*stride])
	t8 := int(c[16*stride])
	t9 := int(c[18*stride])
	t10 := int(c[20*stride])
	t11 := int(c[22*stride])
	t12 := int(c[24*stride])
	t13 := int(c[26*stride])
	t14 := int(c[28*stride])
	t15 := int(c[30*stride])
	t16 := int(c[32*stride])
	t17 := int(c[34*stride])
	t18 := int(c[36*stride])
	t19 := int(c[38*stride])
	t20 := int(c[40*stride])
	t21 := int(c[42*stride])
	t22 := int(c[44*stride])
	t23 := int(c[46*stride])
	t24 := int(c[48*stride])
	t25 := int(c[50*stride])
	t26 := int(c[52*stride])
	t27 := int(c[54*stride])
	t28 := int(c[56*stride])
	t29 := int(c[58*stride])
	t30 := int(c[60*stride])
	t31 := int(c[62*stride])

	c[0] = int32(clip(t0+t63a, min, max))
	c[stride] = int32(clip(t1+t62, min, max))
	c[2*stride] = int32(clip(t2+t61a, min, max))
	c[3*stride] = int32(clip(t3+t60, min, max))
	c[4*stride] = int32(clip(t4+t59a, min, max))
	c[5*stride] = int32(clip(t5+t58, min, max))
	c[6*stride] = int32(clip(t6+t57a, min, max))
	c[7*stride] = int32(clip(t7+t56, min, max))
	c[8*stride] = int32(clip(t8+t55a, min, max))
	c[9*stride] = int32(clip(t9+t54, min, max))
	c[10*stride] = int32(clip(t10+t53a, min, max))
	c[11*stride] = int32(clip(t11+t52, min, max))
	c[12*stride] = int32(clip(t12+t51a, min, max))
	c[13*stride] = int32(clip(t13+t50, min, max))
	c[14*stride] = int32(clip(t14+t49a, min, max))
	c[15*stride] = int32(clip(t15+t48, min, max))
	c[16*stride] = int32(clip(t16+t47, min, max))
	c[17*stride] = int32(clip(t17+t46a, min, max))
	c[18*stride] = int32(clip(t18+t45, min, max))
	c[19*stride] = int32(clip(t19+t44a, min, max))
	c[20*stride] = int32(clip(t20+t43, min, max))
	c[21*stride] = int32(clip(t21+t42a, min, max))
	c[22*stride] = int32(clip(t22+t41, min, max))
	c[23*stride] = int32(clip(t23+t40a, min, max))
	c[24*stride] = int32(clip(t24+t39, min, max))
	c[25*stride] = int32(clip(t25+t38a, min, max))
	c[26*stride] = int32(clip(t26+t37, min, max))
	c[27*stride] = int32(clip(t27+t36a, min, max))
	c[28*stride] = int32(clip(t28+t35, min, max))
	c[29*stride] = int32(clip(t29+t34a, min, max))
	c[30*stride] = int32(clip(t30+t33, min, max))
	c[31*stride] = int32(clip(t31+t32a, min, max))
	c[32*stride] = int32(clip(t31-t32a, min, max))
	c[33*stride] = int32(clip(t30-t33, min, max))
	c[34*stride] = int32(clip(t29-t34a, min, max))
	c[35*stride] = int32(clip(t28-t35, min, max))
	c[36*stride] = int32(clip(t27-t36a, min, max))
	c[37*stride] = int32(clip(t26-t37, min, max))
	c[38*stride] = int32(clip(t25-t38a, min, max))
	c[39*stride] = int32(clip(t24-t39, min, max))
	c[40*stride] = int32(clip(t23-t40a, min, max))
	c[41*stride] = int32(clip(t22-t41, min, max))
	c[42*stride] = int32(clip(t21-t42a, min, max))
	c[43*stride] = int32(clip(t20-t43, min, max))
	c[44*stride] = int32(clip(t19-t44a, min, max))
	c[45*stride] = int32(clip(t18-t45, min, max))
	c[46*stride] = int32(clip(t17-t46a, min, max))
	c[47*stride] = int32(clip(t16-t47, min, max))
	c[48*stride] = int32(clip(t15-t48, min, max))
	c[49*stride] = int32(clip(t14-t49a, min, max))
	c[50*stride] = int32(clip(t13-t50, min, max))
	c[51*stride] = int32(clip(t12-t51a, min, max))
	c[52*stride] = int32(clip(t11-t52, min, max))
	c[53*stride] = int32(clip(t10-t53a, min, max))
	c[54*stride] = int32(clip(t9-t54, min, max))
	c[55*stride] = int32(clip(t8-t55a, min, max))
	c[56*stride] = int32(clip(t7-t56, min, max))
	c[57*stride] = int32(clip(t6-t57a, min, max))
	c[58*stride] = int32(clip(t5-t58, min, max))
	c[59*stride] = int32(clip(t4-t59a, min, max))
	c[60*stride] = int32(clip(t3-t60, min, max))
	c[61*stride] = int32(clip(t2-t61a, min, max))
	c[62*stride] = int32(clip(t1-t62, min, max))
	c[63*stride] = int32(clip(t0-t63a, min, max))
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
