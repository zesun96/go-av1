package inter

import "testing"

var benchmarkMCSink uint8

func BenchmarkPut8Tap64x64(b *testing.B) {
	const width, height = 64, 64
	src, srcStride, srcBase := makeRampSrc(width, height)
	dst := make([]uint8, width*height)
	Put8Tap(dst, width, src, srcBase, srcStride, width, height, 5, 11, Filter2D8TapRegular)
	if checksumMC(dst) == 0 {
		b.Fatal("verification output is zero")
	}

	b.ReportAllocs()
	b.SetBytes(width * height)
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		Put8Tap(dst, width, src, srcBase, srcStride, width, height, 5, 11, Filter2D8TapRegular)
	}
	benchmarkMCSink = dst[b.N%len(dst)]
}

func BenchmarkCompoundBlend64x64(b *testing.B) {
	const width, height = 64, 64
	tmp1 := make([]int16, width*height)
	tmp2 := make([]int16, width*height)
	for i := range tmp1 {
		tmp1[i] = int16((i*17)%256) << intermediateBits
		tmp2[i] = int16((255 - (i*29)%256)) << intermediateBits
	}
	dst := make([]uint8, width*height)
	for _, tc := range []struct {
		name string
		fn   func()
	}{
		{name: "Avg", fn: func() { Avg(dst, width, tmp1, tmp2, width, height) }},
		{name: "WAvg", fn: func() { WAvg(dst, width, tmp1, tmp2, width, height, 11) }},
	} {
		b.Run(tc.name, func(b *testing.B) {
			tc.fn()
			if checksumMC(dst) == 0 {
				b.Fatal("verification output is zero")
			}
			b.ReportAllocs()
			b.SetBytes(width * height)
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				tc.fn()
			}
			benchmarkMCSink = dst[b.N%len(dst)]
		})
	}
}

func checksumMC(data []uint8) uint64 {
	var sum uint64
	for i, value := range data {
		sum += uint64(value) * uint64(i+1)
	}
	return sum
}
