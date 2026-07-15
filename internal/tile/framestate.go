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

import (
	"github.com/zesun96/go-av1/internal/header"
	"github.com/zesun96/go-av1/internal/refmvs"
	"github.com/zesun96/go-av1/internal/transform"
)

// FrameState holds per-4x4-unit neighbour information for one frame.
// Rows are indexed top→bottom, columns left→right.
type FrameState struct {
	Tracef func(string, ...any)
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
	AboveTx        []uint8
	LeftTx         []uint8
	AboveTxIntra   []uint8
	LeftTxIntra    []uint8
	AbovePalY      []uint8
	LeftPalY       []uint8
	AbovePalUV     []uint8
	LeftPalUV      []uint8
	AboveUVMode    []uint8
	LeftUVMode     []uint8
	AbovePal       [3][][8]uint8
	LeftPal        [3][][8]uint8
	AboveRef       []int8
	LeftRef        []int8
	AboveFilter    []uint8
	LeftFilter     []uint8
	AboveFilterV   []uint8
	LeftFilterV    []uint8
	AboveMV        [2][]int16
	LeftMV         [2][]int16

	// CDEFIndex stores the decoded per-64x64 CDEF strength index. A value of
	// -1 means no non-skip block has read the index for that CDEF unit yet.
	CDEFIndex []int8

	// AboveSegID[col4] / LeftSegID[row4] — segment_id of decoded neighbour
	// blocks (0..MaxSegments-1). Used by SegIDFromNeighbours for predicting
	// the current block's segment id when segmentation is enabled.
	AboveSegID   []uint8
	LeftSegID    []uint8
	AboveSegPred []uint8
	LeftSegPred  []uint8

	AboveLCoef      []uint8
	LeftLCoef       []uint8
	AboveCCoef      [2][]uint8
	LeftCCoef       [2][]uint8
	MVFrame         *refmvs.Frame
	BlockGrid       []Av1Block
	ChromaBlockGrid []Av1Block
	// TxGrid stores the luma transform leaf covering each 4x4 unit. Unset
	// entries are 0xff so TX4x4 (zero) remains distinguishable.
	TxGrid     []uint8
	TxOriginX4 []uint16
	TxOriginY4 []uint16
	SsHor      uint8
	SsVer      uint8
	TileX0     int
	TileY0     int
	TileX1     int
	TileY1     int

	// Frame dimensions in 4-px units.
	Width  int
	Height int
	W4, H4 int
	CW4    int
	CH4    int
	W8, H8 int
	W64    int
}

func (fs *FrameState) intraAvailability(plane, bx, by int) (haveTop, haveLeft bool) {
	x0, y0 := fs.TileX0, fs.TileY0
	if plane > 0 {
		x0 >>= fs.SsHor
		y0 >>= fs.SsVer
	}
	return by > y0, bx > x0
}

func (fs *FrameState) tracef(format string, args ...any) {
	if fs != nil && fs.Tracef != nil {
		fs.Tracef(format, args...)
	}
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
	txGrid := make([]uint8, w4*h4)
	for i := range txGrid {
		txGrid[i] = 0xff
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
		AboveTx:        make([]uint8, w4),
		LeftTx:         make([]uint8, h4),
		AboveTxIntra:   make([]uint8, w4),
		LeftTxIntra:    make([]uint8, h4),
		AbovePalY:      make([]uint8, w4),
		LeftPalY:       make([]uint8, h4),
		AbovePalUV:     make([]uint8, w4),
		LeftPalUV:      make([]uint8, h4),
		AboveUVMode:    filledUint8(w4, DCPred),
		LeftUVMode:     filledUint8(h4, DCPred),
		AbovePal: [3][][8]uint8{
			make([][8]uint8, w4),
			make([][8]uint8, w4),
			make([][8]uint8, w4),
		},
		LeftPal: [3][][8]uint8{
			make([][8]uint8, h4),
			make([][8]uint8, h4),
			make([][8]uint8, h4),
		},
		AboveRef:     filledInt8(w4, -1),
		LeftRef:      filledInt8(h4, -1),
		AboveFilter:  make([]uint8, w4),
		LeftFilter:   make([]uint8, h4),
		AboveFilterV: make([]uint8, w4),
		LeftFilterV:  make([]uint8, h4),
		AboveMV: [2][]int16{
			make([]int16, w4),
			make([]int16, w4),
		},
		LeftMV: [2][]int16{
			make([]int16, h4),
			make([]int16, h4),
		},
		CDEFIndex:    cdefIndex,
		AboveSegID:   make([]uint8, w4),
		LeftSegID:    make([]uint8, h4),
		AboveSegPred: make([]uint8, w4),
		LeftSegPred:  make([]uint8, h4),
		AboveLCoef:   filledUint8(w4, 0x40),
		LeftLCoef:    filledUint8(h4, 0x40),
		AboveCCoef: [2][]uint8{
			filledUint8(w4, 0x40),
			filledUint8(w4, 0x40),
		},
		LeftCCoef: [2][]uint8{
			filledUint8(h4, 0x40),
			filledUint8(h4, 0x40),
		},
		BlockGrid:       make([]Av1Block, w4*h4),
		ChromaBlockGrid: make([]Av1Block, ((w+7)/8)*((h+7)/8)),
		TxGrid:          txGrid,
		TxOriginX4:      make([]uint16, w4*h4),
		TxOriginY4:      make([]uint16, w4*h4),
		Width:           w,
		Height:          h,
		SsHor:           1,
		SsVer:           1,
		W4:              w4,
		H4:              h4,
		CW4:             (w + 7) / 8,
		CH4:             (h + 7) / 8,
		W8:              w8,
		H8:              h8,
		W64:             w64,
	}
}

func (fs *FrameState) SetSubsampling(ssHor, ssVer uint8) {
	fs.SsHor = ssHor
	fs.SsVer = ssVer
	cw := (fs.Width + (1 << ssHor) - 1) >> ssHor
	ch := (fs.Height + (1 << ssVer) - 1) >> ssVer
	fs.CW4 = (cw + 3) >> 2
	fs.CH4 = (ch + 3) >> 2
	fs.ChromaBlockGrid = make([]Av1Block, fs.CW4*fs.CH4)
}

func filledUint8(n int, v uint8) []uint8 {
	out := make([]uint8, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func filledInt8(n int, v int8) []int8 {
	out := make([]int8, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// PartCtx returns the partition context index (0-3) for a block at (bx,by)
// pixels based on whether there is a decoded block above and to the left.
//
//	partCtx = hasAbove | (hasLeft << 1)
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

// SegIDPredCtx mirrors dav1d get_cur_frame_segid, including the above-left
// equality context used by the segment-id CDF.
func (fs *FrameState) SegIDPredCtx(bx, by int) (uint8, int) {
	haveTop := by > fs.TileY0
	haveLeft := bx > fs.TileX0
	segAt := func(x, y int) uint8 {
		if blk, ok := fs.BlockState(x, y); ok {
			return blk.SegID
		}
		return 0
	}
	if haveTop && haveLeft {
		left := segAt(bx-4, by)
		above := segAt(bx, by-4)
		aboveLeft := segAt(bx-4, by-4)
		ctx := 0
		if left == above && aboveLeft == left {
			ctx = 2
		} else if left == above || aboveLeft == left || above == aboveLeft {
			ctx = 1
		}
		if above == aboveLeft {
			return above, ctx
		}
		return left, ctx
	}
	if haveLeft {
		return segAt(bx-4, by), 0
	}
	if haveTop {
		return segAt(bx, by-4), 0
	}
	return 0, 0
}

// SegPredCtx returns the temporal segment-prediction context from the above
// and left 4x4 neighbours, matching dav1d's seg_pred edge arrays.
func (fs *FrameState) SegPredCtx(bx, by int) int {
	ctx := 0
	col4, row4 := bx/4, by/4
	if by > fs.TileY0 && col4 >= 0 && col4 < len(fs.AboveSegPred) {
		ctx += int(fs.AboveSegPred[col4])
	}
	if bx > fs.TileX0 && row4 >= 0 && row4 < len(fs.LeftSegPred) {
		ctx += int(fs.LeftSegPred[row4])
	}
	return ctx
}

// SetSegPred records one temporal segment-prediction flag on the block edges.
func (fs *FrameState) SetSegPred(bx, by, bw, bh int, predicted bool) {
	v := uint8(0)
	if predicted {
		v = 1
	}
	for c, end := bx/4, (bx+bw+3)/4; c < end && c < len(fs.AboveSegPred); c++ {
		if c >= 0 {
			fs.AboveSegPred[c] = v
		}
	}
	for r, end := by/4, (by+bh+3)/4; r < end && r < len(fs.LeftSegPred); r++ {
		if r >= 0 {
			fs.LeftSegPred[r] = v
		}
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
				fs.AboveRef[c] = -1
				fs.AboveFilter[c] = 0
				fs.AboveFilterV[c] = 0
				fs.AboveMV[0][c] = 0
				fs.AboveMV[1][c] = 0
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
				fs.LeftRef[r] = -1
				fs.LeftFilter[r] = 0
				fs.LeftFilterV[r] = 0
				fs.LeftMV[0][r] = 0
				fs.LeftMV[1][r] = 0
			}
		}
	}
}

func (fs *FrameState) SetBlockState(bx, by, bw, bh int, blk Av1Block) {
	blk.X4 = uint16(maxInt(bx/4, 0))
	blk.Y4 = uint16(maxInt(by/4, 0))
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
	for r := row4Start; r < row4End; r++ {
		base := r * fs.W4
		for c := col4Start; c < col4End; c++ {
			fs.BlockGrid[base+c] = blk
		}
	}
}

func (fs *FrameState) SetChromaBlockState(bx, by, bw, bh int, blk Av1Block) {
	blk.X4 = uint16(maxInt(bx/4, 0))
	blk.Y4 = uint16(maxInt(by/4, 0))
	px := bx >> fs.SsHor
	py := by >> fs.SsVer
	pw := (bw + (1 << fs.SsHor) - 1) >> fs.SsHor
	ph := (bh + (1 << fs.SsVer) - 1) >> fs.SsVer
	x0, y0 := clampInt(px/4, 0, fs.CW4), clampInt(py/4, 0, fs.CH4)
	x1, y1 := clampInt((px+pw+3)/4, 0, fs.CW4), clampInt((py+ph+3)/4, 0, fs.CH4)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			fs.ChromaBlockGrid[y*fs.CW4+x] = blk
		}
	}
}

// SetTxState records the transform leaf covering a luma rectangle.
func (fs *FrameState) SetTxState(bx, by, bw, bh int, tx uint8) {
	x0 := clampInt(bx/4, 0, fs.W4)
	x1 := clampInt((bx+bw+3)/4, 0, fs.W4)
	y0 := clampInt(by/4, 0, fs.H4)
	y1 := clampInt((by+bh+3)/4, 0, fs.H4)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			i := y*fs.W4 + x
			fs.TxGrid[i] = tx
			fs.TxOriginX4[i] = uint16(x0)
			fs.TxOriginY4[i] = uint16(y0)
		}
	}
}

// MergeFilterState copies durable post-filter metadata from one independently
// decoded tile. Rolling above/left entropy contexts must not be merged.
func (fs *FrameState) MergeFilterState(src *FrameState) {
	if fs == nil || src == nil || fs.W4 != src.W4 || fs.H4 != src.H4 {
		return
	}
	x0 := clampInt(src.TileX0/4, 0, fs.W4)
	x1 := clampInt((src.TileX1+3)/4, 0, fs.W4)
	y0 := clampInt(src.TileY0/4, 0, fs.H4)
	y1 := clampInt((src.TileY1+3)/4, 0, fs.H4)
	for y := y0; y < y1; y++ {
		copy(fs.BlockGrid[y*fs.W4+x0:y*fs.W4+x1], src.BlockGrid[y*src.W4+x0:y*src.W4+x1])
		copy(fs.TxGrid[y*fs.W4+x0:y*fs.W4+x1], src.TxGrid[y*src.W4+x0:y*src.W4+x1])
		copy(fs.TxOriginX4[y*fs.W4+x0:y*fs.W4+x1], src.TxOriginX4[y*src.W4+x0:y*src.W4+x1])
		copy(fs.TxOriginY4[y*fs.W4+x0:y*fs.W4+x1], src.TxOriginY4[y*src.W4+x0:y*src.W4+x1])
	}
	cx0 := clampInt((src.TileX0>>src.SsHor)/4, 0, fs.CW4)
	cx1 := clampInt(((src.TileX1>>src.SsHor)+3)/4, 0, fs.CW4)
	cy0 := clampInt((src.TileY0>>src.SsVer)/4, 0, fs.CH4)
	cy1 := clampInt(((src.TileY1>>src.SsVer)+3)/4, 0, fs.CH4)
	for y := cy0; y < cy1; y++ {
		copy(fs.ChromaBlockGrid[y*fs.CW4+cx0:y*fs.CW4+cx1], src.ChromaBlockGrid[y*src.CW4+cx0:y*src.CW4+cx1])
	}
	for i, v := range src.CDEFIndex {
		if v >= 0 {
			fs.CDEFIndex[i] = v
		}
	}
}

func (fs *FrameState) BlockState(bx, by int) (Av1Block, bool) {
	col4 := bx / 4
	row4 := by / 4
	if col4 < 0 || col4 >= fs.W4 || row4 < 0 || row4 >= fs.H4 {
		return Av1Block{}, false
	}
	return fs.BlockGrid[row4*fs.W4+col4], true
}

func isSmoothIntraMode(mode uint8) bool {
	switch mode {
	case SmoothPred, SmoothVPred, SmoothHPred:
		return true
	default:
		return false
	}
}

func (fs *FrameState) IntraSmoothFlags(bx, by, stepX, stepY int, plane int) int {
	if fs == nil {
		return 0
	}
	if plane > 0 {
		flags := 0
		col4 := bx / 4
		row4 := by / 4
		if by > 0 && col4 >= 0 && col4 < fs.CW4 && col4 < len(fs.AboveUVMode) {
			if isSmoothIntraMode(fs.AboveUVMode[col4]) {
				flags |= 1 << 9
			}
		}
		if bx > 0 && row4 >= 0 && row4 < fs.CH4 && row4 < len(fs.LeftUVMode) {
			if isSmoothIntraMode(fs.LeftUVMode[row4]) {
				flags |= 1 << 9
			}
		}
		return flags
	}
	flags := 0
	if stepX <= 0 {
		stepX = 4
	}
	if stepY <= 0 {
		stepY = 4
	}
	if above, ok := fs.BlockState(bx, by-stepY); ok && above.Intra {
		mode := above.YMode
		if isSmoothIntraMode(mode) {
			flags |= 1 << 9
		}
	}
	if left, ok := fs.BlockState(bx-stepX, by); ok && left.Intra {
		mode := left.YMode
		if isSmoothIntraMode(mode) {
			flags |= 1 << 9
		}
	}
	return flags
}

// SetInterBlock records the decoded neighbour state for an inter-coded block.
// The block still updates the generic skip/segmentation/presence caches through
// SetBlockSeg; inter-specific neighbour state is tracked separately for future
// reference/motion-vector syntax.
func (fs *FrameState) SetInterBlock(bx, by, bw, bh int, skip bool, segID uint8, refSlot, refFrame int, filter, filterV uint8, interMode int, mv refmvs.MV) {
	fs.SetBlockSeg(bx, by, bw, bh, skip, DCPred, segID)
	if len(fs.AboveUVMode) > 0 && len(fs.LeftUVMode) > 0 {
		cbx, cby, cbw, cbh := chromaRect(&header.SequenceHeader{SsHor: fs.SsHor, SsVer: fs.SsVer}, bx, by, bw, bh)
		fs.SetUVModeState(cbx, cby, cbw, cbh, DCPred)
	}

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

	refVal := int8(refSlot)
	aboveRow4 := row4End - 1
	if aboveRow4 >= 0 && aboveRow4 < fs.H4 {
		for c := col4Start; c < col4End; c++ {
			fs.AboveRef[c] = refVal
			fs.AboveFilter[c] = filter
			fs.AboveFilterV[c] = filterV
			fs.AboveMV[0][c] = mv.Y
			fs.AboveMV[1][c] = mv.X
		}
	}

	leftCol4 := col4End - 1
	if leftCol4 >= 0 && leftCol4 < fs.W4 {
		for r := row4Start; r < row4End; r++ {
			fs.LeftRef[r] = refVal
			fs.LeftFilter[r] = filter
			fs.LeftFilterV[r] = filterV
			fs.LeftMV[0][r] = mv.Y
			fs.LeftMV[1][r] = mv.X
		}
	}

	fs.setTemporalMVBlock(bx, by, bw, bh, refFrame, mv)
	fs.setCurrentMVBlock(bx, by, bw, bh, refFrame, interMode, mv)
}

func (fs *FrameState) CommitInterBlock(bx, by, bw, bh int, blk Av1Block, refFrame int) {
	if blk.RefFrame > 0 {
		refFrame = int(blk.RefFrame)
	}
	fs.SetBlockState(bx, by, bw, bh, blk)
	fs.SetInterBlock(
		bx, by, bw, bh,
		blk.Skip,
		blk.SegID,
		int(blk.RefSlot),
		refFrame,
		blk.Filter,
		blk.FilterV,
		int(blk.InterMode),
		refmvs.MV{Y: blk.MV[0], X: blk.MV[1]},
	)
	if blk.Compound {
		fs.setCurrentCompoundMVBlock(bx, by, bw, bh, blk)
	}
}

// CommitIntraMVBlock records an intra block in the reference-MV grid. Spatial
// scans need its real dimensions to skip over the block even though it cannot
// contribute a motion-vector candidate.
func (fs *FrameState) CommitIntraMVBlock(bx, by, bw, bh int) {
	if fs.MVFrame == nil {
		return
	}
	bw4 := (bw + 3) >> 2
	bh4 := (bh + 3) >> 2
	fs.MVFrame.PutGridBlock(bx>>2, by>>2, bw4, bh4, refmvs.Block{
		Ref: refmvs.RefPair{0, -1},
		BS:  uint8(maxInt(bsizeFromDim(bw, bh), 0)),
	})
}

func (fs *FrameState) setTemporalMVBlock(bx, by, bw, bh int, refFrame int, mv refmvs.MV) {
	if fs.MVFrame == nil || fs.MVFrame.RPStride == 0 {
		return
	}
	col8Start := bx >> 3
	col8End := (bx + bw + 7) >> 3
	if col8End > fs.MVFrame.IW8 {
		col8End = fs.MVFrame.IW8
	}
	row8Start := by >> 3
	row8End := (by + bh + 7) >> 3
	if row8End > fs.MVFrame.IH8 {
		row8End = fs.MVFrame.IH8
	}
	refVal := uint8(0)
	if refFrame > 0 && refFrame <= header.RefsPerFrame {
		refVal = uint8(refFrame)
	}
	for y := row8Start; y < row8End; y++ {
		base := y * fs.MVFrame.RPStride
		for x := col8Start; x < col8End; x++ {
			idx := base + x
			if idx < 0 || idx >= len(fs.MVFrame.RP) {
				continue
			}
			fs.MVFrame.RP[idx] = refmvs.TemporalBlock{
				MV:  mv,
				Ref: refVal,
			}
		}
	}
}

func (fs *FrameState) setCurrentMVBlock(bx, by, bw, bh int, refFrame, interMode int, mv refmvs.MV) {
	if fs.MVFrame == nil {
		return
	}
	bw4 := (bw + 3) >> 2
	bh4 := (bh + 3) >> 2
	mf := uint8(0)
	if interMode == InterModeGlobalMV {
		mf |= 1
	}
	if interMode == InterModeNewMV {
		mf |= 2
	}
	blk := refmvs.Block{
		MV: refmvs.MVPair{
			mv,
			{},
		},
		Ref: refmvs.RefPair{int8(refFrame), -1},
		BS:  uint8(maxInt(bsizeFromDim(bw, bh), 0)),
		MF:  mf,
	}
	fs.MVFrame.PutGridBlock(bx>>2, by>>2, bw4, bh4, blk)
}

func (fs *FrameState) setCurrentCompoundMVBlock(bx, by, bw, bh int, av1Blk Av1Block) {
	if fs.MVFrame == nil {
		return
	}
	mf := uint8(0)
	if int(av1Blk.InterMode) == InterModeGlobalMV {
		mf |= 1
	}
	if int(av1Blk.InterMode) == InterModeNewMV {
		mf |= 2
	}
	fs.MVFrame.PutGridBlock(bx>>2, by>>2, (bw+3)>>2, (bh+3)>>2, refmvs.Block{
		MV: refmvs.MVPair{
			{Y: av1Blk.MV[0], X: av1Blk.MV[1]},
			{Y: av1Blk.MV2[0], X: av1Blk.MV2[1]},
		},
		Ref: refmvs.RefPair{av1Blk.RefFrame, av1Blk.RefFrame2},
		BS:  uint8(maxInt(bsizeFromDim(bw, bh), 0)),
		MF:  mf,
	})
}

func (fs *FrameState) NeighbourInterRef(bx, by int) (slot int, ok bool) {
	col4 := bx / 4
	row4 := by / 4
	if row4 > 0 && col4 < fs.W4 && fs.AbovePresent[col4] != 0 && fs.AboveRef[col4] >= 0 {
		return int(fs.AboveRef[col4]), true
	}
	if col4 > 0 && row4 < fs.H4 && fs.LeftPresent[row4] != 0 && fs.LeftRef[row4] >= 0 {
		return int(fs.LeftRef[row4]), true
	}
	return -1, false
}

func (fs *FrameState) NeighbourInterMV(bx, by int) (mv refmvs.MV, ok bool) {
	col4 := bx / 4
	row4 := by / 4
	if row4 > 0 && col4 < fs.W4 && fs.AbovePresent[col4] != 0 && fs.AboveRef[col4] >= 0 {
		return refmvs.MV{Y: fs.AboveMV[0][col4], X: fs.AboveMV[1][col4]}, true
	}
	if col4 > 0 && row4 < fs.H4 && fs.LeftPresent[row4] != 0 && fs.LeftRef[row4] >= 0 {
		return refmvs.MV{Y: fs.LeftMV[0][row4], X: fs.LeftMV[1][row4]}, true
	}
	return refmvs.MV{}, false
}

func (fs *FrameState) GridInterBlock(bx, by int) (refmvs.Block, bool) {
	if fs.MVFrame == nil {
		return refmvs.Block{}, false
	}
	return fs.MVFrame.GridBlock(bx>>2, by>>2)
}

func (fs *FrameState) NeighbourGridInterBlock(bx, by int) (refmvs.Block, bool) {
	if fs.MVFrame == nil {
		return refmvs.Block{}, false
	}
	if blk, ok := fs.MVFrame.GridBlock(bx>>2, (by>>2)-1); ok && !blk.Ref.IsIntra() {
		return blk, true
	}
	if blk, ok := fs.MVFrame.GridBlock((bx>>2)-1, by>>2); ok && !blk.Ref.IsIntra() {
		return blk, true
	}
	return refmvs.Block{}, false
}

func (fs *FrameState) PaletteYCtx(bx, by int) int {
	col4 := bx / 4
	row4 := by / 4
	above := 0
	if row4 > 0 && col4 < fs.W4 && fs.AbovePalY[col4] != 0 {
		above = 1
	}
	left := 0
	if col4 > 0 && row4 < fs.H4 && fs.LeftPalY[row4] != 0 {
		left = 1
	}
	return above + left
}

func (fs *FrameState) PaletteUVCtx(bx, by int) int {
	col4 := bx / 4
	row4 := by / 4
	if row4 > 0 && col4 < fs.W4 && fs.AbovePalUV[col4] != 0 {
		return 1
	}
	if col4 > 0 && row4 < fs.H4 && fs.LeftPalUV[row4] != 0 {
		return 1
	}
	return 0
}

func (fs *FrameState) SetPaletteCtx(bx, by, bw, bh, palY, palUV int) {
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
	palYVal := uint8(0)
	if palY > 0 {
		palYVal = uint8(palY)
	}
	palUVVal := uint8(0)
	if palUV > 0 {
		palUVVal = uint8(palUV)
	}
	for c := col4Start; c < col4End; c++ {
		fs.AbovePalY[c] = palYVal
		fs.AbovePalUV[c] = palUVVal
	}
	for r := row4Start; r < row4End; r++ {
		fs.LeftPalY[r] = palYVal
		fs.LeftPalUV[r] = palUVVal
	}
}

func (fs *FrameState) SetPaletteColors(bx, by, bw, bh int, pal [3][8]uint8) {
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
	for pl := 0; pl < 3; pl++ {
		for c := col4Start; c < col4End; c++ {
			fs.AbovePal[pl][c] = pal[pl]
		}
		for r := row4Start; r < row4End; r++ {
			fs.LeftPal[pl][r] = pal[pl]
		}
	}
}

func (fs *FrameState) SetUVModeState(bx, by, bw, bh int, mode uint8) {
	col4Start := bx / 4
	col4End := (bx + bw + 3) / 4
	if col4End > fs.CW4 {
		col4End = fs.CW4
	}
	row4Start := by / 4
	row4End := (by + bh + 3) / 4
	if row4End > fs.CH4 {
		row4End = fs.CH4
	}
	for c := col4Start; c < col4End && c < len(fs.AboveUVMode); c++ {
		fs.AboveUVMode[c] = mode
	}
	for r := row4Start; r < row4End && r < len(fs.LeftUVMode); r++ {
		fs.LeftUVMode[r] = mode
	}
}

func (fs *FrameState) coefEdges(plane int) (above, left []uint8) {
	if plane == 0 {
		return fs.AboveLCoef, fs.LeftLCoef
	}
	idx := plane - 1
	if idx < 0 {
		idx = 0
	} else if idx > 1 {
		idx = 1
	}
	return fs.AboveCCoef[idx], fs.LeftCCoef[idx]
}

func (fs *FrameState) coefEdgeLimits(plane int) (w4, h4 int) {
	if plane == 0 {
		return fs.W4, fs.H4
	}
	return fs.CW4, fs.CH4
}

func (fs *FrameState) CoefSkipCtx(plane, bx, by, bw, bh int, tx uint8) int {
	above, left := fs.coefEdges(plane)
	edgeW4, edgeH4 := fs.coefEdgeLimits(plane)
	td := transform.TxfmDimensions[tx]
	col4 := bx >> 2
	row4 := by >> 2
	bwLog := blockDimLog4(bw)
	bhLog := blockDimLog4(bh)
	txw4 := txSpan4(int(td.Lw))
	txh4 := txSpan4(int(td.Lh))

	if plane == 0 {
		if bwLog == int(td.Lw) && bhLog == int(td.Lh) {
			return 0
		}
		a := mergedCoefLow(above, col4, txw4, edgeW4)
		l := mergedCoefLow(left, row4, txh4, edgeH4)
		if a > 4 {
			a = 4
		}
		if l > 4 {
			l = 4
		}
		return int(DAV1DSkipCtx[a][l])
	}

	ssHor := int(fs.SsHor)
	ssVer := int(fs.SsVer)
	lumaBwLog := bwLog + ssHor
	lumaBhLog := bhLog + ssVer
	chromaBwLog := lumaBwLog
	chromaBhLog := lumaBhLog
	if chromaBwLog > 0 && ssHor != 0 {
		chromaBwLog--
	}
	if chromaBhLog > 0 && ssVer != 0 {
		chromaBhLog--
	}
	notOneBlk := boolInt(chromaBwLog > int(td.Lw) || chromaBhLog > int(td.Lh))
	ca := boolInt(!allCoefNeutral(above, col4, txw4, edgeW4))
	cl := boolInt(!allCoefNeutral(left, row4, txh4, edgeH4))
	return 7 + notOneBlk*3 + ca + cl
}

func (fs *FrameState) DCSignCtx(plane, bx, by int, tx uint8) int {
	above, left := fs.coefEdges(plane)
	edgeW4, edgeH4 := fs.coefEdgeLimits(plane)
	td := transform.TxfmDimensions[tx]
	col4 := bx >> 2
	row4 := by >> 2
	txw4 := txSpan4(int(td.Lw))
	txh4 := txSpan4(int(td.Lh))
	s := coefSignSum(above, col4, txw4, edgeW4) + coefSignSum(left, row4, txh4, edgeH4)
	return boolInt(s != 0) + boolInt(s > 0)
}

func (fs *FrameState) DCSignCtxBlock(plane, bx, by, bw, bh int) int {
	above, left := fs.coefEdges(plane)
	edgeW4, edgeH4 := fs.coefEdgeLimits(plane)
	col4 := bx >> 2
	row4 := by >> 2
	w4 := blockDim4Units(bw)
	h4 := blockDim4Units(bh)
	s := coefSignSum(above, col4, w4, edgeW4) + coefSignSum(left, row4, h4, edgeH4)
	return boolInt(s != 0) + boolInt(s > 0)
}

func (fs *FrameState) SetCoefCtx(plane, bx, by int, tx uint8, resCtx uint8) {
	above, left := fs.coefEdges(plane)
	edgeW4, edgeH4 := fs.coefEdgeLimits(plane)
	td := transform.TxfmDimensions[tx]
	col4 := bx >> 2
	row4 := by >> 2
	for i := 0; i < int(td.W) && col4+i < edgeW4; i++ {
		above[col4+i] = resCtx
	}
	for i := 0; i < int(td.H) && row4+i < edgeH4; i++ {
		left[row4+i] = resCtx
	}
}

func (fs *FrameState) SetCoefCtxBlock(plane, bx, by, bw, bh int, resCtx uint8) {
	above, left := fs.coefEdges(plane)
	edgeW4, edgeH4 := fs.coefEdgeLimits(plane)
	col4 := bx >> 2
	row4 := by >> 2
	w4 := blockDim4Units(bw)
	h4 := blockDim4Units(bh)
	for i := 0; i < w4 && col4+i < edgeW4; i++ {
		above[col4+i] = resCtx
	}
	for i := 0; i < h4 && row4+i < edgeH4; i++ {
		left[row4+i] = resCtx
	}
}

func (fs *FrameState) TxCtx(bx, by int, maxTx uint8) int {
	td := transform.TxfmDimensions[maxTx]
	col4 := bx >> 2
	row4 := by >> 2

	above := 0
	if by > fs.TileY0 && col4 < len(fs.AboveTx) && fs.AboveTx[col4] < td.Lw {
		above = 1
	}
	left := 0
	if bx > fs.TileX0 && row4 < len(fs.LeftTx) && fs.LeftTx[row4] < td.Lh {
		left = 1
	}
	return above + left
}

// IntraTxCtx mirrors dav1d get_tx_ctx and uses the separate tx_intra edge
// state. Larger or equal neighbouring transforms contribute to the context.
func (fs *FrameState) IntraTxCtx(bx, by int, maxTx uint8) int {
	td := transform.TxfmDimensions[maxTx]
	col4 := bx >> 2
	row4 := by >> 2
	ctx := 0
	if col4 < len(fs.AboveTxIntra) && fs.AboveTxIntra[col4] >= td.Lw {
		ctx++
	}
	if row4 < len(fs.LeftTxIntra) && fs.LeftTxIntra[row4] >= td.Lh {
		ctx++
	}
	return ctx
}

func (fs *FrameState) setTxIntraEdges(bx, by, bw, bh, txw, txh int) {
	col4, row4 := bx>>2, by>>2
	for i, n := 0, blockDim4Units(bw); i < n && col4+i < len(fs.AboveTxIntra); i++ {
		fs.AboveTxIntra[col4+i] = uint8(txw)
	}
	for i, n := 0, blockDim4Units(bh); i < n && row4+i < len(fs.LeftTxIntra); i++ {
		fs.LeftTxIntra[row4+i] = uint8(txh)
	}
}

func (fs *FrameState) SetIntraTxCtx(bx, by, bw, bh int, tx uint8) {
	td := transform.TxfmDimensions[tx]
	fs.SetTxCtx(bx, by, bw, bh, tx, true, false)
	fs.setTxIntraEdges(bx, by, bw, bh, int(td.Lw), int(td.Lh))
}

func (fs *FrameState) SetInterTxIntraCtx(bx, by, bw, bh int) {
	fs.setTxIntraEdges(bx, by, bw, bh, blockDimLog4(bw), blockDimLog4(bh))
}

func (fs *FrameState) SetTxCtx(bx, by, bw, bh int, tx uint8, switchable, skip bool) {
	col4 := bx >> 2
	row4 := by >> 2

	txw := blockDimLog4(bw)
	txh := blockDimLog4(bh)
	if switchable && !skip {
		td := transform.TxfmDimensions[tx]
		txw = int(td.Lw)
		txh = int(td.Lh)
	}

	w4 := blockDim4Units(bw)
	h4 := blockDim4Units(bh)
	for i := 0; i < w4 && col4+i < len(fs.AboveTx); i++ {
		fs.AboveTx[col4+i] = uint8(txw)
	}
	for i := 0; i < h4 && row4+i < len(fs.LeftTx); i++ {
		fs.LeftTx[row4+i] = uint8(txh)
	}
}

func maxCoefLow(v []uint8, start, n int) int {
	max := 0
	for i := 0; i < n && start+i < len(v); i++ {
		x := int(v[start+i] & 0x3f)
		if x > max {
			max = x
		}
	}
	return max
}

func mergedCoefLow(v []uint8, start, n, limit int) int {
	merged := 0
	for i := 0; i < n && start+i < len(v) && start+i < limit; i++ {
		merged |= int(v[start+i])
	}
	return merged & 0x3f
}

func coefEdgeMergeLow(v []uint8, start, txLog, limit int) int {
	n := 1 << txLog
	if txLog >= 4 {
		n = 16
	}
	return mergedCoefLow(v, start, n, limit)
}

func txSpan4(txLog int) int {
	if txLog <= 0 {
		return 1
	}
	if txLog >= 4 {
		return 16
	}
	return 1 << txLog
}

func allCoefNeutral(v []uint8, start, n, limit int) bool {
	for i := 0; i < n && start+i < len(v) && start+i < limit; i++ {
		if v[start+i] != 0x40 {
			return false
		}
	}
	return true
}

func coefEdgeAllNeutral(v []uint8, start, txLog, limit int) bool {
	n := 1 << txLog
	if txLog >= 4 {
		n = 16
	}
	return allCoefNeutral(v, start, n, limit)
}

func coefSignSum(v []uint8, start, n, limit int) int {
	sum := 0
	for i := 0; i < n && start+i < len(v) && start+i < limit; i++ {
		sum += int(v[start+i]>>6) - 1
	}
	return sum
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func blockDimLog4(px int) int {
	n := 0
	v := (px + 3) >> 2
	for v > 1 {
		v >>= 1
		n++
	}
	return n
}

func blockDim4Units(px int) int {
	return (px + 3) >> 2
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
