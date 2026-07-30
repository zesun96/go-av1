package tile

import (
	"fmt"
	"testing"
)

func BenchmarkFillUint8(b *testing.B) {
	for _, size := range []int{16, 256, 4096, 256 * 1024} {
		b.Run(fmt.Sprintf("Size%d", size), func(b *testing.B) {
			values := make([]uint8, size)
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				fillUint8(values, uint8(i))
			}
		})
	}
}

func BenchmarkSetTxState(b *testing.B) {
	for _, size := range []int{4, 8, 16, 64} {
		b.Run(fmt.Sprintf("%dx%d", size, size), func(b *testing.B) {
			fs := NewFrameState(2560, 1392)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				fs.SetTxState(128, 128, size, size, uint8(i)&txSizeMask)
			}
		})
	}
}
