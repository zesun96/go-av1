package refmvs

// SearchConfig describes one reference-specific spatial MV search in 4x4
// units. BlockDims maps a Block.BS value to {width4, height4}.
type SearchConfig struct {
	Frame              *Frame
	TemporalSource     *Frame
	Ref                int8
	TargetSlot         int
	GlobalMV           MV
	Bx4, By4, Bw4, Bh4 int
	TileX0, TileY0     int
	TileX1, TileY1     int
	BlockDims          [][2]uint8
}

type SearchResult struct {
	Candidates        [8]Candidate
	Count             int
	NearestCount      int
	HaveNewMV         bool
	RowMatch          bool
	ColMatch          bool
	SecondaryRowMatch bool
	SecondaryColMatch bool
	GlobalMVContext   int
}

// Find builds the complete single-reference candidate stack for one target
// reference. Spatial candidates retain priority; a projected temporal sample
// is appended to the secondary range and merged when its MV already exists.
func Find(cfg SearchConfig) SearchResult {
	out := FindSpatial(cfg)
	// dav1d initializes globalmv_ctx from use_ref_frame_mvs. Without order
	// hints temporal projection is disabled, so the context remains zero even
	// when a decoded reference picture is available.
	if cfg.TemporalSource == nil || cfg.TargetSlot < 0 || cfg.Frame == nil || cfg.Frame.OrderBits == 0 {
		return out
	}
	out.GlobalMVContext = 1
	temporal := temporalCandidates(cfg)
	if len(temporal) > 0 {
		dx := absSearch(int(temporal[0].X) - int(cfg.GlobalMV.X))
		dy := absSearch(int(temporal[0].Y) - int(cfg.GlobalMV.Y))
		out.GlobalMVContext = boolSearch(dx|dy >= 16)
	}
	for _, mv := range temporal {
		out.Count = AddCandidate(out.Candidates[:], out.Count, MVPair{mv, {}}, 2)
	}
	SortCandidates(out.Candidates[out.NearestCount:], out.Count-out.NearestCount)
	return out
}

func temporalCandidates(cfg SearchConfig) []MV {
	var out []MV
	add := func(x8, y8 int) {
		mv, ok := projectTemporalAt(cfg.Frame, cfg.TemporalSource, cfg.TargetSlot, x8, y8)
		if ok {
			out = append(out, mv)
		}
	}
	bx8, by8 := cfg.Bx4>>1, cfg.By4>>1
	w4 := minSearch(cfg.Bw4, cfg.Frame.IW4-cfg.Bx4)
	h4 := minSearch(cfg.Bh4, cfg.Frame.IH4-cfg.By4)
	w8, h8 := minSearch((w4+1)>>1, 8), minSearch((h4+1)>>1, 8)
	stepH, stepV := 1, 1
	if cfg.Bw4 >= 16 {
		stepH = 2
	}
	if cfg.Bh4 >= 16 {
		stepV = 2
	}
	for y := 0; y < h8; y += stepV {
		for x := 0; x < w8; x += stepH {
			add(bx8+x, by8+y)
		}
	}
	if minSearch(cfg.Bw4, cfg.Bh4) < 2 || maxSearch(cfg.Bw4, cfg.Bh4) >= 16 {
		return out
	}
	tileX0 := cfg.TileX0 >> 1
	tileX1, tileY1 := cfg.TileX1>>1, cfg.TileY1>>1
	if cfg.TileX1 <= cfg.TileX0 {
		tileX1 = cfg.Frame.IW8
	}
	if cfg.TileY1 <= cfg.TileY0 {
		tileY1 = cfg.Frame.IH8
	}
	bw8, bh8 := cfg.Bw4>>1, cfg.Bh4>>1
	regionX1 := minSearch(tileX1, (bx8&^7)+8)
	regionY1 := minSearch(tileY1, (by8&^7)+8)
	hasBottom := by8+bh8 < regionY1
	if hasBottom && bx8-1 >= maxSearch(tileX0, bx8&^7) {
		add(bx8-1, by8+bh8)
	}
	if bx8+bw8 < regionX1 {
		if hasBottom {
			add(bx8+bw8, by8+bh8)
		}
		if by8+bh8-1 < regionY1 {
			add(bx8+bw8, by8+bh8-1)
		}
	}
	return out
}

// FindTemporal projects one motion-field sample from a saved reference frame
// to targetSlot in the current frame.
func FindTemporal(current, source *Frame, targetSlot, bx4, by4 int) (MV, bool) {
	return projectTemporalAt(current, source, targetSlot, bx4>>1, by4>>1)
}

func projectTemporalAt(current, source *Frame, targetSlot, x8, y8 int) (MV, bool) {
	if current == nil || source == nil || targetSlot < 0 || targetSlot >= len(current.RefOrderHints) || current.RPStride <= 0 {
		return MV{}, false
	}
	if x8 < 0 || y8 < 0 || x8 >= current.IW8 || y8 >= current.IH8 {
		return MV{}, false
	}
	tb := current.RPProj[y8*current.RPStride+x8]
	if tb.Ref == 0 {
		return MV{}, false
	}
	if current.OrderBits == 0 {
		return MV{}, false
	}
	td := RelativeDist(current.OrderHint, current.RefOrderHints[targetSlot], current.OrderBits)
	mv := ScaleMV(tb.MV, td, int(tb.Ref))
	mv = NormalizeMVPrecision(mv, current.HighPrecision, current.ForceInteger)
	return mv, !mv.IsInvalid()
}

// FindSpatial builds dav1d's nearest spatial range. Row scanning precedes
// column scanning, so stable sorting preserves row-first ties.
func FindSpatial(cfg SearchConfig) SearchResult {
	var out SearchResult
	if cfg.Frame == nil || cfg.Bw4 <= 0 || cfg.Bh4 <= 0 || len(cfg.BlockDims) == 0 {
		return out
	}
	add := func(blk Block, weight int, row bool) {
		if weight <= 0 || blk.Ref[0] == 0 {
			return
		}
		mv := InvalidMV
		for i := 0; i < 2; i++ {
			if blk.Ref[i] == cfg.Ref {
				mv = blk.MV[i]
				break
			}
		}
		if mv.IsInvalid() {
			return
		}
		if row {
			out.RowMatch = true
		} else {
			out.ColMatch = true
		}
		out.HaveNewMV = out.HaveNewMV || blk.MF&2 != 0
		out.Count = AddCandidate(out.Candidates[:], out.Count, MVPair{mv, {}}, weight)
	}
	dims := func(blk Block) (int, int, bool) {
		if int(blk.BS) >= len(cfg.BlockDims) {
			return 0, 0, false
		}
		d := cfg.BlockDims[blk.BS]
		return maxSearch(1, int(d[0])), maxSearch(1, int(d[1])), true
	}

	if cfg.By4 > cfg.TileY0 {
		maxRows := minSearch((cfg.By4-cfg.TileY0+1)>>1, 2+boolSearch(cfg.Bh4 > 1))
		step := 1
		if cfg.Bw4 >= 16 {
			step = 4
		}
		for x := 0; x < cfg.Bw4; {
			probeX := cfg.Bx4 + x
			blk, ok := cfg.Frame.GridBlock(probeX, cfg.By4-1)
			if !ok {
				break
			}
			cw, ch, ok := dims(blk)
			if !ok {
				break
			}
			remainingW := maxSearch(1, cw-(probeX-int(blk.X4)))
			length := maxSearch(step, minSearch(cfg.Bw4-x, remainingW))
			if cfg.Bw4-x <= remainingW {
				weight := 2
				if cfg.Bw4 > 1 {
					weight = maxSearch(2, minSearch(2*maxRows, ch))
				}
				add(blk, length*weight, true)
				break
			}
			add(blk, length*2, true)
			x += length
		}
	}
	if cfg.Bx4 > cfg.TileX0 {
		maxCols := minSearch((cfg.Bx4-cfg.TileX0+1)>>1, 2+boolSearch(cfg.Bw4 > 1))
		step := 1
		if cfg.Bh4 >= 16 {
			step = 4
		}
		for y := 0; y < cfg.Bh4; {
			probeY := cfg.By4 + y
			blk, ok := cfg.Frame.GridBlock(cfg.Bx4-1, probeY)
			if !ok {
				break
			}
			cw, ch, ok := dims(blk)
			if !ok {
				break
			}
			remainingH := maxSearch(1, ch-(probeY-int(blk.Y4)))
			length := maxSearch(step, minSearch(cfg.Bh4-y, remainingH))
			if cfg.Bh4-y <= remainingH {
				weight := 2
				if cfg.Bh4 > 1 {
					weight = maxSearch(2, minSearch(2*maxCols, cw))
				}
				add(blk, length*weight, false)
				break
			}
			add(blk, length*2, false)
			y += length
		}
	}
	appendTopRight(&out, cfg)
	nearestCount := out.Count
	appendSecondarySpatial(&out, cfg)
	out.NearestCount = nearestCount
	for i := 0; i < out.NearestCount; i++ {
		out.Candidates[i].Weight += 640
	}
	SortCandidates(out.Candidates[:], out.NearestCount)
	return out
}

func appendTopRight(out *SearchResult, cfg SearchConfig) {
	if out == nil || cfg.Frame == nil || cfg.By4 <= cfg.TileY0 ||
		cfg.Bx4+cfg.Bw4 >= cfg.Frame.IW4 || maxSearch(cfg.Bw4, cfg.Bh4) > 16 {
		return
	}
	blk, ok := cfg.Frame.GridBlock(cfg.Bx4+cfg.Bw4, cfg.By4-1)
	if !ok || blk.Ref[0] == 0 {
		return
	}
	mv := InvalidMV
	for i := 0; i < 2; i++ {
		if blk.Ref[i] == cfg.Ref {
			mv = blk.MV[i]
			break
		}
	}
	if mv.IsInvalid() {
		return
	}
	out.Count = AddCandidate(out.Candidates[:], out.Count, MVPair{mv, {}}, 4)
	out.RowMatch = true
	out.HaveNewMV = out.HaveNewMV || blk.MF&2 != 0
}

func appendSecondarySpatial(out *SearchResult, cfg SearchConfig) {
	if out == nil || cfg.Frame == nil {
		return
	}
	add := func(x4, y4, weight int, row, col, trackNewMV bool) bool {
		blk, ok := cfg.Frame.GridBlock(x4, y4)
		if !ok || blk.Ref[0] == 0 {
			return false
		}
		mv := InvalidMV
		for i := 0; i < 2; i++ {
			if blk.Ref[i] == cfg.Ref {
				mv = blk.MV[i]
				break
			}
		}
		if mv.IsInvalid() {
			return false
		}
		out.SecondaryRowMatch = out.SecondaryRowMatch || row
		out.SecondaryColMatch = out.SecondaryColMatch || col
		if trackNewMV {
			out.HaveNewMV = out.HaveNewMV || blk.MF&2 != 0
		}
		out.Count = AddCandidate(out.Candidates[:], out.Count, MVPair{mv, {}}, weight)
		return true
	}
	if cfg.By4 > cfg.TileY0 && cfg.Bx4 > cfg.TileX0 {
		// The diagonal contributes a candidate, but it is not a row or column
		// match for mode-context derivation.
		add(cfg.Bx4-1, cfg.By4-1, 4, false, false, false)
	}
	// dav1d's secondary scan positions are at odd 8x8-resolution offsets.
	for n := 2; n <= 3; n++ {
		y4 := (cfg.By4 - 2*n + 1) | 1
		if y4 >= cfg.TileY0 {
			add(cfg.Bx4|1, y4, 4, true, false, false)
		}
		x4 := (cfg.Bx4 - 2*n + 1) | 1
		if x4 >= cfg.TileX0 {
			add(x4, cfg.By4|1, 4, false, true, false)
		}
	}
	SortCandidates(out.Candidates[out.NearestCount:], out.Count-out.NearestCount)
}

func minSearch(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func maxSearch(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func boolSearch(v bool) int {
	if v {
		return 1
	}
	return 0
}

func absSearch(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
