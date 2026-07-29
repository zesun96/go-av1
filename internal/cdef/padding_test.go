package cdef

import (
	"math/rand"
	"testing"
)

func TestPaddingMatchesReference(t *testing.T) {
	const stride = 16
	rng := rand.New(rand.NewSource(73))
	dst := make([]byte, stride*16)
	top := make([]byte, stride*16)
	bottom := make([]byte, stride*16)
	if _, err := rng.Read(dst); err != nil {
		t.Fatal(err)
	}
	if _, err := rng.Read(top); err != nil {
		t.Fatal(err)
	}
	if _, err := rng.Read(bottom); err != nil {
		t.Fatal(err)
	}

	for _, w := range []int{4, 8} {
		for _, h := range []int{4, 8} {
			columns := []int{0, 1, 2, stride - w - 1, stride - w}
			for _, column := range columns {
				left := make([][2]byte, h)
				for y := range left {
					left[y][0] = byte(rng.Intn(256))
					left[y][1] = byte(rng.Intn(256))
				}
				base := 4*stride + column
				for edgeBits := 0; edgeBits < 16; edgeBits++ {
					var got, want [144]int16
					for i := range got {
						got[i] = 777
						want[i] = 777
					}
					edges := EdgeFlags(edgeBits)
					padding(got[:], 2*tmpStride+2,
						dst, base, stride, left,
						top, base, stride, bottom, base, stride,
						w, h, edges)
					paddingReference(want[:], 2*tmpStride+2,
						dst, base, stride, left,
						top, base, stride, bottom, base, stride,
						w, h, edges)
					if got != want {
						for i := range got {
							if got[i] != want[i] {
								t.Fatalf("mismatch w=%d h=%d column=%d edges=%04b i=%d got=%d want=%d",
									w, h, column, edgeBits, i, got[i], want[i])
							}
						}
					}
				}
			}
		}
	}
}

func BenchmarkPadding8x8(b *testing.B) {
	benchmarkPadding8x8(b, padding)
}

func BenchmarkPadding8x8Reference(b *testing.B) {
	benchmarkPadding8x8(b, paddingReference)
}

func benchmarkPadding8x8(b *testing.B, fn func([]int16, int,
	[]uint8, int, int, [][2]uint8,
	[]uint8, int, int, []uint8, int, int,
	int, int, EdgeFlags)) {
	const stride = 16
	dst := make([]byte, stride*16)
	top := make([]byte, stride*16)
	bottom := make([]byte, stride*16)
	left := make([][2]byte, 8)
	for i := range dst {
		dst[i] = byte(i*29 + 17)
		top[i] = byte(i*31 + 11)
		bottom[i] = byte(i*37 + 5)
	}
	var tmp [144]int16
	base := 4*stride + 4
	b.ReportAllocs()
	b.SetBytes(12 * 12)
	for n := 0; n < b.N; n++ {
		fn(tmp[:], 2*tmpStride+2,
			dst, base, stride, left,
			top, base, stride, bottom, base, stride,
			8, 8, allEdges())
	}
}

func paddingReference(tmp []int16, tmpBase int,
	dst []uint8, dstBase, dstStride int,
	left [][2]uint8,
	top []uint8, topBase, topStride int,
	bottom []uint8, bottomBase, bottomStride int,
	w, h int, edges EdgeFlags) {

	xStart, xEnd := -2, w+2
	yStart, yEnd := -2, h+2

	if edges&HaveTop == 0 {
		fill(tmp, tmpBase-2-2*tmpStride, tmpStride, w+4, 2)
		yStart = 0
	}
	if edges&HaveBottom == 0 {
		fill(tmp, tmpBase+h*tmpStride-2, tmpStride, w+4, 2)
		yEnd -= 2
	}
	if edges&HaveLeft == 0 {
		fill(tmp, tmpBase+yStart*tmpStride-2, tmpStride, 2, yEnd-yStart)
		xStart = 0
	}
	if edges&HaveRight == 0 {
		fill(tmp, tmpBase+yStart*tmpStride+w, tmpStride, 2, yEnd-yStart)
		xEnd -= 2
	}

	tb := topBase
	for y := yStart; y < 0; y++ {
		rowStart := tb - tb%topStride
		rowEnd := rowStart + topStride
		for x := xStart; x < xEnd; x++ {
			xi := tb + x
			if xi < rowStart || xi >= rowEnd || xi < 0 || xi >= len(top) {
				xi = tb + iclip(x, 0, w-1)
			}
			tmp[tmpBase+x+y*tmpStride] = int16(top[xi])
		}
		tb += topStride
	}

	for y := 0; y < h; y++ {
		for x := xStart; x < 0; x++ {
			tmp[tmpBase+x+y*tmpStride] = int16(left[y][2+x])
		}
	}

	sb := dstBase
	tt := tmpBase
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			tmp[tt+x] = int16(dst[sb+x])
		}
		if xEnd > w {
			rowEnd := sb - sb%dstStride + dstStride
			for x := w; x < xEnd; x++ {
				xi := x
				if edges&HaveRight == 0 || sb+x >= rowEnd || sb+x >= len(dst) {
					xi = w - 1
				}
				tmp[tt+x] = int16(dst[sb+xi])
			}
		}
		sb += dstStride
		tt += tmpStride
	}

	bb := bottomBase
	tt = tmpBase + h*tmpStride
	for y := h; y < yEnd; y++ {
		rowStart := bb - bb%bottomStride
		rowEnd := rowStart + bottomStride
		for x := xStart; x < xEnd; x++ {
			xi := bb + x
			if xi < rowStart || xi >= rowEnd || xi < 0 || xi >= len(bottom) {
				xi = bb + iclip(x, 0, w-1)
			}
			tmp[tt+x] = int16(bottom[xi])
		}
		bb += bottomStride
		tt += tmpStride
	}
}
