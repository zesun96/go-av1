// decoder.go implements AV1 tile-level CABAC decoding for M7.
//
// Scope:
//   - Tile group OBU parsing (tile boundary extraction)
//   - Superblock traversal 閳?partition tree decoding
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
// AV1 spec 鎼?.11.1 tile_group_obu():
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

// readUintLE reads an n-byte (1閳?) little-endian unsigned integer.
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
	ctx := NewTileCtxForQIdx(int(fhdr.Quant.YAC))

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
// Superblock 閳?partition tree (Task 4)
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
// bx/by are luma pixel coordinates; bl is block level (BL128閳ヮ毃L8x8).
func decodePartition(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fhdr *header.FrameHeader, seq *header.SequenceHeader,
	fb *FrameBuf, bx, by, bl int) {

	// Clamp to frame.
	if bx >= fb.Width || by >= fb.Height {
		return
	}

	blSz := blkSizeFromLevel(bl) // full block size in luma px

	// Select partition CDF and symbol count based on block level.
	// AV1 spec: 128x128閳? syms, 64/32/16閳?0 syms, 8x8閳? syms.
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

// decodeBlock decodes one coding block of size bw鑴砨h (luma pixels) at (bx,by).
type blockSyntaxState struct {
	segID      uint8
	skip       bool
	isIntra    bool
	hasChroma  bool
	qidx       int
	qidxIsZero bool
	lossless   bool
}

func decodeBlockSyntaxState(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fhdr *header.FrameHeader, seq *header.SequenceHeader, fb *FrameBuf,
	bx, by, bw, bh int,
) blockSyntaxState {
	var st blockSyntaxState

	if fhdr.Segmentation.Enabled != 0 && fhdr.Segmentation.UpdateMap == 0 {
		st.segID = 0
	}
	if fhdr.Segmentation.Enabled != 0 &&
		fhdr.Segmentation.UpdateMap != 0 &&
		fhdr.Segmentation.SegData.PreSkip != 0 {
		st.segID = readSegmentID(m, ctx, fs, fhdr, bx, by)
	}

	skipCtx := fs.SkipCtx(bx, by)
	st.skip = m.BoolAdapt(ctx.SkipCDF[skipCtx][:2]) != 0

	if fhdr.Segmentation.Enabled != 0 &&
		fhdr.Segmentation.UpdateMap != 0 &&
		fhdr.Segmentation.SegData.PreSkip == 0 {
		if st.skip {
			st.segID = fs.SegIDFromNeighbours(bx, by)
		} else {
			st.segID = readSegmentID(m, ctx, fs, fhdr, bx, by)
		}
	}

	if !st.skip {
		readCDEFIndex(m, fs, fhdr, bx, by, bw, bh)
	}
	readDeltaQLF(m, ctx, fhdr, seq, bx, by, bw, bh, st.skip)

	st.isIntra = fhdr.FrameType.IsIntra()
	if !fhdr.FrameType.IsIntra() {
		ictx := intraCtx(fs, bx, by)
		st.isIntra = m.BoolAdapt(ctx.IntraCDF[ictx][:]) == 0
	}
	st.hasChroma = blockHasChroma(seq, fb, bx, by, bw, bh)
	st.qidx = blockQIdx(ctx, fhdr, st.segID)
	st.qidxIsZero = st.qidx == 0
	st.lossless = fhdr.Segmentation.Lossless[st.segID] != 0

	return st
}

func intraCtx(fs *FrameState, bx, by int) int {
	if fs == nil {
		return 0
	}
	haveTop := by > 0
	haveLeft := bx > 0
	topIntra := 0
	leftIntra := 0
	if haveTop {
		if blk, ok := fs.BlockState(bx, by-4); ok && blk.Intra {
			topIntra = 1
		}
	}
	if haveLeft {
		if blk, ok := fs.BlockState(bx-4, by); ok && blk.Intra {
			leftIntra = 1
		}
	}
	if haveLeft {
		if haveTop {
			ctx := leftIntra + topIntra
			if ctx == 2 {
				return 3
			}
			return ctx
		}
		return leftIntra * 2
	}
	if haveTop {
		return topIntra * 2
	}
	return 0
}

type intraSyntaxState struct {
	yMode        int
	yModeNofilt  int
	uvMode       int
	yAngleDelta  int
	uvAngleDelta int
	cflAlphaU    int8
	cflAlphaV    int8
	filterMode   int
	palSzY       int
	palSzUV      int
	pal          [3][8]uint8
	palIdxY      []uint8
	palIdxUV     []uint8
	txY          uint8
	txUV         uint8
	yTxBlocks    []txBlockSpec
	blockState   Av1Block
}

type intraReconState struct {
	dqY            [2]uint16
	dqU            [2]uint16
	dqV            [2]uint16
	reducedTxtpSet bool
}

type interReconState struct {
	dqY            [2]uint16
	dqU            [2]uint16
	dqV            [2]uint16
	reducedTxtpSet bool
}

type interTransformState struct {
	maxYTx    uint8
	uvtx      uint8
	yTxBlocks []txBlockSpec
	block     Av1Block
}

type interTxtpGrid struct {
	bx   int
	by   int
	w4   int
	h4   int
	txtp []uint8
}

func decodeIntraSyntaxState(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fhdr *header.FrameHeader, seq *header.SequenceHeader,
	bx, by, bw, bh int, st blockSyntaxState,
) intraSyntaxState {
	var intraSt intraSyntaxState
	intraSt.filterMode = -1

	if fhdr.FrameType == header.FrameTypeKey {
		topModeCtx := fs.TopModeCtx(bx, by)
		leftModeCtx := fs.LeftModeCtx(bx, by)
		intraSt.yMode = int(m.SymbolAdapt(ctx.KFYModeCDF[topModeCtx][leftModeCtx][:], NIntraPredModes))
	} else {
		bs := bsizeFromDim(bw, bh)
		sizeCtx := 0
		if bs >= 0 && bs < len(YModeSizeContext) {
			sizeCtx = int(YModeSizeContext[bs])
		}
		intraSt.yMode = int(m.SymbolAdapt(ctx.YModeCDF[sizeCtx][:], NIntraPredModes))
	}
	if intraSt.yMode < 0 {
		intraSt.yMode = 0
	} else if intraSt.yMode >= NIntraPredModes {
		intraSt.yMode = NIntraPredModes - 1
	}

	if intraSt.yMode >= VertPred && intraSt.yMode <= VertLeftPred && angleDeltaAllowed(bw, bh) {
		v := int(m.SymbolAdapt(ctx.AngleDeltaCDF[intraSt.yMode-VertPred][:], 7))
		intraSt.yAngleDelta = v - 3
	}

	cflAllowed := 0
	if st.hasChroma && cflAllowedForBlock(seq, bw, bh, st.lossless) {
		cflAllowed = 1
	}
	intraSt.uvMode = DCPred
	if st.hasChroma {
		uvModeSyms := NIntraPredModes
		if cflAllowed != 0 {
			uvModeSyms = NUVIntraModes
		}
		intraSt.uvMode = int(m.SymbolAdapt(ctx.UVModeCDF[cflAllowed][intraSt.yMode][:], uvModeSyms))
	}

	if st.hasChroma && intraSt.uvMode >= VertPred && intraSt.uvMode <= VertLeftPred && angleDeltaAllowed(bw, bh) {
		v := int(m.SymbolAdapt(ctx.AngleDeltaCDF[intraSt.uvMode-VertPred][:], 7))
		intraSt.uvAngleDelta = v - 3
	}
	if st.hasChroma && intraSt.uvMode == CFLPred {
		intraSt.cflAlphaU, intraSt.cflAlphaV = decodeCFLAlphas(m, ctx)
	}

	if fhdr.AllowScreenContentTools != 0 && bw <= 64 && bh <= 64 && (bw+bh) >= 16 {
		szCtx := palSzCtx(bw, bh)
		if intraSt.yMode == DCPred {
			palCtx := fs.PaletteYCtx(bx, by)
			if m.BoolAdapt(ctx.PaletteYCDF[szCtx][palCtx][:]) != 0 {
				intraSt.palSzY = int(m.SymbolAdapt(ctx.PaletteSizeCDF[0][szCtx][:], 7)) + 2
				intraSt.pal[0] = readPalettePlane(m, ctx, fs, seq, 0, szCtx, bx, by, intraSt.palSzY)
			}
		}
		if st.hasChroma && intraSt.uvMode == DCPred {
			palCtx := fs.PaletteUVCtx(bx, by)
			if intraSt.palSzY > 0 || palCtx != 0 {
				palCtx = 1
			}
			if m.BoolAdapt(ctx.PaletteUVCDF[palCtx][:]) != 0 {
				intraSt.palSzUV = int(m.SymbolAdapt(ctx.PaletteSizeCDF[1][szCtx][:], 7)) + 2
				intraSt.pal[1], intraSt.pal[2] = readPaletteUV(m, ctx, fs, seq, szCtx, bx, by, intraSt.palSzUV)
			}
		}
	}
	fs.SetPaletteCtx(bx, by, bw, bh, intraSt.palSzY, intraSt.palSzUV)

	if seq.FilterIntra && intraSt.yMode == DCPred && intraSt.palSzY == 0 && bw <= 32 && bh <= 32 {
		bs := bsizeFromDim(bw, bh)
		if bs >= 0 {
			useFI := m.BoolAdapt(ctx.UseFilterIntraCDF[bs][:])
			if useFI != 0 {
				intraSt.filterMode = int(m.SymbolAdapt(ctx.FilterIntraModeCDF[:], 5))
			}
		}
	}
	intraSt.yModeNofilt = intraSt.yMode
	if intraSt.filterMode >= 0 && intraSt.filterMode < len(FilterModeToYMode) {
		intraSt.yModeNofilt = int(FilterModeToYMode[intraSt.filterMode])
	}
	modeCtxY := intraSt.yMode
	if intraSt.filterMode >= 0 {
		modeCtxY = DCPred
	}

	if intraSt.palSzY > 0 {
		intraSt.palIdxY = readPalIndices(m, &ctx.ColorMapCDF[0][intraSt.palSzY-2], intraSt.palSzY, bw, bh, bw, bh)
	}
	if st.hasChroma && intraSt.palSzUV > 0 {
		_, _, cbw, cbh := chromaRect(seq, bx, by, bw, bh)
		intraSt.palIdxUV = readPalIndices(m, &ctx.ColorMapCDF[1][intraSt.palSzUV-2], intraSt.palSzUV, cbw, cbh, cbw, cbh)
	}

	intraSt.txY = maxTxForBlockSize(seq, bw, bh, 0)
	intraSt.txUV = maxTxForBlockSize(seq, bw, bh, 1)

	intraSt.blockState.Tx = intraSt.txY
	intraSt.blockState.MaxYTx = intraSt.txY
	intraSt.blockState.Intra = true
	intraSt.blockState.SegID = st.segID
	intraSt.blockState.Skip = st.skip
	intraSt.blockState.YMode = uint8(modeCtxY)
	intraSt.blockState.UvMode = uint8(intraSt.uvMode)
	intraSt.blockState.YAngle = int8(intraSt.yAngleDelta)
	intraSt.blockState.UvAngle = int8(intraSt.uvAngleDelta)
	intraSt.blockState.PalSz = [2]uint8{uint8(maxInt(intraSt.palSzY, 0)), uint8(maxInt(intraSt.palSzUV, 0))}
	intraSt.blockState.CflAlpha = [2]int8{intraSt.cflAlphaU, intraSt.cflAlphaV}

	switch {
	case st.skip:
		fs.SetTxCtx(bx, by, bw, bh, intraSt.txY, fhdr.TxfmMode == header.TxfmModeSwitchable, true)
	case st.lossless:
		intraSt.txY = transform.TX4x4
		intraSt.txUV = transform.TX4x4
		intraSt.blockState.Tx = intraSt.txY
		intraSt.blockState.MaxYTx = intraSt.txY
		fs.SetTxCtx(bx, by, bw, bh, intraSt.txY, fhdr.TxfmMode == header.TxfmModeSwitchable, false)
	case fhdr.TxfmMode == header.TxfmModeSwitchable:
		intraSt.blockState.MaxYTx = intraSt.txY
		intraSt.txY = readIntraTxSize(m, ctx, fs, bx, by, intraSt.txY)
		intraSt.blockState.Tx = intraSt.txY
		fs.SetTxCtx(bx, by, bw, bh, intraSt.txY, true, false)
	default:
		fs.SetTxCtx(bx, by, bw, bh, intraSt.txY, false, false)
	}

	return intraSt
}

func readIntraTxSize(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, bx, by int, maxTx uint8) uint8 {
	td := transform.TxfmDimensions[maxTx]
	if td.Max == 0 {
		return maxTx
	}
	txCtx := fs.TxCtx(bx, by, maxTx)
	maxIdx := int(td.Max) - 1
	if maxIdx < 0 || maxIdx >= len(ctx.TxSzCDF) {
		return maxTx
	}
	nSyms := minInt(int(td.Max), 2) + 1
	depth := int(m.SymbolAdapt(ctx.TxSzCDF[maxIdx][txCtx][:], nSyms))
	tx := maxTx
	for depth > 0 {
		sub := transform.TxfmDimensions[tx].Sub
		if sub == tx {
			break
		}
		tx = sub
		depth--
	}
	return tx
}

func buildIntraReconState(fhdr *header.FrameHeader, qidx int) intraReconState {
	return intraReconState{
		dqY: [2]uint16{
			transform.DqTbl[0][clampQIdx(qidx+int(fhdr.Quant.YDCDelta))][0],
			transform.DqTbl[0][clampQIdx(qidx)][1],
		},
		dqU: [2]uint16{
			transform.DqTbl[0][clampQIdx(qidx+int(fhdr.Quant.UDCDelta))][0],
			transform.DqTbl[0][clampQIdx(qidx+int(fhdr.Quant.UACDelta))][1],
		},
		dqV: [2]uint16{
			transform.DqTbl[0][clampQIdx(qidx+int(fhdr.Quant.VDCDelta))][0],
			transform.DqTbl[0][clampQIdx(qidx+int(fhdr.Quant.VACDelta))][1],
		},
		reducedTxtpSet: fhdr.ReducedTxtpSet != 0,
	}
}

func buildInterReconState(fhdr *header.FrameHeader, qidx int) interReconState {
	return interReconState{
		dqY: [2]uint16{
			transform.DqTbl[0][clampQIdx(qidx+int(fhdr.Quant.YDCDelta))][0],
			transform.DqTbl[0][clampQIdx(qidx)][1],
		},
		dqU: [2]uint16{
			transform.DqTbl[0][clampQIdx(qidx+int(fhdr.Quant.UDCDelta))][0],
			transform.DqTbl[0][clampQIdx(qidx+int(fhdr.Quant.UACDelta))][1],
		},
		dqV: [2]uint16{
			transform.DqTbl[0][clampQIdx(qidx+int(fhdr.Quant.VDCDelta))][0],
			transform.DqTbl[0][clampQIdx(qidx+int(fhdr.Quant.VACDelta))][1],
		},
		reducedTxtpSet: fhdr.ReducedTxtpSet != 0,
	}
}

func newInterTxtpGrid(bx, by, bw, bh int, defaultTxtp uint8) *interTxtpGrid {
	w4 := (bw + 3) >> 2
	h4 := (bh + 3) >> 2
	txtp := make([]uint8, w4*h4)
	for i := range txtp {
		txtp[i] = defaultTxtp
	}
	return &interTxtpGrid{
		bx:   bx,
		by:   by,
		w4:   w4,
		h4:   h4,
		txtp: txtp,
	}
}

func (g *interTxtpGrid) fillBlock(bx, by, bw, bh int, txtp uint8) {
	if g == nil {
		return
	}
	x0 := (bx - g.bx) >> 2
	y0 := (by - g.by) >> 2
	w4 := (bw + 3) >> 2
	h4 := (bh + 3) >> 2
	for y := 0; y < h4; y++ {
		gy := y0 + y
		if gy < 0 || gy >= g.h4 {
			continue
		}
		row := gy * g.w4
		for x := 0; x < w4; x++ {
			gx := x0 + x
			if gx < 0 || gx >= g.w4 {
				continue
			}
			g.txtp[row+gx] = txtp
		}
	}
}

func (g *interTxtpGrid) sampleChroma(seq *header.SequenceHeader, cbx, cby int) uint8 {
	if g == nil || len(g.txtp) == 0 {
		return uint8(transform.DCT_DCT)
	}
	ssHor, ssVer := 1, 1
	if seq != nil {
		ssHor = int(seq.SsHor)
		ssVer = int(seq.SsVer)
	}
	lx := cbx << ssHor
	ly := cby << ssVer
	gx := (lx - g.bx) >> 2
	gy := (ly - g.by) >> 2
	if gx < 0 {
		gx = 0
	} else if gx >= g.w4 {
		gx = g.w4 - 1
	}
	if gy < 0 {
		gy = 0
	} else if gy >= g.h4 {
		gy = g.h4 - 1
	}
	return g.txtp[gy*g.w4+gx]
}

func decodeInterTransformState(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fhdr *header.FrameHeader, seq *header.SequenceHeader,
	bx, by, bw, bh int, st blockSyntaxState,
) interTransformState {
	maxYTx := maxTxForBlockSize(seq, bw, bh, 0)
	uvtx := maxTxForBlockSize(seq, bw, bh, 1)
	out := interTransformState{
		maxYTx: maxYTx,
		uvtx:   uvtx,
		block: Av1Block{
			Tx:     maxYTx,
			MaxYTx: maxYTx,
			Uvtx:   uvtx,
		},
	}

	switch {
	case st.skip:
		fs.SetTxCtx(bx, by, bw, bh, maxYTx, fhdr.TxfmMode == header.TxfmModeSwitchable, true)
	case st.lossless || maxYTx == transform.TX4x4:
		out.maxYTx = transform.TX4x4
		out.uvtx = transform.TX4x4
		out.block.Tx = transform.TX4x4
		out.block.MaxYTx = transform.TX4x4
		out.block.Uvtx = transform.TX4x4
		if fhdr.TxfmMode == header.TxfmModeSwitchable {
			fs.SetTxCtx(bx, by, bw, bh, transform.TX4x4, true, false)
		}
	case fhdr.TxfmMode == header.TxfmModeSwitchable:
		out.block.Tx, out.yTxBlocks, out.block = readVarTxTree(m, ctx, fs, bx, by, bw, bh, maxYTx, uvtx)
	default:
		fs.SetTxCtx(bx, by, bw, bh, maxYTx, false, false)
	}

	return out
}

func commitIntraBlockState(fs *FrameState, bx, by, bw, bh int, st blockSyntaxState, intraSt intraSyntaxState) {
	if intraSt.palSzY > 0 || intraSt.palSzUV > 0 {
		fs.SetPaletteColors(bx, by, bw, bh, intraSt.pal)
	}
	modeCtxY := intraSt.yMode
	if intraSt.filterMode >= 0 {
		modeCtxY = DCPred
	}
	intraSt.blockState.Bl = uint8(blockLevelFromDim(bw, bh))
	intraSt.blockState.Bs = uint8(maxInt(bsizeFromDim(bw, bh), 0))
	intraSt.blockState.Uvtx = intraSt.txUV
	fs.SetBlockState(bx, by, bw, bh, intraSt.blockState)
	fs.SetBlockSeg(bx, by, bw, bh, st.skip, modeCtxY, st.segID)
}

func decodeIntraBlockPlanes(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fhdr *header.FrameHeader, seq *header.SequenceHeader, fb *FrameBuf,
	bx, by, bw, bh int, st blockSyntaxState, intraSt intraSyntaxState,
) {
	qidxIsZero := st.qidxIsZero
	lossless := st.lossless
	skip := st.skip
	reconSt := buildIntraReconState(fhdr, st.qidx)

	if intraSt.palSzY > 0 {
		if len(intraSt.yTxBlocks) > 0 {
			decodePalettePlaneVarTx(m, ctx, fs, fb, 0, bx, by, bw, bh, intraSt.yTxBlocks, intraSt.pal[0], intraSt.palIdxY, reconSt.dqY, skip, intraSt.yModeNofilt, reconSt.reducedTxtpSet, qidxIsZero, lossless)
		} else {
			decodePalettePlane(m, ctx, fs, fb, 0, bx, by, bw, bh, intraSt.txY, intraSt.pal[0], intraSt.palIdxY, reconSt.dqY, skip, intraSt.yModeNofilt, reconSt.reducedTxtpSet, qidxIsZero, lossless)
		}
	} else if len(intraSt.yTxBlocks) > 0 {
		decodeIntraPlaneVarTx(m, ctx, fs, fb, 0, bx, by, bw, bh, intraSt.yTxBlocks, intraSt.yMode, intraSt.yAngleDelta, intraSt.filterMode, reconSt.dqY, skip, intraSt.yModeNofilt, reconSt.reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)
	} else {
		decodeIntraPlane(m, ctx, fs, fb, 0, bx, by, bw, bh, intraSt.txY, intraSt.yMode, intraSt.yAngleDelta, intraSt.filterMode, 0, reconSt.dqY, skip, intraSt.yModeNofilt, reconSt.reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)
	}

	if st.hasChroma && len(fb.U) > 0 {
		cbx, cby, cbw, cbh := chromaRect(seq, bx, by, bw, bh)

		if intraSt.palSzUV > 0 {
			decodePalettePlane(m, ctx, fs, fb, 1, cbx, cby, cbw, cbh, intraSt.txUV, intraSt.pal[1], intraSt.palIdxUV, reconSt.dqU, skip, intraSt.uvMode, reconSt.reducedTxtpSet, qidxIsZero, lossless)
			decodePalettePlane(m, ctx, fs, fb, 2, cbx, cby, cbw, cbh, intraSt.txUV, intraSt.pal[2], intraSt.palIdxUV, reconSt.dqV, skip, intraSt.uvMode, reconSt.reducedTxtpSet, qidxIsZero, lossless)
		} else if intraSt.uvMode == CFLPred {
			acCfl := buildCflAc(fb, seq, bx, by, bw, bh, cbw, cbh)
			decodeIntraPlaneCFL(m, ctx, fs, fb, 1, cbx, cby, cbw, cbh, intraSt.txUV, int(intraSt.cflAlphaU), reconSt.dqU, skip, intraSt.yMode, reconSt.reducedTxtpSet, fhdr, seq, qidxIsZero, lossless, acCfl)
			decodeIntraPlaneCFL(m, ctx, fs, fb, 2, cbx, cby, cbw, cbh, intraSt.txUV, int(intraSt.cflAlphaV), reconSt.dqV, skip, intraSt.yMode, reconSt.reducedTxtpSet, fhdr, seq, qidxIsZero, lossless, acCfl)
		} else {
			decodeIntraPlane(m, ctx, fs, fb, 1, cbx, cby, cbw, cbh, intraSt.txUV, intraSt.uvMode, intraSt.uvAngleDelta, -1, 0, reconSt.dqU, skip, intraSt.yMode, reconSt.reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)
			decodeIntraPlane(m, ctx, fs, fb, 2, cbx, cby, cbw, cbh, intraSt.txUV, intraSt.uvMode, intraSt.uvAngleDelta, -1, 0, reconSt.dqV, skip, intraSt.yMode, reconSt.reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)
		}
	}
	commitIntraBlockState(fs, bx, by, bw, bh, st, intraSt)
}

func decodeInterPlaneResidual(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fb *FrameBuf,
	plane, bx, by, bw, bh int,
	tx uint8,
	dq [2]uint16,
	skip bool,
	seq *header.SequenceHeader,
	txtpGrid *interTxtpGrid,
	interYTxtp uint8,
	reducedTxtpSet bool,
	qidxIsZero bool,
	lossless bool,
) uint8 {
	if skip || m == nil || ctx == nil {
		return transform.DCT_DCT
	}

	var planeBuf []byte
	var planeW, planeH int
	switch plane {
	case 0:
		planeBuf = fb.Y
		planeW = fb.Width
		planeH = fb.Height
	case 1:
		planeBuf = fb.U
		planeW = fb.ChromaW
		planeH = fb.ChromaH
	default:
		planeBuf = fb.V
		planeW = fb.ChromaW
		planeH = fb.ChromaH
	}
	if bx >= planeW || by >= planeH || len(planeBuf) == 0 {
		return transform.DCT_DCT
	}
	if bx+bw > planeW {
		bw = planeW - bx
	}
	if by+bh > planeH {
		bh = planeH - by
	}
	return decodeInterPlaneResidualVarTxImpl(m, ctx, fs, fb, plane, bx, by, bw, bh, collectUniformTxBlocks(bw, bh, tx), dq, skip, seq, txtpGrid, interYTxtp, reducedTxtpSet, qidxIsZero, lossless)
}

func decodeInterResidual(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fhdr *header.FrameHeader, seq *header.SequenceHeader, fb *FrameBuf,
	st blockSyntaxState, txSt interTransformState, bx, by, bw, bh int,
) {
	if fs == nil {
		return
	}
	if st.skip {
		fs.SetCoefCtxBlock(0, bx, by, bw, bh, 0x40)
		if st.hasChroma && seq != nil && len(fb.U) > 0 {
			cbx, cby, cbw, cbh := chromaRect(seq, bx, by, bw, bh)
			fs.SetCoefCtxBlock(1, cbx, cby, cbw, cbh, 0x40)
			fs.SetCoefCtxBlock(2, cbx, cby, cbw, cbh, 0x40)
		}
		return
	}
	if m == nil || ctx == nil || fhdr == nil {
		return
	}
	reconSt := buildInterReconState(fhdr, st.qidx)
	txtpGrid := newInterTxtpGrid(bx, by, bw, bh, uint8(transform.DCT_DCT))
	lumaTxtp := uint8(transform.DCT_DCT)
	if txSt.block.TxSplit0 != 0 || txSt.block.TxSplit1 != 0 {
		lumaTxtp = decodeInterPlaneResidualTree(m, ctx, fs, fb, 0, bx, by, bw, bh, txSt.block.MaxYTx, txSt.block.TxSplit0, txSt.block.TxSplit1, reconSt.dqY, st.skip, seq, txtpGrid, uint8(transform.DCT_DCT), reconSt.reducedTxtpSet, st.qidxIsZero, st.lossless)
	} else if len(txSt.yTxBlocks) > 0 {
		lumaTxtp = decodeInterPlaneResidualVarTx(m, ctx, fs, fb, 0, bx, by, bw, bh, txSt.yTxBlocks, reconSt.dqY, st.skip, txtpGrid, uint8(transform.DCT_DCT), reconSt.reducedTxtpSet, st.qidxIsZero, st.lossless)
	} else {
		lumaTxtp = decodeInterPlaneResidual(m, ctx, fs, fb, 0, bx, by, bw, bh, txSt.maxYTx, reconSt.dqY, st.skip, seq, txtpGrid, uint8(transform.DCT_DCT), reconSt.reducedTxtpSet, st.qidxIsZero, st.lossless)
	}
	if st.hasChroma && len(fb.U) > 0 {
		cbx, cby, cbw, cbh := chromaRect(seq, bx, by, bw, bh)
		decodeInterPlaneResidual(m, ctx, fs, fb, 1, cbx, cby, cbw, cbh, txSt.uvtx, reconSt.dqU, st.skip, seq, txtpGrid, lumaTxtp, reconSt.reducedTxtpSet, st.qidxIsZero, st.lossless)
		decodeInterPlaneResidual(m, ctx, fs, fb, 2, cbx, cby, cbw, cbh, txSt.uvtx, reconSt.dqV, st.skip, seq, txtpGrid, lumaTxtp, reconSt.reducedTxtpSet, st.qidxIsZero, st.lossless)
	}
}

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

	// --- Segment id (dav1d decode_b 鎼?.11.9, intra-only path) ---
	// When segmentation is disabled the spec mandates seg_id = 0 and no bits
	// are read. When enabled but segmentation.update_map=0 the previous-frame
	// segment map is used; for intra-only key-frames there is no previous
	// map, so the predictor is the spatial neighbour minimum.
	st := decodeBlockSyntaxState(m, ctx, fs, fhdr, seq, fb, bx, by, bw, bh)

	if !st.isIntra {
		decodeInterBlock(m, ctx, fs, fhdr, seq, fb, st, bx, by, bw, bh)
		return
	}

	intraSt := decodeIntraSyntaxState(m, ctx, fs, fhdr, seq, bx, by, bw, bh, st)
	decodeIntraBlockPlanes(m, ctx, fs, fhdr, seq, fb, bx, by, bw, bh, st, intraSt)
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
	sign := int(m.SymbolAdapt(ctx.CFLSignCDF[:], 8)) + 1
	signU := sign * 0x56 >> 8
	signV := sign - signU*3

	var alphaU, alphaV int
	if signU != 0 {
		c := 0
		if signU == 2 {
			c = 3
		}
		c += signV
		alphaU = int(m.SymbolAdapt(ctx.CFLAlphaCDF[c][:], 16)) + 1
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
		alphaV = int(m.SymbolAdapt(ctx.CFLAlphaCDF[c][:], 16)) + 1
		if signV == 1 {
			alphaV = -alphaV
		}
	}
	return int8(alphaU), int8(alphaV)
}

// buildCflAc constructs a zero-mean luma AC buffer for CFL prediction by
// 4:2:0-subsampling the reconstructed luma block at (bx,by,bw,bh) into a
// cbw鑴砪bh array, then subtracting the mean. The result is in row-major
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
	stepX := 4
	stepY := 4
	if seq != nil {
		stepX <<= seq.SsHor
		stepY <<= seq.SsVer
	}
	smoothFlags := fs.IntraSmoothFlags(bx, by, stepX, stepY, plane)

	for tby := 0; tby < bh; tby += th {
		for tbx := 0; tbx < bw; tbx += tw {
			dstOff := (by+tby)*stride + (bx + tbx)
			if dstOff >= len(planeBuf) {
				continue
			}
			dst := planeBuf[dstOff:]

			prepareIntraPrediction(
				planeBuf, stride, planeW, planeH,
				bx+tbx, by+tby, tw, th,
				tlBuf, tl,
				DCPred, 0, -1,
				false, smoothFlags,
			)
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
				coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, tx, plane, bx+tbx, by+tby, bw, bh, coeffMode, true, transform.DCT_DCT, reducedTxtpSet, qidxIsZero, lossless, dq)
				fs.SetCoefCtx(plane, bx+tbx, by+tby, tx, resCtx)
				if eob >= 0 && len(coeff) > 0 {
					tdFull := transform.TxfmDimensions[tx]
					twFull := int(tdFull.W) * 4
					thFull := int(tdFull.H) * 4
					maxOff := (thFull-1)*stride + (twFull - 1)
					if dstOff+maxOff < len(planeBuf) {
						ReconBlockDequantized(dst, stride, coeff, eob, tx, txtp, 8)
					}
				}
			} else {
				fs.SetCoefCtx(plane, bx+tbx, by+tby, tx, 0x40)
			}
		}
	}
	_ = seq
	_ = fhdr
}

// cflAcSubBlock extracts a tw鑴硉h tile (at offset tbx,tby) from a cbw鑴砪bh
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
				coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, tx, plane, bx+tbx, by+tby, bw, bh, yMode, true, transform.DCT_DCT, reducedTxtpSet, qidxIsZero, lossless, dq)
				fs.SetCoefCtx(plane, bx+tbx, by+tby, tx, resCtx)
				if eob >= 0 && len(coeff) > 0 {
					tdFull := transform.TxfmDimensions[tx]
					twFull := int(tdFull.W) * 4
					thFull := int(tdFull.H) * 4
					maxOff := (thFull-1)*stride + (twFull - 1)
					if dstOff+maxOff < len(planeBuf) {
						ReconBlockDequantized(dst, stride, coeff, eob, tx, txtp, 8)
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
			coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, blk.tx, plane, bx+blk.x, by+blk.y, bw, bh, yMode, true, transform.DCT_DCT, reducedTxtpSet, qidxIsZero, lossless, dq)
			fs.SetCoefCtx(plane, bx+blk.x, by+blk.y, blk.tx, resCtx)
			if eob >= 0 && len(coeff) > 0 {
				tdFull := transform.TxfmDimensions[blk.tx]
				twFull := int(tdFull.W) * 4
				thFull := int(tdFull.H) * 4
				maxOff := (thFull-1)*stride + (twFull - 1)
				if dstOff+maxOff < len(planeBuf) {
					ReconBlockDequantized(dst, stride, coeff, eob, blk.tx, txtp, 8)
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

func collectUniformTxBlocks(bw, bh int, tx uint8) []txBlockSpec {
	td := transform.TxfmDimensions[tx]
	tw := int(td.W) * 4
	th := int(td.H) * 4
	specs := make([]txBlockSpec, 0, 16)
	for y := 0; y < bh; y += th {
		for x := 0; x < bw; x += tw {
			specs = append(specs, txBlockSpec{tx: tx, x: x, y: y, w: tw, h: th})
		}
	}
	return specs
}

func collectTxBlocksFromSplits(bw, bh int, maxTx uint8, split0 uint8, split1 uint16) []txBlockSpec {
	specs := make([]txBlockSpec, 0, 16)
	collectTxBlocksFromSplitNode(bw, bh, maxTx, 0, 0, 0, split0, split1, &specs)
	return specs
}

func collectTxBlocksFromSplitNode(bw, bh int, tx uint8, depth, xOff, yOff int, split0 uint8, split1 uint16, specs *[]txBlockSpec) {
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
		mask := uint16(split0)
		if depth == 1 {
			mask = split1
		}
		isSplit = (mask & (1 << (yOff*4 + xOff))) != 0
	}
	if isSplit && td.Max > 1 {
		sub := td.Sub
		subDim := transform.TxfmDimensions[sub]
		subW := int(subDim.W) * 4
		subH := int(subDim.H) * 4

		collectTxBlocksFromSplitNode(bw, bh, sub, depth+1, xOff*2, yOff*2, split0, split1, specs)
		if txw >= txh && px+subW < bw {
			collectTxBlocksFromSplitNode(bw, bh, sub, depth+1, xOff*2+1, yOff*2, split0, split1, specs)
		}
		if txh >= txw && py+subH < bh {
			collectTxBlocksFromSplitNode(bw, bh, sub, depth+1, xOff*2, yOff*2+1, split0, split1, specs)
			if txw >= txh && px+subW < bw {
				collectTxBlocksFromSplitNode(bw, bh, sub, depth+1, xOff*2+1, yOff*2+1, split0, split1, specs)
			}
		}
		return
	}

	*specs = append(*specs, txBlockSpec{tx: tx, x: px, y: py, w: txw, h: txh})
}

func readVarTxTree(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, bx, by, bw, bh int, maxTx, uvtx uint8) (uint8, []txBlockSpec, Av1Block) {
	block := Av1Block{
		Tx:     maxTx,
		MaxYTx: maxTx,
		Uvtx:   uvtx,
	}
	specs := make([]txBlockSpec, 0, 16)
	minTx := maxTx
	td := transform.TxfmDimensions[maxTx]
	rootW := int(td.W) * 4
	rootH := int(td.H) * 4
	for py, yOff := 0, 0; py < bh; py, yOff = py+rootH, yOff+1 {
		for px, xOff := 0, 0; px < bw; px, xOff = px+rootW, xOff+1 {
			readTxTree(m, ctx, fs, bx, by, bw, bh, maxTx, 0, xOff, yOff, &block, &specs, &minTx)
		}
	}
	block.Tx = minTx
	return minTx, specs, block
}

func decodeInterPlaneResidualVarTx(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fb *FrameBuf, plane, bx, by, bw, bh int,
	blocks []txBlockSpec, dq [2]uint16,
	skip bool, txtpGrid *interTxtpGrid, interYTxtp uint8,
	reducedTxtpSet bool,
	qidxIsZero bool,
	lossless bool,
) uint8 {
	return decodeInterPlaneResidualVarTxImpl(m, ctx, fs, fb, plane, bx, by, bw, bh, blocks, dq, skip, nil, txtpGrid, interYTxtp, reducedTxtpSet, qidxIsZero, lossless)
}

func decodeInterPlaneResidualVarTxImpl(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fb *FrameBuf, plane, bx, by, bw, bh int,
	blocks []txBlockSpec, dq [2]uint16,
	skip bool, seq *header.SequenceHeader, txtpGrid *interTxtpGrid, interYTxtp uint8,
	reducedTxtpSet bool,
	qidxIsZero bool,
	lossless bool,
) uint8 {
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

	txtpOut := interYTxtp
	txtpSet := false
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
		if skip {
			fs.SetCoefCtx(plane, bx+blk.x, by+blk.y, blk.tx, 0x40)
			continue
		}
		blockInterYTxtp := interYTxtp
		if plane > 0 && txtpGrid != nil {
			blockInterYTxtp = txtpGrid.sampleChroma(seq, bx+blk.x, by+blk.y)
		}
		dstOff := (by+blk.y)*stride + (bx + blk.x)
		if dstOff >= len(planeBuf) {
			continue
		}
		dst := planeBuf[dstOff:]
		coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, blk.tx, plane, bx+blk.x, by+blk.y, bw, bh, DCPred, false, blockInterYTxtp, reducedTxtpSet, qidxIsZero, lossless, dq)
		if !txtpSet {
			txtpOut = txtp
			txtpSet = true
		}
		if plane == 0 && txtpGrid != nil {
			txtpGrid.fillBlock(bx+blk.x, by+blk.y, tw, th, txtp)
		}
		fs.SetCoefCtx(plane, bx+blk.x, by+blk.y, blk.tx, resCtx)
		if eob < 0 || len(coeff) == 0 {
			continue
		}
		tdFull := transform.TxfmDimensions[blk.tx]
		twFull := int(tdFull.W) * 4
		thFull := int(tdFull.H) * 4
		maxOff := (thFull-1)*stride + (twFull - 1)
		if dstOff+maxOff < len(planeBuf) {
			ReconBlockDequantized(dst, stride, coeff, eob, blk.tx, txtp, 8)
		}
	}
	return txtpOut
}

func decodeInterPlaneResidualTree(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fb *FrameBuf, plane, bx, by, bw, bh int,
	maxTx uint8, split0 uint8, split1 uint16,
	dq [2]uint16, skip bool, seq *header.SequenceHeader, txtpGrid *interTxtpGrid, interYTxtp uint8,
	reducedTxtpSet bool, qidxIsZero bool, lossless bool,
) uint8 {
	blocks := collectTxBlocksFromSplits(bw, bh, maxTx, split0, split1)
	if len(blocks) == 0 {
		return interYTxtp
	}
	return decodeInterPlaneResidualVarTxImpl(m, ctx, fs, fb, plane, bx, by, bw, bh, blocks, dq, skip, seq, txtpGrid, interYTxtp, reducedTxtpSet, qidxIsZero, lossless)
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
	stepX := 4
	stepY := 4
	if plane > 0 && seq != nil {
		stepX <<= seq.SsHor
		stepY <<= seq.SsVer
	}
	smoothFlags := fs.IntraSmoothFlags(bx, by, stepX, stepY, plane)
	enableEdgeFilter := seq != nil && seq.IntraEdgeFilter
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

		dispatchMode, packedAngle := prepareIntraPrediction(
			planeBuf, stride, planeW, planeH,
			bx+blk.x, by+blk.y, tw, th,
			tlBuf, tl,
			mode, angleDelta, filterMode,
			enableEdgeFilter, smoothFlags,
		)
		callPreparedIntraPred(dispatchMode, packedAngle, filterMode, predBuf, tw, tlBuf, tl, tw, th)
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
			coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, blk.tx, plane, bx+blk.x, by+blk.y, bw, bh, coeffMode, true, transform.DCT_DCT, reducedTxtpSet, qidxIsZero, lossless, dq)
			fs.SetCoefCtx(plane, bx+blk.x, by+blk.y, blk.tx, resCtx)
			if eob >= 0 && len(coeff) > 0 {
				tdFull := transform.TxfmDimensions[blk.tx]
				twFull := int(tdFull.W) * 4
				thFull := int(tdFull.H) * 4
				maxOff := (thFull-1)*stride + (twFull - 1)
				if dstOff+maxOff < len(planeBuf) {
					ReconBlockDequantized(dst, stride, coeff, eob, blk.tx, txtp, 8)
				}
			}
		} else {
			fs.SetCoefCtx(plane, bx+blk.x, by+blk.y, blk.tx, 0x40)
		}
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
	stepX := 4
	stepY := 4
	if plane > 0 && seq != nil {
		stepX <<= seq.SsHor
		stepY <<= seq.SsVer
	}
	smoothFlags := fs.IntraSmoothFlags(bx, by, stepX, stepY, plane)
	enableEdgeFilter := seq != nil && seq.IntraEdgeFilter

	for tby := 0; tby < bh; tby += th {
		for tbx := 0; tbx < bw; tbx += tw {
			dstOff := (by+tby)*stride + (bx + tbx)
			if dstOff >= len(planeBuf) {
				continue
			}
			dst := planeBuf[dstOff:]

			// 1. Intra prediction into predBuf.
			dispatchMode, packedAngle := prepareIntraPrediction(
				planeBuf, stride, planeW, planeH,
				bx+tbx, by+tby, tw, th,
				tlBuf, tl,
				mode, angleDelta, filterMode,
				enableEdgeFilter, smoothFlags,
			)
			callPreparedIntraPred(dispatchMode, packedAngle, filterMode, predBuf, tw, tlBuf, tl, tw, th)

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
				coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, tx, plane, bx+tbx, by+tby, bw, bh, coeffMode, true, transform.DCT_DCT, reducedTxtpSet, qidxIsZero, lossless, dq)
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
						ReconBlockDequantized(dst, stride, coeff, eob, tx, txtp, 8)
					}
				}
			} else {
				fs.SetCoefCtx(plane, bx+tbx, by+tby, tx, 0x40)
			}

		}
	}
}

// ---------------------------------------------------------------------------
// Intra prediction dispatch
// ---------------------------------------------------------------------------

// callIntraPred calls the appropriate intra prediction kernel.
func callIntraPred(mode, angleDelta, filterMode int, dst []byte, stride int, topleft []byte, tl, width, height int, haveTop, haveLeft bool) {
	if filterMode >= 0 {
		intra.PredFilter(dst, stride, topleft, tl, width, height, filterMode)
		return
	}
	if mode >= VertPred && mode <= VertLeftPred {
		angle := intraModeAngle(mode) + 3*angleDelta
		switch {
		case angle <= 90:
			if angle < 90 && haveTop {
				intra.PredZ1(dst, stride, topleft, tl, width, height, angle)
				return
			}
			intra.PredV(dst, stride, topleft, tl, width, height)
		case angle < 180:
			intra.PredZ2(dst, stride, topleft, tl, width, height, angle, width, height)
		default:
			if angle > 180 && haveLeft {
				intra.PredZ3(dst, stride, topleft, tl, width, height, angle)
				return
			}
			intra.PredH(dst, stride, topleft, tl, width, height)
		}
		return
	}
	switch mode {
	case DCPred:
		switch {
		case haveTop && haveLeft:
			intra.PredDC(dst, stride, topleft, tl, width, height)
		case haveTop:
			intra.PredDCTop(dst, stride, topleft, tl, width, height)
		case haveLeft:
			intra.PredDCLeft(dst, stride, topleft, tl, width, height)
		default:
			intra.PredDC128(dst, stride, width, height)
		}
	case PaethPred:
		switch {
		case haveTop && haveLeft:
			intra.PredPaeth(dst, stride, topleft, tl, width, height)
		case haveTop:
			intra.PredV(dst, stride, topleft, tl, width, height)
		case haveLeft:
			intra.PredH(dst, stride, topleft, tl, width, height)
		default:
			intra.PredDC128(dst, stride, width, height)
		}
	case SmoothPred:
		// SMOOTH requires right and bottom extensions.
		intra.PredSmooth(dst, stride, topleft, tl, width, height)
	case SmoothVPred:
		intra.PredSmoothV(dst, stride, topleft, tl, width, height)
	case SmoothHPred:
		intra.PredSmoothH(dst, stride, topleft, tl, width, height)
	default:
		switch {
		case haveTop && haveLeft:
			intra.PredDC(dst, stride, topleft, tl, width, height)
		case haveTop:
			intra.PredDCTop(dst, stride, topleft, tl, width, height)
		case haveLeft:
			intra.PredDCLeft(dst, stride, topleft, tl, width, height)
		default:
			intra.PredDC128(dst, stride, width, height)
		}
	}
}

const (
	intraPredZ1 = -(iota + 1)
	intraPredZ2
	intraPredZ3
	intraPredDCTop
	intraPredDCLeft
	intraPredDC128
)

func callPreparedIntraPred(mode, packedAngle, filterMode int, dst []byte, stride int, topleft []byte, tl, width, height int) {
	if filterMode >= 0 {
		intra.PredFilter(dst, stride, topleft, tl, width, height, filterMode)
		return
	}
	switch mode {
	case intraPredZ1:
		intra.PredZ1(dst, stride, topleft, tl, width, height, packedAngle)
	case intraPredZ2:
		intra.PredZ2(dst, stride, topleft, tl, width, height, packedAngle, width, height)
	case intraPredZ3:
		intra.PredZ3(dst, stride, topleft, tl, width, height, packedAngle)
	case intraPredDCTop:
		intra.PredDCTop(dst, stride, topleft, tl, width, height)
	case intraPredDCLeft:
		intra.PredDCLeft(dst, stride, topleft, tl, width, height)
	case intraPredDC128:
		intra.PredDC128(dst, stride, width, height)
	default:
		callIntraPred(mode, 0, -1, dst, stride, topleft, tl, width, height, true, true)
	}
}

func prepareIntraPrediction(
	planeBuf []byte, stride, planeW, planeH, bx, by, bw, bh int,
	tlBuf []byte, tl int,
	mode, angleDelta, filterMode int,
	enableEdgeFilter bool, smoothFlags int,
) (dispatchMode int, packedAngle int) {
	haveTop := by > 0
	haveLeft := bx > 0
	dispatchMode = mode
	if filterMode >= 0 {
		dispatchMode = DCPred
	}

	switch mode {
	case VertPred, HorPred, DiagDownLeftPred, DiagDownRightPred, VertRightPred, HorDownPred, HorUpPred, VertLeftPred:
		packedAngle = intraModeAngle(mode) + 3*angleDelta
		switch {
		case packedAngle <= 90:
			if packedAngle < 90 && haveTop {
				dispatchMode = intraPredZ1
			} else {
				dispatchMode = VertPred
			}
		case packedAngle < 180:
			dispatchMode = intraPredZ2
		default:
			if packedAngle > 180 && haveLeft {
				dispatchMode = intraPredZ3
			} else {
				dispatchMode = HorPred
			}
		}
	case DCPred:
		switch {
		case haveLeft && haveTop:
			dispatchMode = DCPred
		case haveTop:
			dispatchMode = intraPredDCTop
		case haveLeft:
			dispatchMode = intraPredDCLeft
		default:
			dispatchMode = intraPredDC128
		}
	case PaethPred:
		switch {
		case haveLeft && haveTop:
			dispatchMode = PaethPred
		case haveTop:
			dispatchMode = VertPred
		case haveLeft:
			dispatchMode = HorPred
		default:
			dispatchMode = DCPred
		}
	}

	if enableEdgeFilter {
		packedAngle |= 1 << 10
	}
	packedAngle |= smoothFlags
	fillPreparedIntraEdges(planeBuf, stride, planeW, planeH, bx, by, bw, bh, tlBuf, tl, dispatchMode, haveLeft, haveTop)
	return dispatchMode, packedAngle
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
// Coefficient decoding (M8 Task 2 閳?dav1d-aligned)
//
// Mirrors dav1d/src/recon_tmpl.c decode_coefs(). Coefficients are stored in
// the packed rc layouts consumed by the live dequant/itxfm path:
//   - TX_CLASS_2D: rc = (x << shift) | y
//   - TX_CLASS_H:  rc = (x << shift) | y
//   - TX_CLASS_V:  rc = (x << shift) | y
//
// dav1d stores TX_CLASS_H coefficients in a transposed linear layout (`rc=i`)
// because its inverse-transform path consumes that form directly. The Go
// transform path consumes one packed column-major layout for every tx class,
// so decode_coefs() keeps dav1d's token/context traversal but writes H-class
// coefficient payloads into that common storage.
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

func getUVInterTxtp(td transform.TxfmDim, yTxtp uint8) uint8 {
	if td.Max == 3 {
		if yTxtp == transform.IDTX {
			return yTxtp
		}
		return transform.DCT_DCT
	}
	if td.Min == 2 {
		switch yTxtp {
		case transform.H_FLIPADST, transform.V_FLIPADST, transform.H_ADST, transform.V_ADST:
			return transform.DCT_DCT
		}
	}
	return yTxtp
}

func decodeCoeffTransformType(m *bitstream.MSAC, ctx *TileCtx, td transform.TxfmDim, chroma int,
	yMode int, intra bool, interYTxtp uint8, reducedTxtpSet, qidxIsZero, lossless bool,
) uint8 {
	switch {
	case lossless:
		return transform.WHT_WHT
	case int(td.Max)+btoi(intra) >= 4:
		return transform.DCT_DCT
	case chroma == 1:
		if intra {
			if yMode == CFLPred {
				return transform.DCT_DCT
			}
			if yMode >= 0 && yMode < len(TxtpFromUVMode) {
				return TxtpFromUVMode[yMode]
			}
			return transform.DCT_DCT
		}
		return getUVInterTxtp(td, interYTxtp)
	case qidxIsZero:
		return transform.DCT_DCT
	default:
		if !intra {
			switch {
			case reducedTxtpSet || td.Max == 3:
				if m.BoolAdapt(ctx.TxTypeInter3CDF[clampInt(int(td.Min), 0, 3)][:]) != 0 {
					return transform.DCT_DCT
				}
				return transform.IDTX
			case td.Min == 2:
				idx := m.SymbolAdapt(ctx.TxTypeInter2CDF[:], TxTypeInter2Symbols)
				if int(idx) < len(TxTypeInter2Set) {
					return clampTxType(TxTypeInter2Set[idx], td.Lw, td.Lh)
				}
				return transform.DCT_DCT
			default:
				idx := m.SymbolAdapt(ctx.TxTypeInter1CDF[clampInt(int(td.Min), 0, 1)][:], TxTypeInter1Symbols)
				if int(idx) < len(TxTypeInter1Set) {
					return clampTxType(TxTypeInter1Set[idx], td.Lw, td.Lh)
				}
				return transform.DCT_DCT
			}
		}
		yModeCtx := clampInt(yMode, 0, NIntraPredModes-1)
		if reducedTxtpSet || td.Min >= 2 {
			txClassCtx := clampInt(int(td.Min), 0, 2)
			idx := m.SymbolAdapt(ctx.TxTypeIntra2CDF[txClassCtx][yModeCtx][:], TxTypeIntra2Symbols)
			if int(idx) < len(TxTypeIntra2Set) {
				return clampTxType(TxTypeIntra2Set[idx], td.Lw, td.Lh)
			}
			return transform.DCT_DCT
		}
		txClassCtx := clampInt(int(td.Min), 0, 1)
		idx := m.SymbolAdapt(ctx.TxTypeIntra1CDF[txClassCtx][yModeCtx][:], TxTypeIntra1Symbols)
		if int(idx) < len(TxTypeIntra1Set) {
			return clampTxType(TxTypeIntra1Set[idx], td.Lw, td.Lh)
		}
		return transform.DCT_DCT
	}
}

func decodeCoeffEOB(m *bitstream.MSAC, ctx *TileCtx, td transform.TxfmDim, chroma int, is1d uint8, n int) (int, int, int, int) {
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
		eob = int(m.SymbolAdapt(ctx.EobBin16Full[chroma][is1d][:], 5))
	case 1:
		eob = int(m.SymbolAdapt(ctx.EobBin32Full[chroma][is1d][:], 6))
	case 2:
		eob = int(m.SymbolAdapt(ctx.EobBin64Full[chroma][is1d][:], 7))
	case 3:
		eob = int(m.SymbolAdapt(ctx.EobBin128Full[chroma][is1d][:], 8))
	case 4:
		eob = int(m.SymbolAdapt(ctx.EobBin256Full[chroma][is1d][:], 9))
	case 5:
		eob = int(m.SymbolAdapt(ctx.EobBin512Full[chroma][:], 10))
	default:
		eob = int(m.SymbolAdapt(ctx.EobBin1024Full[chroma][:], 11))
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
	return eob, slw, slh, tx2dszctx
}

type coeffTokenState struct {
	coeff  []int32
	levels []uint8
	acHead int
}

type coeffTokenGeom struct {
	cls       TxClass
	blockW    int
	blockH    int
	packedH   int
	stride    int
	shift     uint
	mask      int
	slh       int
	slw       int
	tx2dszctx int
}

const (
	coeffNextMask = 0x3ff
	// dav1d packs coefficient token chains as tok<<11 | next_rc(10-bit).
	// Bit 10 stays clear because packed rc is capped to 10 bits by the
	// clipped 32x32 coefficient window.
	coeffTokShift = 11
)

func packedCoeffIndex(blockW, blockH, packedH, x, y int) int {
	if x < 0 || x >= blockW || y < 0 || y >= blockH {
		return -1
	}
	return x*packedH + y
}

func packedCoeffIndexForClass(cls TxClass, blockW, blockH, packedH, x, y int) int {
	switch cls {
	case TxClassH:
		// H-class token traversal is transposed in dav1d: x walks rows
		// (height), y walks columns (width). Map it back into the common
		// packed column-major layout consumed by Go's inverse transform.
		return packedCoeffIndex(blockW, blockH, packedH, y, x)
	default:
		return packedCoeffIndex(blockW, blockH, packedH, x, y)
	}
}

func residualMagFromTok(m *bitstream.MSAC, tok int) int {
	if tok < 15 {
		return tok
	}
	return (int(readGolomb(m)) + 15) & 0xfffff
}

func decodeCoeffTokens(m *bitstream.MSAC, ctx *TileCtx, td transform.TxfmDim, chroma int,
	geom coeffTokenGeom, eob int, levels []uint8,
) (coeffTokenState, int) {
	cls := geom.cls
	txCtx := int(td.Ctx)
	txCtxBr := txCtx
	if txCtxBr > 3 {
		txCtxBr = 3
	}
	eobCdf := &ctx.EobBaseTokFull[txCtx][chroma]
	loCdf := &ctx.BaseTokFull[txCtx][chroma]
	hiCdf := &ctx.BrTokFull[txCtxBr][chroma]

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

	var scan []uint16
	if cls == TxClass2D {
		scan = GetScan(td.Lw, td.Lh, cls)
	}

	coeffIdx := func(x, y, scanPos int) int {
		if scanPos < 0 {
			return -1
		}
		return packedCoeffIndexForClass(cls, geom.blockW, geom.blockH, geom.packedH, x, y)
	}

	tokState := coeffTokenState{
		coeff:  make([]int32, geom.blockW*geom.blockH),
		levels: levels,
	}
	setCoeffToken := func(coeffIdx, tok, next int) {
		if coeffIdx < 0 || tok == 0 {
			return
		}
		tokState.coeff[coeffIdx] = int32((tok << coeffTokShift) | (next & coeffNextMask))
	}

	dcTok := 0
	if eob > 0 {
		var x, y, levelIdx, rc int
		if cls == TxClass2D {
			if eob >= len(scan) {
				return tokState, 0
			}
			rcRaw := int(scan[eob])
			x = rcRaw >> geom.shift
			y = rcRaw & geom.mask
			levelIdx = rcRaw
			rc = coeffIdx(x, y, eob)
		} else if cls == TxClassH {
			x = eob & geom.mask
			y = eob >> geom.shift
			levelIdx = x*geom.stride + y
			rc = coeffIdx(x, y, eob)
		} else {
			x = eob & geom.mask
			y = eob >> geom.shift
			levelIdx = x*geom.stride + y
			rc = coeffIdx(x, y, eob)
		}

		bctx := 1
		if eob > (2 << uint(geom.tx2dszctx)) {
			bctx++
		}
		if eob > (4 << uint(geom.tx2dszctx)) {
			bctx++
		}
		if bctx > 3 {
			bctx = 3
		}
		eobTok := int(m.SymbolAdapt(eobCdf[bctx][:], 3))
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
		setCoeffToken(rc, tok, 0)
		if levelIdx >= 0 && levelIdx < len(levels) {
			levels[levelIdx] = uint8(levelTok)
		}

		lastRC := rc
		for i := eob - 1; i > 0; i-- {
			var xi, yi, lvlIdx int
			rci := -1
			if cls == TxClass2D {
				if i >= len(scan) {
					continue
				}
				r := int(scan[i])
				xi = r >> geom.shift
				yi = r & geom.mask
				lvlIdx = r
				rci = coeffIdx(xi, yi, i)
			} else if cls == TxClassH {
				xi = i & geom.mask
				yi = i >> geom.shift
				lvlIdx = xi*geom.stride + yi
				rci = coeffIdx(xi, yi, i)
			} else {
				xi = i & geom.mask
				yi = i >> geom.shift
				lvlIdx = xi*geom.stride + yi
				rci = coeffIdx(xi, yi, i)
			}
			var loCtx, hiMag int
			if cls == TxClass2D {
				loCtx, hiMag = getLoCtx2D(levels, lvlIdx, geom.stride, ctxOff, xi, yi)
			} else {
				loCtx, hiMag = getLoCtx1D(levels, lvlIdx, geom.stride, yi)
			}
			ytmp := yi
			if cls == TxClass2D {
				ytmp = yi | xi
			}
			toki := int(m.SymbolAdapt(loCdf[loCtx][:], 4))
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
			if toki != 0 {
				setCoeffToken(rci, toki, lastRC)
				lastRC = rci
			}
		}
		tokState.acHead = lastRC
	}

	if eob == 0 {
		tokBr := int(m.SymbolAdapt(eobCdf[0][:], 3))
		dcTok = tokBr + 1
		if tokBr == 2 {
			dcTok = int(m.HiTok(hiCdf[0][:]))
		}
	} else {
		dcMag := 0
		if cls == TxClass2D {
			dcTok = int(m.SymbolAdapt(loCdf[0][:], 4))
		} else {
			dcCtx, hiMag := getLoCtx1D(levels, 0, geom.stride, 0)
			dcMag = hiMag
			dcTok = int(m.SymbolAdapt(loCdf[dcCtx][:], 4))
		}
		if dcTok == 3 {
			if cls == TxClass2D {
				dcMag = int(levels[0*geom.stride+1]) + int(levels[1*geom.stride+0]) + int(levels[1*geom.stride+1])
			}
			dcMag &= 63
			var hctx int
			if dcMag > 12 {
				hctx = 6
			} else {
				hctx = (dcMag + 1) >> 1
			}
			dcTok = int(m.HiTok(hiCdf[hctx][:]))
		}
	}
	if dcTok != 0 {
		tokState.coeff[0] = int32(dcTok)
	}

	return tokState, dcTok
}

func decodeCoeffSignsAndResiduals(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	tx uint8, plane, bx, by int, chroma int, tokState coeffTokenState, dcTok int, dq [2]uint16,
) uint8 {
	dcSignCtx := fs.DCSignCtx(plane, bx, by, tx)
	dqShift := maxInt(0, int(transform.TxfmDimensions[tx].Ctx)-2)
	// Current decoder path is 8-bit only; mirror dav1d's coefficient-domain
	// clamp for 8bpc residual application.
	cfMax := int32((1 << (8 + 7)) - 1)
	culLevel := 0
	dcSignLevel := uint8(0x40)
	if dcTok != 0 {
		sign := m.BoolAdapt(ctx.DCSignCDF[chroma][dcSignCtx][:])
		mag := residualMagFromTok(m, dcTok)
		culLevel += mag
		dcDq := ((int(dq[0]) * mag) & 0xffffff) >> dqShift
		maxMag := int(cfMax)
		if sign != 0 {
			maxMag++
		}
		if dcDq > maxMag {
			dcDq = maxMag
		}
		if sign != 0 {
			tokState.coeff[0] = int32(-dcDq)
			dcSignLevel = 0x00
		} else {
			tokState.coeff[0] = int32(dcDq)
			dcSignLevel = 0x80
		}
	}
	for idx := tokState.acHead; idx != 0; {
		rcTok := int(tokState.coeff[idx])
		tok := rcTok >> coeffTokShift
		next := rcTok & coeffNextMask
		sign := m.BoolEqui()
		mag := residualMagFromTok(m, tok)
		culLevel += mag
		acDq := ((int(dq[1]) * mag) & 0xffffff) >> dqShift
		maxMag := int(cfMax)
		if sign != 0 {
			maxMag++
		}
		if acDq > maxMag {
			acDq = maxMag
		}
		if sign != 0 {
			tokState.coeff[idx] = int32(-acDq)
		} else {
			tokState.coeff[idx] = int32(acDq)
		}
		idx = next
	}
	if culLevel > 63 {
		culLevel = 63
	}
	return uint8(culLevel) | dcSignLevel
}

// decodeCoefficients reads txtp, EOB, base/hi tokens, dc_sign and golomb
// extra-bits for one transform block. Returns (coefficients, eob, txtp).
//
// `qidxIsZero`: true iff frame_hdr.segmentation.qidx[seg_id] == 0
// `lossless` :  true iff frame_hdr.segmentation.lossless[seg_id]
func decodeCoefficients(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, tx uint8, plane int,
	bx, by, bw, bh int, yMode int, intra bool, interYTxtp uint8, reducedTxtpSet bool,
	qidxIsZero bool, lossless bool, dq [2]uint16,
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
			txtp := uint8(transform.DCT_DCT)
			if lossless {
				txtp = transform.WHT_WHT
			}
			return nil, -1, txtp, 0x40
		}
	}

	// --- Transform type ---------------------------------------------------
	txtp := decodeCoeffTransformType(m, ctx, td, chroma, yMode, intra, interYTxtp, reducedTxtpSet, qidxIsZero, lossless)

	cls := DAV1DTxTypeClass[txtp]
	is1d := uint8(0)
	if cls != TxClass2D {
		is1d = 1
	}

	// --- EOB --------------------------------------------------------------
	eob, slw, slh, tx2dszctx := decodeCoeffEOB(m, ctx, td, chroma, is1d, n)

	// --- Token decode -----------------------------------------------------
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
	packedH := 4 << uint(slh)
	geom := coeffTokenGeom{
		cls:       cls,
		blockW:    blockW,
		blockH:    blockH,
		packedH:   packedH,
		stride:    stride,
		shift:     shift,
		mask:      mask,
		slh:       slh,
		slw:       slw,
		tx2dszctx: tx2dszctx,
	}
	tokState, dcTok := decodeCoeffTokens(m, ctx, td, chroma, geom, eob, levels)

	resCtx := decodeCoeffSignsAndResiduals(m, ctx, fs, tx, plane, bx, by, chroma, tokState, dcTok, dq)

	return tokState.coeff, eob, txtp, resCtx
}

// clampTxType restricts txtp to the 1D transform types supported by the
// given transform dimensions. AV1 spec 鎼?.12.2:
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
	switch {
	case bx > 0 && by > 0:
		off := (by-1)*stride + (bx - 1)
		if off >= 0 && off < len(planeBuf) {
			tlBuf[tl] = planeBuf[off]
		}
	case by > 0:
		off := (by-1)*stride + bx
		if off >= 0 && off < len(planeBuf) {
			tlBuf[tl] = planeBuf[off]
		}
	case bx > 0:
		off := by*stride + (bx - 1)
		if off >= 0 && off < len(planeBuf) {
			tlBuf[tl] = planeBuf[off]
		}
	}

	// Top row (left閳姰ight: tlBuf[tl+1..tl+extent]).
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

	// Left column (top閳妼ottom: tlBuf[tl-1..tl-extent]).
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

func fillPreparedIntraEdges(planeBuf []byte, stride, planeW, planeH, bx, by, bw, bh int,
	tlBuf []byte, tl int, mode int, haveLeft, haveTop bool) {
	for i := range tlBuf {
		tlBuf[i] = 128
	}

	topNeeds := false
	leftNeeds := false
	topLeftNeeds := false
	topRightNeeds := false
	bottomLeftNeeds := false
	switch mode {
	case DCPred, SmoothPred, SmoothVPred, SmoothHPred:
		topNeeds, leftNeeds = true, true
	case intraPredDCTop:
		topNeeds = true
	case intraPredDCLeft:
		leftNeeds = true
	case intraPredDC128:
	case VertPred:
		topNeeds = true
	case HorPred:
		leftNeeds = true
	case PaethPred:
		topNeeds, leftNeeds, topLeftNeeds = true, true, true
	case intraPredZ1:
		topNeeds, topLeftNeeds, topRightNeeds = true, true, true
	case intraPredZ2:
		topNeeds, leftNeeds, topLeftNeeds = true, true, true
	case intraPredZ3:
		leftNeeds, topLeftNeeds, bottomLeftNeeds = true, true, true
	}

	if leftNeeds {
		fillPreparedLeftEdge(planeBuf, stride, planeH, bx, by, bh, tlBuf, tl, haveLeft, haveTop)
		if bottomLeftNeeds {
			fillPreparedBottomLeftEdge(planeBuf, stride, planeH, bx, by, bh, tlBuf, tl, haveLeft)
		}
	}
	if topNeeds {
		fillPreparedTopEdge(planeBuf, stride, planeW, bx, by, bw, tlBuf, tl, haveLeft, haveTop)
		if topRightNeeds {
			fillPreparedTopRightEdge(planeBuf, stride, planeW, bx, by, bw, tlBuf, tl, haveTop)
		}
	}
	if topLeftNeeds {
		fillPreparedTopLeft(planeBuf, stride, bx, by, tlBuf, tl, haveLeft, haveTop)
	}
}

func fillPreparedLeftEdge(planeBuf []byte, stride, planeH, bx, by, bh int, tlBuf []byte, tl int, haveLeft, haveTop bool) {
	if haveLeft {
		limit := bh
		if remain := planeH - by; remain < limit {
			limit = remain
		}
		for i := 0; i < limit; i++ {
			off := (by+i)*stride + (bx - 1)
			if off >= 0 && off < len(planeBuf) {
				tlBuf[tl-1-i] = planeBuf[off]
			}
		}
		fill := byte(128)
		if limit > 0 {
			fill = tlBuf[tl-limit]
		}
		for i := limit; i < bh; i++ {
			tlBuf[tl-1-i] = fill
		}
		return
	}
	fill := byte(129)
	if haveTop {
		off := (by-1)*stride + bx
		if off >= 0 && off < len(planeBuf) {
			fill = planeBuf[off]
		}
	}
	for i := 0; i < bh; i++ {
		tlBuf[tl-1-i] = fill
	}
}

func fillPreparedBottomLeftEdge(planeBuf []byte, stride, planeH, bx, by, bh int, tlBuf []byte, tl int, haveLeft bool) {
	if !haveLeft || by+bh >= planeH {
		fill := tlBuf[tl-bh]
		for i := 0; i < bh; i++ {
			tlBuf[tl-bh-1-i] = fill
		}
		return
	}
	limit := bh
	if remain := planeH - by - bh; remain < limit {
		limit = remain
	}
	for i := 0; i < limit; i++ {
		off := (by+bh+i)*stride + (bx - 1)
		if off >= 0 && off < len(planeBuf) {
			tlBuf[tl-bh-1-i] = planeBuf[off]
		}
	}
	fill := tlBuf[tl-bh-limit]
	for i := limit; i < bh; i++ {
		tlBuf[tl-bh-1-i] = fill
	}
}

func fillPreparedTopEdge(planeBuf []byte, stride, planeW, bx, by, bw int, tlBuf []byte, tl int, haveLeft, haveTop bool) {
	if haveTop {
		limit := bw
		if remain := planeW - bx; remain < limit {
			limit = remain
		}
		for i := 0; i < limit; i++ {
			off := (by-1)*stride + (bx + i)
			if off >= 0 && off < len(planeBuf) {
				tlBuf[tl+1+i] = planeBuf[off]
			}
		}
		fill := byte(128)
		if limit > 0 {
			fill = tlBuf[tl+limit]
		}
		for i := limit; i < bw; i++ {
			tlBuf[tl+1+i] = fill
		}
		return
	}
	fill := byte(127)
	if haveLeft {
		off := by*stride + (bx - 1)
		if off >= 0 && off < len(planeBuf) {
			fill = planeBuf[off]
		}
	}
	for i := 0; i < bw; i++ {
		tlBuf[tl+1+i] = fill
	}
}

func fillPreparedTopRightEdge(planeBuf []byte, stride, planeW, bx, by, bw int, tlBuf []byte, tl int, haveTop bool) {
	if !haveTop || bx+bw >= planeW {
		fill := tlBuf[tl+bw]
		for i := 0; i < bw; i++ {
			tlBuf[tl+bw+1+i] = fill
		}
		return
	}
	limit := bw
	if remain := planeW - bx - bw; remain < limit {
		limit = remain
	}
	for i := 0; i < limit; i++ {
		off := (by-1)*stride + (bx + bw + i)
		if off >= 0 && off < len(planeBuf) {
			tlBuf[tl+bw+1+i] = planeBuf[off]
		}
	}
	fill := tlBuf[tl+bw+limit]
	for i := limit; i < bw; i++ {
		tlBuf[tl+bw+1+i] = fill
	}
}

func fillPreparedTopLeft(planeBuf []byte, stride, bx, by int, tlBuf []byte, tl int, haveLeft, haveTop bool) {
	switch {
	case haveLeft && haveTop:
		off := (by-1)*stride + (bx - 1)
		if off >= 0 && off < len(planeBuf) {
			tlBuf[tl] = planeBuf[off]
			return
		}
	case haveLeft:
		off := by*stride + (bx - 1)
		if off >= 0 && off < len(planeBuf) {
			tlBuf[tl] = planeBuf[off]
			return
		}
	case haveTop:
		off := (by-1)*stride + bx
		if off >= 0 && off < len(planeBuf) {
			tlBuf[tl] = planeBuf[off]
			return
		}
	}
	tlBuf[tl] = 128
}

// updateTopleft refreshes the topleft buffer from the reconstructed tx block,
// so subsequent tx blocks within the same coding block see correct neighbours.
func updateTopleft(planeBuf []byte, stride, planeW, planeH, bx, by, tw, th int,
	tlBuf []byte, tl int) {

	// Update right edge of left column (bottom of the tx block 閳?tl-th).
	lastY := by + th - 1
	if bx > 0 && lastY < planeH {
		off := lastY*stride + (bx - 1)
		if off >= 0 && off < len(planeBuf) {
			tlBuf[tl-th] = planeBuf[off]
		}
	}
	// Update bottom of top row (right edge 閳?tl+tw).
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
	copyInterPredictPlane(fb.Y, fb.StrideY, fb.Width, fb.Height, ref.Y, ref.StrideY, ref.Width, ref.Height, bx, by, bw, bh, mv, header.FilterMode8TapRegular, header.FilterMode8TapRegular)
	if fb.Monochrome || ref.Monochrome {
		return true
	}
	cbx, cby, cbw, cbh := chromaRect(seq, bx, by, bw, bh)
	copyInterPredictPlane(fb.U, fb.StrideUV, fb.ChromaW, fb.ChromaH, ref.U, ref.StrideUV, ref.ChromaW, ref.ChromaH, cbx, cby, cbw, cbh, mv, header.FilterMode8TapRegular, header.FilterMode8TapRegular)
	copyInterPredictPlane(fb.V, fb.StrideUV, fb.ChromaW, fb.ChromaH, ref.V, ref.StrideUV, ref.ChromaW, ref.ChromaH, cbx, cby, cbw, cbh, mv, header.FilterMode8TapRegular, header.FilterMode8TapRegular)
	return true
}

func copySelectedInterRefBlock(fb *FrameBuf, seq *header.SequenceHeader, bx, by, bw, bh int, st interState) bool {
	if st.ref == nil || len(st.ref.Y) == 0 {
		return false
	}
	mv := refmvs.MV{}
	copyInterPredictPlane(fb.Y, fb.StrideY, fb.Width, fb.Height, st.ref.Y, st.ref.StrideY, st.ref.Width, st.ref.Height, bx, by, bw, bh, mv, st.filterMode, st.filterModeV)
	if fb.Monochrome || st.ref.Monochrome {
		return true
	}
	cbx, cby, cbw, cbh := chromaRect(seq, bx, by, bw, bh)
	copyInterPredictPlane(fb.U, fb.StrideUV, fb.ChromaW, fb.ChromaH, st.ref.U, st.ref.StrideUV, st.ref.ChromaW, st.ref.ChromaH, cbx, cby, cbw, cbh, mv, st.filterMode, st.filterModeV)
	copyInterPredictPlane(fb.V, fb.StrideUV, fb.ChromaW, fb.ChromaH, st.ref.V, st.ref.StrideUV, st.ref.ChromaW, st.ref.ChromaH, cbx, cby, cbw, cbh, mv, st.filterMode, st.filterModeV)
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

func decodeInterBlock(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, fhdr *header.FrameHeader, seq *header.SequenceHeader,
	fb *FrameBuf, st blockSyntaxState, bx, by, bw, bh int) {
	syntax := decodeSingleRefInterSyntax(m, ctx, fs, fhdr, st.segID, st.skip, bx, by)
	_ = decodeSingleRefInterBlockWithSyntax(m, ctx, fs, fhdr, seq, fb, st, bx, by, bw, bh, syntax)
}

func predictInterFallback(fb *FrameBuf, fhdr *header.FrameHeader, seq *header.SequenceHeader, segID uint8, bx, by, bw, bh int) bool {
	st := singleRefInterState(nil, fb, fhdr, segID, false, bx, by)
	_ = st.refSlot
	_ = st.refOrder
	return applyInterState(fb, seq, bx, by, bw, bh, st)
}

func deriveInterFallback(fs *FrameState, fb *FrameBuf, fhdr *header.FrameHeader, segID uint8, skip bool, bx, by int) (refSlot, refFrame, refOrder int, mv refmvs.MV, filterMode header.FilterMode, interMode int, skipMode bool, ref *PlaneBuf) {
	return singleRefInterState(fs, fb, fhdr, segID, skip, bx, by).values()
}

type interCandidate struct {
	mv       refmvs.MV
	refSlot  int
	refFrame int
	weight   int
}

type interState struct {
	refSlot     int
	refFrame    int
	refOrder    int
	baseMV      refmvs.MV
	deltaMV     refmvs.MV
	mv          refmvs.MV
	filterMode  header.FilterMode
	filterModeV header.FilterMode
	interMode   int
	skipMode    bool
	candCnt     int
	ref         *PlaneBuf
}

type singleRefInterSyntax struct {
	modeHint     int
	motionSource int
	deltaMV      refmvs.MV
	refSlot      int
	hasRef       bool
	drlIdx       int
}

const (
	interModeHintAuto = iota
	interModeHintNearest
	interModeHintNear
	interModeHintRef
	interModeHintNew
)

const (
	interMotionSourceAuto = iota
	interMotionSourceGlobal
	interMotionSourceTemporal
	interMotionSourceCandidate
)

const (
	mvJointZero = iota
	mvJointH
	mvJointV
	mvJointHV
)

func (s interState) values() (refSlot, refFrame, refOrder int, mv refmvs.MV, filterMode header.FilterMode, interMode int, skipMode bool, ref *PlaneBuf) {
	return s.refSlot, s.refFrame, s.refOrder, s.mv, s.filterMode, s.interMode, s.skipMode, s.ref
}

func singleRefInterCandidates(fs *FrameState, fhdr *header.FrameHeader, fb *FrameBuf, bx, by int) (int, [8]interCandidate) {
	var stack [8]interCandidate
	if fs == nil || fhdr == nil || fs.MVFrame == nil {
		if fs == nil || fhdr == nil {
			return 0, stack
		}
	}
	cnt := 0
	add := func(blk refmvs.Block, weight int) {
		if blk.Ref[0] <= 0 {
			return
		}
		refSlot, ok := frameRefSlot(fhdr, int(blk.Ref[0]))
		if !ok || refSlot < 0 {
			return
		}
		if fb != nil {
			if refSlot >= len(fb.Refs) || fb.Refs[refSlot] == nil {
				return
			}
		}
		for i := 0; i < cnt; i++ {
			if stack[i].refSlot == refSlot && stack[i].mv == blk.MV[0] {
				stack[i].weight += weight
				return
			}
		}
		if cnt >= len(stack) {
			return
		}
		stack[cnt] = interCandidate{
			mv:       blk.MV[0],
			refSlot:  refSlot,
			refFrame: int(blk.Ref[0]),
			weight:   weight,
		}
		cnt++
	}
	addBlockState := func(blk Av1Block, weight int) {
		if blk.Intra || blk.RefSlot < 0 {
			return
		}
		refSlot := int(blk.RefSlot)
		if refSlot < 0 {
			return
		}
		if fb != nil {
			if refSlot >= len(fb.Refs) || fb.Refs[refSlot] == nil {
				return
			}
		}
		refFrame, ok := slotRefFrame(fhdr, refSlot)
		if !ok || refFrame <= 0 {
			return
		}
		mv := refmvs.MV{Y: blk.MV[0], X: blk.MV[1]}
		for i := 0; i < cnt; i++ {
			if stack[i].refSlot == refSlot && stack[i].mv == mv {
				stack[i].weight += weight
				return
			}
		}
		if cnt >= len(stack) {
			return
		}
		stack[cnt] = interCandidate{
			mv:       mv,
			refSlot:  refSlot,
			refFrame: refFrame,
			weight:   weight,
		}
		cnt++
	}
	bx4 := bx >> 2
	by4 := by >> 2
	if fs.MVFrame != nil {
		if blk, ok := fs.MVFrame.GridBlock(bx4, by4-1); ok {
			add(blk, 8)
		}
		if blk, ok := fs.MVFrame.GridBlock(bx4-1, by4); ok {
			add(blk, 7)
		}
		if blk, ok := fs.MVFrame.GridBlock(bx4-1, by4-1); ok {
			add(blk, 6)
		}
		if blk, ok := fs.MVFrame.GridBlock(bx4+1, by4-1); ok {
			add(blk, 5)
		}
		if blk, ok := fs.MVFrame.GridBlock(bx4-1, by4+1); ok {
			add(blk, 4)
		}
		if blk, ok := fs.MVFrame.GridBlock(bx4, by4-2); ok {
			add(blk, 3)
		}
		if blk, ok := fs.MVFrame.GridBlock(bx4-2, by4); ok {
			add(blk, 2)
		}
	}
	if blk, ok := fs.BlockState(bx, by-4); ok {
		addBlockState(blk, 8)
	}
	if blk, ok := fs.BlockState(bx-4, by); ok {
		addBlockState(blk, 7)
	}
	if blk, ok := fs.BlockState(bx-4, by-4); ok {
		addBlockState(blk, 6)
	}
	if blk, ok := fs.BlockState(bx+4, by-4); ok {
		addBlockState(blk, 5)
	}
	for i := 1; i < cnt; i++ {
		key := stack[i]
		j := i - 1
		for j >= 0 && stack[j].weight < key.weight {
			stack[j+1] = stack[j]
			j--
		}
		stack[j+1] = key
	}
	return cnt, stack
}

func singleRefInterState(fs *FrameState, fb *FrameBuf, fhdr *header.FrameHeader, segID uint8, skip bool, bx, by int) interState {
	return singleRefInterStateWithHint(fs, fb, fhdr, segID, skip, bx, by, singleRefInterSyntax{modeHint: interModeHintAuto, motionSource: interMotionSourceAuto, refSlot: -1, drlIdx: -1})
}

func buildInterBlockState(segID uint8, skip bool, st interState) Av1Block {
	return Av1Block{
		Intra:     false,
		SegID:     segID,
		Skip:      skip,
		SkipMode:  st.skipMode,
		InterMode: uint8(st.interMode),
		RefSlot:   int8(st.refSlot),
		Filter:    uint8(st.filterMode),
		FilterV:   uint8(st.filterModeV),
		BaseMV:    [2]int16{st.baseMV.Y, st.baseMV.X},
		DeltaMV:   [2]int16{st.deltaMV.Y, st.deltaMV.X},
		MV:        [2]int16{st.mv.Y, st.mv.X},
	}
}

func buildInterBlockStateForRect(segID uint8, skip bool, bw, bh int, st interState) Av1Block {
	blk := buildInterBlockState(segID, skip, st)
	blk.Bl = uint8(blockLevelFromDim(bw, bh))
	blk.Bs = uint8(maxInt(bsizeFromDim(bw, bh), 0))
	return blk
}

func decodeSingleRefInterSyntax(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, fhdr *header.FrameHeader, segID uint8, skip bool, bx, by int) singleRefInterSyntax {
	syntax := deriveSingleRefInterSyntax(fs, bx, by)
	if fhdr == nil || m == nil || ctx == nil {
		return syntax
	}
	if fhdr.Segmentation.Enabled != 0 && fhdr.Segmentation.SegData.D[segID].GlobalMV != 0 {
		syntax.motionSource = interMotionSourceGlobal
		syntax.modeHint = interModeHintAuto
		return syntax
	}
	if skip {
		return syntax
	}
	if fhdr.Segmentation.Enabled == 0 || fhdr.Segmentation.SegData.D[segID].Ref < 0 {
		if refSlot, ok := decodeSingleRefReferenceSlot(m, ctx, fs, fhdr, bx, by); ok {
			syntax.refSlot = refSlot
			syntax.hasRef = true
		}
	}

	newMVCtx, globalMVCtx, refMVCtx := singleRefModeContexts(fs, fhdr, bx, by)
	if m.BoolAdapt(ctx.NewMVModeCDF[newMVCtx][:]) != 0 {
		if m.BoolAdapt(ctx.GlobalMVModeCDF[globalMVCtx][:]) == 0 {
			syntax.motionSource = interMotionSourceGlobal
			syntax.modeHint = interModeHintAuto
			return syntax
		}
		if m.BoolAdapt(ctx.RefMVModeCDF[refMVCtx][:]) != 0 {
			syntax.motionSource = interMotionSourceCandidate
			syntax.modeHint = interModeHintNear
			syntax.drlIdx = decodeSingleRefDRLIndex(m, ctx, fs, fhdr, bx, by, 1)
			return syntax
		}
		syntax.motionSource = interMotionSourceCandidate
		syntax.modeHint = interModeHintNearest
		syntax.drlIdx = 0
		return syntax
	}

	syntax.motionSource = interMotionSourceCandidate
	syntax.modeHint = interModeHintNew
	syntax.drlIdx = decodeSingleRefDRLIndex(m, ctx, fs, fhdr, bx, by, 0)
	syntax.deltaMV = readMVResidual(m, ctx, fhdr)
	return syntax
}

func deriveSingleRefInterSyntax(fs *FrameState, bx, by int) singleRefInterSyntax {
	syntax := singleRefInterSyntax{modeHint: interModeHintAuto, motionSource: interMotionSourceAuto, refSlot: -1, drlIdx: -1}
	if fs == nil {
		return syntax
	}
	if blk, ok := fs.BlockState(bx, by-4); ok && !blk.Intra {
		if applyNeighbourInterSyntax(&syntax, blk) {
			return syntax
		}
	}
	if blk, ok := fs.BlockState(bx-4, by); ok && !blk.Intra {
		applyNeighbourInterSyntax(&syntax, blk)
	}
	return syntax
}

func applyNeighbourInterSyntax(syntax *singleRefInterSyntax, blk Av1Block) bool {
	if syntax == nil || blk.Intra {
		return false
	}
	if blk.RefSlot >= 0 {
		syntax.refSlot = int(blk.RefSlot)
		syntax.hasRef = true
	}
	switch blk.InterMode {
	case InterModeGlobalMV:
		syntax.motionSource = interMotionSourceGlobal
		return true
	case InterModeNearestMV:
		syntax.modeHint = interModeHintNearest
		syntax.motionSource = interMotionSourceCandidate
		return true
	case InterModeNewMV:
		syntax.modeHint = interModeHintNew
		syntax.motionSource = interMotionSourceCandidate
		syntax.deltaMV = refmvs.MV{Y: blk.DeltaMV[0], X: blk.DeltaMV[1]}
		return true
	case InterModeRefMV:
		syntax.modeHint = interModeHintRef
		syntax.motionSource = interMotionSourceCandidate
		return true
	case InterModeNearMV:
		syntax.modeHint = interModeHintNear
		syntax.motionSource = interMotionSourceCandidate
		return true
	}
	return false
}

func singleRefModeContexts(fs *FrameState, fhdr *header.FrameHeader, bx, by int) (newMVCtx, globalMVCtx, refMVCtx int) {
	cnt, stack := singleRefInterCandidates(fs, fhdr, &FrameBuf{}, bx, by)
	nearestMatch := 0
	for i := 0; i < cnt && i < 2; i++ {
		if stack[i].weight >= 5 {
			nearestMatch++
		}
	}
	refMatchCount := cnt
	if refMatchCount > 2 {
		refMatchCount = 2
	}
	haveNewMV := 0
	switch nearestMatch {
	case 0:
		refMVCtx = clampInt(refMatchCount, 0, 2)
		if refMatchCount > 0 {
			newMVCtx = 1
		}
	case 1:
		refMVCtx = clampInt(refMatchCount*3, 0, 4)
		newMVCtx = 3 - haveNewMV
	default:
		refMVCtx = 5
		newMVCtx = 5 - haveNewMV
	}
	globalMVCtx = 0
	if refMVCtx >= 3 {
		globalMVCtx = 1
	}
	return
}

func neighbourSingleRefFrame(fs *FrameState, fhdr *header.FrameHeader, bx, by int, top bool) (int, bool) {
	if fs == nil || fhdr == nil {
		return 0, false
	}
	var blk Av1Block
	var ok bool
	if top {
		blk, ok = fs.BlockState(bx, by-4)
	} else {
		blk, ok = fs.BlockState(bx-4, by)
	}
	if !ok || blk.Intra || blk.RefSlot < 0 {
		return 0, false
	}
	refFrame, ok := slotRefFrame(fhdr, int(blk.RefSlot))
	if !ok || refFrame <= 0 {
		return 0, false
	}
	return refFrame - 1, true
}

func refCtx(fs *FrameState, fhdr *header.FrameHeader, bx, by int) int {
	cnt := [2]int{}
	if ref, ok := neighbourSingleRefFrame(fs, fhdr, bx, by, true); ok && ref < 2 {
		cnt[ref]++
	}
	if ref, ok := neighbourSingleRefFrame(fs, fhdr, bx, by, false); ok && ref < 2 {
		cnt[ref]++
	}
	if cnt[0] == cnt[1] {
		return 1
	}
	if cnt[0] < cnt[1] {
		return 0
	}
	return 2
}

func ref2Ctx(fs *FrameState, fhdr *header.FrameHeader, bx, by int) int {
	cnt := [3]int{}
	if ref, ok := neighbourSingleRefFrame(fs, fhdr, bx, by, true); ok && ref >= 4 {
		cnt[ref-4]++
	}
	if ref, ok := neighbourSingleRefFrame(fs, fhdr, bx, by, false); ok && ref >= 4 {
		cnt[ref-4]++
	}
	cnt[1] += cnt[0]
	if cnt[2] == cnt[1] {
		return 1
	}
	if cnt[1] < cnt[2] {
		return 0
	}
	return 2
}

func ref3Ctx(fs *FrameState, fhdr *header.FrameHeader, bx, by int) int {
	cnt := [3]int{}
	if ref, ok := neighbourSingleRefFrame(fs, fhdr, bx, by, true); ok && ref >= 0 && ref <= 2 {
		cnt[ref]++
	}
	if ref, ok := neighbourSingleRefFrame(fs, fhdr, bx, by, false); ok && ref >= 0 && ref <= 2 {
		cnt[ref]++
	}
	cnt[1] += cnt[2]
	if cnt[0] == cnt[1] {
		return 1
	}
	if cnt[0] < cnt[1] {
		return 0
	}
	return 2
}

func ref4Ctx(fs *FrameState, fhdr *header.FrameHeader, bx, by int) int {
	cnt := [2]int{}
	if ref, ok := neighbourSingleRefFrame(fs, fhdr, bx, by, true); ok && (ref^0) < 2 {
		cnt[ref]++
	}
	if ref, ok := neighbourSingleRefFrame(fs, fhdr, bx, by, false); ok && (ref^0) < 2 {
		cnt[ref]++
	}
	if cnt[0] == cnt[1] {
		return 1
	}
	if cnt[0] < cnt[1] {
		return 0
	}
	return 2
}

func ref5Ctx(fs *FrameState, fhdr *header.FrameHeader, bx, by int) int {
	cnt := [2]int{}
	if ref, ok := neighbourSingleRefFrame(fs, fhdr, bx, by, true); ok && (ref^2) < 2 {
		cnt[ref-2]++
	}
	if ref, ok := neighbourSingleRefFrame(fs, fhdr, bx, by, false); ok && (ref^2) < 2 {
		cnt[ref-2]++
	}
	if cnt[0] == cnt[1] {
		return 1
	}
	if cnt[0] < cnt[1] {
		return 0
	}
	return 2
}

func ref6Ctx(fs *FrameState, fhdr *header.FrameHeader, bx, by int) int {
	cnt := [3]int{}
	if ref, ok := neighbourSingleRefFrame(fs, fhdr, bx, by, true); ok && ref >= 4 {
		cnt[ref-4]++
	}
	if ref, ok := neighbourSingleRefFrame(fs, fhdr, bx, by, false); ok && ref >= 4 {
		cnt[ref-4]++
	}
	if cnt[0] == cnt[1] {
		return 1
	}
	if cnt[0] < cnt[1] {
		return 0
	}
	return 2
}

func decodeSingleRefReferenceSlot(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, fhdr *header.FrameHeader, bx, by int) (int, bool) {
	refFrame := 0
	if m.BoolAdapt(ctx.RefCDF[0][refCtx(fs, fhdr, bx, by)][:]) != 0 {
		if m.BoolAdapt(ctx.RefCDF[1][ref2Ctx(fs, fhdr, bx, by)][:]) != 0 {
			refFrame = 6
		} else {
			refFrame = 4 + int(m.BoolAdapt(ctx.RefCDF[5][ref6Ctx(fs, fhdr, bx, by)][:]))
		}
	} else {
		if m.BoolAdapt(ctx.RefCDF[2][ref3Ctx(fs, fhdr, bx, by)][:]) != 0 {
			refFrame = 2 + int(m.BoolAdapt(ctx.RefCDF[4][ref5Ctx(fs, fhdr, bx, by)][:]))
		} else {
			refFrame = int(m.BoolAdapt(ctx.RefCDF[3][ref4Ctx(fs, fhdr, bx, by)][:]))
		}
	}
	refSlot, ok := frameRefSlot(fhdr, refFrame+1)
	return refSlot, ok
}

func getInterFilterCtx(fs *FrameState, dir, refSlot, bx, by int) int {
	if fs == nil {
		return 3
	}
	col4 := bx >> 2
	row4 := by >> 2
	aFilter := int(header.NumSwitchableFilters)
	lFilter := int(header.NumSwitchableFilters)
	if by > 0 && col4 < fs.W4 && int(fs.AboveRef[col4]) == refSlot {
		if dir != 0 {
			aFilter = int(fs.AboveFilterV[col4])
		} else {
			aFilter = int(fs.AboveFilter[col4])
		}
	}
	if bx > 0 && row4 < fs.H4 && int(fs.LeftRef[row4]) == refSlot {
		if dir != 0 {
			lFilter = int(fs.LeftFilterV[row4])
		} else {
			lFilter = int(fs.LeftFilter[row4])
		}
	}
	if aFilter == lFilter {
		return clampInt(aFilter, 0, 3)
	}
	if aFilter == int(header.NumSwitchableFilters) {
		return clampInt(lFilter, 0, 3)
	}
	if lFilter == int(header.NumSwitchableFilters) {
		return clampInt(aFilter, 0, 3)
	}
	return int(header.NumSwitchableFilters)
}

func decodeSingleRefFilterMode(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, fhdr *header.FrameHeader, seq *header.SequenceHeader, st interState, bx, by int) (header.FilterMode, header.FilterMode) {
	if fhdr == nil {
		return header.FilterMode8TapRegular, header.FilterMode8TapRegular
	}
	if fhdr.SubpelFilterMode != header.FilterModeSwitchable {
		return fhdr.SubpelFilterMode, fhdr.SubpelFilterMode
	}
	if m == nil || ctx == nil || st.refSlot < 0 {
		return header.FilterMode8TapRegular, header.FilterMode8TapRegular
	}
	ctx1 := getInterFilterCtx(fs, 0, st.refSlot, bx, by)
	f0 := header.FilterMode(m.SymbolAdapt(ctx.FilterCDF[0][ctx1][:], int(header.NumSwitchableFilters)))
	f1 := f0
	if seq != nil && seq.DualFilter {
		ctx2 := getInterFilterCtx(fs, 1, st.refSlot, bx, by)
		f1 = header.FilterMode(m.SymbolAdapt(ctx.FilterCDF[1][ctx2][:], int(header.NumSwitchableFilters)))
	}
	return f0, f1
}

func drlContextFromCandidates(stack [8]interCandidate, cnt, refIdx int) int {
	if refIdx+1 >= cnt {
		return 0
	}
	strong := stack[refIdx].weight >= 5
	nextWeak := stack[refIdx+1].weight < 5
	if strong {
		if nextWeak {
			return 1
		}
		return 0
	}
	if nextWeak {
		return 2
	}
	return 0
}

func decodeSingleRefDRLIndex(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, fhdr *header.FrameHeader, bx, by, base int) int {
	cnt, stack := singleRefInterCandidates(fs, fhdr, &FrameBuf{}, bx, by)
	if cnt <= 1 {
		return 0
	}
	drlIdx := base
	if base == 0 {
		drlCtx := drlContextFromCandidates(stack, cnt, 0)
		drlIdx += int(m.BoolAdapt(ctx.DRLBitCDF[drlCtx][:]))
		if drlIdx == 1 && cnt > 2 {
			drlCtx = drlContextFromCandidates(stack, cnt, 1)
			drlIdx += int(m.BoolAdapt(ctx.DRLBitCDF[drlCtx][:]))
		}
		return clampInt(drlIdx, 0, cnt-1)
	}
	if cnt > 2 {
		drlCtx := drlContextFromCandidates(stack, cnt, 1)
		drlIdx += int(m.BoolAdapt(ctx.DRLBitCDF[drlCtx][:]))
		if drlIdx == 2 && cnt > 3 {
			drlCtx = drlContextFromCandidates(stack, cnt, 2)
			drlIdx += int(m.BoolAdapt(ctx.DRLBitCDF[drlCtx][:]))
		}
	}
	return clampInt(drlIdx, 0, cnt-1)
}

func readMVComponentDiff(m *bitstream.MSAC, ctx *TileCtx, comp, mvPrec int) int16 {
	sign := m.BoolAdapt(ctx.MVSignCDF[comp][:])
	cl := int(m.SymbolAdapt(ctx.MVClassesCDF[comp][:], 11))
	up := 0
	fp := 3
	hp := 1

	if cl == 0 {
		up = int(m.BoolAdapt(ctx.MVClass0CDF[comp][:]))
		if mvPrec >= 0 {
			fp = int(m.SymbolAdapt(ctx.MVClass0FPCDF[comp][up][:], 4))
			if mvPrec > 0 {
				hp = int(m.BoolAdapt(ctx.MVClass0HPCDF[comp][:]))
			}
		}
	} else {
		up = 1 << cl
		for n := 0; n < cl && n < len(ctx.MVClassNCDF[comp]); n++ {
			up |= int(m.BoolAdapt(ctx.MVClassNCDF[comp][n][:])) << n
		}
		if mvPrec >= 0 {
			fp = int(m.SymbolAdapt(ctx.MVClassNFPCDF[comp][:], 4))
			if mvPrec > 0 {
				hp = int(m.BoolAdapt(ctx.MVClassNHPCDF[comp][:]))
			}
		}
	}

	diff := ((up << 3) | (fp << 1) | hp) + 1
	if sign != 0 {
		diff = -diff
	}
	return int16(diff)
}

func readMVResidual(m *bitstream.MSAC, ctx *TileCtx, fhdr *header.FrameHeader) refmvs.MV {
	mv := refmvs.MV{}
	if m == nil || ctx == nil || fhdr == nil {
		return mv
	}
	mvPrec := int(fhdr.HP) - int(fhdr.ForceIntegerMV)
	joint := int(m.SymbolAdapt(ctx.MVJointCDF[:], 4))
	if joint == mvJointV || joint == mvJointHV {
		mv.Y += readMVComponentDiff(m, ctx, 0, mvPrec)
	}
	if joint == mvJointH || joint == mvJointHV {
		mv.X += readMVComponentDiff(m, ctx, 1, mvPrec)
	}
	return mv
}

func singleRefInterStateFromSyntax(fs *FrameState, fb *FrameBuf, fhdr *header.FrameHeader, segID uint8, skip bool, bx, by int, syntax singleRefInterSyntax) interState {
	st := singleRefInterStateWithHint(fs, fb, fhdr, segID, skip, bx, by, syntax)
	st.deltaMV = syntax.deltaMV
	if st.interMode == InterModeNewMV {
		st.mv = composeNewMV(st.baseMV, st.deltaMV)
	}
	return st
}

func decodeSingleRefInterBlockWithSyntax(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, fhdr *header.FrameHeader, seq *header.SequenceHeader, fb *FrameBuf,
	blkSt blockSyntaxState, bx, by, bw, bh int, syntax singleRefInterSyntax) interState {
	st := singleRefInterStateFromSyntax(fs, fb, fhdr, blkSt.segID, blkSt.skip, bx, by, syntax)
	st.filterMode, st.filterModeV = decodeSingleRefFilterMode(m, ctx, fs, fhdr, seq, st, bx, by)
	txSt := decodeInterTransformState(m, ctx, fs, fhdr, seq, bx, by, bw, bh, blkSt)
	blk := buildInterBlockStateForRect(blkSt.segID, blkSt.skip, bw, bh, st)
	blk.Tx = txSt.block.Tx
	blk.MaxYTx = txSt.block.MaxYTx
	blk.Uvtx = txSt.block.Uvtx
	blk.TxSplit0 = txSt.block.TxSplit0
	blk.TxSplit1 = txSt.block.TxSplit1
	if !applyInterState(fb, seq, bx, by, bw, bh, st) {
		if !copySelectedInterRefBlock(fb, seq, bx, by, bw, bh, st) && !copyInterRefBlock(fb, seq, bx, by, bw, bh) {
			fillDC128(fb, seq, bx, by, bw, bh)
		}
	}
	decodeInterResidual(m, ctx, fs, fhdr, seq, fb, blkSt, txSt, bx, by, bw, bh)
	fs.CommitInterBlock(bx, by, bw, bh, blk, st.refFrame)
	return st
}

func decodeSingleRefInterBlock(fs *FrameState, fhdr *header.FrameHeader, seq *header.SequenceHeader, fb *FrameBuf,
	segID uint8, skip bool, bx, by, bw, bh, modeHint int) interState {
	return decodeSingleRefInterBlockWithSyntax(nil, nil, fs, fhdr, seq, fb, blockSyntaxState{segID: segID, skip: skip}, bx, by, bw, bh, singleRefInterSyntax{
		modeHint:     modeHint,
		motionSource: interMotionSourceAuto,
		refSlot:      -1,
		drlIdx:       -1,
	})
}

func singleRefInterStateWithHint(fs *FrameState, fb *FrameBuf, fhdr *header.FrameHeader, segID uint8, skip bool, bx, by int, syntax singleRefInterSyntax) interState {
	st := interState{
		refOrder:    0,
		filterMode:  header.FilterMode8TapRegular,
		filterModeV: header.FilterMode8TapRegular,
		interMode:   InterModeZeroMV,
	}
	st.refSlot, st.ref = primaryInterRef(fb, fhdr)
	if fhdr == nil {
		return st
	}
	updateInterRefState(&st, fhdr, fb)
	resolveSingleRefReference(&st, fs, fb, fhdr, segID, skip, bx, by, syntax)
	finalizeSingleRefState(&st, fhdr, fb)
	resolveSingleRefMotion(&st, fs, fb, fhdr, segID, bx, by, syntax)
	if fhdr.ForceIntegerMV != 0 {
		st.mv.X = truncateMVToIntPel(st.mv.X)
		st.mv.Y = truncateMVToIntPel(st.mv.Y)
	}
	return st
}

func updateInterRefState(st *interState, fhdr *header.FrameHeader, fb *FrameBuf) {
	if st == nil || fhdr == nil {
		return
	}
	if rf, ok := slotRefFrame(fhdr, st.refSlot); ok {
		st.refFrame = rf
	}
	if st.refSlot >= 0 && st.refSlot < len(fb.Refs) {
		st.ref = fb.Refs[st.refSlot]
	}
	st.refOrder = 0
	for i, idx := range fhdr.Refidx {
		if int(idx) == st.refSlot {
			st.refOrder = i
			break
		}
	}
}

func chooseSkipModeRef(fhdr *header.FrameHeader, fb *FrameBuf) (refSlot, refFrame, refOrder int, ref *PlaneBuf, ok bool) {
	if fhdr == nil || fhdr.SkipModeEnabled == 0 {
		return 0, 0, 0, nil, false
	}
	refOrder = int(fhdr.SkipModeRefs[0])
	if refOrder < 0 || refOrder >= len(fhdr.Refidx) {
		return 0, 0, 0, nil, false
	}
	refSlot = int(fhdr.Refidx[refOrder])
	if refSlot < 0 || refSlot >= len(fb.Refs) || fb.Refs[refSlot] == nil {
		return 0, 0, 0, nil, false
	}
	return refSlot, refOrder + 1, refOrder, fb.Refs[refSlot], true
}

func chooseSegmentRef(fhdr *header.FrameHeader, fb *FrameBuf, segID uint8) (refSlot, refFrame, refOrder int, ref *PlaneBuf, ok bool) {
	if fhdr == nil || fhdr.Segmentation.Enabled == 0 {
		return 0, 0, 0, nil, false
	}
	segRef := int(fhdr.Segmentation.SegData.D[segID].Ref)
	refSlot, ok = frameRefSlot(fhdr, segRef)
	if !ok || refSlot < 0 || refSlot >= len(fb.Refs) || fb.Refs[refSlot] == nil {
		return 0, 0, 0, nil, false
	}
	return refSlot, segRef, segRef - 1, fb.Refs[refSlot], true
}

func applySyntaxInterRef(st *interState, fb *FrameBuf, fhdr *header.FrameHeader, syntax singleRefInterSyntax) bool {
	if st == nil || fhdr == nil || !syntax.hasRef || syntax.refSlot < 0 || syntax.refSlot >= len(fb.Refs) || fb.Refs[syntax.refSlot] == nil {
		return false
	}
	st.refSlot = syntax.refSlot
	updateInterRefState(st, fhdr, fb)
	return st.ref != nil
}

func resolveSingleRefReference(st *interState, fs *FrameState, fb *FrameBuf, fhdr *header.FrameHeader, segID uint8, skip bool, bx, by int, syntax singleRefInterSyntax) {
	if st == nil || fhdr == nil {
		return
	}
	if skip {
		if refSlot, refFrame, refOrder, ref, ok := chooseSkipModeRef(fhdr, fb); ok {
			st.refSlot, st.refFrame, st.refOrder, st.ref = refSlot, refFrame, refOrder, ref
			st.skipMode = true
		}
	}
	if refSlot, refFrame, refOrder, ref, ok := chooseSegmentRef(fhdr, fb, segID); ok {
		st.refSlot, st.refFrame, st.refOrder, st.ref = refSlot, refFrame, refOrder, ref
		st.skipMode = false
	}
	if !st.skipMode {
		applySyntaxInterRef(st, fb, fhdr, syntax)
	}
	if !st.skipMode && !syntax.hasRef && fs != nil {
		if neighSlot, ok := fs.NeighbourInterRef(bx, by); ok && neighSlot >= 0 && neighSlot < len(fb.Refs) && fb.Refs[neighSlot] != nil {
			st.refSlot = neighSlot
			updateInterRefState(st, fhdr, fb)
		}
	}
}

func finalizeSingleRefState(st *interState, fhdr *header.FrameHeader, fb *FrameBuf) {
	if st == nil || fhdr == nil {
		return
	}
	updateInterRefState(st, fhdr, fb)
	st.filterMode = fhdr.SubpelFilterMode
	st.filterModeV = fhdr.SubpelFilterMode
	if st.filterMode == header.FilterModeSwitchable {
		st.filterMode = header.FilterMode8TapRegular
		st.filterModeV = header.FilterMode8TapRegular
	}
}

func applyTemporalInterMV(st *interState, fs *FrameState, fb *FrameBuf, fhdr *header.FrameHeader, bx, by int) bool {
	if st == nil || fs == nil || fhdr == nil || fhdr.UseRefFrameMVs == 0 || st.skipMode {
		return false
	}
	tMV, tRefSlot, ok := fs.TemporalInterMV(bx, by)
	if !ok || tRefSlot < 0 || tRefSlot >= len(fb.Refs) || fb.Refs[tRefSlot] == nil {
		return false
	}
	st.mv = tMV
	st.refSlot = tRefSlot
	updateInterRefState(st, fhdr, fb)
	return true
}

func applyNeighbourGridMV(st *interState, fs *FrameState, fb *FrameBuf, fhdr *header.FrameHeader, bx, by int) bool {
	if st == nil || fs == nil || fhdr == nil || st.skipMode {
		return false
	}
	blk, ok := fs.NeighbourGridInterBlock(bx, by)
	if !ok || blk.Ref[0] <= 0 {
		return false
	}
	gridRefSlot, okRef := frameRefSlot(fhdr, int(blk.Ref[0]))
	if !okRef || gridRefSlot < 0 || gridRefSlot >= len(fb.Refs) || fb.Refs[gridRefSlot] == nil {
		return false
	}
	st.mv = blk.MV[0]
	st.refSlot = gridRefSlot
	st.refFrame = int(blk.Ref[0])
	st.ref = fb.Refs[gridRefSlot]
	for i, idx := range fhdr.Refidx {
		if int(idx) == st.refSlot {
			st.refOrder = i
			break
		}
	}
	return true
}

func applyGlobalInterMV(st *interState, fhdr *header.FrameHeader, segID uint8) bool {
	if st == nil || fhdr == nil {
		return false
	}
	if fhdr.Segmentation.Enabled != 0 && fhdr.Segmentation.SegData.D[segID].GlobalMV != 0 {
		st.interMode = InterModeGlobalMV
	}
	if st.refOrder < 0 || st.refOrder >= len(fhdr.GMV) || fhdr.GMV[st.refOrder].Type != header.WMTypeTranslation {
		return false
	}
	st.interMode = InterModeGlobalMV
	shift := 13
	if fhdr.HP == 0 {
		shift = 14
	}
	st.mv.X = int16(fhdr.GMV[st.refOrder].Matrix[0] >> shift)
	st.mv.Y = int16(fhdr.GMV[st.refOrder].Matrix[1] >> shift)
	return true
}

func applyCandidateInterMotion(st *interState, fs *FrameState, fb *FrameBuf, fhdr *header.FrameHeader, bx, by, modeHint, drlIdx int) bool {
	if st == nil || fs == nil || fhdr == nil {
		return false
	}
	cnt, stack := singleRefInterCandidates(fs, fhdr, fb, bx, by)
	if cnt <= 0 {
		return false
	}
	st.candCnt = cnt
	mode, pick := selectInterCandidateMode(modeHint, cnt)
	if drlIdx >= 0 && drlIdx < cnt && (modeHint == interModeHintNearest || drlIdx > 0) {
		pick = drlIdx
	}
	applyInterCandidate(st, fhdr, fb, stack[pick], mode)
	return true
}

func applySkipModeMotion(st *interState, fs *FrameState, fb *FrameBuf, fhdr *header.FrameHeader, bx, by int) bool {
	if st == nil || fs == nil || fhdr == nil || !st.skipMode {
		return false
	}
	cnt, stack := singleRefInterCandidates(fs, fhdr, fb, bx, by)
	if cnt <= 0 {
		return false
	}
	for i := 0; i < cnt; i++ {
		if stack[i].refSlot == st.refSlot {
			applyInterCandidate(st, fhdr, fb, stack[i], InterModeNearestMV)
			return true
		}
	}
	applyInterCandidate(st, fhdr, fb, stack[0], InterModeNearestMV)
	return true
}

func resolveSingleRefMotion(st *interState, fs *FrameState, fb *FrameBuf, fhdr *header.FrameHeader, segID uint8, bx, by int, syntax singleRefInterSyntax) {
	if st == nil || fhdr == nil {
		return
	}
	if st.skipMode {
		if applySkipModeMotion(st, fs, fb, fhdr, bx, by) {
			return
		}
		if applyTemporalInterMV(st, fs, fb, fhdr, bx, by) {
			return
		}
		if applyNeighbourGridMV(st, fs, fb, fhdr, bx, by) {
			return
		}
		return
	}
	switch syntax.motionSource {
	case interMotionSourceCandidate:
		if applyCandidateInterMotion(st, fs, fb, fhdr, bx, by, syntax.modeHint, syntax.drlIdx) {
			return
		}
	case interMotionSourceTemporal:
		if applyTemporalInterMV(st, fs, fb, fhdr, bx, by) {
			return
		}
	case interMotionSourceGlobal:
		if applyGlobalInterMV(st, fhdr, segID) {
			return
		}
	}
	if applyGlobalInterMV(st, fhdr, segID) {
		return
	}
	if applyTemporalInterMV(st, fs, fb, fhdr, bx, by) {
		return
	}
	if st.mv == (refmvs.MV{}) && applyCandidateInterMotion(st, fs, fb, fhdr, bx, by, syntax.modeHint, syntax.drlIdx) {
		return
	}
	if st.mv == (refmvs.MV{}) && applyNeighbourGridMV(st, fs, fb, fhdr, bx, by) {
		return
	}
	if st.mv == (refmvs.MV{}) && fs != nil && !st.skipMode {
		if neighMV, ok := fs.NeighbourInterMV(bx, by); ok {
			st.mv = neighMV
			st.interMode = InterModeNearestMV
		}
	}
}

func selectInterCandidateMode(modeHint, candCnt int) (mode, pick int) {
	mode = InterModeNearestMV
	pick = 0
	switch modeHint {
	case interModeHintNear:
		mode = InterModeNearMV
		if candCnt > 1 {
			pick = 1
		}
	case interModeHintRef:
		mode = InterModeRefMV
	case interModeHintNew:
		mode = InterModeNewMV
	}
	return mode, pick
}

func applyInterCandidate(st *interState, fhdr *header.FrameHeader, fb *FrameBuf, cand interCandidate, mode int) {
	if st == nil {
		return
	}
	st.refSlot = cand.refSlot
	st.refFrame = cand.refFrame
	st.ref = fb.Refs[st.refSlot]
	st.baseMV = cand.mv
	st.deltaMV = refmvs.MV{}
	for i, idx := range fhdr.Refidx {
		if int(idx) == st.refSlot {
			st.refOrder = i
			break
		}
	}
	switch mode {
	case InterModeNearMV:
		st.mv = cand.mv
		st.interMode = InterModeNearMV
	case InterModeRefMV:
		st.mv = cand.mv
		st.interMode = InterModeRefMV
	case InterModeNewMV:
		st.mv = composeNewMV(cand.mv, st.deltaMV)
		st.interMode = InterModeNewMV
	default:
		st.mv = cand.mv
		st.interMode = InterModeNearestMV
	}
}

func composeNewMV(base, delta refmvs.MV) refmvs.MV {
	return refmvs.MV{
		Y: base.Y + delta.Y,
		X: base.X + delta.X,
	}
}

func applyInterState(fb *FrameBuf, seq *header.SequenceHeader, bx, by, bw, bh int, st interState) bool {
	if st.ref == nil {
		return false
	}
	copyInterPredictPlane(fb.Y, fb.StrideY, fb.Width, fb.Height, st.ref.Y, st.ref.StrideY, st.ref.Width, st.ref.Height, bx, by, bw, bh, st.mv, st.filterMode, st.filterModeV)
	if fb.Monochrome || st.ref.Monochrome || len(fb.U) == 0 || len(st.ref.U) == 0 {
		return true
	}

	ssHor := int(seq.SsHor)
	ssVer := int(seq.SsVer)
	cmv := refmvs.MV{
		X: int16(floorDivPow2(int(st.mv.X), ssHor)),
		Y: int16(floorDivPow2(int(st.mv.Y), ssVer)),
	}
	cbx, cby, cbw, cbh := chromaRect(seq, bx, by, bw, bh)
	copyInterPredictPlane(fb.U, fb.StrideUV, fb.ChromaW, fb.ChromaH, st.ref.U, st.ref.StrideUV, st.ref.ChromaW, st.ref.ChromaH, cbx, cby, cbw, cbh, cmv, st.filterMode, st.filterModeV)
	copyInterPredictPlane(fb.V, fb.StrideUV, fb.ChromaW, fb.ChromaH, st.ref.V, st.ref.StrideUV, st.ref.ChromaW, st.ref.ChromaH, cbx, cby, cbw, cbh, cmv, st.filterMode, st.filterModeV)
	return true
}

func truncateMVToIntPel(v int16) int16 {
	return int16((int(v) / 8) * 8)
}

func interFilter2D(modeH, modeV header.FilterMode) predinter.Filter2D {
	if modeH == header.FilterModeBilinear || modeV == header.FilterModeBilinear {
		return predinter.Filter2DBilinear
	}
	switch {
	case modeH == header.FilterMode8TapRegular && modeV == header.FilterMode8TapRegular:
		return predinter.Filter2D8TapRegular
	case modeH == header.FilterMode8TapRegular && modeV == header.FilterMode8TapSmooth:
		return predinter.Filter2D8TapRegularSmooth
	case modeH == header.FilterMode8TapRegular && modeV == header.FilterMode8TapSharp:
		return predinter.Filter2D8TapRegularSharp
	case modeH == header.FilterMode8TapSharp && modeV == header.FilterMode8TapRegular:
		return predinter.Filter2D8TapSharpRegular
	case modeH == header.FilterMode8TapSharp && modeV == header.FilterMode8TapSmooth:
		return predinter.Filter2D8TapSharpSmooth
	case modeH == header.FilterMode8TapSharp && modeV == header.FilterMode8TapSharp:
		return predinter.Filter2D8TapSharp
	case modeH == header.FilterMode8TapSmooth && modeV == header.FilterMode8TapRegular:
		return predinter.Filter2D8TapSmoothRegular
	case modeH == header.FilterMode8TapSmooth && modeV == header.FilterMode8TapSmooth:
		return predinter.Filter2D8TapSmooth
	case modeH == header.FilterMode8TapSmooth && modeV == header.FilterMode8TapSharp:
		return predinter.Filter2D8TapSmoothSharp
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
	mv refmvs.MV, modeH, modeV header.FilterMode,
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
	filt := interFilter2D(modeH, modeV)
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

// largestTx returns the largest square RectTxfmSize that fits within w鑴砲 pixels.
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

func chromaLayoutIndex(seq *header.SequenceHeader) int {
	if seq == nil {
		return 1
	}
	switch {
	case seq.SsHor != 0 && seq.SsVer != 0:
		return 1
	case seq.SsHor != 0 && seq.SsVer == 0:
		return 2
	case seq.SsHor == 0 && seq.SsVer == 0:
		return 3
	default:
		return 1
	}
}

func maxTxForBlockSize(seq *header.SequenceHeader, bw, bh, plane int) uint8 {
	bs := bsizeFromDim(bw, bh)
	layout := 0
	if plane > 0 {
		layout = chromaLayoutIndex(seq)
	}
	if bs >= 0 && bs < len(MaxTxfmSizeForBS) {
		if tx := MaxTxfmSizeForBS[bs][layout]; tx != 0 {
			return tx
		}
	}
	if plane == 0 {
		return largestTx(bw, bh)
	}
	ssHor, ssVer := 1, 1
	if seq != nil {
		ssHor = int(seq.SsHor)
		ssVer = int(seq.SsVer)
	}
	return largestTx(
		(bw+(1<<ssHor)-1)>>ssHor,
		(bh+(1<<ssVer)-1)>>ssVer,
	)
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
