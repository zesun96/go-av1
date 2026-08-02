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

func BenchmarkFilterBlock8x8FromSource(b *testing.B) {
	benchmarkFilterBlock8x8Source(b, func(dst, src []byte, base, stride int) {
		FilterBlock8x8FromSource(dst, base, stride, src, base, stride, 8, 4, 2, 3)
	})
}

func BenchmarkFilterBlock8x8FromSourceScratch(b *testing.B) {
	benchmarkFilterBlock8x8Source(b, func(dst, src []byte, base, stride int) {
		var tmp [144]int16
		paddingContiguous8x8(&tmp, src, base, stride)
		filterBlockPrepared(dst, base, stride, &tmp, 2*tmpStride+2, 8, 4, 2, 3, 8, 8)
	})
}

func benchmarkFilterBlock8x8Source(b *testing.B, fn func(dst, src []byte, base, stride int)) {
	const stride = 32
	src := make([]byte, stride*20)
	dst := make([]byte, len(src))
	for i := range src {
		src[i] = byte(i*29 + i/stride*17 + 11)
	}
	copy(dst, src)
	base := 6*stride + 8
	fn(dst, src, base, stride)
	b.ReportAllocs()
	b.SetBytes(64)
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		fn(dst, src, base, stride)
	}
	benchmarkCDEFSink = dst[base]
}

func checksumCDEF(data []uint8) uint64 {
	var sum uint64
	for i, value := range data {
		sum += uint64(value) * uint64(i+1)
	}
	return sum
}

func BenchmarkFindDir8x8(b *testing.B) {
	benchmarkFindDir8x8(b, FindDir)
}

func BenchmarkFindDir8x8Scalar(b *testing.B) {
	benchmarkFindDir8x8(b, findDirScalar)
}

func benchmarkFindDir8x8(b *testing.B, findDir func([]byte, int, int) (int, uint)) {
	const stride = 16
	img := make([]byte, stride*8)
	for i := range img {
		img[i] = byte(i*37 + i/stride*19 + 11)
	}
	b.ReportAllocs()
	b.SetBytes(64)
	var dir int
	var variance uint
	for n := 0; n < b.N; n++ {
		dir, variance = findDir(img, 3, stride)
	}
	benchmarkCDEFSink = byte(dir) ^ byte(variance)
}
