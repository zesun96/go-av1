package cdef

import "testing"

var benchmarkCDEFSink uint8

func BenchmarkFilterBlock8x8(b *testing.B) {
	const width, height = 8, 8
	template := make([]uint8, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			template[y*width+x] = uint8(96 + x*5 + y*3)
		}
	}
	dst := make([]uint8, len(template))
	top, bottom := makeTopBottom(112, width)
	left := makeLeft(108, height)
	run := func() {
		FilterBlock(dst, 0, width, left, top, 0, width, bottom, 0, width, 8, 4, 2, 3, width, height, allEdges())
	}
	copy(dst, template)
	run()
	if checksumCDEF(dst) == 0 {
		b.Fatal("verification output is zero")
	}

	b.ReportAllocs()
	b.SetBytes(width * height)
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		copy(dst, template)
		run()
	}
	benchmarkCDEFSink = dst[b.N%len(dst)]
}

func checksumCDEF(data []uint8) uint64 {
	var sum uint64
	for i, value := range data {
		sum += uint64(value) * uint64(i+1)
	}
	return sum
}
