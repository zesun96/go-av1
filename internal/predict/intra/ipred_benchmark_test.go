package intra

import "testing"

var benchmarkIntraSink uint8

func BenchmarkIntraPrediction32x32(b *testing.B) {
	const width, height = 32, 32
	edge, tl := makeEdge(width, height, 127,
		func(i int) uint8 { return uint8(32 + (i*7)%192) },
		func(i int) uint8 { return uint8(224 - (i*5)%192) })
	dst := make([]uint8, width*height)
	for _, tc := range []struct {
		name string
		fn   func()
	}{
		{name: "DC", fn: func() { PredDC(dst, width, edge, tl, width, height) }},
		{name: "Paeth", fn: func() { PredPaeth(dst, width, edge, tl, width, height) }},
		{name: "Smooth", fn: func() { PredSmooth(dst, width, edge, tl, width, height) }},
	} {
		b.Run(tc.name, func(b *testing.B) {
			tc.fn()
			if checksumIntra(dst) == 0 {
				b.Fatal("verification output is zero")
			}
			b.ReportAllocs()
			b.SetBytes(width * height)
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				tc.fn()
			}
			benchmarkIntraSink = dst[b.N%len(dst)]
		})
	}
}

func checksumIntra(data []uint8) uint64 {
	var sum uint64
	for i, value := range data {
		sum += uint64(value) * uint64(i+1)
	}
	return sum
}
