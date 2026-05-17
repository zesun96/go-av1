package obu

import (
	"github.com/zesun96/go-av1/internal/header"
)

// ----------------------------------------------------------------------------
// Tile, quant, segmentation, delta_q/lf.
// ----------------------------------------------------------------------------

func (p *frameParser) parseTileInfo() error {
	seq, hdr, gb := p.seq, p.hdr, p.gb

	if !seq.ReducedStillPictureHeader && hdr.DisableCDFUpdate == 0 {
		hdr.RefreshContext = 1
		if gb.Bit() != 0 {
			hdr.RefreshContext = 0
		}
	}

	hdr.Tiling.Uniform = uint8(gb.Bit())
	sb128 := uint8(0)
	if seq.SB128 {
		sb128 = 1
	}
	sbszMin1 := (64 << sb128) - 1
	sbszLog2 := 6 + int(sb128)
	sbw := (hdr.Width[0] + sbszMin1) >> sbszLog2
	sbh := (hdr.Height + sbszMin1) >> sbszLog2
	maxTileWidthSB := 4096 >> sbszLog2
	maxTileAreaSB := (4096 * 2304) >> (2 * sbszLog2)
	hdr.Tiling.MinLog2Cols = uint8(tileLog2(maxTileWidthSB, sbw))
	hdr.Tiling.MaxLog2Cols = uint8(tileLog2(1, imin(sbw, header.MaxTileCols)))
	hdr.Tiling.MaxLog2Rows = uint8(tileLog2(1, imin(sbh, header.MaxTileRows)))
	minLog2Tiles := imax(tileLog2(maxTileAreaSB, sbw*sbh), int(hdr.Tiling.MinLog2Cols))

	if hdr.Tiling.Uniform != 0 {
		hdr.Tiling.Log2Cols = hdr.Tiling.MinLog2Cols
		for hdr.Tiling.Log2Cols < hdr.Tiling.MaxLog2Cols && gb.Bit() != 0 {
			hdr.Tiling.Log2Cols++
		}
		tileW := 1 + ((sbw - 1) >> hdr.Tiling.Log2Cols)
		hdr.Tiling.Cols = 0
		for sbx := 0; sbx < sbw; sbx += tileW {
			hdr.Tiling.ColStartSB[hdr.Tiling.Cols] = uint16(sbx)
			hdr.Tiling.Cols++
		}
		hdr.Tiling.MinLog2Rows = uint8(imax(minLog2Tiles-int(hdr.Tiling.Log2Cols), 0))
		hdr.Tiling.Log2Rows = hdr.Tiling.MinLog2Rows
		for hdr.Tiling.Log2Rows < hdr.Tiling.MaxLog2Rows && gb.Bit() != 0 {
			hdr.Tiling.Log2Rows++
		}
		tileH := 1 + ((sbh - 1) >> hdr.Tiling.Log2Rows)
		hdr.Tiling.Rows = 0
		for sby := 0; sby < sbh; sby += tileH {
			hdr.Tiling.RowStartSB[hdr.Tiling.Rows] = uint16(sby)
			hdr.Tiling.Rows++
		}
	} else {
		hdr.Tiling.Cols = 0
		widest, areaSB := 0, sbw*sbh
		for sbx := 0; sbx < sbw && int(hdr.Tiling.Cols) < header.MaxTileCols; {
			tw := imin(sbw-sbx, maxTileWidthSB)
			tile := 1
			if tw > 1 {
				tile = 1 + int(gb.Uniform(uint32(tw)))
			}
			hdr.Tiling.ColStartSB[hdr.Tiling.Cols] = uint16(sbx)
			sbx += tile
			if tile > widest {
				widest = tile
			}
			hdr.Tiling.Cols++
		}
		hdr.Tiling.Log2Cols = uint8(tileLog2(1, int(hdr.Tiling.Cols)))
		if minLog2Tiles != 0 {
			areaSB >>= minLog2Tiles + 1
		}
		maxTileHeightSB := imax(areaSB/widest, 1)
		hdr.Tiling.Rows = 0
		for sby := 0; sby < sbh && int(hdr.Tiling.Rows) < header.MaxTileRows; {
			th := imin(sbh-sby, maxTileHeightSB)
			tile := 1
			if th > 1 {
				tile = 1 + int(gb.Uniform(uint32(th)))
			}
			hdr.Tiling.RowStartSB[hdr.Tiling.Rows] = uint16(sby)
			sby += tile
			hdr.Tiling.Rows++
		}
		hdr.Tiling.Log2Rows = uint8(tileLog2(1, int(hdr.Tiling.Rows)))
	}
	hdr.Tiling.ColStartSB[hdr.Tiling.Cols] = uint16(sbw)
	hdr.Tiling.RowStartSB[hdr.Tiling.Rows] = uint16(sbh)
	if hdr.Tiling.Log2Cols != 0 || hdr.Tiling.Log2Rows != 0 {
		hdr.Tiling.Update = uint16(gb.F(int(hdr.Tiling.Log2Cols + hdr.Tiling.Log2Rows)))
		if hdr.Tiling.Update >= uint16(hdr.Tiling.Cols)*uint16(hdr.Tiling.Rows) {
			return ErrInvalidTileUpdate
		}
		hdr.Tiling.NBytes = uint8(gb.F(2)) + 1
	}
	return nil
}

func (p *frameParser) parseQuant() {
	seq, hdr, gb := p.seq, p.hdr, p.gb

	hdr.Quant.YAC = uint8(gb.F(8))
	if gb.Bit() != 0 {
		hdr.Quant.YDCDelta = int8(gb.SU(7))
	}
	if !seq.Monochrome {
		diffUV := uint32(0)
		if seq.SeparateUVDeltaQ {
			diffUV = gb.Bit()
		}
		if gb.Bit() != 0 {
			hdr.Quant.UDCDelta = int8(gb.SU(7))
		}
		if gb.Bit() != 0 {
			hdr.Quant.UACDelta = int8(gb.SU(7))
		}
		if diffUV != 0 {
			if gb.Bit() != 0 {
				hdr.Quant.VDCDelta = int8(gb.SU(7))
			}
			if gb.Bit() != 0 {
				hdr.Quant.VACDelta = int8(gb.SU(7))
			}
		} else {
			hdr.Quant.VDCDelta = hdr.Quant.UDCDelta
			hdr.Quant.VACDelta = hdr.Quant.UACDelta
		}
	}
	hdr.Quant.QM = uint8(gb.Bit())
	if hdr.Quant.QM != 0 {
		hdr.Quant.QMY = uint8(gb.F(4))
		hdr.Quant.QMU = uint8(gb.F(4))
		if seq.SeparateUVDeltaQ {
			hdr.Quant.QMV = uint8(gb.F(4))
		} else {
			hdr.Quant.QMV = hdr.Quant.QMU
		}
	}
}

func (p *frameParser) parseSegmentation() error {
	hdr, gb := p.hdr, p.gb
	hdr.Segmentation.Enabled = uint8(gb.Bit())
	if hdr.Segmentation.Enabled != 0 {
		if hdr.PrimaryRefFrame == header.PrimaryRefNone {
			hdr.Segmentation.UpdateMap = 1
			hdr.Segmentation.UpdateData = 1
		} else {
			hdr.Segmentation.UpdateMap = uint8(gb.Bit())
			if hdr.Segmentation.UpdateMap != 0 {
				hdr.Segmentation.Temporal = uint8(gb.Bit())
			}
			hdr.Segmentation.UpdateData = uint8(gb.Bit())
		}
		if hdr.Segmentation.UpdateData != 0 {
			hdr.Segmentation.SegData.LastActiveSegID = -1
			for i := 0; i < header.MaxSegments; i++ {
				seg := &hdr.Segmentation.SegData.D[i]
				if gb.Bit() != 0 {
					seg.DeltaQ = int16(gb.SU(9))
					hdr.Segmentation.SegData.LastActiveSegID = int8(i)
				}
				if gb.Bit() != 0 {
					seg.DeltaLFYV = int8(gb.SU(7))
					hdr.Segmentation.SegData.LastActiveSegID = int8(i)
				}
				if gb.Bit() != 0 {
					seg.DeltaLFYH = int8(gb.SU(7))
					hdr.Segmentation.SegData.LastActiveSegID = int8(i)
				}
				if gb.Bit() != 0 {
					seg.DeltaLFU = int8(gb.SU(7))
					hdr.Segmentation.SegData.LastActiveSegID = int8(i)
				}
				if gb.Bit() != 0 {
					seg.DeltaLFV = int8(gb.SU(7))
					hdr.Segmentation.SegData.LastActiveSegID = int8(i)
				}
				if gb.Bit() != 0 {
					seg.Ref = int8(gb.F(3))
					hdr.Segmentation.SegData.LastActiveSegID = int8(i)
					hdr.Segmentation.SegData.PreSkip = 1
				} else {
					seg.Ref = -1
				}
				seg.Skip = uint8(gb.Bit())
				if seg.Skip != 0 {
					hdr.Segmentation.SegData.LastActiveSegID = int8(i)
					hdr.Segmentation.SegData.PreSkip = 1
				}
				seg.GlobalMV = uint8(gb.Bit())
				if seg.GlobalMV != 0 {
					hdr.Segmentation.SegData.LastActiveSegID = int8(i)
					hdr.Segmentation.SegData.PreSkip = 1
				}
			}
		} else {
			if p.opts.Refs == nil {
				return ErrRefsRequired
			}
			ref := p.opts.Refs[hdr.Refidx[hdr.PrimaryRefFrame]].FrameHdr
			if ref == nil {
				return ErrRefsRequired
			}
			hdr.Segmentation.SegData = ref.Segmentation.SegData
		}
	} else {
		for i := 0; i < header.MaxSegments; i++ {
			hdr.Segmentation.SegData.D[i].Ref = -1
		}
	}
	return nil
}

func (p *frameParser) parseDeltaQLF() {
	hdr, gb := p.hdr, p.gb
	if hdr.Quant.YAC == 0 {
		return
	}
	hdr.Delta.Q.Present = uint8(gb.Bit())
	if hdr.Delta.Q.Present == 0 {
		return
	}
	hdr.Delta.Q.ResLog2 = uint8(gb.F(2))
	if hdr.AllowIntrabc != 0 {
		return
	}
	hdr.Delta.LF.Present = uint8(gb.Bit())
	if hdr.Delta.LF.Present != 0 {
		hdr.Delta.LF.ResLog2 = uint8(gb.F(2))
		hdr.Delta.LF.Multi = uint8(gb.Bit())
	}
}

func (p *frameParser) deriveLossless() {
	hdr := p.hdr
	deltaLossless := hdr.Quant.YDCDelta == 0 && hdr.Quant.UDCDelta == 0 &&
		hdr.Quant.UACDelta == 0 && hdr.Quant.VDCDelta == 0 && hdr.Quant.VACDelta == 0
	hdr.AllLossless = 1
	for i := 0; i < header.MaxSegments; i++ {
		var qidx uint8
		if hdr.Segmentation.Enabled != 0 {
			qidx = clipU8(int(hdr.Quant.YAC) + int(hdr.Segmentation.SegData.D[i].DeltaQ))
		} else {
			qidx = hdr.Quant.YAC
		}
		hdr.Segmentation.QIdx[i] = qidx
		lossless := uint8(0)
		if qidx == 0 && deltaLossless {
			lossless = 1
		}
		hdr.Segmentation.Lossless[i] = lossless
		hdr.AllLossless &= lossless
	}
}

// ----------------------------------------------------------------------------
// Loop filter, CDEF, loop restoration, tx mode.
// ----------------------------------------------------------------------------

func (p *frameParser) parseLoopFilter() error {
	seq, hdr, gb := p.seq, p.hdr, p.gb
	if hdr.AllLossless != 0 || hdr.AllowIntrabc != 0 {
		hdr.LoopFilter.ModeRefDeltaEnabled = 1
		hdr.LoopFilter.ModeRefDeltaUpdate = 1
		hdr.LoopFilter.ModeRefDeltas = header.DefaultLoopfilterModeRefDeltas
		return nil
	}
	hdr.LoopFilter.LevelY[0] = uint8(gb.F(6))
	hdr.LoopFilter.LevelY[1] = uint8(gb.F(6))
	if !seq.Monochrome && (hdr.LoopFilter.LevelY[0] != 0 || hdr.LoopFilter.LevelY[1] != 0) {
		hdr.LoopFilter.LevelU = uint8(gb.F(6))
		hdr.LoopFilter.LevelV = uint8(gb.F(6))
	}
	hdr.LoopFilter.Sharpness = uint8(gb.F(3))
	if hdr.PrimaryRefFrame == header.PrimaryRefNone {
		hdr.LoopFilter.ModeRefDeltas = header.DefaultLoopfilterModeRefDeltas
	} else {
		if p.opts.Refs == nil {
			return ErrRefsRequired
		}
		ref := p.opts.Refs[hdr.Refidx[hdr.PrimaryRefFrame]].FrameHdr
		if ref == nil {
			return ErrRefsRequired
		}
		hdr.LoopFilter.ModeRefDeltas = ref.LoopFilter.ModeRefDeltas
	}
	hdr.LoopFilter.ModeRefDeltaEnabled = uint8(gb.Bit())
	if hdr.LoopFilter.ModeRefDeltaEnabled != 0 {
		hdr.LoopFilter.ModeRefDeltaUpdate = uint8(gb.Bit())
		if hdr.LoopFilter.ModeRefDeltaUpdate != 0 {
			for i := 0; i < 8; i++ {
				if gb.Bit() != 0 {
					hdr.LoopFilter.ModeRefDeltas.RefDelta[i] = int8(gb.SU(7))
				}
			}
			for i := 0; i < 2; i++ {
				if gb.Bit() != 0 {
					hdr.LoopFilter.ModeRefDeltas.ModeDelta[i] = int8(gb.SU(7))
				}
			}
		}
	}
	return nil
}

func (p *frameParser) parseCDEF() {
	seq, hdr, gb := p.seq, p.hdr, p.gb
	if hdr.AllLossless != 0 || !seq.CDEF || hdr.AllowIntrabc != 0 {
		return
	}
	hdr.CDEF.Damping = uint8(gb.F(2)) + 3
	hdr.CDEF.NBits = uint8(gb.F(2))
	n := 1 << hdr.CDEF.NBits
	for i := 0; i < n; i++ {
		hdr.CDEF.YStrength[i] = uint8(gb.F(6))
		if !seq.Monochrome {
			hdr.CDEF.UVStrength[i] = uint8(gb.F(6))
		}
	}
}

func (p *frameParser) parseLR() {
	seq, hdr, gb := p.seq, p.hdr, p.gb
	if !((hdr.AllLossless == 0 || hdr.SuperRes.Enabled != 0) && seq.Restoration && hdr.AllowIntrabc == 0) {
		return
	}
	hdr.Restoration.Type[0] = header.RestorationType(gb.F(2))
	if !seq.Monochrome {
		hdr.Restoration.Type[1] = header.RestorationType(gb.F(2))
		hdr.Restoration.Type[2] = header.RestorationType(gb.F(2))
	}
	if hdr.Restoration.Type[0] != header.RestorationNone ||
		hdr.Restoration.Type[1] != header.RestorationNone ||
		hdr.Restoration.Type[2] != header.RestorationNone {
		sb128 := uint8(0)
		if seq.SB128 {
			sb128 = 1
		}
		hdr.Restoration.UnitSize[0] = 6 + sb128
		if gb.Bit() != 0 {
			hdr.Restoration.UnitSize[0]++
			if !seq.SB128 {
				hdr.Restoration.UnitSize[0] += uint8(gb.Bit())
			}
		}
		hdr.Restoration.UnitSize[1] = hdr.Restoration.UnitSize[0]
		if (hdr.Restoration.Type[1] != header.RestorationNone ||
			hdr.Restoration.Type[2] != header.RestorationNone) &&
			seq.SsHor == 1 && seq.SsVer == 1 {
			hdr.Restoration.UnitSize[1] -= uint8(gb.Bit())
		}
	} else {
		hdr.Restoration.UnitSize[0] = 8
	}
}

func (p *frameParser) parseTxfmMode() {
	hdr, gb := p.hdr, p.gb
	if hdr.AllLossless != 0 {
		return
	}
	if gb.Bit() != 0 {
		hdr.TxfmMode = header.TxfmModeSwitchable
	} else {
		hdr.TxfmMode = header.TxfmModeLargest
	}
}

// ----------------------------------------------------------------------------
// Skip mode, warp motion, global mv, film grain.
// ----------------------------------------------------------------------------

func (p *frameParser) parseSkipMode() error {
	seq, hdr, gb := p.seq, p.hdr, p.gb
	if !hdr.FrameType.IsIntra() {
		hdr.SwitchableCompRefs = uint8(gb.Bit())
	}
	if hdr.SwitchableCompRefs != 0 && !hdr.FrameType.IsIntra() && seq.OrderHint {
		if err := p.selectSkipModeRefs(); err != nil {
			return err
		}
	}
	if hdr.SkipModeAllowed != 0 {
		hdr.SkipModeEnabled = uint8(gb.Bit())
	}
	return nil
}

func (p *frameParser) selectSkipModeRefs() error {
	hdr := p.hdr
	if p.opts.Refs == nil {
		return ErrRefsRequired
	}
	nBits := int(p.seq.OrderHintNBits)
	poc := int(hdr.FrameOffset)
	offBefore, offAfter := -1, -1
	offBeforeIdx, offAfterIdx := -1, -1
	for i := 0; i < header.RefsPerFrame; i++ {
		ref := p.opts.Refs[hdr.Refidx[i]].FrameHdr
		if ref == nil {
			return ErrRefsRequired
		}
		refpoc := int(ref.FrameOffset)
		diff := getPOCDiff(nBits, refpoc, poc)
		switch {
		case diff > 0:
			if offAfter < 0 || getPOCDiff(nBits, offAfter, refpoc) > 0 {
				offAfter = refpoc
				offAfterIdx = i
			}
		case diff < 0:
			if offBefore < 0 || getPOCDiff(nBits, refpoc, offBefore) > 0 {
				offBefore = refpoc
				offBeforeIdx = i
			}
		}
	}
	if offBefore >= 0 && offAfter >= 0 {
		hdr.SkipModeRefs[0] = int8(imin(offBeforeIdx, offAfterIdx))
		hdr.SkipModeRefs[1] = int8(imax(offBeforeIdx, offAfterIdx))
		hdr.SkipModeAllowed = 1
	} else if offBefore >= 0 {
		offBefore2, offBefore2Idx := -1, -1
		for i := 0; i < header.RefsPerFrame; i++ {
			ref := p.opts.Refs[hdr.Refidx[i]].FrameHdr
			if ref == nil {
				return ErrRefsRequired
			}
			refpoc := int(ref.FrameOffset)
			if getPOCDiff(nBits, refpoc, offBefore) < 0 {
				if offBefore2 < 0 || getPOCDiff(nBits, refpoc, offBefore2) > 0 {
					offBefore2 = refpoc
					offBefore2Idx = i
				}
			}
		}
		if offBefore2 >= 0 {
			hdr.SkipModeRefs[0] = int8(imin(offBeforeIdx, offBefore2Idx))
			hdr.SkipModeRefs[1] = int8(imax(offBeforeIdx, offBefore2Idx))
			hdr.SkipModeAllowed = 1
		}
	}
	return nil
}

func (p *frameParser) parseWarpMotion() {
	seq, hdr, gb := p.seq, p.hdr, p.gb
	if hdr.ErrorResilientMode == 0 && !hdr.FrameType.IsIntra() && seq.WarpedMotion {
		hdr.WarpMotion = uint8(gb.Bit())
	}
}

func (p *frameParser) parseGlobalMV() error {
	hdr, gb := p.hdr, p.gb
	for i := 0; i < header.RefsPerFrame; i++ {
		hdr.GMV[i] = defaultWarpParams
	}
	if hdr.FrameType.IsIntra() {
		return nil
	}
	for i := 0; i < header.RefsPerFrame; i++ {
		typ := header.WMTypeIdentity
		if gb.Bit() != 0 {
			if gb.Bit() != 0 {
				typ = header.WMTypeRotZoom
			} else if gb.Bit() != 0 {
				typ = header.WMTypeTranslation
			} else {
				typ = header.WMTypeAffine
			}
		}
		hdr.GMV[i].Type = typ
		if typ == header.WMTypeIdentity {
			continue
		}
		var refGMV header.WarpedMotionParams
		if hdr.PrimaryRefFrame == header.PrimaryRefNone {
			refGMV = defaultWarpParams
		} else {
			if p.opts.Refs == nil {
				return ErrRefsRequired
			}
			ref := p.opts.Refs[hdr.Refidx[hdr.PrimaryRefFrame]].FrameHdr
			if ref == nil {
				return ErrRefsRequired
			}
			refGMV = ref.GMV[i]
		}
		mat := &hdr.GMV[i].Matrix
		ref := &refGMV.Matrix
		bits, shift := 0, 0
		if typ >= header.WMTypeRotZoom {
			mat[2] = (1 << 16) + 2*gb.BitsSubexp((ref[2]-(1<<16))>>1, 12)
			mat[3] = 2 * gb.BitsSubexp(ref[3]>>1, 12)
			bits, shift = 12, 10
		} else {
			if hdr.HP != 0 {
				bits, shift = 9, 13
			} else {
				bits, shift = 8, 14
			}
		}
		if typ == header.WMTypeAffine {
			mat[4] = 2 * gb.BitsSubexp(ref[4]>>1, 12)
			mat[5] = (1 << 16) + 2*gb.BitsSubexp((ref[5]-(1<<16))>>1, 12)
		} else {
			mat[4] = -mat[3]
			mat[5] = mat[2]
		}
		mat[0] = gb.BitsSubexp(ref[0]>>shift, uint32(bits)) * (1 << shift)
		mat[1] = gb.BitsSubexp(ref[1]>>shift, uint32(bits)) * (1 << shift)
	}
	return nil
}

func (p *frameParser) parseFilmGrain() error {
	seq, hdr, gb := p.seq, p.hdr, p.gb
	if !seq.FilmGrainPresent || (hdr.ShowFrame == 0 && hdr.ShowableFrame == 0) {
		return nil
	}
	hdr.FilmGrain.Present = uint8(gb.Bit())
	if hdr.FilmGrain.Present == 0 {
		return nil
	}
	seed := gb.F(16)
	update := uint8(1)
	if hdr.FrameType == header.FrameTypeInter {
		update = uint8(gb.Bit())
	}
	hdr.FilmGrain.Update = update
	if update == 0 {
		refidx := int8(gb.F(3))
		found := false
		for i := 0; i < header.RefsPerFrame; i++ {
			if hdr.Refidx[i] == refidx {
				found = true
				break
			}
		}
		if !found {
			return ErrInvalidFilmGrainRef
		}
		if p.opts.Refs == nil {
			return ErrRefsRequired
		}
		ref := p.opts.Refs[refidx].FrameHdr
		if ref == nil {
			return ErrRefsRequired
		}
		hdr.FilmGrain.Data = ref.FilmGrain.Data
		hdr.FilmGrain.Data.Seed = seed
		return nil
	}
	return p.parseFilmGrainData(seed)
}

func (p *frameParser) parseFilmGrainData(seed uint32) error {
	seq, hdr, gb := p.seq, p.hdr, p.gb
	fgd := &hdr.FilmGrain.Data
	fgd.Seed = seed
	fgd.NumYPoints = int(gb.F(4))
	if fgd.NumYPoints > 14 {
		return ErrInvalidFilmGrainPoints
	}
	for i := 0; i < fgd.NumYPoints; i++ {
		fgd.YPoints[i][0] = uint8(gb.F(8))
		if i > 0 && fgd.YPoints[i-1][0] >= fgd.YPoints[i][0] {
			return ErrInvalidFilmGrainPoints
		}
		fgd.YPoints[i][1] = uint8(gb.F(8))
	}
	if !seq.Monochrome {
		fgd.ChromaScalingFromLuma = int(gb.Bit())
	}
	useUV := !(seq.Monochrome || fgd.ChromaScalingFromLuma != 0 ||
		(seq.SsVer == 1 && seq.SsHor == 1 && fgd.NumYPoints == 0))
	if !useUV {
		fgd.NumUVPoints[0], fgd.NumUVPoints[1] = 0, 0
	} else {
		for pl := 0; pl < 2; pl++ {
			fgd.NumUVPoints[pl] = int(gb.F(4))
			if fgd.NumUVPoints[pl] > 10 {
				return ErrInvalidFilmGrainPoints
			}
			for i := 0; i < fgd.NumUVPoints[pl]; i++ {
				fgd.UVPoints[pl][i][0] = uint8(gb.F(8))
				if i > 0 && fgd.UVPoints[pl][i-1][0] >= fgd.UVPoints[pl][i][0] {
					return ErrInvalidFilmGrainPoints
				}
				fgd.UVPoints[pl][i][1] = uint8(gb.F(8))
			}
		}
	}
	if seq.SsHor == 1 && seq.SsVer == 1 &&
		(fgd.NumUVPoints[0] != 0) != (fgd.NumUVPoints[1] != 0) {
		return ErrInvalidChromaScaling
	}
	fgd.ScalingShift = int(gb.F(2)) + 8
	fgd.ARCoeffLag = int(gb.F(2))
	numYPos := 2 * fgd.ARCoeffLag * (fgd.ARCoeffLag + 1)
	if fgd.NumYPoints != 0 {
		for i := 0; i < numYPos; i++ {
			fgd.ARCoeffsY[i] = int8(int(gb.F(8)) - 128)
		}
	}
	for pl := 0; pl < 2; pl++ {
		if fgd.NumUVPoints[pl] != 0 || fgd.ChromaScalingFromLuma != 0 {
			numUVPos := numYPos
			if fgd.NumYPoints != 0 {
				numUVPos++
			}
			for i := 0; i < numUVPos; i++ {
				fgd.ARCoeffsUV[pl][i] = int8(int(gb.F(8)) - 128)
			}
			if fgd.NumYPoints == 0 {
				fgd.ARCoeffsUV[pl][numUVPos] = 0
			}
		}
	}
	fgd.ARCoeffShift = uint64(gb.F(2)) + 6
	fgd.GrainScaleShift = int(gb.F(2))
	for pl := 0; pl < 2; pl++ {
		if fgd.NumUVPoints[pl] != 0 {
			fgd.UVMult[pl] = int(gb.F(8)) - 128
			fgd.UVLumaMult[pl] = int(gb.F(8)) - 128
			fgd.UVOffset[pl] = int(gb.F(9)) - 256
		}
	}
	fgd.OverlapFlag = int(gb.Bit())
	fgd.ClipToRestrictedRange = int(gb.Bit())
	return nil
}

// ----------------------------------------------------------------------------
// Small utility helpers.
// ----------------------------------------------------------------------------

func tileLog2(sz, tgt int) int {
	k := 0
	for (sz << k) < tgt {
		k++
	}
	return k
}

func imin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func imax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clipU8(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
