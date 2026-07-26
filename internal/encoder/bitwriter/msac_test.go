package bitwriter_test

import (
	"math/rand"
	"testing"

	"github.com/zesun96/go-av1/internal/bitstream"
	"github.com/zesun96/go-av1/internal/encoder/bitwriter"
)

func TestMSACEncoderRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	const count = 2048
	values := make([]uint32, count)
	enc := bitwriter.NewMSACEncoder(256)
	for i := range values {
		values[i] = uint32(r.Intn(2))
		enc.BoolEqui(values[i])
	}

	dec := bitstream.NewMSAC(enc.Flush(), true)
	for i, want := range values {
		if got := dec.BoolEqui(); got != want {
			t.Fatalf("bit %d = %d, want %d", i, got, want)
		}
	}
}

func TestMSACEncoderSymbolRoundTrip(t *testing.T) {
	cdf := []uint16{24576, 16384, 8192, 0, 0}
	values := []uint32{0, 3, 2, 1, 0, 0, 3, 1, 2, 3}
	enc := bitwriter.NewMSACEncoder(16)
	for _, value := range values {
		enc.Symbol(value, cdf, 4)
	}

	dec := bitstream.NewMSAC(enc.Flush(), true)
	for i, want := range values {
		if got := dec.SymbolAdaptDav1d(cdf, 3); got != want {
			t.Fatalf("symbol %d = %d, want %d", i, got, want)
		}
	}
}

func TestMSACEncoderAdaptiveSymbolRoundTrip(t *testing.T) {
	initial := []uint16{24576, 16384, 8192, 0}
	encCDF := append([]uint16(nil), initial...)
	values := []uint32{0, 3, 2, 1, 0, 0, 3, 1, 2, 3, 3, 0, 1, 2, 2, 2}
	enc := bitwriter.NewMSACEncoder(32)
	for _, value := range values {
		enc.SymbolAdaptDav1d(value, encCDF, 3)
	}

	decCDF := append([]uint16(nil), initial...)
	dec := bitstream.NewMSAC(enc.Flush(), false)
	for i, want := range values {
		if got := dec.SymbolAdaptDav1d(decCDF, 3); got != want {
			t.Fatalf("symbol %d = %d, want %d", i, got, want)
		}
	}
	for i := range encCDF {
		if encCDF[i] != decCDF[i] {
			t.Fatalf("cdf[%d]=%d, decoder=%d", i, encCDF[i], decCDF[i])
		}
	}
}
