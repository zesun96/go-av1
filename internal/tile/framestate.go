// framestate.go — per-frame top/left neighbour state used for context derivation.
//
// AV1 CABAC context derivation depends on the decoded state of neighbouring
// coding units. This file tracks, in 4×4-luma-unit granularity:
//
//   - skip flag     (top row + left column)
//   - intra Y mode  (top row + left column, clamped to KFY ctx range 0-4)
//   - partition presence (above / left, for partCtx = hasAbove<<1|hasLeft)
//
// All arrays are indexed in 4-px units from the frame origin (not tile-local).
// They are allocated once per frame and reused across tiles.
package tile

// FrameState holds per-4x4-unit neighbour information for one frame.
// Rows are indexed top→bottom, columns left→right.
type FrameState struct {
	// AboveSkip[col4] — skip flag of the block above column col4 (4-px units).
	AboveSkip []uint8
	// LeftSkip[row4] — skip flag of the block to the left of row row4.
	LeftSkip []uint8

	// AboveMode[col4] — KFY-context mode (0-4) of the block above col4.
	AboveMode []uint8
	// LeftMode[row4] — KFY-context mode (0-4) of the block to the left of row4.
	LeftMode []uint8

	// AbovePresent[col4] — 1 if there is a decoded block above col4.
	AbovePresent []uint8
	// LeftPresent[row4] — 1 if there is a decoded block to the left of row4.
	LeftPresent []uint8

	// AboveSegID[col4] / LeftSegID[row4] — segment_id of decoded neighbour
	// blocks (0..MaxSegments-1). Used by SegIDFromNeighbours for predicting
	// the current block's segment id when segmentation is enabled.
	AboveSegID []uint8
	LeftSegID  []uint8

	// Frame dimensions in 4-px units.
	W4, H4 int
}

// NewFrameState allocates a FrameState for a frame of size (w×h) luma pixels.
func NewFrameState(w, h int) *FrameState {
	w4 := (w + 3) / 4
	h4 := (h + 3) / 4
	return &FrameState{
		AboveSkip:    make([]uint8, w4),
		LeftSkip:     make([]uint8, h4),
		AboveMode:    make([]uint8, w4),
		LeftMode:     make([]uint8, h4),
		AbovePresent: make([]uint8, w4),
		LeftPresent:  make([]uint8, h4),
		AboveSegID:   make([]uint8, w4),
		LeftSegID:    make([]uint8, h4),
		W4:           w4,
		H4:           h4,
	}
}

// PartCtx returns the partition context index (0-3) for a block at (bx,by)
// pixels based on whether there is a decoded block above and to the left.
//
//	partCtx = (hasAbove << 1) | hasLeft
func (fs *FrameState) PartCtx(bx, by int) int {
	col4 := bx / 4
	row4 := by / 4
	hasAbove := 0
	if row4 > 0 && col4 < fs.W4 && fs.AbovePresent[col4] != 0 {
		hasAbove = 1
	}
	hasLeft := 0
	if col4 > 0 && row4 < fs.H4 && fs.LeftPresent[row4] != 0 {
		hasLeft = 1
	}
	return (hasAbove << 1) | hasLeft
}

// SkipCtx returns the skip context (0-2) for a block at (bx,by) pixels.
//
//	skipCtx = aboveSkip + leftSkip
func (fs *FrameState) SkipCtx(bx, by int) int {
	col4 := bx / 4
	row4 := by / 4
	above := 0
	if row4 > 0 && col4 < fs.W4 {
		above = int(fs.AboveSkip[col4])
	}
	left := 0
	if col4 > 0 && row4 < fs.H4 {
		left = int(fs.LeftSkip[row4])
	}
	return above + left
}

// TopModeCtx returns the KFY top-mode context (0-4) for a block at (bx,by).
func (fs *FrameState) TopModeCtx(bx, by int) int {
	col4 := bx / 4
	if by == 0 || col4 >= fs.W4 {
		return 0 // DC_PRED context
	}
	return int(fs.AboveMode[col4])
}

// LeftModeCtx returns the KFY left-mode context (0-4) for a block at (bx,by).
func (fs *FrameState) LeftModeCtx(bx, by int) int {
	row4 := by / 4
	if bx == 0 || row4 >= fs.H4 {
		return 0 // DC_PRED context
	}
	return int(fs.LeftMode[row4])
}

// SegIDFromNeighbours returns the predicted segment_id for a block at (bx,by)
// pixels. Mirrors AV1 spec §5.11.9 / dav1d decode_b: the predictor is the
// minimum of the available above and left neighbour segment ids; if neither
// neighbour exists the predictor is 0.
func (fs *FrameState) SegIDFromNeighbours(bx, by int) uint8 {
	col4 := bx / 4
	row4 := by / 4
	haveAbove := row4 > 0 && col4 < fs.W4 && fs.AbovePresent[col4] != 0
	haveLeft := col4 > 0 && row4 < fs.H4 && fs.LeftPresent[row4] != 0
	switch {
	case haveAbove && haveLeft:
		a := fs.AboveSegID[col4]
		l := fs.LeftSegID[row4]
		if a < l {
			return a
		}
		return l
	case haveAbove:
		return fs.AboveSegID[col4]
	case haveLeft:
		return fs.LeftSegID[row4]
	default:
		return 0
	}
}

// SetBlock records the decoded state of a block occupying (bw×bh) luma pixels
// at position (bx,by). Called after each block is decoded.
//
//	skip:   decoded skip flag
//	yMode:  decoded luma intra prediction mode (0..NIntraPredModes-1)
func (fs *FrameState) SetBlock(bx, by, bw, bh int, skip bool, yMode int) {
	fs.SetBlockSeg(bx, by, bw, bh, skip, yMode, 0)
}

// SetBlockSeg is the segmentation-aware variant of SetBlock; it additionally
// records the block's segment id into the AboveSegID / LeftSegID neighbour
// arrays for future SegIDFromNeighbours predictions.
func (fs *FrameState) SetBlockSeg(bx, by, bw, bh int, skip bool, yMode int, segID uint8) {
	col4Start := bx / 4
	col4End := (bx + bw + 3) / 4
	if col4End > fs.W4 {
		col4End = fs.W4
	}
	row4Start := by / 4
	row4End := (by + bh + 3) / 4
	if row4End > fs.H4 {
		row4End = fs.H4
	}

	// Map intra mode to KFY context category (0-4), matching dav1d's kfymode_b.
	modeCtx := intraToKFYCtx(yMode)

	skipVal := uint8(0)
	if skip {
		skipVal = 1
	}

	// Update above row: last row of this block becomes the "above" for future blocks below.
	aboveRow4 := row4End - 1
	if aboveRow4 >= 0 && aboveRow4 < fs.H4 {
		for c := col4Start; c < col4End; c++ {
			if c < fs.W4 {
				fs.AboveSkip[c] = skipVal
				fs.AboveMode[c] = uint8(modeCtx)
				fs.AbovePresent[c] = 1
				fs.AboveSegID[c] = segID
			}
		}
	}

	// Update left column: last column of this block becomes "left" for future blocks to the right.
	leftCol4 := col4End - 1
	if leftCol4 >= 0 && leftCol4 < fs.W4 {
		for r := row4Start; r < row4End; r++ {
			if r < fs.H4 {
				fs.LeftSkip[r] = skipVal
				fs.LeftMode[r] = uint8(modeCtx)
				fs.LeftPresent[r] = 1
				fs.LeftSegID[r] = segID
			}
		}
	}
}

// intraToKFYCtx maps an AV1 intra prediction mode to the 5-category
// KFY-mode context used by dav1d (matches dav1d's kfymode_b[] LUT):
//
//	0 = DC_PRED
//	1 = VERT_PRED   (V)
//	2 = HOR_PRED    (H)
//	3 = D45_PRED … D135_PRED … (all diagonal/directional)
//	4 = SMOOTH*
//
// The exact mapping is:
//
//	DC_PRED   → 0
//	V_PRED    → 1
//	H_PRED    → 2
//	D45..D135 → 3  (modes 3-8, i.e. all directional except V and H)
//	PAETH     → 3  (mode 9)
//	SMOOTH*   → 4  (modes 10-12)
func intraToKFYCtx(mode int) int {
	switch mode {
	case DCPred:
		return 0
	case VertPred:
		return 1
	case HorPred:
		return 2
	case SmoothPred, SmoothVPred, SmoothHPred:
		return 4
	default:
		// Directional (D45/D135/D113/D157/D203/D67) and Paeth → 3
		return 3
	}
}
