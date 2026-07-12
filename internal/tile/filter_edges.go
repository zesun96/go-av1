package tile

import "github.com/zesun96/go-av1/internal/transform"

import "github.com/zesun96/go-av1/internal/header"

// LumaFilterEdge reports whether the 4x4-grid boundary at (x4,y4) is a
// deblocking candidate and its normative luma filter width (4, 8, or 16).
// vertical selects a left/right boundary; otherwise it selects a top/bottom
// boundary.
func (fs *FrameState) LumaFilterEdge(x4, y4 int, vertical bool) (int, bool) {
	if fs == nil || x4 < 0 || y4 < 0 || x4 >= fs.W4 || y4 >= fs.H4 {
		return 0, false
	}
	a, b := y4*fs.W4+x4, 0
	if vertical {
		if x4 == 0 {
			return 0, false
		}
		b = a - 1
	} else {
		if y4 == 0 {
			return 0, false
		}
		b = a - fs.W4
	}
	if fs.TxGrid[a] == 0xff || fs.TxGrid[b] == 0xff {
		return 0, false
	}
	blockEdge := fs.BlockGrid[a].X4 != fs.BlockGrid[b].X4 || fs.BlockGrid[a].Y4 != fs.BlockGrid[b].Y4
	txEdge := fs.TxOriginX4[a] != fs.TxOriginX4[b] || fs.TxOriginY4[a] != fs.TxOriginY4[b]
	if !blockEdge && (!txEdge || (!fs.BlockGrid[a].Intra && fs.BlockGrid[a].Skip)) {
		return 0, false
	}
	da := transform.TxfmDimensions[fs.TxGrid[a]]
	db := transform.TxfmDimensions[fs.TxGrid[b]]
	logSizeA, logSizeB := int(da.Lh), int(db.Lh)
	if vertical {
		logSizeA, logSizeB = int(da.Lw), int(db.Lw)
	}
	logSize := minInt(logSizeA, logSizeB)
	return 4 << minInt(logSize, 2), true
}

// LumaFilterLevel calculates the block-level luma strength using segmentation,
// delta-lf, and mode/reference deltas. vertical selects LevelY[0].
func (fs *FrameState) LumaFilterLevel(fhdr *header.FrameHeader, x4, y4 int, vertical bool) int {
	if fs == nil || fhdr == nil || x4 < 0 || y4 < 0 || x4 >= fs.W4 || y4 >= fs.H4 {
		return 0
	}
	blk := fs.BlockGrid[y4*fs.W4+x4]
	dir := 0
	if !vertical {
		dir = 1
	}
	base := int(fhdr.LoopFilter.LevelY[dir])
	deltaIdx := 0
	if fhdr.Delta.LF.Multi != 0 {
		deltaIdx = dir
	}
	base = clampInt(base+int(blk.LFDelta[deltaIdx]), 0, 63)
	if fhdr.Segmentation.Enabled != 0 {
		seg := fhdr.Segmentation.SegData.D[blk.SegID]
		if vertical {
			base += int(seg.DeltaLFYV)
		} else {
			base += int(seg.DeltaLFYH)
		}
		base = clampInt(base, 0, 63)
	}
	if fhdr.LoopFilter.ModeRefDeltaEnabled == 0 {
		return base
	}
	ref, mode := 0, 0
	if !blk.Intra {
		ref = clampInt(int(blk.RefFrame)+1, 1, len(fhdr.LoopFilter.ModeRefDeltas.RefDelta)-1)
		if blk.InterMode != InterModeZeroMV && blk.InterMode != InterModeGlobalMV {
			mode = 1
		}
	}
	shift := 1
	if base >= 32 {
		shift = 2
	}
	delta := int(fhdr.LoopFilter.ModeRefDeltas.RefDelta[ref])
	if ref != 0 {
		delta += int(fhdr.LoopFilter.ModeRefDeltas.ModeDelta[mode])
	}
	return clampInt(base+delta*shift, 0, 63)
}

// ChromaFilterEdge reports a U/V edge candidate in chroma 4x4 units.
func (fs *FrameState) ChromaFilterEdge(x4, y4 int, vertical bool) (int, bool) {
	if fs == nil || x4 < 0 || y4 < 0 || x4 >= fs.CW4 || y4 >= fs.CH4 {
		return 0, false
	}
	blockAt := func(cx4, cy4 int) Av1Block {
		return fs.ChromaBlockGrid[cy4*fs.CW4+cx4]
	}
	a := blockAt(x4, y4)
	b := Av1Block{}
	if vertical {
		if x4 == 0 {
			return 0, false
		}
		b = blockAt(x4-1, y4)
	} else {
		if y4 == 0 {
			return 0, false
		}
		b = blockAt(x4, y4-1)
	}
	blockEdge := a.X4 != b.X4 || a.Y4 != b.Y4
	txOrigin := func(blk Av1Block, cx4, cy4 int) (int, int) {
		td := transform.TxfmDimensions[blk.Uvtx]
		bx4, by4 := int(blk.X4)>>fs.SsHor, int(blk.Y4)>>fs.SsVer
		return bx4 + ((cx4-bx4)/int(td.W))*int(td.W), by4 + ((cy4-by4)/int(td.H))*int(td.H)
	}
	ax, ay := txOrigin(a, x4, y4)
	bx, by := x4, y4
	if vertical {
		bx--
	} else {
		by--
	}
	bx, by = txOrigin(b, bx, by)
	txEdge := ax != bx || ay != by
	if !blockEdge && (!txEdge || (!a.Intra && a.Skip)) {
		return 0, false
	}
	da, db := transform.TxfmDimensions[a.Uvtx], transform.TxfmDimensions[b.Uvtx]
	szA, szB := int(da.H), int(db.H)
	if vertical {
		szA, szB = int(da.W), int(db.W)
	}
	if minInt(szA, szB) > 1 {
		return 6, true
	}
	return 4, true
}

// ChromaFilterLevel calculates the U or V block-level strength.
func (fs *FrameState) ChromaFilterLevel(fhdr *header.FrameHeader, x4, y4, plane int) int {
	if fs == nil || fhdr == nil || x4 < 0 || y4 < 0 || x4 >= fs.CW4 || y4 >= fs.CH4 {
		return 0
	}
	blk := fs.ChromaBlockGrid[y4*fs.CW4+x4]
	base, deltaIdx := int(fhdr.LoopFilter.LevelU), 2
	if plane == 2 {
		base, deltaIdx = int(fhdr.LoopFilter.LevelV), 3
	}
	if base == 0 {
		return 0
	}
	if fhdr.Delta.LF.Multi == 0 {
		deltaIdx = 0
	}
	base = clampInt(base+int(blk.LFDelta[deltaIdx]), 0, 63)
	if fhdr.Segmentation.Enabled != 0 {
		seg := fhdr.Segmentation.SegData.D[blk.SegID]
		if plane == 2 {
			base += int(seg.DeltaLFV)
		} else {
			base += int(seg.DeltaLFU)
		}
		base = clampInt(base, 0, 63)
	}
	if fhdr.LoopFilter.ModeRefDeltaEnabled == 0 {
		return base
	}
	ref, mode := 0, 0
	if !blk.Intra {
		ref = clampInt(int(blk.RefFrame)+1, 1, len(fhdr.LoopFilter.ModeRefDeltas.RefDelta)-1)
		if blk.InterMode != InterModeZeroMV && blk.InterMode != InterModeGlobalMV {
			mode = 1
		}
	}
	shift := 1
	if base >= 32 {
		shift = 2
	}
	delta := int(fhdr.LoopFilter.ModeRefDeltas.RefDelta[ref])
	if ref != 0 {
		delta += int(fhdr.LoopFilter.ModeRefDeltas.ModeDelta[mode])
	}
	return clampInt(base+delta*shift, 0, 63)
}
