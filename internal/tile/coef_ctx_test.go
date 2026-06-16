package tile

import (
	"testing"

	"github.com/zesun96/go-av1/internal/transform"
)

func TestFrameStateCoefSkipCtxUsesMergedResidualLowBits(t *testing.T) {
	fs := NewFrameState(64, 64)
	bx, by := 0, 16

	// dav1d merges neighbour res_ctx bytes with bitwise OR before clamping the
	// low 6 bits into dav1d_skip_ctx[5][5].
	fs.AboveLCoef[0] = 0x41
	fs.AboveLCoef[1] = 0x44
	fs.LeftLCoef[by>>2] = 0x42

	got := fs.CoefSkipCtx(0, bx, by, 32, 16, transform.TX16x16)
	want := int(DAV1DSkipCtx[4][2])
	if got != want {
		t.Fatalf("CoefSkipCtx luma = %d, want %d", got, want)
	}
}

func TestFrameStateCoefSkipCtxSingleTransformBlockIsZero(t *testing.T) {
	fs := NewFrameState(64, 64)
	got := fs.CoefSkipCtx(0, 0, 0, 16, 16, transform.TX16x16)
	if got != 0 {
		t.Fatalf("CoefSkipCtx single tx block = %d, want 0", got)
	}
}

func TestFrameStateTxCtxUsesNeighbourTransformLogs(t *testing.T) {
	fs := NewFrameState(64, 64)

	fs.SetTxCtx(0, 0, 32, 16, transform.TX16x16, true, false)
	got := fs.TxCtx(0, 16, transform.TX16x16)
	if got != 1 {
		t.Fatalf("TxCtx top-only = %d, want 1", got)
	}

	fs.SetTxCtx(0, 16, 16, 32, transform.TX16x16, true, false)
	got = fs.TxCtx(16, 16, transform.TX16x16)
	if got != 2 {
		t.Fatalf("TxCtx left-only = %d, want 2", got)
	}
}
