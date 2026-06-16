package tile

import "testing"

func TestPalIdxFinishPadsEdges(t *testing.T) {
	idx := []uint8{
		0, 1, 2, 0,
		1, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
	}
	palIdxFinish(idx, 4, 4, 3, 2)
	want := []uint8{
		0, 1, 2, 2,
		1, 0, 0, 0,
		1, 0, 0, 0,
		1, 0, 0, 0,
	}
	for i := range want {
		if idx[i] != want[i] {
			t.Fatalf("idx[%d]=%d want %d", i, idx[i], want[i])
		}
	}
}

func TestOrderPalette(t *testing.T) {
	// 2x2 block with decoded neighbours:
	// top row    : [1 2]
	// left col   : [1 3]
	// top-left   : 0
	idx := make([]uint8, 16)
	stride := 4
	idx[0] = 1
	idx[1] = 2
	idx[stride] = 3

	var order [64][8]uint8
	var ctx [64]uint8
	orderPalette(idx, stride, 2, 1, 0, &order, &ctx)

	if ctx[0] != 1 {
		t.Fatalf("ctx[0]=%d want 1", ctx[0])
	}
	if got := order[0][:3]; got[0] != 2 || got[1] != 3 || got[2] != 1 {
		t.Fatalf("order[0][:3]=%v want [2 3 1]", got)
	}
}

func TestPredictPalette(t *testing.T) {
	pal := [8]uint8{10, 20, 30}
	idx := []uint8{
		0, 1,
		2, 1,
	}
	dst := make([]byte, 4)
	predictPalette(dst, 2, pal, idx, 2, 2, 2)
	want := []byte{10, 20, 30, 20}
	for i := range want {
		if dst[i] != want[i] {
			t.Fatalf("dst[%d]=%d want %d", i, dst[i], want[i])
		}
	}
}
