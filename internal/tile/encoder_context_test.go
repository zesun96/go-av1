package tile

import "testing"

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
