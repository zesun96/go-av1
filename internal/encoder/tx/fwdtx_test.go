package tx_test

import (
	"testing"

	encodertx "github.com/zesun96/go-av1/internal/encoder/tx"
	"github.com/zesun96/go-av1/internal/transform"
)

func TestFwdDCT8x8MatchesInverseScale(t *testing.T) {
	var src [64]int16
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			src[y*8+x] = int16((x-3)*7 + (y-3)*5)
		}
	}
	var rowMajor [64]int32
	encodertx.FwdDCT8x8(rowMajor[:], src[:], 8)
	coeff := make([]int32, 64)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			coeff[x*8+y] = rowMajor[y*8+x]
		}
	}
	dst := make([]byte, 64)
	for i := range dst {
		dst[i] = 128
	}
	transform.InvTxfmAdd(dst, 8, coeff, 63, transform.TX8x8, 1, transform.DCT_DCT, 8)
	assertResidualClose(t, dst, src[:], 2)
}

func TestFwdDCT16x16MatchesInverseScale(t *testing.T) {
	var src [256]int16
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			src[y*16+x] = int16((x-7)*3 + (y-7)*2)
		}
	}
	var rowMajor [256]int32
	encodertx.FwdDCT16x16(rowMajor[:], src[:], 16)
	coeff := make([]int32, 256)
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			coeff[x*16+y] = rowMajor[y*16+x]
		}
	}
	dst := make([]byte, 256)
	for i := range dst {
		dst[i] = 128
	}
	transform.InvTxfmAdd(dst, 16, coeff, 255, transform.TX16x16, 2, transform.DCT_DCT, 8)
	assertResidualClose(t, dst, src[:], 3)
}

func TestFwdDCT4x4MatchesInverseScale(t *testing.T) {
	var src [16]int16
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			src[y*4+x] = int16((x-1)*11 + (y-1)*9)
		}
	}
	var rowMajor [16]int32
	encodertx.FwdDCT4x4(rowMajor[:], src[:], 4)
	coeff := make([]int32, 16)
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			coeff[x*4+y] = rowMajor[y*4+x]
		}
	}
	dst := make([]byte, 16)
	for i := range dst {
		dst[i] = 128
	}
	transform.InvTxfmAdd(dst, 4, coeff, 15, transform.TX4x4, 0, transform.DCT_DCT, 8)
	assertResidualClose(t, dst, src[:], 2)
}

func assertResidualClose(t *testing.T, got []byte, want []int16, tolerance int) {
	t.Helper()
	maxErr := 0
	for i, wantResidual := range want {
		err := absInt(int(got[i]) - 128 - int(wantResidual))
		if err > maxErr {
			maxErr = err
		}
	}
	if maxErr > tolerance {
		t.Fatalf("forward/inverse transform max error=%d, want <=%d", maxErr, tolerance)
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
