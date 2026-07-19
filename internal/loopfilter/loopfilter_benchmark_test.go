package loopfilter

import "testing"

var benchmarkLoopFilterSink uint8

func BenchmarkFilterEdge(b *testing.B) {
	const stride = 32
	template := make([]uint8, stride*20)
	for i := range template {
		template[i] = uint8(96 + (i*13)%48)
	}
	base := 8*stride + 8
	lut := NewFilterLUT(2)
	for _, tc := range []struct {
		name string
		fn   func([]uint8)
	}{
		{name: "Horizontal8", fn: func(dst []uint8) { FilterEdgeH(dst, base, stride, 32, 8, &lut) }},
		{name: "Vertical8", fn: func(dst []uint8) { FilterEdgeV(dst, base, stride, 32, 8, &lut) }},
	} {
		b.Run(tc.name, func(b *testing.B) {
			dst := make([]uint8, len(template))
			copy(dst, template)
			tc.fn(dst)
			if dst[base] == 0 {
				b.Fatal("verification output is zero")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				copy(dst, template)
				tc.fn(dst)
			}
			benchmarkLoopFilterSink = dst[base]
		})
	}
}
