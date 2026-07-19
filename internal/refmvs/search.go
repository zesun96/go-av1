package refmvs

// SearchConfig describes one reference-specific spatial MV search in 4x4
// units. BlockDims maps a Block.BS value to {width4, height4}.
type SearchConfig struct {
	Frame              *Frame
	TemporalSource     *Frame
	UseRefFrameMVs     bool
	Ref                int8
	Ref2               int8
	TargetSlot         int
	GlobalMV           MV
	Bx4, By4, Bw4, Bh4 int
	TileX0, TileY0     int
	TileX1, TileY1     int
	TopRightKnown      bool
	TopRightAvailable  bool
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
	if cfg.UseRefFrameMVs && cfg.TargetSlot >= 0 && cfg.Frame != nil && cfg.Frame.OrderBits != 0 {
		out.GlobalMVContext = 1
		// Only the temporal sample at the block's top-left 8x8 position
		// updates globalmv_ctx. Later samples may extend the candidate stack,
		// but dav1d passes a nil context pointer for all of them.
		if mv, ok := projectTemporalAt(cfg.Frame, cfg.TargetSlot, cfg.Bx4>>1, cfg.By4>>1); ok {
			dx := absSearch(int(mv.X) - int(cfg.GlobalMV.X))
			dy := absSearch(int(mv.Y) - int(cfg.GlobalMV.Y))
			out.GlobalMVContext = boolSearch(dx|dy >= 16)
		}
		temporal := temporalCandidates(cfg)
		for _, mv := range temporal {
			out.Count = AddCandidate(out.Candidates[:], out.Count, MVPair{mv, {}}, 2)
		}
		SortCandidates(out.Candidates[out.NearestCount:], out.Count-out.NearestCount)
	}
	appendSingleExtendedCandidates(&out, cfg)
	clampCandidates(&out, cfg)
	return out
}

func clampCandidates(out *SearchResult, cfg SearchConfig) {
	if out == nil || cfg.Frame == nil || out.Count == 0 {
		return
	}
	left := -(cfg.Bx4 + cfg.Bw4 + 4) * 32
	right := (cfg.Frame.IW4 - cfg.Bx4 + 4) * 32
	top := -(cfg.By4 + cfg.Bh4 + 4) * 32
	bottom := (cfg.Frame.IH4 - cfg.By4 + 4) * 32
	for i := 0; i < out.Count; i++ {
		out.Candidates[i].MV[0].X = int16(clampSearch(int(out.Candidates[i].MV[0].X), left, right))
		out.Candidates[i].MV[0].Y = int16(clampSearch(int(out.Candidates[i].MV[0].Y), top, bottom))
		if cfg.Ref2 > 0 {
			out.Candidates[i].MV[1].X = int16(clampSearch(int(out.Candidates[i].MV[1].X), left, right))
			out.Candidates[i].MV[1].Y = int16(clampSearch(int(out.Candidates[i].MV[1].Y), top, bottom))
		}
	}
}

// appendSingleExtendedCandidates mirrors dav1d's non-self-reference fallback.
// When fewer than two candidates match the requested logical reference, MVs
// from the nearest top and left blocks are reused, reversing their direction
// when the source and target references lie on opposite sides of this frame.
func appendSingleExtendedCandidates(out *SearchResult, cfg SearchConfig) {
	if out == nil || out.Count >= 2 || cfg.Frame == nil || cfg.Ref <= 0 || cfg.Ref2 > 0 {
		return
	}
	w4 := minSearch(minSearch(cfg.Bw4, 16), cfg.Frame.IW4-cfg.Bx4)
	h4 := minSearch(minSearch(cfg.Bh4, 16), cfg.Frame.IH4-cfg.By4)
	sz4 := minSearch(w4, h4)
	if sz4 <= 0 {
		return
	}
	targetSign := referenceSignBias(cfg.Frame, cfg.Ref)
	addBlock := func(blk Block) {
		for n := 0; n < 2 && out.Count < 2; n++ {
			candRef := blk.Ref[n]
			if candRef <= 0 {
				break
			}
			mv := blk.MV[n]
			if targetSign != referenceSignBias(cfg.Frame, candRef) {
				mv.Y = -mv.Y
				mv.X = -mv.X
			}
			out.Count = AddCandidate(out.Candidates[:], out.Count, MVPair{mv, {}}, 2)
		}
	}
	if cfg.By4 > cfg.TileY0 {
		for x := 0; x < sz4 && out.Count < 2; {
			blk, ok := cfg.Frame.GridBlock(cfg.Bx4+x, cfg.By4-1)
			if !ok {
				break
			}
			addBlock(blk)
			bw, _, ok := dimsForSearch(cfg, blk)
			if !ok {
				break
			}
			x += bw
		}
	}
	if cfg.Bx4 > cfg.TileX0 {
		for y := 0; y < sz4 && out.Count < 2; {
			blk, ok := cfg.Frame.GridBlock(cfg.Bx4-1, cfg.By4+y)
			if !ok {
				break
			}
			addBlock(blk)
			_, bh, ok := dimsForSearch(cfg, blk)
			if !ok {
				break
			}
			y += bh
		}
	}
}

func referenceSignBias(frame *Frame, ref int8) bool {
	if frame == nil || ref <= 0 || int(ref) > len(frame.RefFrameOrderHints) || frame.OrderBits == 0 {
		return false
	}
	return RelativeDist(frame.RefFrameOrderHints[ref-1], frame.OrderHint, frame.OrderBits) > 0
}

func temporalCandidates(cfg SearchConfig) []MV {
	var out []MV
	add := func(x8, y8 int) {
		mv, ok := projectTemporalAt(cfg.Frame, cfg.TargetSlot, x8, y8)
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
	if source == nil {
		return MV{}, false
	}
	return projectTemporalAt(current, targetSlot, bx4>>1, by4>>1)
}

func projectTemporalAt(current *Frame, targetSlot, x8, y8 int) (MV, bool) {
	if current == nil || targetSlot < 0 || targetSlot >= len(current.RefOrderHints) || current.RPStride <= 0 {
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
	tileX1, tileY1 := cfg.TileX1, cfg.TileY1
	if tileX1 <= cfg.TileX0 {
		tileX1 = cfg.Frame.IW4
	}
	if tileY1 <= cfg.TileY0 {
		tileY1 = cfg.Frame.IH4
	}
	w4 := minSearch(minSearch(cfg.Bw4, 16), tileX1-cfg.Bx4)
	h4 := minSearch(minSearch(cfg.Bh4, 16), tileY1-cfg.By4)
	nRows, nCols := -1, -1
	maxRows, maxCols := 0, 0
	if cfg.By4 > cfg.TileY0 {
		maxRows = minSearch((cfg.By4-cfg.TileY0+1)>>1, 2+boolSearch(cfg.Bh4 > 1))
		step := 1
		if cfg.Bw4 >= 16 {
			step = 4
		}
		nRows = scanSpatialRow(&out, cfg, cfg.Bx4, cfg.By4-1, cfg.Bw4, w4, maxRows, step, true)
	}
	if cfg.Bx4 > cfg.TileX0 {
		maxCols = minSearch((cfg.Bx4-cfg.TileX0+1)>>1, 2+boolSearch(cfg.Bw4 > 1))
		step := 1
		if cfg.Bh4 >= 16 {
			step = 4
		}
		nCols = scanSpatialCol(&out, cfg, cfg.Bx4-1, cfg.By4, cfg.Bh4, h4, maxCols, step, true)
	}
	appendTopRight(&out, cfg)
	nearestCount := out.Count
	out.NearestCount = nearestCount
	for i := 0; i < out.NearestCount; i++ {
		out.Candidates[i].Weight += 640
	}
	appendSecondarySpatial(&out, cfg, nRows, nCols, maxRows, maxCols, w4, h4)
	SortCandidates(out.Candidates[:], out.NearestCount)
	return out
}

func addSpatialCandidate(out *SearchResult, cfg SearchConfig, blk Block, weight int, row, direct, trackNewMV bool) {
	if out == nil || weight <= 0 || blk.Ref.IsIntra() {
		return
	}
	mvp := MVPair{InvalidMV, {}}
	if cfg.Ref2 > 0 {
		if blk.Ref != (RefPair{cfg.Ref, cfg.Ref2}) {
			return
		}
		mvp = blk.MV
	} else {
		for i := 0; i < 2; i++ {
			if blk.Ref[i] == cfg.Ref {
				mvp[0] = blk.MV[i]
				if blk.MF&1 != 0 && !cfg.GlobalMV.IsInvalid() {
					mvp[0] = cfg.GlobalMV
				}
				break
			}
		}
	}
	if mvp[0].IsInvalid() || cfg.Ref2 > 0 && mvp[1].IsInvalid() {
		return
	}
	if row {
		if direct {
			out.RowMatch = true
		} else {
			out.SecondaryRowMatch = true
		}
	} else if direct {
		out.ColMatch = true
	} else {
		out.SecondaryColMatch = true
	}
	if trackNewMV {
		out.HaveNewMV = out.HaveNewMV || blk.MF&2 != 0
	}
	out.Count = AddCandidate(out.Candidates[:], out.Count, mvp, weight)
}

func scanSpatialRow(out *SearchResult, cfg SearchConfig, x4, y4, bw4, w4, maxRows, step int, direct bool) int {
	blk, ok := cfg.Frame.GridBlock(x4, y4)
	if !ok {
		return 0
	}
	candW, candH, ok := dimsForSearch(cfg, blk)
	if !ok {
		return 0
	}
	length := maxSearch(step, minSearch(bw4, candW))
	if bw4 <= candW {
		weight := 2
		if bw4 != 1 {
			weight = maxSearch(2, minSearch(2*maxRows, candH))
		}
		addSpatialCandidate(out, cfg, blk, length*weight, true, direct, direct)
		return weight >> 1
	}
	for x := 0; ; {
		addSpatialCandidate(out, cfg, blk, length*2, true, direct, direct)
		x += length
		if x >= w4 {
			return 1
		}
		blk, ok = cfg.Frame.GridBlock(x4+x, y4)
		if !ok {
			return 1
		}
		candW, _, ok = dimsForSearch(cfg, blk)
		if !ok {
			return 1
		}
		length = maxSearch(step, candW)
	}
}

func scanSpatialCol(out *SearchResult, cfg SearchConfig, x4, y4, bh4, h4, maxCols, step int, direct bool) int {
	blk, ok := cfg.Frame.GridBlock(x4, y4)
	if !ok {
		return 0
	}
	candW, candH, ok := dimsForSearch(cfg, blk)
	if !ok {
		return 0
	}
	length := maxSearch(step, minSearch(bh4, candH))
	if bh4 <= candH {
		weight := 2
		if bh4 != 1 {
			weight = maxSearch(2, minSearch(2*maxCols, candW))
		}
		addSpatialCandidate(out, cfg, blk, length*weight, false, direct, direct)
		return weight >> 1
	}
	for y := 0; ; {
		addSpatialCandidate(out, cfg, blk, length*2, false, direct, direct)
		y += length
		if y >= h4 {
			return 1
		}
		blk, ok = cfg.Frame.GridBlock(x4, y4+y)
		if !ok {
			return 1
		}
		_, candH, ok = dimsForSearch(cfg, blk)
		if !ok {
			return 1
		}
		length = maxSearch(step, candH)
	}
}

func appendTopRight(out *SearchResult, cfg SearchConfig) {
	if out == nil || cfg.Frame == nil || cfg.By4 <= cfg.TileY0 ||
		cfg.Bx4+cfg.Bw4 >= cfg.Frame.IW4 || maxSearch(cfg.Bw4, cfg.Bh4) > 16 {
		return
	}
	if cfg.TopRightKnown && !cfg.TopRightAvailable {
		return
	}
	blk, ok := cfg.Frame.GridBlock(cfg.Bx4+cfg.Bw4, cfg.By4-1)
	if !ok {
		return
	}
	addSpatialCandidate(out, cfg, blk, 4, true, true, true)
}

func appendSecondarySpatial(out *SearchResult, cfg SearchConfig, nRows, nCols, maxRows, maxCols, w4, h4 int) {
	if out == nil || cfg.Frame == nil {
		return
	}
	if nRows >= 0 && nCols >= 0 {
		if blk, ok := cfg.Frame.GridBlock(cfg.Bx4-1, cfg.By4-1); ok {
			addSpatialCandidate(out, cfg, blk, 4, true, false, false)
		}
	}
	for n := 2; n <= 3; n++ {
		if n > nRows && n <= maxRows {
			nRows += scanSpatialRow(out, cfg, cfg.Bx4|1, (cfg.By4-2*n+1)|1,
				cfg.Bw4, w4, 1+maxRows-n, 2+2*boolSearch(cfg.Bw4 >= 16), false)
		}
		if n > nCols && n <= maxCols {
			nCols += scanSpatialCol(out, cfg, (cfg.Bx4-2*n+1)|1, cfg.By4|1,
				cfg.Bh4, h4, 1+maxCols-n, 2+2*boolSearch(cfg.Bh4 >= 16), false)
		}
	}
	SortCandidates(out.Candidates[out.NearestCount:], out.Count-out.NearestCount)
}

func dimsForSearch(cfg SearchConfig, blk Block) (int, int, bool) {
	if int(blk.BS) >= len(cfg.BlockDims) {
		return 0, 0, false
	}
	d := cfg.BlockDims[blk.BS]
	return maxSearch(1, int(d[0])), maxSearch(1, int(d[1])), true
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

func clampSearch(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
