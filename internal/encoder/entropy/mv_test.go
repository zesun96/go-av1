package entropy_test

import (
	"testing"

	"github.com/zesun96/go-av1/internal/bitstream"
	"github.com/zesun96/go-av1/internal/encoder/bitwriter"
	"github.com/zesun96/go-av1/internal/encoder/entropy"
	"github.com/zesun96/go-av1/internal/tile"
)

func TestEncodeMVResidualRoundTrip(t *testing.T) {
	vectors := [][2]int{
		{0, 0}, {1, 0}, {-1, 1}, {4, -2}, {8, -16},
		{31, 63}, {-127, 255}, {1024, -2047},
	}
	encCtx := tile.NewTileCtxForQIdx(120)
	ec := bitwriter.NewMSACEncoder(64)
	for _, mv := range vectors {
		entropy.EncodeMVResidual(ec, encCtx, mv[0], mv[1], true)
	}

	decCtx := tile.NewTileCtxForQIdx(120)
	m := bitstream.NewMSAC(ec.Flush(), false)
	for i, want := range vectors {
		joint := int(m.SymbolAdaptDav1d(decCtx.MVJointCDF[:], 3))
		got := [2]int{}
		if joint == 2 || joint == 3 {
			got[1] = decodeMVComponent(m, decCtx, 0, true)
		}
		if joint == 1 || joint == 3 {
			got[0] = decodeMVComponent(m, decCtx, 1, true)
		}
		if got != want {
			t.Fatalf("vector %d=%v, want %v", i, got, want)
		}
	}
}

func decodeMVComponent(m *bitstream.MSAC, ctx *tile.TileCtx, comp int, hpEnabled bool) int {
	sign := m.BoolAdapt(ctx.MVSignCDF[comp][:])
	class := int(m.SymbolAdaptDav1d(ctx.MVClassesCDF[comp][:], 10))
	up, fp, hp := 0, 3, 1
	if class == 0 {
		up = int(m.BoolAdapt(ctx.MVClass0CDF[comp][:]))
		fp = int(m.SymbolAdaptDav1d(ctx.MVClass0FPCDF[comp][up][:], 3))
		if hpEnabled {
			hp = int(m.BoolAdapt(ctx.MVClass0HPCDF[comp][:]))
		}
	} else {
		up = 1 << class
		for n := 0; n < class; n++ {
			up |= int(m.BoolAdapt(ctx.MVClassNCDF[comp][n][:])) << n
		}
		fp = int(m.SymbolAdaptDav1d(ctx.MVClassNFPCDF[comp][:], 3))
		if hpEnabled {
			hp = int(m.BoolAdapt(ctx.MVClassNHPCDF[comp][:]))
		}
	}
	diff := ((up << 3) | (fp << 1) | hp) + 1
	if sign != 0 {
		diff = -diff
	}
	return diff
}
