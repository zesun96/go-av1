//go:build amd64 && !purego

package cdef

import (
	"math/rand"
	"testing"
)

func TestFindDirSIMDCostsMatchReference(t *testing.T) {
	if !havePrimary8SSE41 {
		t.Skip("SSSE3/SSE4.1 unavailable")
	}
	rng := rand.New(rand.NewSource(101))
	for iteration := 0; iteration < 20_000; iteration++ {
		stride := 8 + rng.Intn(57)
		column := rng.Intn(stride - 7)
		img := make([]byte, stride*9)
		if _, err := rng.Read(img); err != nil {
			t.Fatal(err)
		}
		base := stride + column
		var got [8]uint32
		gotDir, gotVariance := findDirSSE41(&img[base], stride, &got)
		want := findDirReferenceCosts(img, base, stride)
		if got != want {
			t.Fatalf("iteration=%d stride=%d column=%d\ngot  %v\nwant %v",
				iteration, stride, column, got, want)
		}
		wantDir, wantVariance := findDirReference(img, base, stride)
		if gotDir != wantDir || gotVariance != wantVariance {
			t.Fatalf("iteration=%d result=(%d,%d), want=(%d,%d)",
				iteration, gotDir, gotVariance, wantDir, wantVariance)
		}
	}
}

func findDirReferenceCosts(img []byte, imgBase, stride int) (cost [8]uint32) {
	var partialSumHV [2][8]int
	var partialSumDiag [2][15]int
	var partialSumAlt [4][11]int
	ib := imgBase
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			px := int(img[ib+x]) - 128
			partialSumDiag[0][y+x] += px
			partialSumAlt[0][y+(x>>1)] += px
			partialSumHV[0][y] += px
			partialSumAlt[1][3+y-(x>>1)] += px
			partialSumDiag[1][7+y-x] += px
			partialSumAlt[2][3-(y>>1)+x] += px
			partialSumHV[1][x] += px
			partialSumAlt[3][(y>>1)+x] += px
		}
		ib += stride
	}
	for n := 0; n < 8; n++ {
		cost[2] += uint32(partialSumHV[0][n] * partialSumHV[0][n])
		cost[6] += uint32(partialSumHV[1][n] * partialSumHV[1][n])
	}
	cost[2] *= 105
	cost[6] *= 105
	divTable := [7]uint32{840, 420, 280, 210, 168, 140, 120}
	for n := 0; n < 7; n++ {
		cost[0] += uint32(partialSumDiag[0][n]*partialSumDiag[0][n]+
			partialSumDiag[0][14-n]*partialSumDiag[0][14-n]) * divTable[n]
		cost[4] += uint32(partialSumDiag[1][n]*partialSumDiag[1][n]+
			partialSumDiag[1][14-n]*partialSumDiag[1][14-n]) * divTable[n]
	}
	cost[0] += uint32(partialSumDiag[0][7]*partialSumDiag[0][7]) * 105
	cost[4] += uint32(partialSumDiag[1][7]*partialSumDiag[1][7]) * 105
	for n := 0; n < 4; n++ {
		cp := &cost[n*2+1]
		for m := 0; m < 5; m++ {
			*cp += uint32(partialSumAlt[n][3+m] * partialSumAlt[n][3+m])
		}
		*cp *= 105
		for m := 0; m < 3; m++ {
			*cp += uint32(partialSumAlt[n][m]*partialSumAlt[n][m]+
				partialSumAlt[n][10-m]*partialSumAlt[n][10-m]) * divTable[2*m+1]
		}
	}
	return cost
}
