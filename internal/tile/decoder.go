// decoder.go implements AV1 tile-level CABAC decoding for M7.
//
// Scope:
//   - Tile group OBU parsing (tile boundary extraction)
//   - Superblock traversal → partition tree decoding
//   - Intra block: mode decode, prediction, coefficient decode, reconstruction
//   - Inter block: DC128 fill (motion compensation deferred to M8)
//
// This is a best-effort decoder. Individual tile/block decode errors are
// logged and silently skipped so that partial output is still produced.
package tile

import (
	"encoding/binary"
	"fmt"
	"runtime/debug"

	"github.com/zesun96/go-av1/internal/bitstream"
	"github.com/zesun96/go-av1/internal/header"
	predinter "github.com/zesun96/go-av1/internal/predict/inter"
	"github.com/zesun96/go-av1/internal/predict/intra"
	"github.com/zesun96/go-av1/internal/refmvs"
	"github.com/zesun96/go-av1/internal/transform"
)

// ---------------------------------------------------------------------------
// Tile data descriptor
// ---------------------------------------------------------------------------

// TileData holds the raw MSAC bitstream bytes for a single tile.
type TileData struct {
	Row, Col uint8
	Data     []byte
}

// ---------------------------------------------------------------------------
// Tile group OBU parser (Task 3a)
// ---------------------------------------------------------------------------

// ParseTileGroup parses a tile_group_obu() or the tile portion of an
// OBU_FRAME payload and returns one TileData per tile.
//
// AV1 spec §5.11.1 tile_group_obu():
//
//	if numTiles > 1: read tile_start_and_end_present_flag (1 bit)
//	if flag set:     read tg_start/tg_end (TileColsLog2+TileRowsLog2 bits each)
//	byte_alignment() to skip to next byte boundary
//	for each tile [tg_start..tg_end-1]: tile_size_minus_1 (NBytes bytes, LE)
//	last tile: remainder of payload
func ParseTileGroup(payload []byte, fhdr *header.FrameHeader) ([]TileData, error) {
	if len(payload) == 0 {
		return nil, nil
	}

	numTiles := int(fhdr.Tiling.Cols) * int(fhdr.Tiling.Rows)
	tgStart, tgEnd := 0, numTiles-1

	// Bit reader for the header portion.
	bitOff := 0 // current bit offset into payload

	readBits := func(n int) uint32 {
		var v uint32
		for i := 0; i < n; i++ {
			byteIdx := bitOff / 8
			bitIdx := 7 - (bitOff % 8)
			if byteIdx < len(payload) {
				v = (v << 1) | uint32((payload[byteIdx]>>uint(bitIdx))&1)
			}
			bitOff++
		}
		return v
	}

	if numTiles > 1 {
		flag := readBits(1)
		if flag != 0 {
			// tg_start and tg_end use tileBits = Log2Cols + Log2Rows bits each.
			tileBits := int(fhdr.Tiling.Log2Cols + fhdr.Tiling.Log2Rows)
			if tileBits == 0 {
				tileBits = 1
			}
			tgStart = int(readBits(tileBits))
			tgEnd = int(readBits(tileBits))
		}
	}

	// byte_alignment(): advance bitOff to next byte boundary.
	if bitOff%8 != 0 {
		bitOff += 8 - (bitOff % 8)
	}
	off := bitOff / 8 // byte offset into payload after header

	nBytes := int(fhdr.Tiling.NBytes) // bytes per tile-size field (1..4), 0 if single tile

	tiles := make([]TileData, 0, tgEnd-tgStart+1)

	for tileNum := tgStart; tileNum <= tgEnd; tileNum++ {
		row := uint8(tileNum / int(fhdr.Tiling.Cols))
		col := uint8(tileNum % int(fhdr.Tiling.Cols))

		var tileSize int
		if tileNum < tgEnd {
			// All but the last tile have an explicit tile_size_minus_1 field.
			nb := nBytes
			if nb == 0 {
				nb = 4
			}
			if off+nb > len(payload) {
				return tiles, fmt.Errorf("tile_group: short read at tile %d", tileNum)
			}
			tileSize = int(readUintLE(payload[off:], nb)) + 1
			off += nb
		} else {
			// Last tile: consume remainder of payload.
			tileSize = len(payload) - off
		}

		if off+tileSize > len(payload) {
			return tiles, fmt.Errorf("tile_group: tile %d size %d exceeds payload", tileNum, tileSize)
		}
		tiles = append(tiles, TileData{
			Row:  row,
			Col:  col,
			Data: payload[off : off+tileSize],
		})
		off += tileSize
	}
	return tiles, nil
}

// readUintLE reads an n-byte (1–4) little-endian unsigned integer.
func readUintLE(b []byte, n int) uint32 {
	switch n {
	case 1:
		return uint32(b[0])
	case 2:
		return uint32(binary.LittleEndian.Uint16(b))
	case 3:
		return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
	default:
		return binary.LittleEndian.Uint32(b)
	}
}

// ---------------------------------------------------------------------------
// Single-tile decoder (Task 3b)
// ---------------------------------------------------------------------------

// DecodeTile decodes one tile and writes reconstructed samples into fb.
func DecodeTile(td TileData, fhdr *header.FrameHeader,
	seq *header.SequenceHeader, fb *FrameBuf, fs *FrameState, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}

	// Recover from any panic raised by the best-effort M7 CABAC pipeline
	// (e.g. CDF table inconsistencies, malformed syntax elements). A tile
	// failure must not bring down the whole decoder goroutine; partial
	// reconstruction is preferred over no output at all.
	defer func() {
		if r := recover(); r != nil {
			logf("tile: DecodeTile row=%d col=%d recovered from panic: %v\n%s",
				td.Row, td.Col, r, debug.Stack())
		}
	}()

	m := bitstream.NewMSAC(td.Data, fhdr.DisableCDFUpdate != 0)
	ctx := NewTileCtx()

	sbSz := 64 // superblock size in luma pixels
	if seq.SB128 {
		sbSz = 128
	}
	sbSzLog2 := 6
	if seq.SB128 {
		sbSzLog2 = 7
	}

	// Tile column / row bounds in superblock units.
	tileCol := int(td.Col)
	tileRow := int(td.Row)

	colStartSB := int(fhdr.Tiling.ColStartSB[tileCol])
	colEndSB := int(fhdr.Tiling.ColStartSB[tileCol+1])
	rowStartSB := int(fhdr.Tiling.RowStartSB[tileRow])
	rowEndSB := int(fhdr.Tiling.RowStartSB[tileRow+1])

	_ = sbSzLog2
	for sbRow := rowStartSB; sbRow < rowEndSB; sbRow++ {
		for sbCol := colStartSB; sbCol < colEndSB; sbCol++ {
			sbx := sbCol * sbSz // luma pixel x
			sby := sbRow * sbSz // luma pixel y
			if sbx >= fb.Width || sby >= fb.Height {
				continue
			}
			decodeSuperBlock(m, ctx, fs, fhdr, seq, fb, sbx, sby, sbSz)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Superblock → partition tree (Task 4)
// ---------------------------------------------------------------------------

// decodeSuperBlock starts the recursive partition tree at the superblock root.
func decodeSuperBlock(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fhdr *header.FrameHeader, seq *header.SequenceHeader,
	fb *FrameBuf, sbx, sby, sbSz int) {

	bl := BL64X64
	if sbSz == 128 {
		bl = BL128X128
	}
	decodePartition(m, ctx, fs, fhdr, seq, fb, sbx, sby, bl)
}

// blkSizeFromLevel returns the luma block size in pixels for a given block
// level when the partition is NONE (full block).
func blkSizeFromLevel(bl int) int {
	switch bl {
	case BL128X128:
		return 128
	case BL64X64:
		return 64
	case BL32X32:
		return 32
	case BL16X16:
		return 16
	default:
		return 8
	}
}

// decodePartition recursively decodes the partition tree.
// bx/by are luma pixel coordinates; bl is block level (BL128…BL8x8).
func decodePartition(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fhdr *header.FrameHeader, seq *header.SequenceHeader,
	fb *FrameBuf, bx, by, bl int) {

	// Clamp to frame.
	if bx >= fb.Width || by >= fb.Height {
		return
	}

	blSz := blkSizeFromLevel(bl) // full block size in luma px

	// Select partition CDF and symbol count based on block level.
	// AV1 spec: 128x128→8 syms, 64/32/16→10 syms, 8x8→4 syms.
	// Context: partCtx = hasAbove | (hasLeft<<1) from FrameState.
	partCtx := fs.PartCtx(bx, by, bl)
	var partCDF []uint16
	var nPart int
	switch bl {
	case BL128X128:
		partCDF = ctx.Partition128CDF[partCtx][:]
		nPart = 8
	case BL64X64:
		partCDF = ctx.Partition64CDF[partCtx][:]
		nPart = 10
	case BL32X32:
		partCDF = ctx.Partition32CDF[partCtx][:]
		nPart = 10
	case BL16X16:
		partCDF = ctx.Partition16CDF[partCtx][:]
		nPart = 10
	default: // BL8X8
		partCDF = ctx.Partition8CDF[partCtx][:]
		nPart = 4
	}

	half := blSz / 2
	haveHSplit := fb.Width > bx+half
	haveVSplit := fb.Height > by+half
	if bl == BL8X8 && (!haveHSplit || !haveVSplit) {
		decodeBlock(m, ctx, fs, fhdr, seq, fb, bx, by, blSz, blSz)
		fs.SetPartition(bx, by, bl, PartitionNone, blSz)
		return
	}
	if !haveHSplit && !haveVSplit {
		decodePartition(m, ctx, fs, fhdr, seq, fb, bx, by, bl+1)
		return
	}
	if !haveVSplit {
		isSplit := m.Bool(gatherTopPartitionProb(partCDF, bl))
		if isSplit != 0 {
			decodePartition(m, ctx, fs, fhdr, seq, fb, bx, by, bl+1)
			decodePartition(m, ctx, fs, fhdr, seq, fb, bx+half, by, bl+1)
			fs.SetPartition(bx, by, bl, PartitionSplit, blSz)
		} else {
			decodeBlock(m, ctx, fs, fhdr, seq, fb, bx, by, blSz, half)
			fs.SetPartition(bx, by, bl, PartitionH, blSz)
		}
		return
	}
	if !haveHSplit {
		isSplit := m.Bool(gatherLeftPartitionProb(partCDF, bl))
		if isSplit != 0 {
			decodePartition(m, ctx, fs, fhdr, seq, fb, bx, by, bl+1)
			decodePartition(m, ctx, fs, fhdr, seq, fb, bx, by+half, bl+1)
			fs.SetPartition(bx, by, bl, PartitionSplit, blSz)
		} else {
			decodeBlock(m, ctx, fs, fhdr, seq, fb, bx, by, half, blSz)
			fs.SetPartition(bx, by, bl, PartitionV, blSz)
		}
		return
	}

	part := int(m.SymbolAdapt(partCDF, nPart))

	switch part {
	case PartitionNone:
		decodeBlock(m, ctx, fs, fhdr, seq, fb, bx, by, blSz, blSz)

	case PartitionH:
		decodeBlock(m, ctx, fs, fhdr, seq, fb, bx, by, blSz, half)
		decodeBlock(m, ctx, fs, fhdr, seq, fb, bx, by+half, blSz, half)

	case PartitionV:
		decodeBlock(m, ctx, fs, fhdr, seq, fb, bx, by, half, blSz)
		decodeBlock(m, ctx, fs, fhdr, seq, fb, bx+half, by, half, blSz)

	case PartitionSplit:
		if bl == BL8X8 {
			decodeBlock(m, ctx, fs, fhdr, seq, fb, bx, by, half, half)
			decodeBlock(m, ctx, fs, fhdr, seq, fb, bx+half, by, half, half)
			decodeBlock(m, ctx, fs, fhdr, seq, fb, bx, by+half, half, half)
			decodeBlock(m, ctx, fs, fhdr, seq, fb, bx+half, by+half, half, half)
		} else {
			decodePartition(m, ctx, fs, fhdr, seq, fb, bx, by, bl+1)
			decodePartition(m, ctx, fs, fhdr, seq, fb, bx+half, by, bl+1)
			decodePartition(m, ctx, fs, fhdr, seq, fb, bx, by+half, bl+1)
			decodePartition(m, ctx, fs, fhdr, seq, fb, bx+half, by+half, bl+1)
		}

	case PartitionTTopSplit:
		decodeBlock(m, ctx, fs, fhdr, seq, fb, bx, by, blSz, half)
		decodePartition(m, ctx, fs, fhdr, seq, fb, bx, by+half, bl+1)
		decodePartition(m, ctx, fs, fhdr, seq, fb, bx+half, by+half, bl+1)

	case PartitionTBottomSplit:
		decodePartition(m, ctx, fs, fhdr, seq, fb, bx, by, bl+1)
		decodePartition(m, ctx, fs, fhdr, seq, fb, bx+half, by, bl+1)
		decodeBlock(m, ctx, fs, fhdr, seq, fb, bx, by+half, blSz, half)

	case PartitionTLeftSplit:
		decodeBlock(m, ctx, fs, fhdr, seq, fb, bx, by, half, blSz)
		decodePartition(m, ctx, fs, fhdr, seq, fb, bx+half, by, bl+1)
		decodePartition(m, ctx, fs, fhdr, seq, fb, bx+half, by+half, bl+1)

	case PartitionTRightSplit:
		decodePartition(m, ctx, fs, fhdr, seq, fb, bx, by, bl+1)
		decodePartition(m, ctx, fs, fhdr, seq, fb, bx, by+half, bl+1)
		decodeBlock(m, ctx, fs, fhdr, seq, fb, bx+half, by, half, blSz)

	case PartitionH4:
		q := blSz / 4
		for i := 0; i < 4; i++ {
			decodeBlock(m, ctx, fs, fhdr, seq, fb, bx, by+i*q, blSz, q)
		}

	case PartitionV4:
		q := blSz / 4
		for i := 0; i < 4; i++ {
			decodeBlock(m, ctx, fs, fhdr, seq, fb, bx+i*q, by, q, blSz)
		}
	}

	if part != PartitionSplit || bl == BL8X8 {
		fs.SetPartition(bx, by, bl, part, blSz)
	}
}

func gatherLeftPartitionProb(cdf []uint16, bl int) uint32 {
	out := int(cdf[PartitionH-1]) - int(cdf[PartitionH])
	out += int(cdf[PartitionSplit-1]) - int(cdf[PartitionTLeftSplit])
	if bl != BL128X128 {
		out += int(cdf[PartitionH4-1]) - int(cdf[PartitionH4])
	}
	if out < 0 {
		return 0
	}
	if out > 32768 {
		return 32768
	}
	return uint32(out)
}

func gatherTopPartitionProb(cdf []uint16, bl int) uint32 {
	out := int(cdf[PartitionV-1]) - int(cdf[PartitionTTopSplit])
	out += int(cdf[PartitionTLeftSplit-1])
	if bl != BL128X128 {
		out += int(cdf[PartitionV4-1]) - int(cdf[PartitionTRightSplit])
	}
	if out < 0 {
		return 0
	}
	if out > 32768 {
		return 32768
	}
	return uint32(out)
}

// ---------------------------------------------------------------------------
// Block decoder (Task 5)
// ---------------------------------------------------------------------------

// decodeBlock decodes one coding block of size bw×bh (luma pixels) at (bx,by).
func decodeBlock(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fhdr *header.FrameHeader, seq *header.SequenceHeader,
	fb *FrameBuf, bx, by, bw, bh int) {

	if bx >= fb.Width || by >= fb.Height {
		return
	}
	// Clamp block to frame boundary.
	if bx+bw > fb.Width {
		bw = fb.Width - bx
	}
	if by+bh > fb.Height {
		bh = fb.Height - by
	}

	// --- Segment id (dav1d decode_b §5.11.9, intra-only path) ---
	// When segmentation is disabled the spec mandates seg_id = 0 and no bits
	// are read. When enabled but segmentation.update_map=0 the previous-frame
	// segment map is used; for intra-only key-frames there is no previous
	// map, so the predictor is the spatial neighbour minimum.
	var segID uint8
	if fhdr.Segmentation.Enabled != 0 && fhdr.Segmentation.UpdateMap == 0 {
		segID = 0
	}
	if fhdr.Segmentation.Enabled != 0 &&
		fhdr.Segmentation.UpdateMap != 0 &&
		fhdr.Segmentation.SegData.PreSkip != 0 {
		segID = readSegmentID(m, ctx, fs, fhdr, bx, by)
	}

	// --- Skip flag ---
	skipCtx := fs.SkipCtx(bx, by)
	skip := m.SymbolAdapt(ctx.SkipCDF[skipCtx][:], 2) != 0

	if fhdr.Segmentation.Enabled != 0 &&
		fhdr.Segmentation.UpdateMap != 0 &&
		fhdr.Segmentation.SegData.PreSkip == 0 {
		if skip {
			segID = fs.SegIDFromNeighbours(bx, by)
		} else {
			segID = readSegmentID(m, ctx, fs, fhdr, bx, by)
		}
	}

	// --- CDEF index ---
	// dav1d reads the per-64x64 CDEF strength index immediately after seg_id
	// for the first non-skip block touching that CDEF unit. The actual filter
	// application can still be approximate, but these raw bits must be consumed
	// here or all following intra syntax is decoded from the wrong position.
	if !skip {
		readCDEFIndex(m, fs, fhdr, bx, by, bw, bh)
	}
	readDeltaQLF(m, ctx, fhdr, seq, bx, by, bw, bh, skip)

	// --- Intra vs Inter ---
	isIntra := fhdr.FrameType.IsIntra()
	// For inter frames, treat every block as intra DC128 (M7 stub).

	if !isIntra {
		decodeInterBlockFallback(fs, fhdr, seq, fb, segID, skip, bx, by, bw, bh)
		return
	}

	hasChroma := blockHasChroma(seq, fb, bx, by, bw, bh)
	qidx := blockQIdx(ctx, fhdr, segID)
	qidxIsZero := qidx == 0
	lossless := fhdr.Segmentation.Lossless[segID] != 0

	// --- Intra luma mode ---
	// KFY mode context: [topMode][leftMode], from FrameState neighbour info.
	topModeCtx := fs.TopModeCtx(bx, by)
	leftModeCtx := fs.LeftModeCtx(bx, by)
	yMode := int(m.SymbolAdapt(ctx.KFYModeCDF[topModeCtx][leftModeCtx][:], NIntraPredModes))
	// Clamp yMode to valid range (defensive).
	if yMode < 0 {
		yMode = 0
	} else if yMode >= NIntraPredModes {
		yMode = NIntraPredModes - 1
	}

	// --- Angle-delta luma (A6: bit consume only) ---
	// dav1d decode_b L1060-L1069: when b_dim[2]+b_dim[3] >= 2
	// and y_mode is in [VERT_PRED..VERT_LEFT_PRED], read a 7-symbol delta.
	var yAngleDelta int
	if yMode >= VertPred && yMode <= VertLeftPred && angleDeltaAllowed(bw, bh) {
		v := int(m.SymbolAdapt(ctx.AngleDeltaCDF[yMode-VertPred][:], 7))
		yAngleDelta = v - 3
	}

	// --- Intra UV mode ---
	cflAllowed := 0
	if hasChroma && cflAllowedForBlock(seq, bw, bh, lossless) {
		cflAllowed = 1
	}
	uvMode := DCPred
	if hasChroma {
		uvModeSyms := NIntraPredModes
		if cflAllowed != 0 {
			uvModeSyms = NUVIntraModes
		}
		uvMode = int(m.SymbolAdapt(ctx.UVModeCDF[cflAllowed][yMode][:], uvModeSyms))
	}

	// --- Angle-delta chroma (A6: bit consume only) ---
	// dav1d decode_b L1107-L1113: when uv_mode is not CFL_PRED, the same
	// gating as luma applies and a chroma angle delta is read.
	var uvAngleDelta int
	if hasChroma && uvMode >= VertPred && uvMode <= VertLeftPred && angleDeltaAllowed(bw, bh) {
		v := int(m.SymbolAdapt(ctx.AngleDeltaCDF[uvMode-VertPred][:], 7))
		uvAngleDelta = v - 3
	}

	// --- Palette flags / size syntax ---
	var palSzY, palSzUV int
	var pal [3][8]uint8
	var palIdxY, palIdxUV []uint8
	if fhdr.AllowScreenContentTools != 0 && bw <= 64 && bh <= 64 && (bw+bh) >= 16 {
		szCtx := palSzCtx(bw, bh)
		if yMode == DCPred {
			palCtx := fs.PaletteYCtx(bx, by)
			if m.BoolAdapt(ctx.PaletteYCDF[szCtx][palCtx][:]) != 0 {
				palSzY = int(m.SymbolAdapt(ctx.PaletteSizeCDF[0][szCtx][:], 7)) + 2
				pal[0] = readPalettePlane(m, ctx, fs, seq, 0, szCtx, bx, by, palSzY)
			}
		}
		if hasChroma && uvMode == DCPred {
			palCtx := fs.PaletteUVCtx(bx, by)
			if palSzY > 0 || palCtx != 0 {
				palCtx = 1
			}
			if m.BoolAdapt(ctx.PaletteUVCDF[palCtx][:]) != 0 {
				palSzUV = int(m.SymbolAdapt(ctx.PaletteSizeCDF[1][szCtx][:], 7)) + 2
				pal[1], pal[2] = readPaletteUV(m, ctx, fs, seq, szCtx, bx, by, palSzUV)
			}
		}
	}
	fs.SetPaletteCtx(bx, by, bw, bh, palSzY, palSzUV)

	// --- Filter-intra ---
	// When enabled on eligible DC-predicted luma blocks, decode the
	// filter intra mode and route prediction through FILTER_PRED.
	filterMode := -1
	if seq.FilterIntra && yMode == DCPred && palSzY == 0 && bw <= 32 && bh <= 32 {
		bs := bsizeFromDim(bw, bh)
		if bs >= 0 {
			useFI := m.BoolAdapt(ctx.UseFilterIntraCDF[bs][:])
			if useFI != 0 {
				filterMode = int(m.SymbolAdapt(ctx.FilterIntraModeCDF[:], 5))
			}
		}
	}
	yModeNofilt := yMode
	if filterMode >= 0 && filterMode < len(FilterModeToYMode) {
		yModeNofilt = int(FilterModeToYMode[filterMode])
	}
	if palSzY > 0 {
		palIdxY = readPalIndices(m, &ctx.ColorMapCDF[0][palSzY-2], palSzY, bw, bh, bw, bh)
	}
	if hasChroma && palSzUV > 0 {
		_, _, cbw, cbh := chromaRect(seq, bx, by, bw, bh)
		palIdxUV = readPalIndices(m, &ctx.ColorMapCDF[1][palSzUV-2], palSzUV, cbw, cbh, cbw, cbh)
	}

	// --- Transform size selection (M7: use largest fitting square tx) ---
	txY := largestTx(bw, bh)
	_, _, cbw, cbh := chromaRect(seq, bx, by, bw, bh)
	txUV := largestTx(cbw, cbh)
	var yTxBlocks []txBlockSpec
	var blockState Av1Block

	blockState.Tx = txY
	blockState.MaxYTx = txY
	blockState.Intra = true
	blockState.SegID = segID
	blockState.Skip = skip
	blockState.YMode = uint8(yModeNofilt)
	blockState.UvMode = uint8(uvMode)
	blockState.YAngle = int8(yAngleDelta)
	blockState.UvAngle = int8(uvAngleDelta)
	blockState.PalSz = [2]uint8{uint8(maxInt(palSzY, 0)), uint8(maxInt(palSzUV, 0))}

	switch {
	case skip:
		fs.SetTxCtx(bx, by, bw, bh, txY, fhdr.TxfmMode == header.TxfmModeSwitchable, true)
	case lossless:
		txY = transform.TX4x4
		txUV = transform.TX4x4
		blockState.Tx = txY
		blockState.MaxYTx = txY
		fs.SetTxCtx(bx, by, bw, bh, txY, fhdr.TxfmMode == header.TxfmModeSwitchable, false)
	case fhdr.TxfmMode == header.TxfmModeSwitchable:
		txY, yTxBlocks, blockState = readVarTxTree(m, ctx, fs, bx, by, bw, bh, txY)
	default:
		fs.SetTxCtx(bx, by, bw, bh, txY, false, false)
	}
	dqY := [2]uint16{
		transform.DqTbl[0][clampQIdx(qidx+int(fhdr.Quant.YDCDelta))][0],
		transform.DqTbl[0][clampQIdx(qidx)][1],
	}
	dqU := [2]uint16{
		transform.DqTbl[0][clampQIdx(qidx+int(fhdr.Quant.UDCDelta))][0],
		transform.DqTbl[0][clampQIdx(qidx+int(fhdr.Quant.UACDelta))][1],
	}
	dqV := [2]uint16{
		transform.DqTbl[0][clampQIdx(qidx+int(fhdr.Quant.VDCDelta))][0],
		transform.DqTbl[0][clampQIdx(qidx+int(fhdr.Quant.VACDelta))][1],
	}

	reducedTxtpSet := fhdr.ReducedTxtpSet != 0

	// --- Luma plane ---
	if palSzY > 0 {
		if len(yTxBlocks) > 0 {
			decodePalettePlaneVarTx(m, ctx, fs, fb, 0, bx, by, bw, bh, yTxBlocks, pal[0], palIdxY, dqY, skip, yModeNofilt, reducedTxtpSet, qidxIsZero, lossless)
		} else {
			decodePalettePlane(m, ctx, fs, fb, 0, bx, by, bw, bh, txY, pal[0], palIdxY, dqY, skip, yModeNofilt, reducedTxtpSet, qidxIsZero, lossless)
		}
	} else if len(yTxBlocks) > 0 {
		decodeIntraPlaneVarTx(m, ctx, fs, fb, 0, bx, by, bw, bh, yTxBlocks, yMode, yAngleDelta, filterMode, dqY, skip, yModeNofilt, reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)
	} else {
		decodeIntraPlane(m, ctx, fs, fb, 0, bx, by, bw, bh, txY, yMode, yAngleDelta, filterMode, 0, dqY, skip, yModeNofilt, reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)
	}

	// --- Chroma planes (skip for monochrome) ---
	if hasChroma && len(fb.U) > 0 {
		cbx, cby, cbw, cbh := chromaRect(seq, bx, by, bw, bh)

		var cflAlphaU, cflAlphaV int8 // CFL alpha parameters

		if uvMode == CFLPred {
			// Decode CFL alpha signs and magnitudes.
			cflAlphaU, cflAlphaV = decodeCFLAlphas(m, ctx)
		}

		if palSzUV > 0 {
			decodePalettePlane(m, ctx, fs, fb, 1, cbx, cby, cbw, cbh, txUV, pal[1], palIdxUV, dqU, skip, yMode, reducedTxtpSet, qidxIsZero, lossless)
			decodePalettePlane(m, ctx, fs, fb, 2, cbx, cby, cbw, cbh, txUV, pal[2], palIdxUV, dqV, skip, yMode, reducedTxtpSet, qidxIsZero, lossless)
		} else if uvMode == CFLPred {
			// Build zero-mean luma AC buffer (4:2:0 subsampled from reconstructed Y).
			acCfl := buildCflAc(fb, seq, bx, by, bw, bh, cbw, cbh)
			// CFL prediction: chroma = DC_chroma + (alpha*luma_AC + 32) >> 6.
			decodeIntraPlaneCFL(m, ctx, fs, fb, 1, cbx, cby, cbw, cbh, txUV, int(cflAlphaU), dqU, skip, yMode, reducedTxtpSet, fhdr, seq, qidxIsZero, lossless, acCfl)
			decodeIntraPlaneCFL(m, ctx, fs, fb, 2, cbx, cby, cbw, cbh, txUV, int(cflAlphaV), dqV, skip, yMode, reducedTxtpSet, fhdr, seq, qidxIsZero, lossless, acCfl)
		} else {
			decodeIntraPlane(m, ctx, fs, fb, 1, cbx, cby, cbw, cbh, txUV, uvMode, uvAngleDelta, -1, 0, dqU, skip, yMode, reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)
			decodeIntraPlane(m, ctx, fs, fb, 2, cbx, cby, cbw, cbh, txUV, uvMode, uvAngleDelta, -1, 0, dqV, skip, yMode, reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)
		}
	}

	// Record decoded block state for future neighbour context derivation.
	if palSzY > 0 || palSzUV > 0 {
		fs.SetPaletteColors(bx, by, bw, bh, pal)
	}
	fs.SetBlockSeg(bx, by, bw, bh, skip, yModeNofilt, segID)
}

func blockHasChroma(seq *header.SequenceHeader, fb *FrameBuf, bx, by, bw, bh int) bool {
	if fb.Monochrome || len(fb.U) == 0 {
		return false
	}
	bw4 := (bw + 3) >> 2
	bh4 := (bh + 3) >> 2
	bx4 := bx >> 2
	by4 := by >> 2
	ssHor := int(seq.SsHor)
	ssVer := int(seq.SsVer)
	return (bw4 > ssHor || (bx4&1) != 0) && (bh4 > ssVer || (by4&1) != 0)
}

func chromaRect(seq *header.SequenceHeader, bx, by, bw, bh int) (cbx, cby, cbw, cbh int) {
	ssHor := int(seq.SsHor)
	ssVer := int(seq.SsVer)
	cbx = bx >> ssHor
	cby = by >> ssVer
	cbw = (bw + (1 << ssHor) - 1) >> ssHor
	cbh = (bh + (1 << ssVer) - 1) >> ssVer
	return
}

func cflAllowedForBlock(seq *header.SequenceHeader, bw, bh int, lossless bool) bool {
	bw4 := (bw + 3) >> 2
	bh4 := (bh + 3) >> 2
	cbw4 := (bw4 + int(seq.SsHor)) >> int(seq.SsHor)
	cbh4 := (bh4 + int(seq.SsVer)) >> int(seq.SsVer)
	if lossless {
		return cbw4 == 1 && cbh4 == 1
	}
	bs := bsizeFromDim(bw, bh)
	switch bs {
	case BS32x32, BS32x16, BS32x8,
		BS16x32, BS16x16, BS16x8, BS16x4,
		BS8x32, BS8x16, BS8x8, BS8x4,
		BS4x16, BS4x8, BS4x4:
		return true
	default:
		return false
	}
}

func readSegmentID(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, fhdr *header.FrameHeader, bx, by int) uint8 {
	pred := int(fs.SegIDFromNeighbours(bx, by))
	segCtx := 0
	haveAbove := by > 0
	haveLeft := bx > 0
	if haveAbove && haveLeft {
		segCtx = 2
	} else if haveAbove || haveLeft {
		segCtx = 1
	}
	diff := int(m.SymbolAdapt(ctx.SegIDCDF[segCtx][:], int(header.MaxSegments)))
	maxSeg := int(fhdr.Segmentation.SegData.LastActiveSegID) + 1
	if maxSeg <= 0 || maxSeg > int(header.MaxSegments) {
		maxSeg = int(header.MaxSegments)
	}
	segID := negDeinterleave(diff, pred, maxSeg)
	if segID < 0 || segID >= maxSeg {
		return 0
	}
	return uint8(segID)
}

func readCDEFIndex(m *bitstream.MSAC, fs *FrameState, fhdr *header.FrameHeader, bx, by, bw, bh int) {
	if fhdr.CDEF.NBits == 0 || fs.W64 <= 0 || len(fs.CDEFIndex) == 0 {
		return
	}
	col64Start := bx / 64
	row64Start := by / 64
	if col64Start < 0 || row64Start < 0 || col64Start >= fs.W64 {
		return
	}
	idx := row64Start*fs.W64 + col64Start
	if idx < 0 || idx >= len(fs.CDEFIndex) || fs.CDEFIndex[idx] != -1 {
		return
	}

	v := int8(m.Bools(int(fhdr.CDEF.NBits)))
	col64End := (bx + bw + 63) / 64
	row64End := (by + bh + 63) / 64
	for r := row64Start; r < row64End; r++ {
		for c := col64Start; c < col64End && c < fs.W64; c++ {
			i := r*fs.W64 + c
			if i >= 0 && i < len(fs.CDEFIndex) {
				fs.CDEFIndex[i] = v
			}
		}
	}
}

func readDeltaQLF(m *bitstream.MSAC, ctx *TileCtx, fhdr *header.FrameHeader, seq *header.SequenceHeader, bx, by, bw, bh int, skip bool) {
	if !ctx.LastQIdxValid {
		ctx.LastQIdx = int(fhdr.Quant.YAC)
		ctx.LastQIdxValid = true
	}

	bx4 := bx >> 2
	by4 := by >> 2
	mask := 15
	root := 64
	if seq.SB128 {
		mask = 31
		root = 128
	}
	if ((bx4 | by4) & mask) != 0 {
		return
	}

	haveDeltaQ := fhdr.Delta.Q.Present != 0 && (bw != root || bh != root || !skip)
	if !haveDeltaQ {
		return
	}

	deltaQ := readDeltaSymbol(m, ctx.DeltaQCDF[:])
	if deltaQ != 0 {
		deltaQ <<= fhdr.Delta.Q.ResLog2
	}
	ctx.LastQIdx = clampInt(ctx.LastQIdx+deltaQ, 1, 255)

	if fhdr.Delta.LF.Present == 0 {
		return
	}
	nLFs := 1
	if fhdr.Delta.LF.Multi != 0 {
		nLFs = 2
		if !seq.Monochrome {
			nLFs = 4
		}
	}
	for i := 0; i < nLFs; i++ {
		cdfIdx := i
		if fhdr.Delta.LF.Multi != 0 {
			cdfIdx = i + 1
		}
		deltaLF := readDeltaSymbol(m, ctx.DeltaLFCDF[cdfIdx][:])
		if deltaLF != 0 {
			deltaLF <<= fhdr.Delta.LF.ResLog2
		}
		ctx.LastDeltaLF[i] = int8(clampInt(int(ctx.LastDeltaLF[i])+deltaLF, -63, 63))
	}
}

func readDeltaSymbol(m *bitstream.MSAC, cdf []uint16) int {
	delta := int(m.SymbolAdapt(cdf, 4))
	if delta == 3 {
		nBits := 1 + int(m.Bools(3))
		delta = int(m.Bools(nBits)) + 1 + (1 << nBits)
	}
	if delta != 0 && m.BoolEqui() != 0 {
		delta = -delta
	}
	return delta
}

func blockQIdx(ctx *TileCtx, fhdr *header.FrameHeader, segID uint8) int {
	qidx := int(fhdr.Segmentation.QIdx[segID])
	if !ctx.LastQIdxValid {
		return qidx
	}
	segDelta := qidx - int(fhdr.Quant.YAC)
	return clampQIdx(ctx.LastQIdx + segDelta)
}

func negDeinterleave(diff, ref, max int) int {
	if max <= 0 {
		return 0
	}
	if ref < 0 {
		ref = 0
	}
	if ref >= max {
		ref = max - 1
	}
	if ref == 0 {
		return diff
	}
	if ref >= max-1 {
		return max - diff - 1
	}
	if 2*ref < max {
		if diff <= 2*ref {
			if diff&1 != 0 {
				return ref + ((diff + 1) >> 1)
			}
			return ref - (diff >> 1)
		}
		return diff
	}
	if diff <= 2*(max-ref-1) {
		if diff&1 != 0 {
			return ref + ((diff + 1) >> 1)
		}
		return ref - (diff >> 1)
	}
	return max - (diff + 1)
}

// decodeCFLAlphas reads CFL alpha syntax using dav1d's sign and alpha CDFs.
func decodeCFLAlphas(m *bitstream.MSAC, ctx *TileCtx) (int8, int8) {
	sign := int(m.SymbolAdapt(ctx.CFLSignCDF[:], 7)) + 1
	signU := sign * 0x56 >> 8
	signV := sign - signU*3

	var alphaU, alphaV int
	if signU != 0 {
		c := 0
		if signU == 2 {
			c = 3
		}
		c += signV
		alphaU = int(m.SymbolAdapt(ctx.CFLAlphaCDF[c][:], 15)) + 1
		if signU == 1 {
			alphaU = -alphaU
		}
	}
	if signV != 0 {
		c := 0
		if signV == 2 {
			c = 3
		}
		c += signU
		alphaV = int(m.SymbolAdapt(ctx.CFLAlphaCDF[c][:], 15)) + 1
		if signV == 1 {
			alphaV = -alphaV
		}
	}
	return int8(alphaU), int8(alphaV)
}

// buildCflAc constructs a zero-mean luma AC buffer for CFL prediction by
// 4:2:0-subsampling the reconstructed luma block at (bx,by,bw,bh) into a
// cbw×cbh array, then subtracting the mean. The result is in row-major
// layout, length cbw*cbh.
func buildCflAc(fb *FrameBuf, seq *header.SequenceHeader, bx, by, bw, bh, cbw, cbh int) []int16 {
	ac := make([]int16, cbw*cbh)
	if len(fb.Y) == 0 || cbw == 0 || cbh == 0 {
		return ac
	}
	stride := fb.StrideY
	ssHor := int(seq.SsHor)
	ssVer := int(seq.SsVer)
	validW := (bw + (1 << ssHor) - 1) >> ssHor
	validH := (bh + (1 << ssVer) - 1) >> ssVer
	if validW > cbw {
		validW = cbw
	}
	if validH > cbh {
		validH = cbh
	}

	for cy := 0; cy < validH; cy++ {
		rowOff := cy * cbw
		srcY := by + (cy << ssVer)
		for cx := 0; cx < validW; cx++ {
			srcX := bx + (cx << ssHor)
			acSum := int(fb.Y[srcY*stride+srcX])
			if ssHor != 0 {
				acSum += int(fb.Y[srcY*stride+srcX+1])
			}
			if ssVer != 0 {
				acSum += int(fb.Y[(srcY+1)*stride+srcX])
				if ssHor != 0 {
					acSum += int(fb.Y[(srcY+1)*stride+srcX+1])
				}
			}
			ac[rowOff+cx] = int16(acSum << (1 + btoi(ssVer == 0) + btoi(ssHor == 0)))
		}
		for cx := validW; cx < cbw; cx++ {
			ac[rowOff+cx] = ac[rowOff+cx-1]
		}
	}
	for cy := validH; cy < cbh; cy++ {
		copy(ac[cy*cbw:(cy+1)*cbw], ac[(cy-1)*cbw:cy*cbw])
	}

	log2sz := ctzPow2(cbw) + ctzPow2(cbh)
	sum := (1 << log2sz) >> 1
	for i := range ac {
		sum += int(ac[i])
	}
	sum >>= log2sz
	for i := range ac {
		ac[i] -= int16(sum)
	}
	return ac
}

func predictCFLBlock(dst []byte, stride int, tlBuf []byte, tl, bx, by, tw, th, alpha int, ac []int16) {
	switch {
	case bx > 0 && by > 0:
		intra.PredCFLBoth(dst, stride, tlBuf, tl, ac, tw, th, alpha)
	case by > 0:
		intra.PredCFLTop(dst, stride, tlBuf, tl, ac, tw, th, alpha)
	case bx > 0:
		intra.PredCFLLeft(dst, stride, tlBuf, tl, ac, tw, th, alpha)
	default:
		intra.PredCFL128(dst, stride, ac, tw, th, alpha)
	}
}

func ctzPow2(v int) int {
	n := 0
	for v > 1 && (v&1) == 0 {
		n++
		v >>= 1
	}
	return n
}

func btoi(v bool) int {
	if v {
		return 1
	}
	return 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// decodeIntraPlaneCFL decodes a chroma plane using CFL prediction. It is
// a CFL-specialised variant of decodeIntraPlane: prediction is built via
// PredCFL (DC base + alpha*ac), then chroma residual is added on top.
func decodeIntraPlaneCFL(
	m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fb *FrameBuf,
	plane, bx, by, bw, bh int,
	tx uint8,
	cflAlpha int,
	dq [2]uint16,
	skip bool,
	yMode int,
	reducedTxtpSet bool,
	fhdr *header.FrameHeader,
	seq *header.SequenceHeader,
	qidxIsZero bool,
	lossless bool,
	ac []int16,
) {
	var planeBuf []byte
	var stride, planeW, planeH int
	if plane == 1 {
		planeBuf = fb.U
	} else {
		planeBuf = fb.V
	}
	stride = fb.StrideUV
	planeW = fb.ChromaW
	planeH = fb.ChromaH

	if bx >= planeW || by >= planeH || len(planeBuf) == 0 {
		return
	}
	if bx+bw > planeW {
		bw = planeW - bx
	}
	if by+bh > planeH {
		bh = planeH - by
	}

	maxDim := bw
	if bh > maxDim {
		maxDim = bh
	}
	tlBuf := make([]byte, 4*maxDim+2)
	tl := 2 * maxDim
	fillTopleft(planeBuf, stride, planeW, planeH, bx, by, bw, bh, tlBuf, tl)

	td := transform.TxfmDimensions[tx]
	tw := int(td.W) * 4
	th := int(td.H) * 4
	if tw > bw {
		tw = bw
	}
	if th > bh {
		th = bh
	}
	predBuf := make([]byte, tw*th)

	for tby := 0; tby < bh; tby += th {
		for tbx := 0; tbx < bw; tbx += tw {
			dstOff := (by+tby)*stride + (bx + tbx)
			if dstOff >= len(planeBuf) {
				continue
			}
			dst := planeBuf[dstOff:]

			acSlice := cflAcSubBlock(ac, bw, bh, tbx, tby, tw, th)
			predictCFLBlock(predBuf, tw, tlBuf, tl, bx+tbx, by+tby, tw, th, cflAlpha, acSlice)

			for row := 0; row < th; row++ {
				dstRow := (by+tby+row)*stride + (bx + tbx)
				if dstRow+tw > len(planeBuf) {
					break
				}
				copy(planeBuf[dstRow:dstRow+tw], predBuf[row*tw:(row+1)*tw])
			}

			if !skip {
				coeffMode := yMode
				if plane > 0 {
					coeffMode = CFLPred
				}
				coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, tx, plane, bx+tbx, by+tby, bw, bh, coeffMode, reducedTxtpSet, qidxIsZero, lossless)
				fs.SetCoefCtx(plane, bx+tbx, by+tby, tx, resCtx)
				if eob >= 0 && len(coeff) > 0 {
					tdFull := transform.TxfmDimensions[tx]
					twFull := int(tdFull.W) * 4
					thFull := int(tdFull.H) * 4
					maxOff := (thFull-1)*stride + (twFull - 1)
					if dstOff+maxOff < len(planeBuf) {
						ReconBlock(dst, stride, coeff, eob, tx, txtp, dq, 8)
					}
				}
			} else {
				fs.SetCoefCtx(plane, bx+tbx, by+tby, tx, 0x40)
			}
			updateTopleft(planeBuf, stride, planeW, planeH, bx+tbx, by+tby, tw, th, tlBuf, tl)
		}
	}
	_ = seq
	_ = fhdr
}

// cflAcSubBlock extracts a tw×th tile (at offset tbx,tby) from a cbw×cbh
// CFL AC buffer, copying it into a freshly-allocated row-major slice.
func cflAcSubBlock(ac []int16, cbw, cbh, tbx, tby, tw, th int) []int16 {
	out := make([]int16, tw*th)
	for y := 0; y < th; y++ {
		sy := tby + y
		if sy >= cbh {
			break
		}
		for x := 0; x < tw; x++ {
			sx := tbx + x
			if sx >= cbw {
				break
			}
			out[y*tw+x] = ac[sy*cbw+sx]
		}
	}
	return out
}

func decodePalettePlane(
	m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fb *FrameBuf,
	plane, bx, by, bw, bh int,
	tx uint8,
	pal [8]uint8,
	palIdx []uint8,
	dq [2]uint16,
	skip bool,
	yMode int,
	reducedTxtpSet bool,
	qidxIsZero bool,
	lossless bool,
) {
	var planeBuf []byte
	var stride, planeW, planeH int
	switch plane {
	case 0:
		planeBuf = fb.Y
		stride = fb.StrideY
		planeW = fb.Width
		planeH = fb.Height
	case 1:
		planeBuf = fb.U
		stride = fb.StrideUV
		planeW = fb.ChromaW
		planeH = fb.ChromaH
	default:
		planeBuf = fb.V
		stride = fb.StrideUV
		planeW = fb.ChromaW
		planeH = fb.ChromaH
	}
	if bx >= planeW || by >= planeH || len(planeBuf) == 0 {
		return
	}
	if bx+bw > planeW {
		bw = planeW - bx
	}
	if by+bh > planeH {
		bh = planeH - by
	}
	if len(palIdx) < bw*bh {
		return
	}

	td := transform.TxfmDimensions[tx]
	tw := int(td.W) * 4
	th := int(td.H) * 4
	if tw > bw {
		tw = bw
	}
	if th > bh {
		th = bh
	}
	predBuf := make([]byte, tw*th)

	for tby := 0; tby < bh; tby += th {
		for tbx := 0; tbx < bw; tbx += tw {
			dstOff := (by+tby)*stride + (bx + tbx)
			if dstOff >= len(planeBuf) {
				continue
			}
			dst := planeBuf[dstOff:]
			predictPalette(predBuf, tw, pal, palIdx[tby*bw+tbx:], tw, th, bw)
			for row := 0; row < th; row++ {
				dstRow := (by+tby+row)*stride + (bx + tbx)
				if dstRow+tw > len(planeBuf) {
					break
				}
				copy(planeBuf[dstRow:dstRow+tw], predBuf[row*tw:(row+1)*tw])
			}
			if !skip {
				coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, tx, plane, bx+tbx, by+tby, bw, bh, yMode, reducedTxtpSet, qidxIsZero, lossless)
				fs.SetCoefCtx(plane, bx+tbx, by+tby, tx, resCtx)
				if eob >= 0 && len(coeff) > 0 {
					tdFull := transform.TxfmDimensions[tx]
					twFull := int(tdFull.W) * 4
					thFull := int(tdFull.H) * 4
					maxOff := (thFull-1)*stride + (twFull - 1)
					if dstOff+maxOff < len(planeBuf) {
						ReconBlock(dst, stride, coeff, eob, tx, txtp, dq, 8)
					}
				}
			} else {
				fs.SetCoefCtx(plane, bx+tbx, by+tby, tx, 0x40)
			}
		}
	}
}

func decodePalettePlaneVarTx(
	m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fb *FrameBuf,
	plane, bx, by, bw, bh int,
	blocks []txBlockSpec,
	pal [8]uint8,
	palIdx []uint8,
	dq [2]uint16,
	skip bool,
	yMode int,
	reducedTxtpSet bool,
	qidxIsZero bool,
	lossless bool,
) {
	var planeBuf []byte
	var stride, planeW, planeH int
	switch plane {
	case 0:
		planeBuf = fb.Y
		stride = fb.StrideY
		planeW = fb.Width
		planeH = fb.Height
	case 1:
		planeBuf = fb.U
		stride = fb.StrideUV
		planeW = fb.ChromaW
		planeH = fb.ChromaH
	default:
		planeBuf = fb.V
		stride = fb.StrideUV
		planeW = fb.ChromaW
		planeH = fb.ChromaH
	}
	if len(palIdx) < bw*bh {
		return
	}
	for _, blk := range blocks {
		tw := blk.w
		th := blk.h
		if bx+blk.x+tw > planeW {
			tw = planeW - (bx + blk.x)
		}
		if by+blk.y+th > planeH {
			th = planeH - (by + blk.y)
		}
		if tw <= 0 || th <= 0 {
			continue
		}
		dstOff := (by+blk.y)*stride + (bx + blk.x)
		if dstOff >= len(planeBuf) {
			continue
		}
		dst := planeBuf[dstOff:]
		predBuf := make([]byte, tw*th)
		predictPalette(predBuf, tw, pal, palIdx[blk.y*bw+blk.x:], tw, th, bw)
		for row := 0; row < th; row++ {
			dstRow := (by+blk.y+row)*stride + (bx + blk.x)
			if dstRow+tw > len(planeBuf) {
				break
			}
			copy(planeBuf[dstRow:dstRow+tw], predBuf[row*tw:(row+1)*tw])
		}
		if !skip {
			coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, blk.tx, plane, bx+blk.x, by+blk.y, tw, th, yMode, reducedTxtpSet, qidxIsZero, lossless)
			fs.SetCoefCtx(plane, bx+blk.x, by+blk.y, blk.tx, resCtx)
			if eob >= 0 && len(coeff) > 0 {
				tdFull := transform.TxfmDimensions[blk.tx]
				twFull := int(tdFull.W) * 4
				thFull := int(tdFull.H) * 4
				maxOff := (thFull-1)*stride + (twFull - 1)
				if dstOff+maxOff < len(planeBuf) {
					ReconBlock(dst, stride, coeff, eob, blk.tx, txtp, dq, 8)
				}
			}
		} else {
			fs.SetCoefCtx(plane, bx+blk.x, by+blk.y, blk.tx, 0x40)
		}
	}
}

type txBlockSpec struct {
	tx uint8
	x  int
	y  int
	w  int
	h  int
}

func readVarTxTree(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, bx, by, bw, bh int, maxTx uint8) (uint8, []txBlockSpec, Av1Block) {
	block := Av1Block{
		Tx:     maxTx,
		MaxYTx: maxTx,
		Uvtx:   largestTx((bw+1)/2, (bh+1)/2),
	}
	specs := make([]txBlockSpec, 0, 16)
	minTx := maxTx
	readTxTree(m, ctx, fs, bx, by, bw, bh, maxTx, 0, 0, 0, &block, &specs, &minTx)
	block.Tx = minTx
	return minTx, specs, block
}

func readTxTree(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, bx, by, bw, bh int, tx uint8, depth, xOff, yOff int, block *Av1Block, specs *[]txBlockSpec, minTx *uint8) {
	td := transform.TxfmDimensions[tx]
	txw := int(td.W) * 4
	txh := int(td.H) * 4
	px := xOff * txw
	py := yOff * txh
	if px >= bw || py >= bh {
		return
	}

	isSplit := false
	if depth < 2 && tx > transform.TX4x4 {
		cat := 2*(int(transform.TX64x64)-int(td.Max)) - depth
		if cat >= 0 && cat < len(ctx.TxPartCDF) {
			tctx := fs.TxCtx(bx+px, by+py, tx)
			isSplit = m.BoolAdapt(ctx.TxPartCDF[cat][tctx][:]) != 0
			if isSplit {
				if depth == 0 {
					block.TxSplit0 |= 1 << (yOff*4 + xOff)
				} else {
					block.TxSplit1 |= 1 << (yOff*4 + xOff)
				}
			}
		}
	}

	if isSplit && td.Max > 1 {
		sub := td.Sub
		subDim := transform.TxfmDimensions[sub]
		subW := int(subDim.W) * 4
		subH := int(subDim.H) * 4

		readTxTree(m, ctx, fs, bx, by, bw, bh, sub, depth+1, xOff*2, yOff*2, block, specs, minTx)
		if txw >= txh && px+subW < bw {
			readTxTree(m, ctx, fs, bx, by, bw, bh, sub, depth+1, xOff*2+1, yOff*2, block, specs, minTx)
		}
		if txh >= txw && py+subH < bh {
			readTxTree(m, ctx, fs, bx, by, bw, bh, sub, depth+1, xOff*2, yOff*2+1, block, specs, minTx)
			if txw >= txh && px+subW < bw {
				readTxTree(m, ctx, fs, bx, by, bw, bh, sub, depth+1, xOff*2+1, yOff*2+1, block, specs, minTx)
			}
		}
		return
	}

	if tx < *minTx {
		*minTx = tx
	}
	fs.SetTxCtx(bx+px, by+py, txw, txh, tx, true, false)
	*specs = append(*specs, txBlockSpec{tx: tx, x: px, y: py, w: txw, h: txh})
}

func decodeIntraPlaneVarTx(
	m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fb *FrameBuf,
	plane, bx, by, bw, bh int,
	blocks []txBlockSpec,
	mode, angleDelta, filterMode int,
	dq [2]uint16,
	skip bool,
	yMode int,
	reducedTxtpSet bool,
	fhdr *header.FrameHeader,
	seq *header.SequenceHeader,
	qidxIsZero bool,
	lossless bool,
) {
	var planeBuf []byte
	var stride, planeW, planeH int
	switch plane {
	case 0:
		planeBuf = fb.Y
		stride = fb.StrideY
		planeW = fb.Width
		planeH = fb.Height
	case 1:
		planeBuf = fb.U
		stride = fb.StrideUV
		planeW = fb.ChromaW
		planeH = fb.ChromaH
	default:
		planeBuf = fb.V
		stride = fb.StrideUV
		planeW = fb.ChromaW
		planeH = fb.ChromaH
	}

	maxDim := bw
	if bh > maxDim {
		maxDim = bh
	}
	tlBuf := make([]byte, 4*maxDim+2)
	tl := 2 * maxDim
	fillTopleft(planeBuf, stride, planeW, planeH, bx, by, bw, bh, tlBuf, tl)

	for _, blk := range blocks {
		tw := blk.w
		th := blk.h
		if bx+blk.x+tw > planeW {
			tw = planeW - (bx + blk.x)
		}
		if by+blk.y+th > planeH {
			th = planeH - (by + blk.y)
		}
		if tw <= 0 || th <= 0 {
			continue
		}
		dstOff := (by+blk.y)*stride + (bx + blk.x)
		if dstOff >= len(planeBuf) {
			continue
		}
		dst := planeBuf[dstOff:]
		predBuf := make([]byte, tw*th)

		callIntraPred(mode, angleDelta, filterMode, predBuf, tw, tlBuf, tl, tw, th)
		for row := 0; row < th; row++ {
			dstRow := (by+blk.y+row)*stride + (bx + blk.x)
			if dstRow+tw > len(planeBuf) {
				break
			}
			copy(planeBuf[dstRow:dstRow+tw], predBuf[row*tw:(row+1)*tw])
		}

		if !skip {
			coeffMode := yMode
			if plane > 0 {
				coeffMode = mode
			}
			coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, blk.tx, plane, bx+blk.x, by+blk.y, tw, th, coeffMode, reducedTxtpSet, qidxIsZero, lossless)
			fs.SetCoefCtx(plane, bx+blk.x, by+blk.y, blk.tx, resCtx)
			if eob >= 0 && len(coeff) > 0 {
				tdFull := transform.TxfmDimensions[blk.tx]
				twFull := int(tdFull.W) * 4
				thFull := int(tdFull.H) * 4
				maxOff := (thFull-1)*stride + (twFull - 1)
				if dstOff+maxOff < len(planeBuf) {
					ReconBlock(dst, stride, coeff, eob, blk.tx, txtp, dq, 8)
				}
			}
		} else {
			fs.SetCoefCtx(plane, bx+blk.x, by+blk.y, blk.tx, 0x40)
		}
		updateTopleft(planeBuf, stride, planeW, planeH, bx+blk.x, by+blk.y, tw, th, tlBuf, tl)
	}
	_ = fhdr
	_ = seq
}

// decodeIntraPlane performs intra prediction + coefficient decode + reconstruction
// for one plane within a block.
//
//	plane: 0=Y, 1=U, 2=V
//	mode:  IntraPredMode constant
//	cflAlpha: only used when mode == CFLPred
//	yMode: luma intra prediction mode (used for chroma txtp derivation)
//	reducedTxtpSet: from fhdr.ReducedTxtpSet
func decodeIntraPlane(
	m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fb *FrameBuf,
	plane, bx, by, bw, bh int,
	tx uint8,
	mode, angleDelta, filterMode, cflAlpha int,
	dq [2]uint16,
	skip bool,
	yMode int,
	reducedTxtpSet bool,
	fhdr *header.FrameHeader,
	seq *header.SequenceHeader,
	qidxIsZero bool,
	lossless bool,
) {
	// Select plane buffer.
	var planeBuf []byte
	var stride, planeW, planeH int
	switch plane {
	case 0:
		planeBuf = fb.Y
		stride = fb.StrideY
		planeW = fb.Width
		planeH = fb.Height
	case 1:
		planeBuf = fb.U
		stride = fb.StrideUV
		planeW = fb.ChromaW
		planeH = fb.ChromaH
	default:
		planeBuf = fb.V
		stride = fb.StrideUV
		planeW = fb.ChromaW
		planeH = fb.ChromaH
	}

	if bx >= planeW || by >= planeH || len(planeBuf) == 0 {
		return
	}
	if bx+bw > planeW {
		bw = planeW - bx
	}
	if by+bh > planeH {
		bh = planeH - by
	}

	// Build topleft reference buffer for intra prediction.
	// Layout (matches intra package convention, with extension for Z1/Z3
	// directional prediction which can index up to ~2*(w+h) samples):
	//   topleft[tl-2*maxDim..tl-1] = left samples (top-to-bottom),
	//                                extended past bh by replicating last
	//   topleft[tl]                = top-left sample
	//   topleft[tl+1..tl+2*maxDim] = top samples (left-to-right),
	//                                extended past bw by replicating last
	maxDim := bw
	if bh > maxDim {
		maxDim = bh
	}
	tlBufSize := 4*maxDim + 2 // extended for Z1/Z3 directional reach
	tlBuf := make([]byte, tlBufSize)
	tl := 2 * maxDim // index of the top-left sample

	fillTopleft(planeBuf, stride, planeW, planeH, bx, by, bw, bh, tlBuf, tl)

	// Transform dimensions.
	td := transform.TxfmDimensions[tx]
	tw := int(td.W) * 4
	th := int(td.H) * 4
	if tw > bw {
		tw = bw
	}
	if th > bh {
		th = bh
	}

	// Iterate over transform blocks within the coding block.
	predBuf := make([]byte, tw*th)

	for tby := 0; tby < bh; tby += th {
		for tbx := 0; tbx < bw; tbx += tw {
			dstOff := (by+tby)*stride + (bx + tbx)
			if dstOff >= len(planeBuf) {
				continue
			}
			dst := planeBuf[dstOff:]

			// 1. Intra prediction into predBuf.
			callIntraPred(mode, angleDelta, filterMode, predBuf, tw, tlBuf, tl, tw, th)

			// 2. Copy prediction to destination.
			for row := 0; row < th; row++ {
				dstRow := (by+tby+row)*stride + (bx + tbx)
				if dstRow+tw > len(planeBuf) {
					break
				}
				copy(planeBuf[dstRow:dstRow+tw], predBuf[row*tw:(row+1)*tw])
			}

			// 3. Decode and apply residual (if not skipped).
			if !skip {
				coeffMode := yMode
				if plane > 0 {
					coeffMode = mode
				}
				coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, tx, plane, bx+tbx, by+tby, bw, bh, coeffMode, reducedTxtpSet, qidxIsZero, lossless)
				fs.SetCoefCtx(plane, bx+tbx, by+tby, tx, resCtx)
				if eob >= 0 && len(coeff) > 0 {
					// Guard against partial tx blocks at plane edges:
					// InvTxfmAdd writes dst[(th-1)*stride + (tw-1)] which would
					// overrun planeBuf when the block straddles the bottom/right.
					tdFull := transform.TxfmDimensions[tx]
					twFull := int(tdFull.W) * 4
					thFull := int(tdFull.H) * 4
					maxOff := (thFull-1)*stride + (twFull - 1)
					if dstOff+maxOff < len(planeBuf) {
						ReconBlock(dst, stride, coeff, eob, tx, txtp, dq, 8)
					}
				}
			} else {
				fs.SetCoefCtx(plane, bx+tbx, by+tby, tx, 0x40)
			}

			// Update topleft buffer from reconstructed samples for next tx block.
			updateTopleft(planeBuf, stride, planeW, planeH, bx+tbx, by+tby, tw, th, tlBuf, tl)
		}
	}
}

// ---------------------------------------------------------------------------
// Intra prediction dispatch
// ---------------------------------------------------------------------------

// callIntraPred calls the appropriate intra prediction kernel.
func callIntraPred(mode, angleDelta, filterMode int, dst []byte, stride int, topleft []byte, tl, width, height int) {
	if filterMode >= 0 {
		intra.PredFilter(dst, stride, topleft, tl, width, height, filterMode)
		return
	}
	if mode >= VertPred && mode <= VertLeftPred {
		angle := intraModeAngle(mode) + 3*angleDelta
		switch {
		case angle < 90:
			intra.PredZ1(dst, stride, topleft, tl, width, height, angle)
		case angle == 90:
			intra.PredV(dst, stride, topleft, tl, width, height)
		case angle < 180:
			intra.PredZ2(dst, stride, topleft, tl, width, height, angle, width, height)
		case angle == 180:
			intra.PredH(dst, stride, topleft, tl, width, height)
		default:
			intra.PredZ3(dst, stride, topleft, tl, width, height, angle)
		}
		return
	}
	switch mode {
	case DCPred:
		intra.PredDC(dst, stride, topleft, tl, width, height)
	case PaethPred:
		intra.PredPaeth(dst, stride, topleft, tl, width, height)
	case SmoothPred:
		// SMOOTH requires right and bottom extensions.
		intra.PredSmooth(dst, stride, topleft, tl, width, height)
	case SmoothVPred:
		intra.PredSmoothV(dst, stride, topleft, tl, width, height)
	case SmoothHPred:
		intra.PredSmoothH(dst, stride, topleft, tl, width, height)
	default:
		// CFL or unknown → DC fallback.
		intra.PredDC(dst, stride, topleft, tl, width, height)
	}
}

func intraModeAngle(mode int) int {
	switch mode {
	case VertPred:
		return 90
	case HorPred:
		return 180
	case DiagDownLeftPred:
		return 45
	case DiagDownRightPred:
		return 135
	case VertRightPred:
		return 113
	case HorDownPred:
		return 157
	case HorUpPred:
		return 203
	case VertLeftPred:
		return 67
	default:
		return 90
	}
}

// ---------------------------------------------------------------------------
// Coefficient decoding (M8 Task 2 — dav1d-aligned)
//
// Mirrors dav1d/src/recon_tmpl.c decode_coefs(). Coefficients are stored in
// the packed rc layouts consumed by the live dequant/itxfm path:
//   - TX_CLASS_2D: rc = (x << shift) | y
//   - TX_CLASS_H:  rc = scan position i (dav1d's transposed H-class layout)
//   - TX_CLASS_V:  rc = (x << shift2) | y
//   - dav1d's MSAC.SymbolAdapt returns 0..n_symbols (n_symbols+1 values) by
//     using cdf[n_symbols] as a counter. Our Go MSAC.Symbol returns 0..n-1
//     and requires cdf[n-1]=0 sentinel. So our CDF size is dav1d's + 1, and
//     we pass n = (dav1d n_symbols) + 1.
// ---------------------------------------------------------------------------

// readGolomb decodes a unary-prefixed value, mirroring dav1d's read_golomb.
func readGolomb(m *bitstream.MSAC) uint32 {
	length := 0
	val := uint32(1)
	for length < 32 {
		if m.BoolEqui() != 0 {
			break
		}
		length++
	}
	for ; length > 0; length-- {
		val = (val << 1) | m.BoolEqui()
	}
	return val - 1
}

// getLoCtx2D mirrors dav1d get_lo_ctx for TX_CLASS_2D. Returns (ctx, hi_mag).
func getLoCtx2D(levels []uint8, base int, stride int, ctxOff *[5][5]uint8, x, y int) (int, int) {
	mag := int(levels[base+0*stride+1]) + int(levels[base+1*stride+0])
	mag += int(levels[base+1*stride+1])
	hiMag := mag
	mag += int(levels[base+0*stride+2]) + int(levels[base+2*stride+0])
	xi := x
	yi := y
	if xi > 4 {
		xi = 4
	}
	if yi > 4 {
		yi = 4
	}
	offset := int(ctxOff[yi][xi])
	var add int
	if mag > 512 {
		add = 4
	} else {
		add = (mag + 64) >> 7
	}
	return offset + add, hiMag
}

// getLoCtx1D mirrors dav1d get_lo_ctx for TX_CLASS_H/V.
func getLoCtx1D(levels []uint8, base, stride, y int) (int, int) {
	mag := int(levels[base+0*stride+1]) + int(levels[base+1*stride+0])
	mag += int(levels[base+0*stride+2])
	hiMag := mag
	mag += int(levels[base+0*stride+3]) + int(levels[base+0*stride+4])
	var offset int
	if y > 1 {
		offset = 26 + 10
	} else {
		offset = 26 + y*5
	}
	var add int
	if mag > 512 {
		add = 4
	} else {
		add = (mag + 64) >> 7
	}
	return offset + add, hiMag
}

// decodeCoefficients reads txtp, EOB, base/hi tokens, dc_sign and golomb
// extra-bits for one transform block. Returns (coefficients, eob, txtp).
//
// `qidxIsZero`: true iff frame_hdr.segmentation.qidx[seg_id] == 0
// `lossless` :  true iff frame_hdr.segmentation.lossless[seg_id]
func decodeCoefficients(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, tx uint8, plane int,
	bx, by, bw, bh int, yMode int, reducedTxtpSet bool, qidxIsZero bool, lossless bool,
) ([]int32, int, uint8, uint8) {
	td := transform.TxfmDimensions[tx]
	chroma := 0
	if plane > 0 {
		chroma = 1
	}
	blockW := int(td.W) * 4
	blockH := int(td.H) * 4
	n := blockW * blockH

	skipCtx := fs.CoefSkipCtx(plane, bx, by, bw, bh, tx)
	if int(td.Ctx) < len(ctx.CoefSkipFull) {
		allSkip := m.BoolAdapt(ctx.CoefSkipFull[td.Ctx][skipCtx][:])
		if allSkip != 0 {
			return nil, -1, transform.DCT_DCT, 0x40
		}
	}

	// --- Transform type ---------------------------------------------------
	var txtp uint8
	switch {
	case lossless:
		// dav1d uses WHT_WHT here; transform pkg has none, fall back to DCT_DCT
		// (visual quality picked up in Task 7).
		txtp = transform.DCT_DCT
	case int(td.Max) >= 3: // TX32/TX64: forced DCT_DCT
		txtp = transform.DCT_DCT
	case chroma == 1:
		if yMode == CFLPred {
			txtp = transform.DCT_DCT
		} else if yMode >= 0 && yMode < len(TxtpFromUVMode) {
			txtp = TxtpFromUVMode[yMode]
		}
	case qidxIsZero:
		// Implicit-lossless luma path (dav1d L361-L365).
		txtp = transform.DCT_DCT
	default:
		yModeCtx := clampInt(yMode, 0, NIntraPredModes-1)
		if reducedTxtpSet || td.Min >= 2 {
			txClassCtx := clampInt(int(td.Min), 0, 2)
			idx := m.SymbolAdapt(ctx.TxTypeIntra2CDF[txClassCtx][yModeCtx][:], TxTypeIntra2Symbols)
			if int(idx) < len(TxTypeIntra2Set) {
				txtp = TxTypeIntra2Set[idx]
			}
		} else {
			txClassCtx := clampInt(int(td.Min), 0, 1)
			idx := m.SymbolAdapt(ctx.TxTypeIntra1CDF[txClassCtx][yModeCtx][:], TxTypeIntra1Symbols)
			if int(idx) < len(TxTypeIntra1Set) {
				txtp = TxTypeIntra1Set[idx]
			}
		}
	}
	txtp = clampTxType(txtp, td.Lw, td.Lh)

	cls := DAV1DTxTypeClass[txtp]
	is1d := uint8(0)
	if cls != TxClass2D {
		is1d = 1
	}

	// --- EOB --------------------------------------------------------------
	slw := int(td.Lw)
	if slw > 3 {
		slw = 3
	}
	slh := int(td.Lh)
	if slh > 3 {
		slh = 3
	}
	tx2dszctx := slw + slh
	var eob int
	switch tx2dszctx {
	case 0:
		eob = int(m.SymbolAdapt(ctx.EobBin16Full[chroma][is1d][:], 4))
	case 1:
		eob = int(m.SymbolAdapt(ctx.EobBin32Full[chroma][is1d][:], 5))
	case 2:
		eob = int(m.SymbolAdapt(ctx.EobBin64Full[chroma][is1d][:], 6))
	case 3:
		eob = int(m.SymbolAdapt(ctx.EobBin128Full[chroma][is1d][:], 7))
	case 4:
		eob = int(m.SymbolAdapt(ctx.EobBin256Full[chroma][is1d][:], 8))
	case 5:
		eob = int(m.SymbolAdapt(ctx.EobBin512Full[chroma][:], 9))
	default: // 6
		eob = int(m.SymbolAdapt(ctx.EobBin1024Full[chroma][:], 10))
	}
	if eob > 1 {
		eb := eob - 2
		if eb < 0 {
			eb = 0
		} else if eb > 8 {
			eb = 8
		}
		eobHiBit := int(m.BoolAdapt(ctx.EobHiBitFull[td.Ctx][chroma][eb][:]))
		extra := uint32(0)
		for k := 0; k < eb; k++ {
			extra = (extra << 1) | m.BoolEqui()
		}
		eob = ((eobHiBit | 2) << uint(eb)) | int(extra)
	}
	if eob >= n {
		eob = n - 1
	}

	// --- Token decode -----------------------------------------------------
	coeff := make([]int32, n)

	// levels buffer & strides per dav1d.
	var stride, levelsLen int
	var shift uint
	var mask int
	switch cls {
	case TxClass2D:
		stride = 4 << uint(slh)
		levelsLen = stride * ((4 << uint(slw)) + 2)
		shift = uint(slh + 2)
		mask = (4 << uint(slh)) - 1
	case TxClassH:
		stride = 16
		levelsLen = stride * ((4 << uint(slh)) + 2)
		shift = uint(slh + 2)
		mask = (4 << uint(slh)) - 1
	default: // TxClassV
		stride = 16
		levelsLen = stride * ((4 << uint(slw)) + 2)
		shift = uint(slw + 2)
		mask = (4 << uint(slw)) - 1
	}
	levels := make([]uint8, levelsLen)

	// 2D shape index for lo_ctx_offsets.
	var ctxOff *[5][5]uint8
	if cls == TxClass2D {
		shape := 0
		if td.Lw > td.Lh {
			shape = 1
		} else if td.Lw < td.Lh {
			shape = 2
		}
		ctxOff = &DAV1DLoCtxOffsets[shape]
	}

	// 2D scan table (only used by TX_CLASS_2D).
	var scan []uint16
	if cls == TxClass2D {
		scan = GetScan(td.Lw, td.Lh, cls)
	}

	txCtx := int(td.Ctx)
	txCtxBr := txCtx
	if txCtxBr > 3 {
		txCtxBr = 3
	}
	eobCdf := &ctx.EobBaseTokFull[txCtx][chroma]
	loCdf := &ctx.BaseTokFull[txCtx][chroma]
	hiCdf := &ctx.BrTokFull[txCtxBr][chroma]

	packedH := 4 << uint(slh)

	// coeffIdx maps dav1d decode-coefs coordinates to the live coefficient
	// buffer layout consumed by dequant/itxfm.
	coeffIdx := func(x, y, scanPos int) int {
		if scanPos < 0 {
			return -1
		}
		if cls == TxClassH {
			if scanPos >= n {
				return -1
			}
			return scanPos
		}
		if x < 0 || x >= blockW || y < 0 || y >= blockH {
			return -1
		}
		return x*packedH + y
	}
	// dav1d first decodes all base/high tokens, then reads the DC sign and AC
	// signs/residuals. Keep token magnitudes here and apply signs afterward.
	dcSignCtx := fs.DCSignCtx(plane, bx, by, tx)
	dcTok := 0
	acNonZero := make([]int, 0, eob)
	setCoeffToken := func(coeffIdx, tok int) {
		if coeffIdx < 0 || tok == 0 {
			return
		}
		coeff[coeffIdx] = int32(tok)
		if coeffIdx != 0 {
			acNonZero = append(acNonZero, coeffIdx)
		}
	}

	if eob > 0 {
		// EOB position (i = eob)
		var x, y, levelIdx int
		if cls == TxClass2D {
			if eob >= len(scan) {
				return coeff, eob, txtp, 0x40
			}
			rcRaw := int(scan[eob])
			x = rcRaw >> shift
			y = rcRaw & mask
			levelIdx = rcRaw
		} else if cls == TxClassH {
			x = eob & mask
			y = eob >> shift
			levelIdx = x*stride + y
		} else {
			x = eob & mask
			y = eob >> shift
			levelIdx = x*stride + y
		}

		bctx := 1
		if eob > (2 << uint(tx2dszctx)) {
			bctx++
		}
		if eob > (4 << uint(tx2dszctx)) {
			bctx++
		}
		if bctx > 3 {
			bctx = 3
		}
		eobTok := int(m.SymbolAdapt(eobCdf[bctx][:], 3)) // 0..2
		tok := eobTok + 1
		levelTok := tok * 0x41
		if eobTok == 2 {
			var hctx int
			if cls == TxClass2D {
				if (x | y) > 1 {
					hctx = 14
				} else {
					hctx = 7
				}
			} else {
				if y != 0 {
					hctx = 14
				} else {
					hctx = 7
				}
			}
			tok = int(m.HiTok(hiCdf[hctx][:]))
			levelTok = tok + (3 << 6)
		}
		setCoeffToken(coeffIdx(x, y, eob), tok)
		if levelIdx >= 0 && levelIdx < len(levels) {
			levels[levelIdx] = uint8(levelTok)
		}

		// AC tokens: i = eob-1 .. 1
		for i := eob - 1; i > 0; i-- {
			var xi, yi, lvlIdx int
			if cls == TxClass2D {
				if i >= len(scan) {
					continue
				}
				r := int(scan[i])
				xi = r >> shift
				yi = r & mask
				lvlIdx = r
			} else if cls == TxClassH {
				xi = i & mask
				yi = i >> shift
				lvlIdx = xi*stride + yi
			} else {
				xi = i & mask
				yi = i >> shift
				lvlIdx = xi*stride + yi
			}
			var loCtx, hiMag int
			if cls == TxClass2D {
				loCtx, hiMag = getLoCtx2D(levels, lvlIdx, stride, ctxOff, xi, yi)
			} else {
				loCtx, hiMag = getLoCtx1D(levels, lvlIdx, stride, yi)
			}
			ytmp := yi
			if cls == TxClass2D {
				ytmp = yi | xi
			}
			toki := int(m.SymbolAdapt(loCdf[loCtx][:], 4)) // 0..3
			if toki == 3 {
				mag := uint(hiMag) & 63
				var hctx int
				yThresh := 0
				if cls == TxClass2D {
					yThresh = 1
				}
				if ytmp > yThresh {
					hctx = 14
				} else {
					hctx = 7
				}
				if mag > 12 {
					hctx += 6
				} else {
					hctx += int((mag + 1) >> 1)
				}
				toki = int(m.HiTok(hiCdf[hctx][:]))
				if lvlIdx >= 0 && lvlIdx < len(levels) {
					levels[lvlIdx] = uint8(toki + (3 << 6))
				}
			} else if lvlIdx >= 0 && lvlIdx < len(levels) {
				levels[lvlIdx] = uint8(toki * 0x41)
			}
			setCoeffToken(coeffIdx(xi, yi, i), toki)
		}
	}

	// DC token (i = 0). For eob==0 dav1d uses eob_base_tok[0] and forces a
	// non-zero DC token; otherwise it uses the regular base_tok context.
	if eob == 0 {
		tokBr := int(m.SymbolAdapt(eobCdf[0][:], 3))
		dcTok = tokBr + 1
		if tokBr == 2 {
			dcTok = int(m.HiTok(hiCdf[0][:]))
		}
	} else {
		if cls == TxClass2D {
			dcCtx, _ := getLoCtx2D(levels, 0, stride, ctxOff, 0, 0)
			dcTok = int(m.SymbolAdapt(loCdf[dcCtx][:], 4))
		} else {
			dcCtx, _ := getLoCtx1D(levels, 0, stride, 0)
			dcTok = int(m.SymbolAdapt(loCdf[dcCtx][:], 4))
		}
		if dcTok == 3 {
			var dcMag uint
			if cls == TxClass2D {
				dcMag = uint(levels[0*stride+1]) + uint(levels[1*stride+0]) + uint(levels[1*stride+1])
			} else {
				dcMag = uint(levels[0*stride+1]) + uint(levels[0*stride+2]) + uint(levels[0*stride+3])
			}
			dcMag &= 63
			var hctx int
			if dcMag > 12 {
				hctx = 6
			} else {
				hctx = int((dcMag + 1) >> 1)
			}
			dcTok = int(m.HiTok(hiCdf[hctx][:]))
		}
	}
	setCoeffToken(coeffIdx(0, 0, 0), dcTok)

	// Sign and residual pass. dav1d reads DC sign first if DC is non-zero,
	// then AC signs in low-to-high scan order. acNonZero was collected while
	// scanning high-to-low, so iterate it backward.
	culLevel := 0
	dcSignLevel := uint8(0x40)
	if dcTok != 0 {
		sign := m.BoolAdapt(ctx.DCSignCDF[chroma][dcSignCtx][:])
		mag := dcTok
		if mag == 15 {
			mag = int(readGolomb(m)) + 15
		}
		culLevel += mag
		if sign != 0 {
			coeff[0] = int32(-mag)
			dcSignLevel = 0x00
		} else {
			coeff[0] = int32(mag)
			dcSignLevel = 0x80
		}
	}
	for i := len(acNonZero) - 1; i >= 0; i-- {
		idx := acNonZero[i]
		mag := int(coeff[idx])
		if mag == 0 {
			continue
		}
		sign := m.BoolEqui()
		if mag == 15 {
			mag = int(readGolomb(m)) + 15
		}
		culLevel += mag
		if sign != 0 {
			coeff[idx] = int32(-mag)
		} else {
			coeff[idx] = int32(mag)
		}
	}
	if culLevel > 63 {
		culLevel = 63
	}
	resCtx := uint8(culLevel) | dcSignLevel

	return coeff, eob, txtp, resCtx
}

// clampTxType restricts txtp to the 1D transform types supported by the
// given transform dimensions. AV1 spec §7.12.2:
//   - TX32 (lw or lh == 3): only DCT and IDENTITY are valid.
//   - TX64 (lw or lh == 4): only DCT is valid.
//
// If either dimension doesn't support the decoded 1D type, fall back to DCT_DCT.
func clampTxType(txtp, lw, lh uint8) uint8 {
	txtps := transform.Tx1dTypes[txtp]
	row1d := txtps[0] // first 1D pass over rows
	col1d := txtps[1] // second 1D pass over columns

	// Check row transform
	if lw >= 3 { // TX32 or TX64 in width
		if row1d != transform.Tx1dDCT && row1d != transform.Tx1dIDENTITY {
			return transform.DCT_DCT
		}
		if lw >= 4 && row1d == transform.Tx1dIDENTITY {
			return transform.DCT_DCT // TX64 doesn't support IDENTITY either
		}
	}
	// Check column transform
	if lh >= 3 { // TX32 or TX64 in height
		if col1d != transform.Tx1dDCT && col1d != transform.Tx1dIDENTITY {
			return transform.DCT_DCT
		}
		if lh >= 4 && col1d == transform.Tx1dIDENTITY {
			return transform.DCT_DCT
		}
	}
	return txtp
}

// ---------------------------------------------------------------------------
// Helper utilities
// ---------------------------------------------------------------------------

// fillTopleft fills the topleft reference buffer for intra prediction
// from previously-reconstructed neighbours. Pixels outside the frame or
// not yet reconstructed default to 128 (the AV1 spec value for missing
// neighbours), matching dav1d's ipred_prepare behaviour.
func fillTopleft(planeBuf []byte, stride, planeW, planeH, bx, by, bw, bh int,
	tlBuf []byte, tl int) {

	// Default to spec-defined 128 for unavailable neighbours.
	for i := range tlBuf {
		tlBuf[i] = 128
	}

	// dav1d's ipred_prepare extends the edge buffer past the block edge by
	// replicating the last available sample, so directional predictors
	// (PredZ1/Z2/Z3) can index up to ~2*(w+h) without reading default 128.
	extent := tl // tl == 2*maxDim (ensures left/top each have 2*maxDim slots)

	// Top-left sample.
	if bx > 0 && by > 0 {
		off := (by-1)*stride + (bx - 1)
		if off >= 0 && off < len(planeBuf) {
			tlBuf[tl] = planeBuf[off]
		}
	}

	// Top row (left→right: tlBuf[tl+1..tl+extent]).
	if by > 0 {
		var lastTop byte = 128
		haveLast := false
		for x := 0; x < extent; x++ {
			srcX := bx + x
			if srcX >= planeW {
				srcX = planeW - 1
			}
			off := (by-1)*stride + srcX
			if off >= 0 && off < len(planeBuf) {
				if x < bw && bx+x < planeW {
					tlBuf[tl+1+x] = planeBuf[off]
					lastTop = planeBuf[off]
					haveLast = true
				} else if haveLast {
					tlBuf[tl+1+x] = lastTop
				} else {
					tlBuf[tl+1+x] = planeBuf[off]
				}
			}
		}
	}

	// Left column (top→bottom: tlBuf[tl-1..tl-extent]).
	if bx > 0 {
		var lastLeft byte = 128
		haveLast := false
		for y := 0; y < extent; y++ {
			srcY := by + y
			if srcY >= planeH {
				srcY = planeH - 1
			}
			off := srcY*stride + (bx - 1)
			if off >= 0 && off < len(planeBuf) {
				if y < bh && by+y < planeH {
					tlBuf[tl-1-y] = planeBuf[off]
					lastLeft = planeBuf[off]
					haveLast = true
				} else if haveLast {
					tlBuf[tl-1-y] = lastLeft
				} else {
					tlBuf[tl-1-y] = planeBuf[off]
				}
			}
		}
	}
}

// updateTopleft refreshes the topleft buffer from the reconstructed tx block,
// so subsequent tx blocks within the same coding block see correct neighbours.
func updateTopleft(planeBuf []byte, stride, planeW, planeH, bx, by, tw, th int,
	tlBuf []byte, tl int) {

	// Update right edge of left column (bottom of the tx block → tl-th).
	lastY := by + th - 1
	if bx > 0 && lastY < planeH {
		off := lastY*stride + (bx - 1)
		if off >= 0 && off < len(planeBuf) {
			tlBuf[tl-th] = planeBuf[off]
		}
	}
	// Update bottom of top row (right edge → tl+tw).
	lastX := bx + tw - 1
	if by > 0 && lastX < planeW {
		off := (by-1)*stride + lastX
		if off >= 0 && off < len(planeBuf) {
			tlBuf[tl+tw] = planeBuf[off]
		}
	}
}

// fillDC128 fills the luma and both chroma planes for block (bx,by,bw,bh)
// with a position-derived pseudo-grey value. Used for inter blocks in M7
// where motion compensation is not yet implemented.
//
// We seed with a position gradient (instead of the spec's flat 128) so that
// the reconstructed frame has visible texture, mirroring fillTopleft's
// behaviour. M8+ replaces this with proper inter prediction.
func fillDC128(fb *FrameBuf, seq *header.SequenceHeader, bx, by, bw, bh int) {
	yFill := byte(64 + ((bx + by) & 0x7F))
	cbx, cby, cbw, cbh := chromaRect(seq, bx, by, bw, bh)
	uFill := byte(96 + ((cbx + cby) & 0x3F))
	vFill := byte(96 + ((cbx - cby) & 0x3F))
	fillPlaneConst(fb.Y, fb.StrideY, fb.Width, fb.Height, bx, by, bw, bh, yFill)
	fillPlaneConst(fb.U, fb.StrideUV, fb.ChromaW, fb.ChromaH, cbx, cby, cbw, cbh, uFill)
	fillPlaneConst(fb.V, fb.StrideUV, fb.ChromaW, fb.ChromaH, cbx, cby, cbw, cbh, vFill)
}

func fillPlaneConst(plane []byte, stride, pw, ph, bx, by, bw, bh int, fill byte) {
	if len(plane) == 0 {
		return
	}
	for row := 0; row < bh; row++ {
		y := by + row
		if y >= ph {
			break
		}
		off := y*stride + bx
		end := off + bw
		if end > len(plane) {
			end = len(plane)
		}
		if off >= end {
			continue
		}
		for i := off; i < end; i++ {
			plane[i] = fill
		}
	}
}

// copyInterRefBlock copies a same-position block from the first available
// reference frame. This is a temporary zero-MV predictor used to connect the
// reference buffer to inter-frame reconstruction before full MV syntax support.
func copyInterRefBlock(fb *FrameBuf, seq *header.SequenceHeader, bx, by, bw, bh int) bool {
	refSlot, ref := primaryInterRef(fb, nil)
	if ref == nil || len(ref.Y) == 0 {
		return false
	}
	mv := refmvs.MV{}
	_ = refSlot
	copyInterPredictPlane(fb.Y, fb.StrideY, fb.Width, fb.Height, ref.Y, ref.StrideY, ref.Width, ref.Height, bx, by, bw, bh, mv, header.FilterMode8TapRegular)
	if fb.Monochrome || ref.Monochrome {
		return true
	}
	cbx, cby, cbw, cbh := chromaRect(seq, bx, by, bw, bh)
	copyInterPredictPlane(fb.U, fb.StrideUV, fb.ChromaW, fb.ChromaH, ref.U, ref.StrideUV, ref.ChromaW, ref.ChromaH, cbx, cby, cbw, cbh, mv, header.FilterMode8TapRegular)
	copyInterPredictPlane(fb.V, fb.StrideUV, fb.ChromaW, fb.ChromaH, ref.V, ref.StrideUV, ref.ChromaW, ref.ChromaH, cbx, cby, cbw, cbh, mv, header.FilterMode8TapRegular)
	return true
}

func firstAvailableRef(fb *FrameBuf) *PlaneBuf {
	for _, ref := range fb.Refs {
		if ref != nil {
			return ref
		}
	}
	return nil
}

func primaryInterRef(fb *FrameBuf, fhdr *header.FrameHeader) (int, *PlaneBuf) {
	if fhdr != nil {
		for _, idx := range fhdr.Refidx {
			refSlot := int(idx)
			if refSlot >= 0 && refSlot < len(fb.Refs) && fb.Refs[refSlot] != nil {
				return refSlot, fb.Refs[refSlot]
			}
		}
	}
	for i, ref := range fb.Refs {
		if ref != nil {
			return i, ref
		}
	}
	return -1, nil
}

func frameRefSlot(fhdr *header.FrameHeader, refFrame int) (int, bool) {
	if fhdr == nil {
		return -1, false
	}
	// AV1 ref-frame enums are 1..7 for LAST..ALTREF. FrameHeader.Refidx is
	// indexed in that order with 0-based positions.
	if refFrame <= 0 || refFrame > len(fhdr.Refidx) {
		return -1, false
	}
	slot := int(fhdr.Refidx[refFrame-1])
	if slot < 0 {
		return -1, false
	}
	return slot, true
}

func slotRefFrame(fhdr *header.FrameHeader, refSlot int) (int, bool) {
	if fhdr == nil || refSlot < 0 {
		return 0, false
	}
	for i, idx := range fhdr.Refidx {
		if int(idx) == refSlot {
			return i + 1, true
		}
	}
	return 0, false
}

func decodeInterBlockFallback(fs *FrameState, fhdr *header.FrameHeader, seq *header.SequenceHeader,
	fb *FrameBuf, segID uint8, skip bool, bx, by, bw, bh int) {
	refSlot, refFrame, _, mv, filterMode, interMode, skipMode, ref := deriveInterFallback(fs, fb, fhdr, segID, skip, bx, by)

	block := Av1Block{
		Intra:     false,
		SegID:     segID,
		Skip:      skip,
		SkipMode:  skipMode,
		InterMode: uint8(interMode),
		RefSlot:   int8(refSlot),
		Filter:    uint8(filterMode),
		MV:        [2]int16{mv.Y, mv.X},
	}
	_ = block

	if ref != nil {
		copyInterPredictPlane(fb.Y, fb.StrideY, fb.Width, fb.Height, ref.Y, ref.StrideY, ref.Width, ref.Height, bx, by, bw, bh, mv, filterMode)
		if !fb.Monochrome && !ref.Monochrome && len(fb.U) != 0 && len(ref.U) != 0 {
			ssHor := int(seq.SsHor)
			ssVer := int(seq.SsVer)
			cmv := refmvs.MV{
				X: int16(floorDivPow2(int(mv.X), ssHor)),
				Y: int16(floorDivPow2(int(mv.Y), ssVer)),
			}
			cbx, cby, cbw, cbh := chromaRect(seq, bx, by, bw, bh)
			copyInterPredictPlane(fb.U, fb.StrideUV, fb.ChromaW, fb.ChromaH, ref.U, ref.StrideUV, ref.ChromaW, ref.ChromaH, cbx, cby, cbw, cbh, cmv, filterMode)
			copyInterPredictPlane(fb.V, fb.StrideUV, fb.ChromaW, fb.ChromaH, ref.V, ref.StrideUV, ref.ChromaW, ref.ChromaH, cbx, cby, cbw, cbh, cmv, filterMode)
		}
	} else {
		fillDC128(fb, seq, bx, by, bw, bh)
	}

	fs.SetInterBlock(bx, by, bw, bh, skip, segID, refSlot, refFrame, uint8(filterMode), interMode, mv)
}

func predictInterFallback(fb *FrameBuf, fhdr *header.FrameHeader, seq *header.SequenceHeader, segID uint8, bx, by, bw, bh int) bool {
	refSlot, _, refOrder, mv, filterMode, _, _, ref := deriveInterFallback(nil, fb, fhdr, segID, false, bx, by)
	if ref == nil {
		return false
	}
	_ = refSlot
	_ = refOrder

	copyInterPredictPlane(fb.Y, fb.StrideY, fb.Width, fb.Height, ref.Y, ref.StrideY, ref.Width, ref.Height, bx, by, bw, bh, mv, filterMode)
	if fb.Monochrome || ref.Monochrome || len(fb.U) == 0 || len(ref.U) == 0 {
		return true
	}

	ssHor := int(seq.SsHor)
	ssVer := int(seq.SsVer)
	cmv := refmvs.MV{
		X: int16(floorDivPow2(int(mv.X), ssHor)),
		Y: int16(floorDivPow2(int(mv.Y), ssVer)),
	}
	cbx, cby, cbw, cbh := chromaRect(seq, bx, by, bw, bh)
	copyInterPredictPlane(fb.U, fb.StrideUV, fb.ChromaW, fb.ChromaH, ref.U, ref.StrideUV, ref.ChromaW, ref.ChromaH, cbx, cby, cbw, cbh, cmv, filterMode)
	copyInterPredictPlane(fb.V, fb.StrideUV, fb.ChromaW, fb.ChromaH, ref.V, ref.StrideUV, ref.ChromaW, ref.ChromaH, cbx, cby, cbw, cbh, cmv, filterMode)
	return true
}

func deriveInterFallback(fs *FrameState, fb *FrameBuf, fhdr *header.FrameHeader, segID uint8, skip bool, bx, by int) (refSlot, refFrame, refOrder int, mv refmvs.MV, filterMode header.FilterMode, interMode int, skipMode bool, ref *PlaneBuf) {
	refOrder = 0
	refSlot, ref = primaryInterRef(fb, fhdr)
	filterMode = header.FilterMode8TapRegular
	interMode = InterModeZeroMV
	if fhdr == nil {
		return
	}
	if rf, ok := slotRefFrame(fhdr, refSlot); ok {
		refFrame = rf
	}
	if skip && fhdr.SkipModeEnabled != 0 {
		skipRefOrder := int(fhdr.SkipModeRefs[0])
		if skipRefOrder >= 0 && skipRefOrder < len(fhdr.Refidx) {
			skipRefSlot := int(fhdr.Refidx[skipRefOrder])
			if skipRefSlot >= 0 && skipRefSlot < len(fb.Refs) && fb.Refs[skipRefSlot] != nil {
				refSlot = skipRefSlot
				refFrame = skipRefOrder + 1
				ref = fb.Refs[skipRefSlot]
				refOrder = skipRefOrder
				skipMode = true
			}
		}
	}
	if fhdr.Segmentation.Enabled != 0 {
		segRef := int(fhdr.Segmentation.SegData.D[segID].Ref)
		if segSlot, ok := frameRefSlot(fhdr, segRef); ok && segSlot < len(fb.Refs) && fb.Refs[segSlot] != nil {
			refSlot = segSlot
			refFrame = segRef
			ref = fb.Refs[segSlot]
			refOrder = segRef - 1
			skipMode = false
		}
	}
	if !skipMode && fs != nil {
		if neighSlot, ok := fs.NeighbourInterRef(bx, by); ok && neighSlot >= 0 && neighSlot < len(fb.Refs) && fb.Refs[neighSlot] != nil {
			refSlot = neighSlot
			if rf, ok := slotRefFrame(fhdr, neighSlot); ok {
				refFrame = rf
			}
			ref = fb.Refs[neighSlot]
		}
	}
	for i, idx := range fhdr.Refidx {
		if int(idx) == refSlot {
			refOrder = i
			break
		}
	}
	filterMode = fhdr.SubpelFilterMode
	if filterMode == header.FilterModeSwitchable {
		filterMode = header.FilterMode8TapRegular
	}
	if fhdr.Segmentation.Enabled != 0 && fhdr.Segmentation.SegData.D[segID].GlobalMV != 0 {
		interMode = InterModeGlobalMV
	}
	if refOrder >= 0 && refOrder < len(fhdr.GMV) && fhdr.GMV[refOrder].Type == header.WMTypeTranslation {
		interMode = InterModeGlobalMV
		shift := 13
		if fhdr.HP == 0 {
			shift = 14
		}
		mv.X = int16(fhdr.GMV[refOrder].Matrix[0] >> shift)
		mv.Y = int16(fhdr.GMV[refOrder].Matrix[1] >> shift)
	} else if fhdr.UseRefFrameMVs != 0 && fs != nil {
		if tMV, tRefSlot, ok := fs.TemporalInterMV(bx, by); ok && tRefSlot >= 0 && tRefSlot < len(fb.Refs) && fb.Refs[tRefSlot] != nil && !skipMode {
			mv = tMV
			refSlot = tRefSlot
			if rf, ok := slotRefFrame(fhdr, tRefSlot); ok {
				refFrame = rf
			}
			ref = fb.Refs[tRefSlot]
			for i, idx := range fhdr.Refidx {
				if int(idx) == refSlot {
					refOrder = i
					break
				}
			}
		}
	}
	if mv == (refmvs.MV{}) && fs != nil && !skipMode {
		if cnt, stack := singleRefInterCandidates(fs, fhdr, fb, bx, by); cnt > 0 {
			mv = stack[0].MV[0]
			if cnt > 1 {
				interMode = InterModeNearMV
			} else {
				interMode = InterModeNearestMV
			}
		}
	}
	if mv == (refmvs.MV{}) && fs != nil && !skipMode {
		if blk, ok := fs.NeighbourGridInterBlock(bx, by); ok && blk.Ref[0] > 0 {
			gridRefSlot, okRef := frameRefSlot(fhdr, int(blk.Ref[0]))
			if okRef && gridRefSlot >= 0 && gridRefSlot < len(fb.Refs) && fb.Refs[gridRefSlot] != nil {
				mv = blk.MV[0]
				refSlot = gridRefSlot
				refFrame = int(blk.Ref[0])
				ref = fb.Refs[gridRefSlot]
				for i, idx := range fhdr.Refidx {
					if int(idx) == refSlot {
						refOrder = i
						break
					}
				}
			}
		}
	}
	if mv == (refmvs.MV{}) {
		if neighMV, ok := fs.NeighbourInterMV(bx, by); ok && !skipMode {
			mv = neighMV
			interMode = InterModeNearestMV
		}
	}
	if fhdr.ForceIntegerMV != 0 {
		mv.X = truncateMVToIntPel(mv.X)
		mv.Y = truncateMVToIntPel(mv.Y)
	}
	return
}

func singleRefInterCandidates(fs *FrameState, fhdr *header.FrameHeader, fb *FrameBuf, bx, by int) (int, [4]refmvs.Candidate) {
	var stack [4]refmvs.Candidate
	if fs == nil || fhdr == nil || fs.MVFrame == nil {
		return 0, stack
	}
	cnt := 0
	add := func(blk refmvs.Block, weight int) {
		if blk.Ref[0] <= 0 {
			return
		}
		refSlot, ok := frameRefSlot(fhdr, int(blk.Ref[0]))
		if !ok || refSlot < 0 || refSlot >= len(fb.Refs) || fb.Refs[refSlot] == nil {
			return
		}
		cnt = refmvs.AddCandidate(stack[:], cnt, blk.MV, weight)
	}
	if blk, ok := fs.MVFrame.GridBlock(bx>>2, (by>>2)-1); ok {
		add(blk, 4)
	}
	if blk, ok := fs.MVFrame.GridBlock((bx>>2)-1, by>>2); ok {
		add(blk, 3)
	}
	if blk, ok := fs.MVFrame.GridBlock((bx>>2)-1, (by>>2)-1); ok {
		add(blk, 2)
	}
	refmvs.SortCandidates(stack[:], cnt)
	return cnt, stack
}

func truncateMVToIntPel(v int16) int16 {
	return int16((int(v) / 8) * 8)
}

func interFilter2D(mode header.FilterMode) predinter.Filter2D {
	switch mode {
	case header.FilterMode8TapSmooth:
		return predinter.Filter2D8TapSmooth
	case header.FilterMode8TapSharp:
		return predinter.Filter2D8TapSharp
	case header.FilterModeBilinear:
		return predinter.Filter2DBilinear
	default:
		return predinter.Filter2D8TapRegular
	}
}

func floorDivPow2(v, shift int) int {
	if shift <= 0 {
		return v
	}
	d := 1 << shift
	if v >= 0 {
		return v / d
	}
	return -(((-v) + d - 1) / d)
}

func splitMV8(mv int) (pix, frac16 int) {
	pix = floorDivPow2(mv, 3)
	frac8 := mv - (pix << 3)
	frac16 = frac8 << 1
	return
}

func copyInterPredictPlane(dst []byte, dstStride, dstW, dstH int,
	src []byte, srcStride, srcW, srcH int,
	bx, by, bw, bh int,
	mv refmvs.MV, mode header.FilterMode,
) {
	if len(dst) == 0 || len(src) == 0 || bw <= 0 || bh <= 0 {
		return
	}
	if bx+bw > dstW {
		bw = dstW - bx
	}
	if by+bh > dstH {
		bh = dstH - by
	}
	if bw <= 0 || bh <= 0 {
		return
	}
	mv = refmvs.ClampMV(mv, bx>>2, by>>2, (bw+3)>>2, (bh+3)>>2, (srcW+3)>>2, (srcH+3)>>2)
	px, mx := splitMV8(int(mv.X))
	py, my := splitMV8(int(mv.Y))
	sx := bx + px
	sy := by + py

	padStride := bw + 7
	padH := bh + 7
	pad := make([]byte, padStride*padH)
	for y := 0; y < padH; y++ {
		srcY := clampInt(sy-3+y, 0, srcH-1)
		for x := 0; x < padStride; x++ {
			srcX := clampInt(sx-3+x, 0, srcW-1)
			pad[y*padStride+x] = src[srcY*srcStride+srcX]
		}
	}

	dstOff := by*dstStride + bx
	if dstOff < 0 || dstOff >= len(dst) {
		return
	}
	filt := interFilter2D(mode)
	srcBase := 3*padStride + 3
	if filt == predinter.Filter2DBilinear {
		predinter.PutBilin(dst[dstOff:], dstStride, pad, srcBase, padStride, bw, bh, mx, my)
		return
	}
	predinter.Put8Tap(dst[dstOff:], dstStride, pad, srcBase, padStride, bw, bh, mx, my, filt)
}

func copyPlaneBlock(dst []byte, dstStride, dstW, dstH int, src []byte, srcStride, srcW, srcH int, x, y, w, h int) {
	if len(dst) == 0 || len(src) == 0 || x >= dstW || y >= dstH || x >= srcW || y >= srcH {
		return
	}
	if x+w > dstW {
		w = dstW - x
	}
	if y+h > dstH {
		h = dstH - y
	}
	if x+w > srcW {
		w = srcW - x
	}
	if y+h > srcH {
		h = srcH - y
	}
	if w <= 0 || h <= 0 {
		return
	}
	for row := 0; row < h; row++ {
		dstOff := (y+row)*dstStride + x
		srcOff := (y+row)*srcStride + x
		if dstOff < 0 || srcOff < 0 || dstOff+w > len(dst) || srcOff+w > len(src) {
			continue
		}
		copy(dst[dstOff:dstOff+w], src[srcOff:srcOff+w])
	}
}

// fillPlane128 retained for backward compatibility.
func fillPlane128(plane []byte, stride, pw, ph, bx, by, bw, bh int) {
	fillPlaneConst(plane, stride, pw, ph, bx, by, bw, bh, 128)
}

// largestTx returns the largest square RectTxfmSize that fits within w×h pixels.
func largestTx(w, h int) uint8 {
	sz := w
	if h < sz {
		sz = h
	}
	switch {
	case sz >= 64:
		return transform.TX64x64
	case sz >= 32:
		return transform.TX32x32
	case sz >= 16:
		return transform.TX16x16
	case sz >= 8:
		return transform.TX8x8
	default:
		return transform.TX4x4
	}
}

// clampQIdx clamps a quantiser index to [0, 255].
func clampQIdx(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// clampInt clamps v to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Silence unused-import error for fmt if no error paths use it at compile time.
var _ = fmt.Sprintf
