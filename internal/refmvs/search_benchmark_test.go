package refmvs

import "testing"

var benchmarkRefMVSSink int

func BenchmarkFindSpatialCandidates(b *testing.B) {
	frame := NewFrame(1920, 1080)
	dims := [][2]uint8{{4, 4}, {2, 2}}
	bx4, by4 := 200, 120
	blocks := []struct {
		x, y int
		mv   MV
		mf   uint8
	}{
		{x: bx4, y: by4 - 4, mv: MV{X: 24, Y: -8}, mf: 2},
		{x: bx4 - 4, y: by4, mv: MV{X: 16, Y: 8}, mf: 0},
		{x: bx4 + 4, y: by4 - 4, mv: MV{X: -8, Y: 16}, mf: 2},
		{x: bx4 - 2, y: by4 - 2, mv: MV{X: 32, Y: -16}, mf: 0},
	}
	for _, item := range blocks {
		frame.PutGridBlock(item.x, item.y, 2, 2, Block{MV: MVPair{item.mv, {}}, Ref: RefPair{1, -1}, BS: 1, MF: item.mf})
	}
	cfg := SearchConfig{Frame: frame, Ref: 1, Bx4: bx4, By4: by4, Bw4: 4, Bh4: 4, BlockDims: dims}
	result := Find(cfg)
	if result.Count == 0 {
		b.Fatal("verification produced no candidates")
	}

	b.ReportAllocs()
	b.ResetTimer()
	var sink int
	for n := 0; n < b.N; n++ {
		result = Find(cfg)
		sink ^= result.Count + int(result.Candidates[0].MV[0].X)
	}
	benchmarkRefMVSSink = sink
}
