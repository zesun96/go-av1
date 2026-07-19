package looprestoration

import "testing"

var benchmarkRestorationSink uint8

func BenchmarkLoopRestoration64x4(b *testing.B) {
	const width, height = 64, 4
	template := make([]uint8, width*height)
	for i := range template {
		template[i] = uint8(48 + (i*19)%160)
	}
	left := makeLeft(112, height)
	lpf, lpfBase, lpfStride := makeLpf(120, width)
	// Horizontal uses its implicit 128 identity term; vertical stores the
	// complete center tap explicitly in the current kernel representation.
	wiener := &WienerParams{Filter: [2][8]int16{{}, {0, 0, 0, 128}}}
	sgr := makeSGRParams3x3(140, 1024)
	for _, tc := range []struct {
		name string
		fn   func([]uint8)
	}{
		{name: "Wiener", fn: func(dst []uint8) {
			WienerFilter(dst, 0, width, left, lpf, lpfBase, lpfStride, width, height, wiener, allLrEdges())
		}},
		{name: "SGR3x3", fn: func(dst []uint8) {
			SGR3x3(dst, 0, width, left, lpf, lpfBase, lpfStride, width, height, sgr, allLrEdges())
		}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			dst := make([]uint8, len(template))
			copy(dst, template)
			tc.fn(dst)
			if checksumRestoration(dst) == 0 {
				b.Fatal("verification output is zero")
			}
			b.ReportAllocs()
			b.SetBytes(width * height)
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				copy(dst, template)
				tc.fn(dst)
			}
			benchmarkRestorationSink = dst[b.N%len(dst)]
		})
	}
}

func checksumRestoration(data []uint8) uint64 {
	var sum uint64
	for i, value := range data {
		sum += uint64(value) * uint64(i+1)
	}
	return sum
}
