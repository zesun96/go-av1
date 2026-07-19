package bitstream

import "testing"

var benchmarkMSACSink uint64

func BenchmarkMSACBoolEqui4096(b *testing.B) {
	const samples = 4096
	enc := newMSACEncoder()
	var want uint64
	for i := 0; i < samples; i++ {
		value := uint32((i*13 + i/7) & 1)
		enc.encodeBoolEqui(value)
		want += uint64(value)
	}
	data := enc.done()
	verify := NewMSAC(data, true)
	var got uint64
	for i := 0; i < samples; i++ {
		got += uint64(verify.BoolEqui())
	}
	if got != want {
		b.Fatalf("verification sum = %d, want %d", got, want)
	}

	b.ReportAllocs()
	b.ResetTimer()
	var sink uint64
	for n := 0; n < b.N; n++ {
		dec := NewMSAC(data, true)
		var sum uint64
		for i := 0; i < samples; i++ {
			sum += uint64(dec.BoolEqui())
		}
		sink ^= sum
	}
	benchmarkMSACSink = sink
}

func BenchmarkMSACSymbol8x1024(b *testing.B) {
	const (
		symbols = 8
		samples = 1024
	)
	cdf := makeUniformDav1dCDF(symbols)
	icdf := dav1dToICDF(cdf, symbols)
	enc := newMSACEncoder()
	var want uint64
	for i := 0; i < samples; i++ {
		value := (i*5 + i/11) & (symbols - 1)
		enc.encodeCDFQ15(value, icdf, symbols)
		want += uint64(value)
	}
	data := enc.done()
	verify := NewMSAC(data, true)
	var got uint64
	for i := 0; i < samples; i++ {
		got += uint64(verify.Symbol(cdf, symbols))
	}
	if got != want {
		b.Fatalf("verification sum = %d, want %d", got, want)
	}

	b.ReportAllocs()
	b.ResetTimer()
	var sink uint64
	for n := 0; n < b.N; n++ {
		dec := NewMSAC(data, true)
		var sum uint64
		for i := 0; i < samples; i++ {
			sum += uint64(dec.Symbol(cdf, symbols))
		}
		sink ^= sum
	}
	benchmarkMSACSink = sink
}
