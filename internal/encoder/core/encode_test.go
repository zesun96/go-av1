package core

import (
	"testing"

	"github.com/zesun96/go-av1/internal/tile"
)

func TestChooseLumaMode(t *testing.T) {
	const width, height = 16, 16
	fe := &FrameEncoder{Width: width, Height: height}

	t.Run("vertical", func(t *testing.T) {
		st := testTileState(width, height)
		for x := 0; x < 8; x++ {
			value := byte(16 + x*29)
			st.recon[0][7*width+x] = value
			for y := 8; y < 16; y++ {
				st.src[0][y*width+x] = value
			}
		}
		if got := fe.chooseLumaMode(st, 0, 8, 8); got != tile.VertPred {
			t.Fatalf("mode=%d, want VertPred", got)
		}
	})

	t.Run("horizontal", func(t *testing.T) {
		st := testTileState(width, height)
		for y := 0; y < 8; y++ {
			value := byte(16 + y*29)
			st.recon[0][y*width+7] = value
			for x := 8; x < 16; x++ {
				st.src[0][y*width+x] = value
			}
		}
		if got := fe.chooseLumaMode(st, 8, 0, 8); got != tile.HorPred {
			t.Fatalf("mode=%d, want HorPred", got)
		}
	})

	t.Run("dc_without_neighbours", func(t *testing.T) {
		st := testTileState(width, height)
		if got := fe.chooseLumaMode(st, 0, 0, 8); got != tile.DCPred {
			t.Fatalf("mode=%d, want DCPred", got)
		}
	})

	for _, tc := range []struct {
		name string
		mode int
	}{
		{"smooth", tile.SmoothPred},
		{"paeth", tile.PaethPred},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := testTileState(width, height)
			for x := 0; x < 8; x++ {
				st.recon[0][7*width+8+x] = byte(20 + x*27)
			}
			for y := 0; y < 8; y++ {
				st.recon[0][(8+y)*width+7] = byte(230 - y*23)
			}
			st.recon[0][7*width+7] = 117
			want := intraPredBlock(st.recon[0], width, height, 8, 8, 8, 8, tc.mode)
			for y := 0; y < 8; y++ {
				copy(st.src[0][(8+y)*width+8:(8+y)*width+16], want[y*8:y*8+8])
			}
			if got := fe.chooseLumaMode(st, 8, 8, 8); got != tc.mode {
				t.Fatalf("mode=%d, want %d", got, tc.mode)
			}
		})
	}
}

func testTileState(width, height int) *tileEncodeState {
	st := &tileEncodeState{
		w: [3]int{width, width / 2, width / 2},
		h: [3]int{height, height / 2, height / 2},
	}
	for plane := 0; plane < 3; plane++ {
		st.src[plane] = make([]byte, st.w[plane]*st.h[plane])
		st.recon[plane] = make([]byte, st.w[plane]*st.h[plane])
		for i := range st.src[plane] {
			st.src[plane][i] = 128
			st.recon[plane][i] = 128
		}
	}
	return st
}
