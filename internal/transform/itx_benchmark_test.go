package transform

import "testing"

var benchmarkTransformSink uint8

func BenchmarkInvTxfmAdd(b *testing.B) {
	cases := []struct {
		name  string
		size  uint8
		width int
		shift int
	}{
		{name: "4x4", size: TX4x4, width: 4, shift: 0},
		{name: "8x8", size: TX8x8, width: 8, shift: 1},
		{name: "16x16", size: TX16x16, width: 16, shift: 2},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			count := tc.width * tc.width
			template := make([]int32, count)
			for i := range template {
				template[i] = int32((i*37)%257 - 128)
			}
			coeff := make([]int32, count)
			dst := make([]uint8, count)
			copy(coeff, template)
			InvTxfmAdd(dst, tc.width, coeff, count-1, tc.size, tc.shift, DCT_DCT, 8)
			if checksumBytes(dst) == 0 {
				b.Fatal("verification output is zero")
			}

			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				copy(coeff, template)
				InvTxfmAdd(dst, tc.width, coeff, count-1, tc.size, tc.shift, DCT_DCT, 8)
			}
			benchmarkTransformSink = dst[b.N%len(dst)]
		})
	}
}

func BenchmarkInvTxfmAddDCT4x4Generic(b *testing.B) {
	template := make([]int32, 16)
	for i := range template {
		template[i] = int32((i*37)%257 - 128)
	}
	coeff := make([]int32, 16)
	dst := make([]uint8, 16)
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		copy(coeff, template)
		invTxfmAddGeneric(dst, 4, coeff, 15, TX4x4, 0, DCT_DCT, -1, 8)
	}
	benchmarkTransformSink = dst[b.N%len(dst)]
}

func BenchmarkInvTxfmAddDCT8x8Generic(b *testing.B) {
	template := make([]int32, 64)
	for i := range template {
		template[i] = int32((i*37)%257 - 128)
	}
	coeff := make([]int32, 64)
	dst := make([]uint8, 64)
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		copy(coeff, template)
		invTxfmAddGeneric(dst, 8, coeff, 63, TX8x8, 1, DCT_DCT, -1, 8)
	}
	benchmarkTransformSink = dst[b.N%len(dst)]
}

func checksumBytes(data []byte) uint64 {
	var sum uint64
	for i, value := range data {
		sum += uint64(value) * uint64(i+1)
	}
	return sum
}

func BenchmarkInvDCT1D(b *testing.B) {
	for _, tc := range []struct {
		name   string
		size   int
		stride int
		fn     func([]int32, int, int, int)
	}{
		{name: "DCT8/Stride1", size: 8, stride: 1, fn: InvDCT8},
		{name: "DCT8/Stride8", size: 8, stride: 8, fn: InvDCT8},
		{name: "DCT16/Stride1", size: 16, stride: 1, fn: InvDCT16},
		{name: "DCT16/Stride16", size: 16, stride: 16, fn: InvDCT16},
	} {
		b.Run(tc.name, func(b *testing.B) {
			values := make([]int32, tc.size*tc.stride)
			for i := 0; i < tc.size; i++ {
				values[i*tc.stride] = int32(i*97 - 311)
			}
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				tc.fn(values, tc.stride, itxMin, itxMax)
			}
			benchmarkTransformSink = byte(values[(b.N%tc.size)*tc.stride])
		})
	}
}
