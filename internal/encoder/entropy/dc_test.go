package entropy_test

import (
	"fmt"
	"testing"

	"github.com/zesun96/go-av1/internal/bitstream"
	"github.com/zesun96/go-av1/internal/encoder/bitwriter"
	"github.com/zesun96/go-av1/internal/encoder/entropy"
	"github.com/zesun96/go-av1/internal/tile"
	"github.com/zesun96/go-av1/internal/transform"
)

func TestEncodeDCOnlyRoundTrip(t *testing.T) {
	for _, want := range []int{-29, -15, -3, -2, -1, 0, 1, 2, 3, 15, 29} {
		t.Run(fmt.Sprintf("level_%d", want), func(t *testing.T) {
			encCtx := tile.NewTileCtxForQIdx(120)
			encFS := tile.NewFrameState(64, 64)
			ec := bitwriter.NewMSACEncoder(16)
			entropy.EncodeDCOnly(ec, encCtx, encFS, transform.TX64x64, 0, 0, 0, 64, 64, want)

			decCtx := tile.NewTileCtxForQIdx(120)
			decFS := tile.NewFrameState(64, 64)
			m := bitstream.NewMSAC(ec.Flush(), false)
			td := transform.TxfmDimensions[transform.TX64x64]
			skipCtx := decFS.CoefSkipCtx(0, 0, 0, 64, 64, transform.TX64x64)
			skip := m.Bool(uint32(decCtx.CoefSkipFull[td.Ctx][skipCtx][0]))
			if want == 0 {
				if skip != 1 {
					t.Fatalf("skip=%d, want 1", skip)
				}
				return
			}
			if skip != 0 {
				t.Fatalf("skip=%d, want 0", skip)
			}
			if got := m.SymbolAdaptDav1d(decCtx.EobBin1024Full[0][:], 10); got != 0 {
				t.Fatalf("eob bin=%d", got)
			}
			base := int(m.SymbolAdaptDav1d(decCtx.EobBaseTokFull[td.Ctx][0][0][:], 2))
			mag := base + 1
			escape := false
			if base == 2 {
				mag = int(m.HiTok(decCtx.BrTokFull[3][0][0][:]))
				if mag == 15 {
					escape = true
				}
			}
			signCtx := decFS.DCSignCtx(0, 0, 0, transform.TX64x64)
			sign := m.Bool(uint32(decCtx.DCSignCDF[0][signCtx][0]))
			if escape {
				mag += int(decodeGolomb(m))
			}
			if sign != 0 {
				mag = -mag
			}
			if mag != want {
				t.Fatalf("decoded level=%d, want %d", mag, want)
			}
		})
	}
}

func decodeGolomb(m *bitstream.MSAC) uint32 {
	length := 0
	value := uint32(1)
	for m.BoolEqui() == 0 {
		length++
	}
	for ; length > 0; length-- {
		value = (value << 1) | m.BoolEqui()
	}
	return value - 1
}
