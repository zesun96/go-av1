package tile

import (
	"math/rand"
	"testing"
)

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

func TestOrderPaletteMatchesReferenceRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(103))
	for iteration := 0; iteration < 1_000; iteration++ {
		stride := 1 + rng.Intn(64)
		w := 1 + rng.Intn(stride)
		h := 1 + rng.Intn(64)
		idx := make([]uint8, stride*h)
		for i := range idx {
			idx[i] = uint8(rng.Intn(8))
		}
		for diagonal := 1; diagonal < w+h-1; diagonal++ {
			first := minInt(diagonal, w-1)
			last := maxInt(0, diagonal-h+1)
			var gotOrder, wantOrder [64][8]uint8
			var gotCtx, wantCtx [64]uint8
			orderPalette(idx, stride, diagonal, first, last, &gotOrder, &gotCtx)
			orderPaletteReference(idx, stride, diagonal, first, last, &wantOrder, &wantCtx)
			count := first - last + 1
			for n := 0; n < count; n++ {
				if gotCtx[n] != wantCtx[n] || gotOrder[n] != wantOrder[n] {
					t.Fatalf("iteration=%d diagonal=%d entry=%d got=(%d,%v) want=(%d,%v)",
						iteration, diagonal, n,
						gotCtx[n], gotOrder[n], wantCtx[n], wantOrder[n])
				}
			}
		}
	}
}

func orderPaletteReference(palIdx []uint8, stride, i, first, last int, order *[64][8]uint8, ctx *[64]uint8) {
	haveTop := i > first
	base := first + (i-first)*stride
	for j, n, off := first, 0, base; j >= last; j, n, off = j-1, n+1, off+stride-1 {
		haveLeft := j > 0
		mask := uint16(0)
		oIdx := 0
		add := func(v uint8) {
			order[n][oIdx] = v
			oIdx++
			mask |= 1 << v
		}
		if !haveLeft {
			ctx[n] = 0
			add(palIdx[off-stride])
		} else if !haveTop {
			ctx[n] = 0
			add(palIdx[off-1])
		} else {
			l := palIdx[off-1]
			top := palIdx[off-stride]
			topLeft := palIdx[off-stride-1]
			sameTopLeft := top == l
			sameTopTopLeft := top == topLeft
			sameLeftTopLeft := l == topLeft
			switch {
			case sameTopLeft && sameTopTopLeft && sameLeftTopLeft:
				ctx[n] = 4
				add(top)
			case sameTopLeft:
				ctx[n] = 3
				add(top)
				add(topLeft)
			case sameTopTopLeft || sameLeftTopLeft:
				ctx[n] = 2
				add(topLeft)
				if sameTopTopLeft {
					add(l)
				} else {
					add(top)
				}
			default:
				ctx[n] = 1
				if top < l {
					add(top)
					add(l)
				} else {
					add(l)
					add(top)
				}
				add(topLeft)
			}
		}
		for color := uint8(0); color < 8; color++ {
			if mask&(1<<color) == 0 {
				order[n][oIdx] = color
				oIdx++
			}
		}
		haveTop = true
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

func BenchmarkOrderPalette64x64(b *testing.B) {
	const size = 64
	idx := make([]uint8, size*size)
	for i := range idx {
		idx[i] = uint8((i*5 + i/size*3) & 7)
	}
	var order [64][8]uint8
	var ctx [64]uint8
	b.ReportAllocs()
	b.SetBytes(size * size)
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		for i := 1; i < size*2-1; i++ {
			first := minInt(i, size-1)
			last := maxInt(0, i-size+1)
			orderPalette(idx, size, i, first, last, &order, &ctx)
		}
	}
}
