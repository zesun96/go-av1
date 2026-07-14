// group.go provides the top-level tile-group decode entry point used by the
// pkg/av1 decoder pipeline.
package tile

import (
	"fmt"
	"os"

	"github.com/zesun96/go-av1/internal/header"
	"github.com/zesun96/go-av1/internal/refmvs"
)

// DecodeTileGroup parses a tile_group_obu() payload (or the tile portion of
// an OBU_FRAME) and reconstructs all tiles into fb.
//
// logf, if non-nil, receives diagnostic messages (tile boundaries, parse
// errors). Pass nil to suppress all logging.
func DecodeTileGroup(
	payload []byte,
	fhdr *header.FrameHeader,
	seq *header.SequenceHeader,
	fb *FrameBuf,
	logf func(string, ...any),
) error {
	_, err := DecodeTileGroupWithContext(payload, fhdr, seq, fb, nil, logf)
	return err
}

// DecodeTileGroupWithContext decodes all tiles from the same frame CDF state
// and returns the context-update tile's final adaptive state.
func DecodeTileGroupWithContext(
	payload []byte,
	fhdr *header.FrameHeader,
	seq *header.SequenceHeader,
	fb *FrameBuf,
	initial *TileCtx,
	logf func(string, ...any),
) (*TileCtx, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if os.Getenv("GOAV1_TRACE_SYMBOLS") != "" {
		logf("sym frame offset=%d type=%d show=%d refresh=%02x primary=%d refidx=%v comp_refs=%d cdf_update=%d refresh_cdf=%d tile_update=%d qidx=%d qm=%d qmy=%d qmu=%d qmv=%d",
			fhdr.FrameOffset, fhdr.FrameType, fhdr.ShowFrame, fhdr.RefreshFrameFlags,
			fhdr.PrimaryRefFrame, fhdr.Refidx, fhdr.SwitchableCompRefs, fhdr.DisableCDFUpdate, fhdr.RefreshContext, fhdr.Tiling.Update,
			fhdr.Quant.YAC, fhdr.Quant.QM,
			fhdr.Quant.QMY, fhdr.Quant.QMU, fhdr.Quant.QMV)
		if fhdr.Segmentation.Enabled != 0 {
			logf("sym frame_segments=%v", fhdr.Segmentation.SegData.D)
		}
		if initial != nil {
			logf("sym cdf_in partition64_0=%v skip_0=%v comp_0=%v", initial.Partition64CDF[0], initial.SkipCDF[0], initial.CompCDF[0])
		}
	}
	logf("tile: DecodeTileGroup payloadLen=%d numTiles=%d×%d NBytes=%d",
		len(payload), fhdr.Tiling.Cols, fhdr.Tiling.Rows, fhdr.Tiling.NBytes)
	tiles, err := ParseTileGroup(payload, fhdr)
	if err != nil {
		return nil, fmt.Errorf("parse tile group: %w", err)
	}
	if len(tiles) == 0 {
		return nil, fmt.Errorf("parse tile group: no tiles")
	}
	logf("tile: ParseTileGroup → %d tiles", len(tiles))

	chromaDbg := newChromaDebugStats()
	activeChromaDebug = chromaDbg
	defer func() {
		activeChromaDebug = nil
	}()

	if fb.MVFrame == nil {
		fb.MVFrame = refmvs.NewFrame(fb.Width, fb.Height)
	}
	if fb.FilterState == nil || fb.FilterState.Width != fb.Width || fb.FilterState.Height != fb.Height {
		fb.FilterState = NewFrameState(fb.Width, fb.Height)
		fb.FilterState.SetSubsampling(seq.SsHor, seq.SsVer)
	}
	fb.MVFrame.OrderHint = int(fhdr.FrameOffset)
	fb.MVFrame.OrderBits = seq.OrderHintNBits
	fb.MVFrame.HighPrecision = fhdr.HP != 0
	fb.MVFrame.ForceInteger = fhdr.ForceIntegerMV != 0
	for i, ref := range fb.RefMVs {
		if ref != nil {
			fb.MVFrame.RefOrderHints[i] = ref.OrderHint
		}
	}
	for i, slot := range fhdr.Refidx {
		fb.MVFrame.RefSlots[i] = slot
		if slot >= 0 && int(slot) < len(fb.RefMVs) && fb.RefMVs[slot] != nil {
			fb.MVFrame.RefFrameOrderHints[i] = fb.RefMVs[slot].OrderHint
		}
	}
	var updateCtx *TileCtx
	for tileIndex, td := range tiles {
		// Tile entropy and neighbour state is independent. Full-frame indexing
		// is retained so block coordinates remain absolute, but no above/left
		// context may leak across a tile boundary.
		fs := NewFrameState(fb.Width, fb.Height)
		if os.Getenv("GOAV1_TRACE_SYMBOLS") != "" {
			fs.Tracef = logf
		}
		fs.SetSubsampling(seq.SsHor, seq.SsVer)
		sbSize := 64
		if seq.SB128 {
			sbSize = 128
		}
		fs.TileX0 = int(fhdr.Tiling.ColStartSB[td.Col]) * sbSize
		fs.TileX1 = int(fhdr.Tiling.ColStartSB[int(td.Col)+1]) * sbSize
		fs.TileY0 = int(fhdr.Tiling.RowStartSB[td.Row]) * sbSize
		fs.TileY1 = int(fhdr.Tiling.RowStartSB[int(td.Row)+1]) * sbSize
		logf("tile: bounds row=%d col=%d x=[%d,%d) y=[%d,%d) payload=%d",
			td.Row, td.Col, fs.TileX0, fs.TileX1, fs.TileY0, fs.TileY1, len(td.Data))
		fs.MVFrame = fb.MVFrame
		for row8 := fs.TileY0 >> 3; row8 < (fs.TileY1+7)>>3; row8 += 16 {
			refmvs.BuildTemporalProjectionRegion(
				fb.MVFrame, fb.RefMVs,
				fs.TileX0>>3, (fs.TileX1+7)>>3,
				row8, minInt(row8+16, (fs.TileY1+7)>>3),
			)
		}
		base := initial
		if base == nil {
			base = NewTileCtxForQIdx(int(fhdr.Quant.YAC))
		}
		tileCtx := base.Clone()
		if err2 := DecodeTileWithContext(td, fhdr, seq, fb, fs, tileCtx, logf); err2 != nil {
			return nil, fmt.Errorf("tile row=%d col=%d: %w", td.Row, td.Col, err2)
		}
		fb.FilterState.MergeFilterState(fs)
		absoluteTile := int(td.Row)*int(fhdr.Tiling.Cols) + int(td.Col)
		if absoluteTile == int(fhdr.Tiling.Update) || (updateCtx == nil && tileIndex == len(tiles)-1) {
			updateCtx = tileCtx
			if os.Getenv("GOAV1_TRACE_SYMBOLS") != "" {
				logf("sym cdf_update_tile=%d comp_0=%v", absoluteTile, tileCtx.CompCDF[0])
			}
		}
	}
	if os.Getenv("GOAV1_TRACE_SYMBOLS") != "" && updateCtx != nil {
		logf("sym cdf_out partition64_0=%v skip_0=%v comp_0=%v", updateCtx.Partition64CDF[0], updateCtx.SkipCDF[0], updateCtx.CompCDF[0])
	}
	chromaDbg.dump(logf)
	return updateCtx, nil
}
