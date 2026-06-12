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

	AbovePartition []uint8
	LeftPartition  []uint8

	// CDEFIndex stores the decoded per-64x64 CDEF strength index. A value of
	// -1 means no non-skip block has read the index for that CDEF unit yet.
	CDEFIndex []int8

	// AboveSegID[col4] / LeftSegID[row4] — segment_id of decoded neighbour
	// blocks (0..MaxSegments-1). Used by SegIDFromNeighbours for predicting
	// the current block's segment id when segmentation is enabled.
	AboveSegID []uint8
	LeftSegID  []uint8

	// Frame dimensions in 4-px units.
	W4, H4 int
	W8, H8 int
	W64    int
}

// NewFrameState allocates a FrameState for a frame of size (w×h) luma pixels.
func NewFrameState(w, h int) *FrameState {
	w4 := (w + 3) / 4
	h4 := (h + 3) / 4
	w8 := (w + 7) / 8
	h8 := (h + 7) / 8
	w64 := (w + 63) / 64
	h64 := (h + 63) / 64
	cdefIndex := make([]int8, w64*h64)
	for i := range cdefIndex {
		cdefIndex[i] = -1
	}
	return &FrameState{
		AboveSkip:      make([]uint8, w4),
		LeftSkip:       make([]uint8, h4),
		AboveMode:      make([]uint8, w4),
		LeftMode:       make([]uint8, h4),
		AbovePresent:   make([]uint8, w4),
		LeftPresent:    make([]uint8, h4),
		AbovePartition: make([]uint8, w8),
		LeftPartition:  make([]uint8, h8),
		CDEFIndex:      cdefIndex,
		AboveSegID:     make([]uint8, w4),
		LeftSegID:      make([]uint8, h4),
		W4:             w4,
		H4:             h4,
		W8:             w8,
		H8:             h8,
		W64:            w64,
	}
}

// PartCtx returns the partition context index (0-3) for a block at (bx,by)
// pixels based on whether there is a decoded block above and to the left.
//
//	partCtx = (hasAbove << 1) | hasLeft
func (fs *FrameState) PartCtx(bx, by, bl int) int {
	col8 := bx / 8
	row8 := by / 8
	shift := 4 - bl
	if shift < 0 {
		shift = 0
	}
	top := 0
	if row8 > 0 && col8 < fs.W8 {
		top = int((fs.AbovePartition[col8] >> uint(shift)) & 1)
	}
	left := 0
	if col8 > 0 && row8 < fs.H8 {
		left = int((fs.LeftPartition[row8] >> uint(shift)) & 1)
	}
	return top + (left << 1)
}

func (fs *FrameState) SetPartition(bx, by, bl, bp, size int) {
	col8Start := bx / 8
	row8Start := by / 8
	units := (size + 7) / 8
	topVal := alPartCtx[0][bl][bp]
	leftVal := alPartCtx[1][bl][bp]
	for c := col8Start; c < col8Start+units && c < fs.W8; c++ {
		fs.AbovePartition[c] = topVal
	}
	for r := row8Start; r < row8Start+units && r < fs.H8; r++ {
		fs.LeftPartition[r] = leftVal
	}
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
// KFY-mode context used by dav1d. The mapping mirrors dav1d's
// `dav1d_intra_mode_context[N_INTRA_PRED_MODES]` LUT in tables.c:
//
//	DC_PRED        → 0
//	VERT_PRED      → 1
//	HOR_PRED       → 2
//	D45  (DiagDL)  → 3
//	D135 (DiagDR)  → 4
//	D113 (VertR)   → 4
//	D157 (HorD)    → 4
//	D203 (HorU)    → 4
//	D67  (VertL)   → 3
//	SMOOTH         → 0
//	SMOOTH_V       → 1
//	SMOOTH_H       → 2
//	PAETH          → 0
func intraToKFYCtx(mode int) int {
	switch mode {
	case DCPred, SmoothPred, PaethPred:
		return 0
	case VertPred, SmoothVPred:
		return 1
	case HorPred, SmoothHPred:
		return 2
	case DiagDownLeftPred, VertLeftPred:
		return 3
	case DiagDownRightPred, VertRightPred, HorDownPred, HorUpPred:
		return 4
	default:
		return 0
	}
}
