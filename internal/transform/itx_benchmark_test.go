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

func checksumBytes(data []byte) uint64 {
	var sum uint64
	for i, value := range data {
		sum += uint64(value) * uint64(i+1)
	}
	return sum
}
