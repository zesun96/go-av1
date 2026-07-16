package tile

import "testing"

func TestPrepareIntraPredictionClippedBottomTransform(t *testing.T) {
	const (
		planeW = 1920
		planeH = 1080
		bx     = 816
		by     = 1056
		bw     = 16
		bh     = 32
	)

	plane := make([]byte, planeW*planeH)
	for y := by; y < planeH; y++ {
		plane[y*planeW+bx-1] = byte(y - by + 1)
	}

	// The visible coding block is only 24 pixels high, but Z3 prediction still
	// needs two complete 32-pixel left-edge spans for the transform block.
	tlBuf, tl := newIntraEdgeBuffer(bw, planeH-by, bw, bh)
	if tl != 2*bh || len(tlBuf) != 4*bh+2 {
		t.Fatalf("edge buffer layout = (len %d, tl %d), want (len %d, tl %d)", len(tlBuf), tl, 4*bh+2, 2*bh)
	}
	mode, _ := prepareIntraPrediction(
		plane, planeW, planeW, planeH,
		bx, by, bw, bh,
		tlBuf, tl,
		HorUpPred, 0, -1,
		false, 0, true, true,
	)
	if mode != intraPredZ3 {
		t.Fatalf("prediction mode = %d, want Z3", mode)
	}

	want := byte(planeH - by)
	for i := planeH - by; i < 2*bh; i++ {
		if got := tlBuf[tl-1-i]; got != want {
			t.Fatalf("extended left edge at %d = %d, want %d", i, got, want)
		}
	}
}
