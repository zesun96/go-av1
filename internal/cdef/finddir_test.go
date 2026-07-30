package cdef

import (
	"math/rand"
	"testing"
)

func TestFindDirMatchesReferenceRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(101))
	for iteration := 0; iteration < 20_000; iteration++ {
		stride := 8 + rng.Intn(57)
		column := rng.Intn(stride - 7)
		img := make([]byte, stride*9)
		if _, err := rng.Read(img); err != nil {
			t.Fatal(err)
		}
		base := stride + column
		gotDir, gotVariance := FindDir(img, base, stride)
		wantDir, wantVariance := findDirReference(img, base, stride)
		if gotDir != wantDir || gotVariance != wantVariance {
			t.Fatalf("iteration=%d stride=%d column=%d got=(%d,%d) want=(%d,%d)",
				iteration, stride, column,
				gotDir, gotVariance, wantDir, wantVariance)
		}
	}

	for value := 0; value < 256; value++ {
		img := make([]byte, 8*8)
		for i := range img {
			img[i] = byte(value)
		}
		gotDir, gotVariance := FindDir(img, 0, 8)
		wantDir, wantVariance := findDirReference(img, 0, 8)
		if gotDir != wantDir || gotVariance != wantVariance {
			t.Fatalf("constant=%d got=(%d,%d) want=(%d,%d)",
				value, gotDir, gotVariance, wantDir, wantVariance)
		}
	}
}

func findDirReference(img []uint8, imgBase, stride int) (dir int, variance uint) {
	if imgBase < 0 || stride <= 0 {
		return 0, 0
	}
	if imgBase+7*stride+8 > len(img) {
		return 0, 0
	}
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

	var cost [8]uint
	for n := 0; n < 8; n++ {
		cost[2] += uint(partialSumHV[0][n] * partialSumHV[0][n])
		cost[6] += uint(partialSumHV[1][n] * partialSumHV[1][n])
	}
	cost[2] *= 105
	cost[6] *= 105

	divTable := [7]uint{840, 420, 280, 210, 168, 140, 120}
	for n := 0; n < 7; n++ {
		d := divTable[n]
		cost[0] += (uint(partialSumDiag[0][n]*partialSumDiag[0][n]) +
			uint(partialSumDiag[0][14-n]*partialSumDiag[0][14-n])) * d
		cost[4] += (uint(partialSumDiag[1][n]*partialSumDiag[1][n]) +
			uint(partialSumDiag[1][14-n]*partialSumDiag[1][14-n])) * d
	}
	cost[0] += uint(partialSumDiag[0][7]*partialSumDiag[0][7]) * 105
	cost[4] += uint(partialSumDiag[1][7]*partialSumDiag[1][7]) * 105

	for n := 0; n < 4; n++ {
		cp := &cost[n*2+1]
		for m := 0; m < 5; m++ {
			*cp += uint(partialSumAlt[n][3+m] * partialSumAlt[n][3+m])
		}
		*cp *= 105
		for m := 0; m < 3; m++ {
			d := divTable[2*m+1]
			*cp += (uint(partialSumAlt[n][m]*partialSumAlt[n][m]) +
				uint(partialSumAlt[n][10-m]*partialSumAlt[n][10-m])) * d
		}
	}

	bestDir := 0
	bestCost := cost[0]
	for n := 1; n < 8; n++ {
		if cost[n] > bestCost {
			bestCost = cost[n]
			bestDir = n
		}
	}
	return bestDir, (bestCost - cost[bestDir^4]) >> 10
}
