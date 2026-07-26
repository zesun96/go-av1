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

func TestEncodeDCT8DCAC1RoundTrip(t *testing.T) {
	cases := []struct {
		dc int
		ac int
	}{
		{0, 1},
		{1, -1},
		{-2, 2},
		{3, -3},
		{-15, 15},
		{29, -29},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("dc_%d_ac_%d", tc.dc, tc.ac), func(t *testing.T) {
			encCtx := tile.NewTileCtxForQIdx(120)
			encFS := tile.NewFrameState(64, 64)
			ec := bitwriter.NewMSACEncoder(32)
			entropy.EncodeDCT8DCAC1(ec, encCtx, encFS, 0, 0, 0, tc.dc, tc.ac)

			decCtx := tile.NewTileCtxForQIdx(120)
			decFS := tile.NewFrameState(64, 64)
			m := bitstream.NewMSAC(ec.Flush(), false)
			td := transform.TxfmDimensions[transform.TX8x8]
			skipCtx := decFS.CoefSkipCtx(0, 0, 0, 8, 8, transform.TX8x8)
			if got := m.Bool(uint32(decCtx.CoefSkipFull[td.Ctx][skipCtx][0])); got != 0 {
				t.Fatalf("skip=%d, want 0", got)
			}
			if got := m.SymbolAdaptDav1d(decCtx.TxTypeIntra2CDF[int(td.Min)][tile.DCPred][:], 4); got != 1 {
				t.Fatalf("tx type=%d, want DCT_DCT symbol 1", got)
			}
			if got := m.SymbolAdaptDav1d(decCtx.EobBin64Full[0][0][:], 6); got != 1 {
				t.Fatalf("eob bin=%d, want 1", got)
			}

			acBase := int(m.SymbolAdaptDav1d(decCtx.EobBaseTokFull[td.Ctx][0][1][:], 2))
			acMag, acEscape := acBase+1, false
			if acBase == 2 {
				acMag = int(m.HiTok(decCtx.BrTokFull[td.Ctx][0][7][:]))
				acEscape = acMag == 15
			}

			dcBase := int(m.SymbolAdaptDav1d(decCtx.BaseTokFull[td.Ctx][0][0][:], 3))
			dcMag, dcEscape := dcBase, false
			if dcBase == 3 {
				acToken := acMag
				if acToken > 15 {
					acToken = 15
				}
				hctx := 6
				if acToken <= 12 {
					hctx = (acToken + 1) >> 1
				}
				dcMag = int(m.HiTok(decCtx.BrTokFull[td.Ctx][0][hctx][:]))
				dcEscape = dcMag == 15
			}

			if dcMag != 0 {
				signCtx := decFS.DCSignCtx(0, 0, 0, transform.TX8x8)
				sign := m.Bool(uint32(decCtx.DCSignCDF[0][signCtx][0]))
				if dcEscape {
					dcMag += int(decodeGolomb(m))
				}
				if sign != 0 {
					dcMag = -dcMag
				}
			}
			acSign := m.BoolEqui()
			if acEscape {
				acMag += int(decodeGolomb(m))
			}
			if acSign != 0 {
				acMag = -acMag
			}
			if dcMag != tc.dc || acMag != tc.ac {
				t.Fatalf("decoded=(%d,%d), want=(%d,%d)", dcMag, acMag, tc.dc, tc.ac)
			}
		})
	}
}
