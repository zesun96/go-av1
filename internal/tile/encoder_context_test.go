package tile

import (
	"bytes"
	"testing"
)

func TestSingleRefEncoderContextsTrackCommittedGlobalNeighbour(t *testing.T) {
	fs := NewFrameState(64, 64)
	EnableEncoderMVContexts(fs, 64, 64)
	first := SingleRefEncoderContexts(fs, 0, 0, 8, 8)
	if first.NewMV != 0 || first.GlobalMV != 0 {
		t.Fatalf("first contexts=%+v", first)
	}
	bl, bs := EncoderBlockGeometry(8, 8)
	fs.CommitInterBlock(0, 0, 8, 8, Av1Block{
		Bl: bl, Bs: bs, InterMode: InterModeGlobalMV,
		RefSlot: 0, RefFrame: 1,
		Tx: 1, MaxYTx: 1,
	}, 1, true)
	next := SingleRefEncoderContexts(fs, 8, 0, 8, 8)
	if next.NewMV != 3 || next.GlobalMV != 0 {
		t.Fatalf("next contexts=%+v, want NewMV=3 GlobalMV=0", next)
	}
}

func TestEncoderOBMCPredictionMatchesNormativeTopBlend(t *testing.T) {
	const width, height = 16, 16
	fs := NewFrameState(width, height)
	EnableEncoderMVContexts(fs, width, height)
	bl, bs := EncoderBlockGeometry(8, 8)
	fs.CommitInterBlock(0, 0, 8, 8, Av1Block{
		Bl: bl, Bs: bs, InterMode: InterModeGlobalMV,
		RefSlot: 0, RefFrame: 1,
		Tx: 1, MaxYTx: 1,
	}, 1, true)

	present, gotBS := EncoderOBMCContext(fs, 0, 8, 8, 8, 0)
	if !present || gotBS != int(bs) {
		t.Fatalf("context present=%t bs=%d, want true/%d", present, gotBS, bs)
	}
	var refs [8][3][]byte
	refs[0][0] = bytes.Repeat([]byte{200}, width*height)
	pred := EncoderOBMCPrediction(
		bytes.Repeat([]byte{100}, 64), fs, refs, width, height, 0, 8, 8, 8)
	for row, want := range []byte{139, 122, 108, 100} {
		for x := 0; x < 8; x++ {
			if got := pred[row*8+x]; got != want {
				t.Fatalf("prediction (%d,%d)=%d, want %d", x, row, got, want)
			}
		}
	}
}
