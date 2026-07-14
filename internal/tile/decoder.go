// decoder.go implements AV1 tile-level CABAC decoding for M7.
//
// Scope:
//   - Tile group OBU parsing (tile boundary extraction)
//   - Superblock traversal 閳?partition tree decoding
//   - Intra block: mode decode, prediction, coefficient decode, reconstruction
//   - Inter block: DC128 fill (motion compensation deferred to M8)
package tile

import (
	"encoding/binary"
	"fmt"
	"os"
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
	seq *header.SequenceHeader, fb *FrameBuf, fs *FrameState, logf func(string, ...any)) (err error) {
	return DecodeTileWithContext(td, fhdr, seq, fb, fs, nil, logf)
}

// DecodeTileWithContext decodes one tile from an optional inherited CDF state.
func DecodeTileWithContext(td TileData, fhdr *header.FrameHeader,
	seq *header.SequenceHeader, fb *FrameBuf, fs *FrameState, inherited *TileCtx, logf func(string, ...any)) (err error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}

	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			logf("tile: DecodeTile row=%d col=%d recovered from panic: %v\n%s",
				td.Row, td.Col, r, stack)
			err = fmt.Errorf("panic at tile row=%d col=%d: %v\n%s", td.Row, td.Col, r, stack)
		}
	}()

	m := bitstream.NewMSAC(td.Data, fhdr.DisableCDFUpdate != 0)
	if os.Getenv("GOAV1_DISABLE_TILE_CDF_UPDATE") != "" {
		m.SetAllowUpdateCDF(false)
	}
	ctx := inherited
	if ctx == nil {
		ctx = NewTileCtxForQIdx(int(fhdr.Quant.YAC))
	}

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
		ms := m.State()
		part := PartitionH
		if isSplit != 0 {
			part = PartitionSplit
		}
		fs.tracef("sym partition x=%d y=%d bl=%d ctx=%d val=%d rng=%d cnt=%d off=%d",
			bx, by, bl, partCtx, part, ms.Range, ms.Count, ms.BufferPosition)
		if isSplit != 0 {
			decodePartition(m, ctx, fs, fhdr, seq, fb, bx, by, bl+1)
			decodePartition(m, ctx, fs, fhdr, seq, fb, bx+half, by, bl+1)
		} else {
			decodeBlock(m, ctx, fs, fhdr, seq, fb, bx, by, blSz, half)
			fs.SetPartition(bx, by, bl, PartitionH, blSz)
		}
		return
	}
	if !haveHSplit {
		isSplit := m.Bool(gatherLeftPartitionProb(partCDF, bl))
		ms := m.State()
		part := PartitionV
		if isSplit != 0 {
			part = PartitionSplit
		}
		fs.tracef("sym partition x=%d y=%d bl=%d ctx=%d val=%d rng=%d cnt=%d off=%d",
			bx, by, bl, partCtx, part, ms.Range, ms.Count, ms.BufferPosition)
		if isSplit != 0 {
			decodePartition(m, ctx, fs, fhdr, seq, fb, bx, by, bl+1)
			decodePartition(m, ctx, fs, fhdr, seq, fb, bx, by+half, bl+1)
		} else {
			decodeBlock(m, ctx, fs, fhdr, seq, fb, bx, by, half, blSz)
			fs.SetPartition(bx, by, bl, PartitionV, blSz)
		}
		return
	}

	if fs.Tracef != nil {
		fs.tracef("sym partition_cdf x=%d y=%d bl=%d ctx=%d cdf=%v", bx, by, bl, partCtx, partCDF[:nPart])
	}
	part := int(m.SymbolAdaptDav1d(partCDF, nPart-1))
	ms := m.State()
	fs.tracef("sym partition x=%d y=%d bl=%d ctx=%d val=%d rng=%d cnt=%d off=%d",
		bx, by, bl, partCtx, part, ms.Range, ms.Count, ms.BufferPosition)

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
	lfDelta    [4]int8
	ctxBW      int
	ctxBH      int
}

func decodeBlockSyntaxState(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fhdr *header.FrameHeader, seq *header.SequenceHeader, fb *FrameBuf,
	bx, by, bw, bh int,
) blockSyntaxState {
	st := blockSyntaxState{ctxBW: bw, ctxBH: bh}

	if fhdr.Segmentation.Enabled != 0 && fhdr.Segmentation.UpdateMap == 0 {
		st.segID = 0
	}
	if fhdr.Segmentation.Enabled != 0 &&
		fhdr.Segmentation.UpdateMap != 0 &&
		fhdr.Segmentation.SegData.PreSkip != 0 {
		st.segID = readSegmentID(m, ctx, fs, fhdr, bx, by)
	}

	skipCtx := fs.SkipCtx(bx, by)
	skipCDF := ctx.SkipCDF[skipCtx]
	st.skip = m.BoolAdapt(ctx.SkipCDF[skipCtx][:2]) != 0
	ms := m.State()
	fs.tracef("sym block x=%d y=%d w=%d h=%d skip_ctx=%d skip=%t skip_cdf=%v->%v rng=%d cnt=%d off=%d",
		bx, by, bw, bh, skipCtx, st.skip, skipCDF, ctx.SkipCDF[skipCtx],
		ms.Range, ms.Count, ms.BufferPosition)

	if fhdr.Segmentation.Enabled != 0 &&
		fhdr.Segmentation.UpdateMap != 0 &&
		fhdr.Segmentation.SegData.PreSkip == 0 {
		if st.skip {
			st.segID, _ = fs.SegIDPredCtx(bx, by)
		} else {
			st.segID = readPostSkipSegmentID(m, ctx, fs, fhdr, bx, by, bw, bh)
		}
	}
	ms = m.State()
	fs.tracef("sym segment x=%d y=%d seg=%d rng=%d cnt=%d off=%d",
		bx, by, st.segID, ms.Range, ms.Count, ms.BufferPosition)

	if !st.skip {
		readCDEFIndex(m, fs, fhdr, bx, by, bw, bh)
	}
	ms = m.State()
	fs.tracef("sym cdef x=%d y=%d rng=%d cnt=%d off=%d",
		bx, by, ms.Range, ms.Count, ms.BufferPosition)
	readDeltaQLF(m, ctx, fhdr, seq, bx, by, bw, bh, st.skip)
	st.lfDelta = ctx.LastDeltaLF

	st.isIntra = fhdr.FrameType.IsIntra()
	if !fhdr.FrameType.IsIntra() {
		ictx := intraCtx(fs, bx, by)
		st.isIntra = m.BoolAdapt(ctx.IntraCDF[ictx][:]) == 0
	}
	ms = m.State()
	fs.tracef("sym intra x=%d y=%d val=%t rng=%d cnt=%d off=%d",
		bx, by, st.isIntra, ms.Range, ms.Count, ms.BufferPosition)
	st.hasChroma = blockHasChroma(seq, fb, bx, by, st.ctxBW, st.ctxBH)
	st.qidx = blockQIdx(ctx, fhdr, st.segID)
	st.qidxIsZero = st.qidx == 0
	st.lossless = fhdr.Segmentation.Lossless[st.segID] != 0
	ms = m.State()
	fs.tracef("sym block_syntax x=%d y=%d seg=%d intra=%t qidx=%d base_qidx=%d seg_delta_q=%d delta_q_present=%d last_qidx=%d rng=%d cnt=%d off=%d",
		bx, by, st.segID, st.isIntra, st.qidx, fhdr.Quant.YAC,
		fhdr.Segmentation.SegData.D[st.segID].DeltaQ, fhdr.Delta.Q.Present, ctx.LastQIdx,
		ms.Range, ms.Count, ms.BufferPosition)

	return st
}

func readPostSkipSegmentID(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fhdr *header.FrameHeader, bx, by, bw, bh int,
) uint8 {
	if fhdr.Segmentation.Temporal == 0 {
		return readSegmentID(m, ctx, fs, fhdr, bx, by)
	}
	segPredCtx := fs.SegPredCtx(bx, by)
	predicted := m.BoolAdapt(ctx.SegPredCDF[segPredCtx][:]) != 0
	fs.SetSegPred(bx, by, bw, bh, predicted)
	ms := m.State()
	fs.tracef("sym segpred x=%d y=%d ctx=%d val=%t rng=%d cnt=%d off=%d",
		bx, by, segPredCtx, predicted, ms.Range, ms.Count, ms.BufferPosition)
	if predicted {
		// The previous segmentation map is zero for an unsegmented reference.
		// Persisted reference segmentation maps will replace this fallback.
		return 0
	}
	return readSegmentID(m, ctx, fs, fhdr, bx, by)
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
		intraSt.yMode = int(m.SymbolAdaptDav1d(ctx.KFYModeCDF[topModeCtx][leftModeCtx][:], NIntraPredModes-1))
	} else {
		bs := bsizeFromDim(st.ctxBW, st.ctxBH)
		sizeCtx := 0
		if bs >= 0 && bs < len(YModeSizeContext) {
			sizeCtx = int(YModeSizeContext[bs])
		}
		intraSt.yMode = int(m.SymbolAdaptDav1d(ctx.YModeCDF[sizeCtx][:], NIntraPredModes-1))
	}
	if intraSt.yMode < 0 {
		intraSt.yMode = 0
	} else if intraSt.yMode >= NIntraPredModes {
		intraSt.yMode = NIntraPredModes - 1
	}
	ms := m.State()
	fs.tracef("sym y_mode x=%d y=%d w=%d h=%d val=%d rng=%d cnt=%d off=%d",
		bx, by, bw, bh, intraSt.yMode, ms.Range, ms.Count, ms.BufferPosition)

	if intraSt.yMode >= VertPred && intraSt.yMode <= VertLeftPred && angleDeltaAllowed(st.ctxBW, st.ctxBH) {
		v := int(m.SymbolAdaptDav1d(ctx.AngleDeltaCDF[intraSt.yMode-VertPred][:], 6))
		intraSt.yAngleDelta = v - 3
	}

	cflAllowed := 0
	if st.hasChroma && cflAllowedForBlock(seq, st.ctxBW, st.ctxBH, st.lossless) {
		cflAllowed = 1
	}
	intraSt.uvMode = DCPred
	if st.hasChroma {
		uvModeSyms := NIntraPredModes
		if cflAllowed != 0 {
			uvModeSyms = NUVIntraModes
		}
		intraSt.uvMode = int(m.SymbolAdaptDav1d(ctx.UVModeCDF[cflAllowed][intraSt.yMode][:], uvModeSyms-1))
		ms = m.State()
		fs.tracef("sym uv_mode x=%d y=%d cfl=%d val=%d rng=%d cnt=%d off=%d",
			bx, by, cflAllowed, intraSt.uvMode, ms.Range, ms.Count, ms.BufferPosition)
	}

	if st.hasChroma && intraSt.uvMode >= VertPred && intraSt.uvMode <= VertLeftPred && angleDeltaAllowed(st.ctxBW, st.ctxBH) {
		v := int(m.SymbolAdaptDav1d(ctx.AngleDeltaCDF[intraSt.uvMode-VertPred][:], 6))
		intraSt.uvAngleDelta = v - 3
	}
	if st.hasChroma && intraSt.uvMode == CFLPred {
		intraSt.cflAlphaU, intraSt.cflAlphaV = decodeCFLAlphas(m, ctx)
	}

	if fhdr.AllowScreenContentTools != 0 && st.ctxBW <= 64 && st.ctxBH <= 64 && (st.ctxBW+st.ctxBH) >= 16 {
		szCtx := palSzCtx(st.ctxBW, st.ctxBH)
		if intraSt.yMode == DCPred {
			palCtx := fs.PaletteYCtx(bx, by)
			if m.BoolAdapt(ctx.PaletteYCDF[szCtx][palCtx][:]) != 0 {
				intraSt.palSzY = int(m.SymbolAdaptDav1d(ctx.PaletteSizeCDF[0][szCtx][:], 6)) + 2
				intraSt.pal[0] = readPalettePlane(m, ctx, fs, seq, 0, szCtx, bx, by, intraSt.palSzY)
			}
		}
		if st.hasChroma && intraSt.uvMode == DCPred {
			palCtx := 0
			if intraSt.palSzY > 0 {
				palCtx = 1
			}
			if m.BoolAdapt(ctx.PaletteUVCDF[palCtx][:]) != 0 {
				intraSt.palSzUV = int(m.SymbolAdaptDav1d(ctx.PaletteSizeCDF[1][szCtx][:], 6)) + 2
				intraSt.pal[1], intraSt.pal[2] = readPaletteUV(m, ctx, fs, seq, szCtx, bx, by, intraSt.palSzUV)
			}
		}
	}
	fs.SetPaletteCtx(bx, by, st.ctxBW, st.ctxBH, intraSt.palSzY, intraSt.palSzUV)

	if seq.FilterIntra && intraSt.yMode == DCPred && intraSt.palSzY == 0 && st.ctxBW <= 32 && st.ctxBH <= 32 {
		bs := bsizeFromDim(st.ctxBW, st.ctxBH)
		if bs >= 0 {
			useFI := m.BoolAdapt(ctx.UseFilterIntraCDF[bs][:])
			if useFI != 0 {
				intraSt.filterMode = int(m.SymbolAdaptDav1d(ctx.FilterIntraModeCDF[:], 4))
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
		intraSt.palIdxY = readPalIndices(m, &ctx.ColorMapCDF[0][intraSt.palSzY-2], intraSt.palSzY, bw, bh, st.ctxBW, st.ctxBH)
	}
	if st.hasChroma && intraSt.palSzUV > 0 {
		_, _, cbw, cbh := chromaRect(seq, bx, by, bw, bh)
		_, _, ctxCBW, ctxCBH := chromaRect(seq, bx, by, st.ctxBW, st.ctxBH)
		intraSt.palIdxUV = readPalIndices(m, &ctx.ColorMapCDF[1][intraSt.palSzUV-2], intraSt.palSzUV, cbw, cbh, ctxCBW, ctxCBH)
	}

	intraSt.txY = maxTxForBlockSize(seq, st.ctxBW, st.ctxBH, 0)
	intraSt.txUV = maxTxForBlockSize(seq, st.ctxBW, st.ctxBH, 1)

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
		fs.SetIntraTxCtx(bx, by, st.ctxBW, st.ctxBH, intraSt.txY)
	case st.lossless:
		intraSt.txY = transform.TX4x4
		intraSt.txUV = transform.TX4x4
		intraSt.blockState.Tx = intraSt.txY
		intraSt.blockState.MaxYTx = intraSt.txY
		fs.SetIntraTxCtx(bx, by, st.ctxBW, st.ctxBH, intraSt.txY)
	case fhdr.TxfmMode == header.TxfmModeSwitchable:
		intraSt.blockState.MaxYTx = intraSt.txY
		intraSt.txY = readIntraTxSize(m, ctx, fs, bx, by, intraSt.txY)
		intraSt.blockState.Tx = intraSt.txY
		fs.SetIntraTxCtx(bx, by, st.ctxBW, st.ctxBH, intraSt.txY)
	default:
		fs.SetIntraTxCtx(bx, by, st.ctxBW, st.ctxBH, intraSt.txY)
	}

	return intraSt
}

func readIntraTxSize(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, bx, by int, maxTx uint8) uint8 {
	td := transform.TxfmDimensions[maxTx]
	if td.Max == 0 {
		return maxTx
	}
	txCtx := fs.IntraTxCtx(bx, by, maxTx)
	maxIdx := int(td.Max) - 1
	if maxIdx < 0 || maxIdx >= len(ctx.TxSzCDF) {
		return maxTx
	}
	nSyms := minInt(int(td.Max), 2) + 1
	beforeCDF := ctx.TxSzCDF[maxIdx][txCtx]
	depth := int(m.SymbolAdaptDav1d(ctx.TxSzCDF[maxIdx][txCtx][:], nSyms-1))
	ms := m.State()
	fs.tracef("sym tx_size x=%d y=%d max=%d ctx=%d depth=%d cdf=%v->%v rng=%d cnt=%d off=%d",
		bx, by, maxTx, txCtx, depth, beforeCDF, ctx.TxSzCDF[maxIdx][txCtx],
		ms.Range, ms.Count, ms.BufferPosition)
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
	ctxBW := st.ctxBW
	ctxBH := st.ctxBH
	if ctxBW <= 0 {
		ctxBW = bw
	}
	if ctxBH <= 0 {
		ctxBH = bh
	}
	maxYTx := maxTxForBlockSize(seq, ctxBW, ctxBH, 0)
	uvtx := maxTxForBlockSize(seq, ctxBW, ctxBH, 1)
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
		fs.SetTxCtx(bx, by, ctxBW, ctxBH, maxYTx, fhdr.TxfmMode == header.TxfmModeSwitchable, true)
	case st.lossless || maxYTx == transform.TX4x4:
		out.maxYTx = transform.TX4x4
		out.uvtx = transform.TX4x4
		out.block.Tx = transform.TX4x4
		out.block.MaxYTx = transform.TX4x4
		out.block.Uvtx = transform.TX4x4
		if fhdr.TxfmMode == header.TxfmModeSwitchable {
			fs.SetTxCtx(bx, by, ctxBW, ctxBH, transform.TX4x4, true, false)
		}
	case fhdr.TxfmMode == header.TxfmModeSwitchable:
		out.block.Tx, out.yTxBlocks, out.block = readVarTxTree(m, ctx, fs, bx, by, ctxBW, ctxBH, maxYTx, uvtx)
	default:
		fs.SetTxCtx(bx, by, ctxBW, ctxBH, maxYTx, false, false)
	}
	fs.SetInterTxIntraCtx(bx, by, ctxBW, ctxBH)

	return out
}

func commitIntraBlockState(fs *FrameState, bx, by, bw, bh int, st blockSyntaxState, intraSt intraSyntaxState) {
	if intraSt.palSzY > 0 || intraSt.palSzUV > 0 {
		fs.SetPaletteColors(bx, by, st.ctxBW, st.ctxBH, intraSt.pal)
	}
	modeCtxY := intraSt.yMode
	if intraSt.filterMode >= 0 {
		modeCtxY = DCPred
	}
	intraSt.blockState.Bl = uint8(blockLevelFromDim(st.ctxBW, st.ctxBH))
	intraSt.blockState.Bs = uint8(maxInt(bsizeFromDim(st.ctxBW, st.ctxBH), 0))
	intraSt.blockState.Uvtx = intraSt.txUV
	intraSt.blockState.LFDelta = st.lfDelta
	fs.SetBlockState(bx, by, st.ctxBW, st.ctxBH, intraSt.blockState)
	if st.hasChroma {
		fs.SetChromaBlockState(bx, by, st.ctxBW, st.ctxBH, intraSt.blockState)
	}
	setFixedTxState(fs, bx, by, st.ctxBW, st.ctxBH, intraSt.txY)
	if st.hasChroma {
		cbx, cby, cbw, cbh := chromaRect(&header.SequenceHeader{SsHor: fs.SsHor, SsVer: fs.SsVer}, bx, by, bw, bh)
		fs.SetUVModeState(cbx, cby, cbw, cbh, uint8(intraSt.uvMode))
	}
	fs.SetBlockSeg(bx, by, st.ctxBW, st.ctxBH, st.skip, modeCtxY, st.segID)
}

func setFixedTxState(fs *FrameState, bx, by, bw, bh int, tx uint8) {
	td := transform.TxfmDimensions[tx]
	tw, th := int(td.W)*4, int(td.H)*4
	for y := 0; y < bh; y += th {
		for x := 0; x < bw; x += tw {
			fs.SetTxState(bx+x, by+y, minInt(tw, bw-x), minInt(th, bh-y), tx)
		}
	}
}

func commitInterTxState(fs *FrameState, bx, by, bw, bh int, st interTransformState) {
	if len(st.yTxBlocks) == 0 {
		setFixedTxState(fs, bx, by, bw, bh, st.maxYTx)
		return
	}
	for _, leaf := range st.yTxBlocks {
		fs.SetTxState(bx+leaf.x, by+leaf.y, minInt(leaf.w, bw-leaf.x), minInt(leaf.h, bh-leaf.y), leaf.tx)
	}
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
			decodePalettePlaneVarTx(m, ctx, fs, fb, 0, bx, by, bw, bh, st.ctxBW, st.ctxBH, intraSt.yTxBlocks, intraSt.pal[0], intraSt.palIdxY, reconSt.dqY, skip, intraSt.yModeNofilt, reconSt.reducedTxtpSet, qidxIsZero, lossless)
		} else {
			decodePalettePlane(m, ctx, fs, fb, 0, bx, by, bw, bh, st.ctxBW, st.ctxBH, intraSt.txY, intraSt.pal[0], intraSt.palIdxY, reconSt.dqY, skip, intraSt.yModeNofilt, reconSt.reducedTxtpSet, qidxIsZero, lossless)
		}
	} else if len(intraSt.yTxBlocks) > 0 {
		decodeIntraPlaneVarTx(m, ctx, fs, fb, 0, bx, by, bw, bh, st.ctxBW, st.ctxBH, intraSt.yTxBlocks, intraSt.yMode, intraSt.yAngleDelta, intraSt.filterMode, reconSt.dqY, skip, intraSt.yModeNofilt, reconSt.reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)
	} else {
		decodeIntraPlane(m, ctx, fs, fb, 0, bx, by, bw, bh, st.ctxBW, st.ctxBH, intraSt.txY, intraSt.yMode, intraSt.yAngleDelta, intraSt.filterMode, 0, reconSt.dqY, skip, intraSt.yModeNofilt, reconSt.reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)
	}

	if st.hasChroma && len(fb.U) > 0 {
		cbx, cby, cbw, cbh := chromaRect(seq, bx, by, bw, bh)
		_, _, ctxCBW, ctxCBH := chromaRect(seq, bx, by, st.ctxBW, st.ctxBH)
		recordChromaDebug(st, intraSt, cbw, cbh)

		if intraSt.palSzUV > 0 {
			decodePalettePlane(m, ctx, fs, fb, 1, cbx, cby, cbw, cbh, ctxCBW, ctxCBH, intraSt.txUV, intraSt.pal[1], intraSt.palIdxUV, reconSt.dqU, skip, intraSt.uvMode, reconSt.reducedTxtpSet, qidxIsZero, lossless)
			decodePalettePlane(m, ctx, fs, fb, 2, cbx, cby, cbw, cbh, ctxCBW, ctxCBH, intraSt.txUV, intraSt.pal[2], intraSt.palIdxUV, reconSt.dqV, skip, intraSt.uvMode, reconSt.reducedTxtpSet, qidxIsZero, lossless)
		} else if intraSt.uvMode == CFLPred {
			acCfl := buildCflAc(fb, seq, bx, by, bw, bh, cbw, cbh)
			if intraSt.cflAlphaU != 0 {
				decodeIntraPlaneCFL(m, ctx, fs, fb, 1, cbx, cby, cbw, cbh, ctxCBW, ctxCBH, intraSt.txUV, int(intraSt.cflAlphaU), reconSt.dqU, skip, intraSt.yMode, reconSt.reducedTxtpSet, fhdr, seq, qidxIsZero, lossless, acCfl)
			} else {
				decodeIntraPlane(m, ctx, fs, fb, 1, cbx, cby, cbw, cbh, ctxCBW, ctxCBH, intraSt.txUV, DCPred, 0, -1, 0, reconSt.dqU, skip, CFLPred, reconSt.reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)
			}
			if intraSt.cflAlphaV != 0 {
				decodeIntraPlaneCFL(m, ctx, fs, fb, 2, cbx, cby, cbw, cbh, ctxCBW, ctxCBH, intraSt.txUV, int(intraSt.cflAlphaV), reconSt.dqV, skip, intraSt.yMode, reconSt.reducedTxtpSet, fhdr, seq, qidxIsZero, lossless, acCfl)
			} else {
				decodeIntraPlane(m, ctx, fs, fb, 2, cbx, cby, cbw, cbh, ctxCBW, ctxCBH, intraSt.txUV, DCPred, 0, -1, 0, reconSt.dqV, skip, CFLPred, reconSt.reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)
			}
		} else {
			decodeIntraPlane(m, ctx, fs, fb, 1, cbx, cby, cbw, cbh, ctxCBW, ctxCBH, intraSt.txUV, intraSt.uvMode, intraSt.uvAngleDelta, -1, 0, reconSt.dqU, skip, intraSt.uvMode, reconSt.reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)
			decodeIntraPlane(m, ctx, fs, fb, 2, cbx, cby, cbw, cbh, ctxCBW, ctxCBH, intraSt.txUV, intraSt.uvMode, intraSt.uvAngleDelta, -1, 0, reconSt.dqV, skip, intraSt.uvMode, reconSt.reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)
		}
	}
	commitIntraBlockState(fs, bx, by, bw, bh, st, intraSt)
}

func decodeInterPlaneResidual(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fb *FrameBuf,
	plane, bx, by, bw, bh, ctxBW, ctxBH int,
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
	return decodeInterPlaneResidualVarTxImpl(m, ctx, fs, fb, plane, bx, by, bw, bh, ctxBW, ctxBH, collectUniformTxBlocks(bw, bh, tx), dq, skip, seq, txtpGrid, interYTxtp, reducedTxtpSet, qidxIsZero, lossless)
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
	// readVarTxTree already records the exact transform leaves in syntax order.
	// Prefer that list over reconstructing it from the compact split masks: the
	// mask bit coordinates are metadata and are not a stable traversal format.
	if len(txSt.yTxBlocks) > 0 {
		lumaTxtp = decodeInterPlaneResidualVarTx(m, ctx, fs, fb, 0, bx, by, bw, bh, st.ctxBW, st.ctxBH, txSt.yTxBlocks, reconSt.dqY, st.skip, txtpGrid, uint8(transform.DCT_DCT), reconSt.reducedTxtpSet, st.qidxIsZero, st.lossless)
	} else if txSt.block.TxSplit0 != 0 || txSt.block.TxSplit1 != 0 {
		lumaTxtp = decodeInterPlaneResidualTree(m, ctx, fs, fb, 0, bx, by, bw, bh, st.ctxBW, st.ctxBH, txSt.block.MaxYTx, txSt.block.TxSplit0, txSt.block.TxSplit1, reconSt.dqY, st.skip, seq, txtpGrid, uint8(transform.DCT_DCT), reconSt.reducedTxtpSet, st.qidxIsZero, st.lossless)
	} else {
		lumaTxtp = decodeInterPlaneResidual(m, ctx, fs, fb, 0, bx, by, bw, bh, st.ctxBW, st.ctxBH, txSt.maxYTx, reconSt.dqY, st.skip, seq, txtpGrid, uint8(transform.DCT_DCT), reconSt.reducedTxtpSet, st.qidxIsZero, st.lossless)
	}
	if st.hasChroma && len(fb.U) > 0 {
		cbx, cby, cbw, cbh := chromaRect(seq, bx, by, bw, bh)
		_, _, ctxCBW, ctxCBH := chromaRect(seq, bx, by, st.ctxBW, st.ctxBH)
		decodeInterPlaneResidual(m, ctx, fs, fb, 1, cbx, cby, cbw, cbh, ctxCBW, ctxCBH, txSt.uvtx, reconSt.dqU, st.skip, seq, txtpGrid, lumaTxtp, reconSt.reducedTxtpSet, st.qidxIsZero, st.lossless)
		decodeInterPlaneResidual(m, ctx, fs, fb, 2, cbx, cby, cbw, cbh, ctxCBW, ctxCBH, txSt.uvtx, reconSt.dqV, st.skip, seq, txtpGrid, lumaTxtp, reconSt.reducedTxtpSet, st.qidxIsZero, st.lossless)
	}
}

func decodeBlock(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fhdr *header.FrameHeader, seq *header.SequenceHeader,
	fb *FrameBuf, bx, by, bw, bh int) {
	ctxBW := bw
	ctxBH := bh

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
	st := decodeBlockSyntaxState(m, ctx, fs, fhdr, seq, fb, bx, by, ctxBW, ctxBH)
	st.ctxBW = ctxBW
	st.ctxBH = ctxBH

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
	bx4 := bx >> 2
	by4 := by >> 2
	bw4 := (bw + 3) >> 2
	bh4 := (bh + 3) >> 2
	cbx = (bx4 >> ssHor) << 2
	cby = (by4 >> ssVer) << 2
	cbw = ((bw4 + ssHor) >> ssHor) << 2
	cbh = ((bh4 + ssVer) >> ssVer) << 2
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
	predID, segCtx := fs.SegIDPredCtx(bx, by)
	pred := int(predID)
	before := m.State()
	beforeCDF := ctx.SegIDCDF[segCtx]
	diff := int(m.SymbolAdaptDav1d(ctx.SegIDCDF[segCtx][:], int(header.MaxSegments)-1))
	maxSeg := int(fhdr.Segmentation.SegData.LastActiveSegID) + 1
	if maxSeg <= 0 || maxSeg > int(header.MaxSegments) {
		maxSeg = int(header.MaxSegments)
	}
	segID := negDeinterleave(diff, pred, maxSeg)
	after := m.State()
	fs.tracef("sym segid-detail x=%d y=%d pred=%d ctx=%d diff=%d max=%d before_rng=%d before_dif=%016x after_rng=%d after_dif=%016x cdf=%v",
		bx, by, pred, segCtx, diff, maxSeg, before.Range, before.Dif, after.Range, after.Dif, beforeCDF)
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
	delta := int(m.SymbolAdaptDav1d(cdf, 3))
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
	sign := int(m.SymbolAdaptDav1d(ctx.CFLSignCDF[:], 7)) + 1
	signU := sign * 0x56 >> 8
	signV := sign - signU*3

	var alphaU, alphaV int
	if signU != 0 {
		c := 0
		if signU == 2 {
			c = 3
		}
		c += signV
		alphaU = int(m.SymbolAdaptDav1d(ctx.CFLAlphaCDF[c][:], 15)) + 1
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
		alphaV = int(m.SymbolAdaptDav1d(ctx.CFLAlphaCDF[c][:], 15)) + 1
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
	baseX := bx
	baseY := by
	if ssHor != 0 {
		baseX &^= (1 << ssHor) - 1
	}
	if ssVer != 0 {
		baseY &^= (1 << ssVer) - 1
	}
	validW := cbw
	validH := cbh
	if remW := fb.Width - baseX; remW >= 0 {
		maxW := (remW + (1 << ssHor) - 1) >> ssHor
		if validW > maxW {
			validW = maxW
		}
	} else {
		validW = 0
	}
	if remH := fb.Height - baseY; remH >= 0 {
		maxH := (remH + (1 << ssVer) - 1) >> ssVer
		if validH > maxH {
			validH = maxH
		}
	} else {
		validH = 0
	}
	if validW > cbw {
		validW = cbw
	}
	if validH > cbh {
		validH = cbh
	}

	for cy := 0; cy < validH; cy++ {
		rowOff := cy * cbw
		srcY := baseY + (cy << ssVer)
		if srcY >= fb.Height {
			srcY = fb.Height - 1
		}
		srcY1 := srcY
		if ssVer != 0 && srcY1+1 < fb.Height {
			srcY1++
		}
		for cx := 0; cx < validW; cx++ {
			srcX := baseX + (cx << ssHor)
			if srcX >= fb.Width {
				srcX = fb.Width - 1
			}
			srcX1 := srcX
			if ssHor != 0 && srcX1+1 < fb.Width {
				srcX1++
			}
			acSum := int(fb.Y[srcY*stride+srcX])
			if ssHor != 0 {
				acSum += int(fb.Y[srcY*stride+srcX1])
			}
			if ssVer != 0 {
				acSum += int(fb.Y[srcY1*stride+srcX])
				if ssHor != 0 {
					acSum += int(fb.Y[srcY1*stride+srcX1])
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
	plane, bx, by, bw, bh, ctxBW, ctxBH int,
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

			haveTop, haveLeft := fs.intraAvailability(plane, bx+tbx, by+tby)
			dispatchMode, packedAngle := prepareIntraPrediction(
				planeBuf, stride, planeW, planeH,
				bx+tbx, by+tby, tw, th,
				tlBuf, tl,
				DCPred, 0, -1,
				false, smoothFlags, haveTop, haveLeft,
			)
			if cflAlpha != 0 {
				acSlice := cflAcSubBlock(ac, bw, bh, tbx, tby, tw, th)
				predictCFLBlock(predBuf, tw, tlBuf, tl, bx+tbx, by+tby, tw, th, cflAlpha, acSlice)
			} else {
				callPreparedIntraPred(dispatchMode, packedAngle, -1, predBuf, tw, tlBuf, tl, tw, th)
			}

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
				coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, tx, plane, bx+tbx, by+tby, ctxBW, ctxBH, tw, th, coeffMode, true, transform.DCT_DCT, reducedTxtpSet, qidxIsZero, lossless, dq)
				fs.SetCoefCtxBlock(plane, bx+tbx, by+tby, tw, th, resCtx)
				if eob >= 0 && len(coeff) > 0 {
					ReconBlockDequantizedVisible(dst, stride, coeff, eob, tx, txtp, 8, tw, th)
				}
			} else {
				fs.SetCoefCtxBlock(plane, bx+tbx, by+tby, tw, th, 0x40)
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
	plane, bx, by, bw, bh, ctxBW, ctxBH int,
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
	palStride := ctxBW
	if palStride <= 0 {
		palStride = bw
	}
	palRows := ctxBH
	if palRows <= 0 {
		palRows = bh
	}
	if len(palIdx) < palStride*palRows {
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
			predictPalette(predBuf, tw, pal, palIdx[tby*palStride+tbx:], tw, th, palStride)
			for row := 0; row < th; row++ {
				dstRow := (by+tby+row)*stride + (bx + tbx)
				if dstRow+tw > len(planeBuf) {
					break
				}
				copy(planeBuf[dstRow:dstRow+tw], predBuf[row*tw:(row+1)*tw])
			}
			if !skip {
				coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, tx, plane, bx+tbx, by+tby, ctxBW, ctxBH, tw, th, yMode, true, transform.DCT_DCT, reducedTxtpSet, qidxIsZero, lossless, dq)
				fs.SetCoefCtxBlock(plane, bx+tbx, by+tby, tw, th, resCtx)
				if eob >= 0 && len(coeff) > 0 {
					ReconBlockDequantizedVisible(dst, stride, coeff, eob, tx, txtp, 8, tw, th)
				}
			} else {
				fs.SetCoefCtxBlock(plane, bx+tbx, by+tby, tw, th, 0x40)
			}
		}
	}
}

func decodePalettePlaneVarTx(
	m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fb *FrameBuf,
	plane, bx, by, bw, bh, ctxBW, ctxBH int,
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
	palStride := ctxBW
	if palStride <= 0 {
		palStride = bw
	}
	palRows := ctxBH
	if palRows <= 0 {
		palRows = bh
	}
	if len(palIdx) < palStride*palRows {
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
		predictPalette(predBuf, tw, pal, palIdx[blk.y*palStride+blk.x:], tw, th, palStride)
		for row := 0; row < th; row++ {
			dstRow := (by+blk.y+row)*stride + (bx + blk.x)
			if dstRow+tw > len(planeBuf) {
				break
			}
			copy(planeBuf[dstRow:dstRow+tw], predBuf[row*tw:(row+1)*tw])
		}
		if !skip {
			coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, blk.tx, plane, bx+blk.x, by+blk.y, ctxBW, ctxBH, tw, th, yMode, true, transform.DCT_DCT, reducedTxtpSet, qidxIsZero, lossless, dq)
			fs.SetCoefCtxBlock(plane, bx+blk.x, by+blk.y, tw, th, resCtx)
			if eob >= 0 && len(coeff) > 0 {
				ReconBlockDequantizedVisible(dst, stride, coeff, eob, blk.tx, txtp, 8, tw, th)
			}
		} else {
			fs.SetCoefCtxBlock(plane, bx+blk.x, by+blk.y, tw, th, 0x40)
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

func collectTxBlocksFromSplits(bx, by, bw, bh, frameW, frameH int, maxTx uint8, split0 uint8, split1 uint16) []txBlockSpec {
	specs := make([]txBlockSpec, 0, 16)
	collectTxBlocksFromSplitNode(bx, by, bw, bh, frameW, frameH, maxTx, 0, 0, 0, split0, split1, &specs)
	return specs
}

func collectTxBlocksFromSplitNode(bx, by, bw, bh, frameW, frameH int, tx uint8, depth, xOff, yOff int, split0 uint8, split1 uint16, specs *[]txBlockSpec) {
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
	if isSplit && td.Max > 0 {
		sub := td.Sub
		subDim := transform.TxfmDimensions[sub]
		subW := int(subDim.W) * 4
		subH := int(subDim.H) * 4

		collectTxBlocksFromSplitNode(bx, by, bw, bh, frameW, frameH, sub, depth+1, xOff*2, yOff*2, split0, split1, specs)
		if txw >= txh && bx+px+subW < frameW {
			collectTxBlocksFromSplitNode(bx, by, bw, bh, frameW, frameH, sub, depth+1, xOff*2+1, yOff*2, split0, split1, specs)
		}
		if txh >= txw && by+py+subH < frameH {
			collectTxBlocksFromSplitNode(bx, by, bw, bh, frameW, frameH, sub, depth+1, xOff*2, yOff*2+1, split0, split1, specs)
			if txw >= txh && bx+px+subW < frameW {
				collectTxBlocksFromSplitNode(bx, by, bw, bh, frameW, frameH, sub, depth+1, xOff*2+1, yOff*2+1, split0, split1, specs)
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
			readTxTree(m, ctx, fs, bx, by, bw, bh, fs.W4*4, fs.H4*4, maxTx, 0, xOff, yOff, &block, &specs, &minTx)
		}
	}
	block.Tx = minTx
	return minTx, specs, block
}

func decodeInterPlaneResidualVarTx(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fb *FrameBuf, plane, bx, by, bw, bh, ctxBW, ctxBH int,
	blocks []txBlockSpec, dq [2]uint16,
	skip bool, txtpGrid *interTxtpGrid, interYTxtp uint8,
	reducedTxtpSet bool,
	qidxIsZero bool,
	lossless bool,
) uint8 {
	return decodeInterPlaneResidualVarTxImpl(m, ctx, fs, fb, plane, bx, by, bw, bh, ctxBW, ctxBH, blocks, dq, skip, nil, txtpGrid, interYTxtp, reducedTxtpSet, qidxIsZero, lossless)
}

func decodeInterPlaneResidualVarTxImpl(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fb *FrameBuf, plane, bx, by, bw, bh, ctxBW, ctxBH int,
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
			fs.SetCoefCtxBlock(plane, bx+blk.x, by+blk.y, tw, th, 0x40)
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
		coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, blk.tx, plane, bx+blk.x, by+blk.y, ctxBW, ctxBH, tw, th, DCPred, false, blockInterYTxtp, reducedTxtpSet, qidxIsZero, lossless, dq)
		if !txtpSet {
			txtpOut = txtp
			txtpSet = true
		}
		if plane == 0 && txtpGrid != nil {
			// dav1d's scratch txtp_map is written in transform-block space,
			// not clipped to the visible plane edge. Keep the luma txtp grid
			// aligned to the decoded transform geometry so chroma txtp
			// derivation samples the same map shape near frame borders.
			txtpGrid.fillBlock(bx+blk.x, by+blk.y, blk.w, blk.h, txtp)
		}
		fs.SetCoefCtxBlock(plane, bx+blk.x, by+blk.y, tw, th, resCtx)
		if eob < 0 || len(coeff) == 0 {
			continue
		}
		ReconBlockDequantizedVisible(dst, stride, coeff, eob, blk.tx, txtp, 8, tw, th)
	}
	return txtpOut
}

func decodeInterPlaneResidualTree(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	fb *FrameBuf, plane, bx, by, bw, bh, ctxBW, ctxBH int,
	maxTx uint8, split0 uint8, split1 uint16,
	dq [2]uint16, skip bool, seq *header.SequenceHeader, txtpGrid *interTxtpGrid, interYTxtp uint8,
	reducedTxtpSet bool, qidxIsZero bool, lossless bool,
) uint8 {
	frameW := fb.Width
	frameH := fb.Height
	if plane > 0 {
		frameW = fb.ChromaW
		frameH = fb.ChromaH
	}
	blocks := collectTxBlocksFromSplits(bx, by, bw, bh, frameW, frameH, maxTx, split0, split1)
	if len(blocks) == 0 {
		return interYTxtp
	}
	return decodeInterPlaneResidualVarTxImpl(m, ctx, fs, fb, plane, bx, by, bw, bh, ctxBW, ctxBH, blocks, dq, skip, seq, txtpGrid, interYTxtp, reducedTxtpSet, qidxIsZero, lossless)
}

func readTxTree(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, bx, by, bw, bh, frameW, frameH int, tx uint8, depth, xOff, yOff int, block *Av1Block, specs *[]txBlockSpec, minTx *uint8) {
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
			ms := m.State()
			fs.tracef("sym txpart x=%d y=%d tx=%d depth=%d xoff=%d yoff=%d cat=%d ctx=%d split=%t rng=%d cnt=%d off=%d",
				bx+px, by+py, tx, depth, xOff, yOff, cat, tctx, isSplit,
				ms.Range, ms.Count, ms.BufferPosition)
			if isSplit {
				if depth == 0 {
					block.TxSplit0 |= 1 << (yOff*4 + xOff)
				} else {
					block.TxSplit1 |= 1 << (yOff*4 + xOff)
				}
			}
		}
	}

	if isSplit && td.Max > 0 {
		sub := td.Sub
		subDim := transform.TxfmDimensions[sub]
		subW := int(subDim.W) * 4
		subH := int(subDim.H) * 4

		readTxTree(m, ctx, fs, bx, by, bw, bh, frameW, frameH, sub, depth+1, xOff*2, yOff*2, block, specs, minTx)
		if txw >= txh && bx+px+subW < frameW {
			readTxTree(m, ctx, fs, bx, by, bw, bh, frameW, frameH, sub, depth+1, xOff*2+1, yOff*2, block, specs, minTx)
		}
		if txh >= txw && by+py+subH < frameH {
			readTxTree(m, ctx, fs, bx, by, bw, bh, frameW, frameH, sub, depth+1, xOff*2, yOff*2+1, block, specs, minTx)
			if txw >= txh && bx+px+subW < frameW {
				readTxTree(m, ctx, fs, bx, by, bw, bh, frameW, frameH, sub, depth+1, xOff*2+1, yOff*2+1, block, specs, minTx)
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
	plane, bx, by, bw, bh, ctxBW, ctxBH int,
	blocks []txBlockSpec,
	mode, angleDelta, filterMode int,
	dq [2]uint16,
	skip bool,
	coeffMode int,
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

		haveTop, haveLeft := fs.intraAvailability(plane, bx+blk.x, by+blk.y)
		dispatchMode, packedAngle := prepareIntraPrediction(
			planeBuf, stride, planeW, planeH,
			bx+blk.x, by+blk.y, tw, th,
			tlBuf, tl,
			mode, angleDelta, filterMode,
			enableEdgeFilter, smoothFlags, haveTop, haveLeft,
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
			coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, blk.tx, plane, bx+blk.x, by+blk.y, ctxBW, ctxBH, tw, th, coeffMode, true, transform.DCT_DCT, reducedTxtpSet, qidxIsZero, lossless, dq)
			if plane > 0 {
				recordChromaResidualDebug(plane, false, eob, -1, -1)
			}
			fs.SetCoefCtxBlock(plane, bx+blk.x, by+blk.y, tw, th, resCtx)
			if eob >= 0 && len(coeff) > 0 {
				ReconBlockDequantizedVisible(dst, stride, coeff, eob, blk.tx, txtp, 8, tw, th)
			}
		} else {
			if plane > 0 {
				recordChromaResidualDebug(plane, true, -1, -1, -1)
			}
			fs.SetCoefCtxBlock(plane, bx+blk.x, by+blk.y, tw, th, 0x40)
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
	plane, bx, by, bw, bh, ctxBW, ctxBH int,
	tx uint8,
	mode, angleDelta, filterMode, cflAlpha int,
	dq [2]uint16,
	skip bool,
	coeffMode int,
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
			haveTop, haveLeft := fs.intraAvailability(plane, bx+tbx, by+tby)
			dispatchMode, packedAngle := prepareIntraPrediction(
				planeBuf, stride, planeW, planeH,
				bx+tbx, by+tby, tw, th,
				tlBuf, tl,
				mode, angleDelta, filterMode,
				enableEdgeFilter, smoothFlags, haveTop, haveLeft,
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
				coeff, eob, txtp, resCtx := decodeCoefficients(m, ctx, fs, tx, plane, bx+tbx, by+tby, ctxBW, ctxBH, tw, th, coeffMode, true, transform.DCT_DCT, reducedTxtpSet, qidxIsZero, lossless, dq)
				if plane > 0 {
					recordChromaResidualDebug(plane, false, eob, -1, -1)
				}
				fs.SetCoefCtxBlock(plane, bx+tbx, by+tby, tw, th, resCtx)
				if eob >= 0 && len(coeff) > 0 {
					ReconBlockDequantizedVisible(dst, stride, coeff, eob, tx, txtp, 8, tw, th)
				}
			} else {
				if plane > 0 {
					recordChromaResidualDebug(plane, true, -1, -1, -1)
				}
				fs.SetCoefCtxBlock(plane, bx+tbx, by+tby, tw, th, 0x40)
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
	haveTop, haveLeft bool,
) (dispatchMode int, packedAngle int) {
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
				idx := m.SymbolAdaptDav1d(ctx.TxTypeInter2CDF[:], TxTypeInter2Symbols-1)
				if int(idx) < len(TxTypeInter2Set) {
					return clampTxType(TxTypeInter2Set[idx], td.Lw, td.Lh)
				}
				return transform.DCT_DCT
			default:
				idx := m.SymbolAdaptDav1d(ctx.TxTypeInter1CDF[clampInt(int(td.Min), 0, 1)][:], TxTypeInter1Symbols-1)
				if int(idx) < len(TxTypeInter1Set) {
					return clampTxType(TxTypeInter1Set[idx], td.Lw, td.Lh)
				}
				return transform.DCT_DCT
			}
		}
		yModeCtx := clampInt(yMode, 0, NIntraPredModes-1)
		if reducedTxtpSet || td.Min >= 2 {
			txClassCtx := clampInt(int(td.Min), 0, 2)
			idx := m.SymbolAdaptDav1d(ctx.TxTypeIntra2CDF[txClassCtx][yModeCtx][:], TxTypeIntra2Symbols-1)
			if int(idx) < len(TxTypeIntra2Set) {
				return clampTxType(TxTypeIntra2Set[idx], td.Lw, td.Lh)
			}
			return transform.DCT_DCT
		}
		txClassCtx := clampInt(int(td.Min), 0, 1)
		idx := m.SymbolAdaptDav1d(ctx.TxTypeIntra1CDF[txClassCtx][yModeCtx][:], TxTypeIntra1Symbols-1)
		if int(idx) < len(TxTypeIntra1Set) {
			return clampTxType(TxTypeIntra1Set[idx], td.Lw, td.Lh)
		}
		return transform.DCT_DCT
	}
}

func decodeCoeffEOB(m *bitstream.MSAC, ctx *TileCtx, td transform.TxfmDim, chroma int, is1d uint8, n int,
	fs *FrameState, bx, by, plane int,
) (int, int, int, int) {
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
		eob = int(m.SymbolAdaptDav1d(ctx.EobBin16Full[chroma][is1d][:], 4))
	case 1:
		eob = int(m.SymbolAdaptDav1d(ctx.EobBin32Full[chroma][is1d][:], 5))
	case 2:
		eob = int(m.SymbolAdaptDav1d(ctx.EobBin64Full[chroma][is1d][:], 6))
	case 3:
		eob = int(m.SymbolAdaptDav1d(ctx.EobBin128Full[chroma][is1d][:], 7))
	case 4:
		eob = int(m.SymbolAdaptDav1d(ctx.EobBin256Full[chroma][is1d][:], 8))
	case 5:
		eob = int(m.SymbolAdaptDav1d(ctx.EobBin512Full[chroma][:], 9))
	default:
		eob = int(m.SymbolAdaptDav1d(ctx.EobBin1024Full[chroma][:], 10))
	}
	fs.tracef("sym coeff_eob x=%d y=%d plane=%d kind=bin val=%d rng=%d", bx, by, plane, eob, m.State().Range)
	if eob > 1 {
		eb := eob - 2
		if eb < 0 {
			eb = 0
		} else if eb > 8 {
			eb = 8
		}
		eobHiBit := int(m.BoolAdapt(ctx.EobHiBitFull[td.Ctx][chroma][eb][:]))
		fs.tracef("sym coeff_eob x=%d y=%d plane=%d kind=hi eb=%d val=%d rng=%d", bx, by, plane, eb, eobHiBit, m.State().Range)
		extra := uint32(0)
		for k := 0; k < eb; k++ {
			extra = (extra << 1) | m.BoolEqui()
			fs.tracef("sym coeff_eob x=%d y=%d plane=%d kind=extra bit=%d val=%d rng=%d", bx, by, plane, k, extra&1, m.State().Range)
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

// coeffTraversalPoint maps dav1d's coefficient traversal position to both
// the padded levels buffer and Go's packed column-major coefficient buffer.
func coeffTraversalPoint(geom coeffTokenGeom, pos int, scan []uint16) (x, y, levelIdx, coeffIdx int, ok bool) {
	if pos < 0 {
		return 0, 0, 0, 0, false
	}
	if geom.cls == TxClass2D {
		if pos >= len(scan) {
			return 0, 0, 0, 0, false
		}
		levelIdx = int(scan[pos])
		x = levelIdx >> geom.shift
		y = levelIdx & geom.mask
	} else {
		x = pos & geom.mask
		y = pos >> geom.shift
		levelIdx = x*geom.stride + y
	}
	coeffIdx = packedCoeffIndexForClass(geom.cls, geom.blockW, geom.blockH, geom.packedH, x, y)
	return x, y, levelIdx, coeffIdx, coeffIdx >= 0
}

func residualMagFromTok(m *bitstream.MSAC, tok int) int {
	if tok < 15 {
		return tok
	}
	return (int(readGolomb(m)) + 15) & 0xfffff
}

func decodeCoeffTokens(m *bitstream.MSAC, ctx *TileCtx, td transform.TxfmDim, chroma int,
	geom coeffTokenGeom, eob int, levels []uint8, fs *FrameState, bx, by, plane int,
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
		x, y, levelIdx, rc, ok := coeffTraversalPoint(geom, eob, scan)
		if !ok {
			return tokState, 0
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
		eobTok := int(m.SymbolAdaptDav1d(eobCdf[bctx][:], 2))
		tok := eobTok + 1
		fs.tracef("sym coeff_token x=%d y=%d plane=%d kind=eob pos=%d rc=%d ctx=%d tok=%d rng=%d",
			bx, by, plane, eob, rc, bctx, tok, m.State().Range)
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
			fs.tracef("sym coeff_token x=%d y=%d plane=%d kind=eob_hi pos=%d rc=%d ctx=%d tok=%d rng=%d",
				bx, by, plane, eob, rc, hctx, tok, m.State().Range)
			levelTok = tok + (3 << 6)
		}
		setCoeffToken(rc, tok, 0)
		if levelIdx >= 0 && levelIdx < len(levels) {
			levels[levelIdx] = uint8(levelTok)
		}

		lastRC := rc
		for i := eob - 1; i > 0; i-- {
			xi, yi, lvlIdx, rci, ok := coeffTraversalPoint(geom, i, scan)
			if !ok {
				continue
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
			toki := int(m.SymbolAdaptDav1d(loCdf[loCtx][:], 3))
			fs.tracef("sym coeff_token x=%d y=%d plane=%d kind=lo pos=%d rc=%d ctx=%d tok=%d rng=%d",
				bx, by, plane, i, rci, loCtx, toki, m.State().Range)
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
				fs.tracef("sym coeff_token x=%d y=%d plane=%d kind=hi pos=%d rc=%d ctx=%d tok=%d rng=%d",
					bx, by, plane, i, rci, hctx, toki, m.State().Range)
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
		tokBr := int(m.SymbolAdaptDav1d(eobCdf[0][:], 2))
		dcTok = tokBr + 1
		fs.tracef("sym coeff_token x=%d y=%d plane=%d kind=dc_eob ctx=0 tok=%d rng=%d",
			bx, by, plane, dcTok, m.State().Range)
		if tokBr == 2 {
			dcTok = int(m.HiTok(hiCdf[0][:]))
		}
	} else {
		dcMag := 0
		if cls == TxClass2D {
			dcTok = int(m.SymbolAdaptDav1d(loCdf[0][:], 3))
		} else {
			dcCtx, hiMag := getLoCtx1D(levels, 0, geom.stride, 0)
			dcMag = hiMag
			dcTok = int(m.SymbolAdaptDav1d(loCdf[dcCtx][:], 3))
		}
		fs.tracef("sym coeff_token x=%d y=%d plane=%d kind=dc_lo ctx=%d tok=%d rng=%d",
			bx, by, plane, dcMag, dcTok, m.State().Range)
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
	tx uint8, plane, bx, by, spanW, spanH int, chroma int, tokState coeffTokenState, dcTok int, dq [2]uint16,
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
		fs.tracef("sym coeff_sign x=%d y=%d plane=%d rc=%d sign=%d rng=%d",
			bx, by, plane, idx, sign, m.State().Range)
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
	bx, by, bw, bh, spanW, spanH int, yMode int, intra bool, interYTxtp uint8, reducedTxtpSet bool,
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
	if plane > 0 && chromaDebugEnabled {
		recordChromaResidualCtx(plane, skipCtx, int(td.Ctx))
	}
	if int(td.Ctx) < len(ctx.CoefSkipFull) {
		allSkip := m.BoolAdapt(ctx.CoefSkipFull[td.Ctx][skipCtx][:])
		fs.tracef("sym coeff_stage x=%d y=%d plane=%d kind=nonzero skip_ctx=%d all_skip=%d rng=%d", bx, by, plane, skipCtx, allSkip, m.State().Range)
		if allSkip != 0 {
			txtp := uint8(transform.DCT_DCT)
			if lossless {
				txtp = transform.WHT_WHT
			}
			return nil, -1, txtp, 0x40
		}
	}

	// --- Transform type ---------------------------------------------------
	if !intra && chroma == 0 && !reducedTxtpSet && td.Max != 3 && td.Min != 2 && !qidxIsZero && !lossless {
		fs.tracef("sym txtp_inter1_cdf x=%d y=%d plane=%d min=%d cdf=%v", bx, by, plane,
			clampInt(int(td.Min), 0, 1), ctx.TxTypeInter1CDF[clampInt(int(td.Min), 0, 1)])
	}
	txtp := decodeCoeffTransformType(m, ctx, td, chroma, yMode, intra, interYTxtp, reducedTxtpSet, qidxIsZero, lossless)
	fs.tracef("sym coeff_stage x=%d y=%d plane=%d kind=txtp val=%d rng=%d", bx, by, plane, txtp, m.State().Range)

	cls := DAV1DTxTypeClass[txtp]
	is1d := uint8(0)
	if cls != TxClass2D {
		is1d = 1
	}

	// --- EOB --------------------------------------------------------------
	eob, slw, slh, tx2dszctx := decodeCoeffEOB(m, ctx, td, chroma, is1d, n, fs, bx, by, plane)
	ms := m.State()
	fs.tracef("sym coeff x=%d y=%d plane=%d tx=%d txtp=%d skip_ctx=%d eob=%d rng=%d cnt=%d off=%d",
		bx, by, plane, tx, txtp, skipCtx, eob, ms.Range, ms.Count, ms.BufferPosition)

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
	tokState, dcTok := decodeCoeffTokens(m, ctx, td, chroma, geom, eob, levels, fs, bx, by, plane)
	ms = m.State()
	fs.tracef("sym coeff_tokens x=%d y=%d plane=%d dc_tok=%d ac_head=%d rng=%d cnt=%d off=%d",
		bx, by, plane, dcTok, tokState.acHead, ms.Range, ms.Count, ms.BufferPosition)
	if plane > 0 && chromaDebugEnabled {
		recordChromaResidualTokenDebug(plane, int(txtp), eob, dcTok)
	}

	resCtx := decodeCoeffSignsAndResiduals(m, ctx, fs, tx, plane, bx, by, spanW, spanH, chroma, tokState, dcTok, dq)
	ms = m.State()
	fs.tracef("sym coeff_done x=%d y=%d plane=%d res_ctx=%d coeff0=%d dq=%v rng=%d cnt=%d off=%d",
		bx, by, plane, resCtx, tokState.coeff[0], dq, ms.Range, ms.Count, ms.BufferPosition)

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
	syntax := decodeSingleRefInterSyntax(m, ctx, fs, fhdr, fb, st.segID, st.skip, bx, by, bw, bh)
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
	refFrame     int
	refOrder     int
	hasRef       bool
	drlIdx       int
	bw           int
	bh           int
	isCompound   bool
	compMode     int
	refSlot2     int
	refFrame2    int
	refOrder2    int
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

func singleRefInterCandidates(fs *FrameState, fhdr *header.FrameHeader, fb *FrameBuf, refSlot, refFrame, bx, by, bw, bh int) (int, [8]interCandidate) {
	var stack [8]interCandidate
	if fs == nil || fhdr == nil || fs.MVFrame == nil {
		return 0, stack
	}
	if fb != nil && (refSlot < 0 || refSlot >= len(fb.Refs) || fb.Refs[refSlot] == nil) {
		return 0, stack
	}
	found, ok := singleRefSearch(fs, fhdr, fb, refSlot, refFrame, bx, by, bw, bh)
	if !ok {
		return 0, stack
	}
	if refFrame <= 0 {
		refFrame, _ = slotRefFrame(fhdr, refSlot)
	}
	for i := 0; i < found.Count; i++ {
		stack[i] = interCandidate{mv: found.Candidates[i].MV[0], refSlot: refSlot, refFrame: refFrame, weight: found.Candidates[i].Weight}
	}
	return found.Count, stack
}

func fbRefMV(fb *FrameBuf, refSlot int) *refmvs.Frame {
	if fb == nil || refSlot < 0 || refSlot >= len(fb.RefMVs) {
		return nil
	}
	return fb.RefMVs[refSlot]
}

var refMVBlockDims = [NBlockSizes][2]uint8{
	{32, 32}, {32, 16}, {16, 32}, {16, 16}, {16, 8}, {16, 4},
	{8, 16}, {8, 8}, {8, 4}, {8, 2}, {4, 16}, {4, 8}, {4, 4},
	{4, 2}, {4, 1}, {2, 8}, {2, 4}, {2, 2}, {2, 1}, {1, 4},
	{1, 2}, {1, 1},
}

func singleRefInterState(fs *FrameState, fb *FrameBuf, fhdr *header.FrameHeader, segID uint8, skip bool, bx, by int) interState {
	return singleRefInterStateWithHint(fs, fb, fhdr, segID, skip, bx, by, singleRefInterSyntax{modeHint: interModeHintAuto, motionSource: interMotionSourceAuto, refSlot: -1, drlIdx: -1, bw: 4, bh: 4})
}

func buildInterBlockState(segID uint8, skip bool, st interState) Av1Block {
	return Av1Block{
		Intra:     false,
		SegID:     segID,
		Skip:      skip,
		SkipMode:  st.skipMode,
		InterMode: uint8(st.interMode),
		RefSlot:   int8(st.refSlot),
		RefFrame:  int8(st.refFrame),
		RefOrder:  int8(st.refOrder),
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

func decodeSingleRefInterSyntax(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, fhdr *header.FrameHeader, fb *FrameBuf, segID uint8, skip bool, bx, by, bw, bh int) (syntax singleRefInterSyntax) {
	syntax = deriveSingleRefInterSyntax(fs, bx, by)
	syntax.bw, syntax.bh = bw, bh
	defer func() {
		if m == nil {
			return
		}
		ms := m.State()
		fs.tracef("sym inter x=%d y=%d ref=%d mode=%d source=%d drl=%d mv_y=%d mv_x=%d rng=%d cnt=%d off=%d",
			bx, by, syntax.refSlot, syntax.modeHint, syntax.motionSource, syntax.drlIdx,
			syntax.deltaMV.Y, syntax.deltaMV.X, ms.Range, ms.Count, ms.BufferPosition)
	}()
	if fhdr == nil || m == nil || ctx == nil {
		return syntax
	}
	if fhdr.Segmentation.Enabled != 0 && fhdr.Segmentation.SegData.D[segID].GlobalMV != 0 {
		syntax.motionSource = interMotionSourceGlobal
		syntax.modeHint = interModeHintAuto
		return syntax
	}
	if compoundFlagPresent(fhdr, segID, bw, bh) {
		compCtx := compoundFlagContext(fs, bx, by)
		before := ctx.CompCDF[compCtx]
		isCompound := m.BoolAdapt(ctx.CompCDF[compCtx][:]) != 0
		ms := m.State()
		fs.tracef("sym compflag x=%d y=%d ctx=%d val=%t cdf=%v->%v rng=%d cnt=%d off=%d",
			bx, by, compCtx, isCompound, before, ctx.CompCDF[compCtx], ms.Range, ms.Count, ms.BufferPosition)
		// Compound reference parsing/reconstruction is not wired yet. Consuming
		// the flag is still required for the normative single-reference branch.
		if isCompound {
			decodeCompoundInterSyntax(m, ctx, fs, fhdr, bx, by, &syntax)
			return syntax
		}
	}
	if fhdr.Segmentation.Enabled == 0 || fhdr.Segmentation.SegData.D[segID].Ref < 0 {
		if refSlot, refFrame, refOrder, ok := decodeSingleRefReferenceSlot(m, ctx, fs, fhdr, bx, by); ok {
			syntax.refSlot, syntax.refFrame, syntax.refOrder = refSlot, refFrame, refOrder
			syntax.hasRef = true
		}
	}
	ms := m.State()
	fs.tracef("sym inter_ref x=%d y=%d slot=%d has=%t rng=%d cnt=%d off=%d",
		bx, by, syntax.refSlot, syntax.hasRef, ms.Range, ms.Count, ms.BufferPosition)

	newMVCtx, globalMVCtx, refMVCtx := singleRefModeContexts(fs, fhdr, fb, syntax.refSlot, syntax.refFrame, bx, by, bw, bh)
	if os.Getenv("GOAV1_TRACE_MODE_TRIAL") != "" {
		for trialNewCtx := range ctx.NewMVModeCDF {
			trialMSAC := m.Clone()
			trialCtx := ctx.Clone()
			trialNew := trialMSAC.BoolAdapt(trialCtx.NewMVModeCDF[trialNewCtx][:])
			if trialNew != 0 {
				continue
			}
			for trialDRLCtx := range trialCtx.DRLBitCDF {
				drlMSAC := trialMSAC.Clone()
				drlCtx := trialCtx.Clone()
				drlVal := drlMSAC.BoolAdapt(drlCtx.DRLBitCDF[trialDRLCtx][:])
				fs.tracef("sym inter_mode_trial x=%d y=%d new_ctx=%d drl_ctx=%d drl_val=%d rng=%d",
					bx, by, trialNewCtx, trialDRLCtx, drlVal, drlMSAC.State().Range)
			}
		}
	}
	modeDone := func(mode int) {
		ms := m.State()
		packed := (refMVCtx << 4) | (globalMVCtx << 3) | newMVCtx
		fs.tracef("sym inter_mode_done x=%d y=%d mode=%d drl=%d ctx=%d rng=%d cnt=%d off=%d",
			bx, by, mode, syntax.drlIdx, packed, ms.Range, ms.Count, ms.BufferPosition)
	}
	newMVBefore := ctx.NewMVModeCDF[newMVCtx]
	newMV := m.BoolAdapt(ctx.NewMVModeCDF[newMVCtx][:])
	ms = m.State()
	fs.tracef("sym inter_newmv x=%d y=%d ctx=%d val=%d cdf=%v->%v rng=%d", bx, by, newMVCtx, newMV, newMVBefore, ctx.NewMVModeCDF[newMVCtx], ms.Range)
	if newMV != 0 {
		globalMV := m.BoolAdapt(ctx.GlobalMVModeCDF[globalMVCtx][:])
		ms = m.State()
		fs.tracef("sym inter_globalmv x=%d y=%d ctx=%d val=%d rng=%d", bx, by, globalMVCtx, globalMV, ms.Range)
		if globalMV == 0 {
			syntax.motionSource = interMotionSourceGlobal
			syntax.modeHint = interModeHintAuto
			modeDone(InterModeGlobalMV)
			return syntax
		}
		if os.Getenv("GOAV1_TRACE_REFMV_TRIAL") != "" {
			for trialCtx := range ctx.RefMVModeCDF {
				trialMSAC := m.Clone()
				trialTileCtx := ctx.Clone()
				trialVal := trialMSAC.BoolAdapt(trialTileCtx.RefMVModeCDF[trialCtx][:])
				fs.tracef("sym inter_refmv_trial x=%d y=%d ctx=%d val=%d rng=%d",
					bx, by, trialCtx, trialVal, trialMSAC.State().Range)
			}
		}
		refMV := m.BoolAdapt(ctx.RefMVModeCDF[refMVCtx][:])
		ms = m.State()
		fs.tracef("sym inter_refmv x=%d y=%d ctx=%d val=%d rng=%d", bx, by, refMVCtx, refMV, ms.Range)
		if refMV != 0 {
			syntax.motionSource = interMotionSourceCandidate
			syntax.modeHint = interModeHintNear
			syntax.drlIdx = decodeSingleRefDRLIndex(m, ctx, fs, fhdr, syntax.refSlot, syntax.refFrame, bx, by, bw, bh, 1)
			modeDone(InterModeNearMV)
			return syntax
		}
		syntax.motionSource = interMotionSourceCandidate
		syntax.modeHint = interModeHintNearest
		syntax.drlIdx = 0
		modeDone(InterModeNearestMV)
		return syntax
	}

	syntax.motionSource = interMotionSourceCandidate
	syntax.modeHint = interModeHintNew
	syntax.drlIdx = decodeSingleRefDRLIndex(m, ctx, fs, fhdr, syntax.refSlot, syntax.refFrame, bx, by, bw, bh, 0)
	modeDone(InterModeNewMV)
	syntax.deltaMV = readMVResidual(m, ctx, fhdr)
	return syntax
}

func decodeCompoundInterSyntax(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, fhdr *header.FrameHeader,
	bx, by int, syntax *singleRefInterSyntax) {
	if m == nil || ctx == nil || syntax == nil {
		return
	}
	ref0, ref1 := 0, 0
	dirCtx := compoundDirContext(fs, fhdr, bx, by)
	bidir := m.BoolAdapt(ctx.CompDirCDF[dirCtx][:]) != 0
	if bidir {
		if m.BoolAdapt(ctx.CompFwdRefCDF[0][ref3Ctx(fs, fhdr, bx, by)][:]) != 0 {
			ref0 = 2 + int(m.BoolAdapt(ctx.CompFwdRefCDF[2][ref5Ctx(fs, fhdr, bx, by)][:]))
		} else {
			ref0 = int(m.BoolAdapt(ctx.CompFwdRefCDF[1][ref4Ctx(fs, fhdr, bx, by)][:]))
		}
		if m.BoolAdapt(ctx.CompBwdRefCDF[0][ref2Ctx(fs, fhdr, bx, by)][:]) != 0 {
			ref1 = 6
		} else {
			ref1 = 4 + int(m.BoolAdapt(ctx.CompBwdRefCDF[1][ref6Ctx(fs, fhdr, bx, by)][:]))
		}
	} else {
		if m.BoolAdapt(ctx.CompUniRefCDF[0][refCtx(fs, fhdr, bx, by)][:]) != 0 {
			ref0, ref1 = 4, 6
		} else {
			ref0 = 0
			ref1 = 1 + int(m.BoolAdapt(ctx.CompUniRefCDF[1][uniP1Context(fs, fhdr, bx, by)][:]))
			if ref1 == 2 {
				ref1 += int(m.BoolAdapt(ctx.CompUniRefCDF[2][ref5Ctx(fs, fhdr, bx, by)][:]))
			}
		}
	}
	syntax.isCompound = true
	syntax.refOrder, syntax.refOrder2 = ref0, ref1
	syntax.refFrame, syntax.refFrame2 = ref0+1, ref1+1
	syntax.refSlot, _ = frameRefSlot(fhdr, syntax.refFrame)
	syntax.refSlot2, _ = frameRefSlot(fhdr, syntax.refFrame2)
	syntax.hasRef = syntax.refSlot >= 0 && syntax.refSlot2 >= 0

	// Compound MV-stack context support is added with the paired stack. Context
	// zero is the normative no-pair-match context and is used by the WebRTC
	// target block (dav1d reports ctx=0, n_mvs=2).
	modeCtx := 0
	syntax.compMode = int(m.SymbolAdaptDav1d(ctx.CompInterModeCDF[modeCtx][:], 7))
	if syntax.compMode == 6 { // GLOBALMV_GLOBALMV
		syntax.motionSource = interMotionSourceGlobal
		syntax.modeHint = interModeHintAuto
	}
	ms := m.State()
	fs.tracef("sym compound x=%d y=%d dir_ctx=%d bidir=%t refs=%d/%d slots=%d/%d mode_ctx=%d mode=%d rng=%d cnt=%d off=%d",
		bx, by, dirCtx, bidir, ref0, ref1, syntax.refSlot, syntax.refSlot2,
		modeCtx, syntax.compMode, ms.Range, ms.Count, ms.BufferPosition)
}

func compoundDirContext(fs *FrameState, fhdr *header.FrameHeader, bx, by int) int {
	top, topOK := neighbourSingleRefFrame(fs, fhdr, bx, by, true)
	left, leftOK := neighbourSingleRefFrame(fs, fhdr, bx, by, false)
	if topOK && leftOK {
		return 1 + 2*btoi((top >= 4) == (left >= 4))
	}
	return 2
}

func uniP1Context(fs *FrameState, fhdr *header.FrameHeader, bx, by int) int {
	cnt := [3]int{}
	for _, top := range []bool{true, false} {
		if ref, ok := neighbourSingleRefFrame(fs, fhdr, bx, by, top); ok && ref >= 1 && ref <= 3 {
			cnt[ref-1]++
		}
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

func compoundFlagPresent(fhdr *header.FrameHeader, segID uint8, bw, bh int) bool {
	if fhdr == nil || fhdr.SwitchableCompRefs == 0 || minInt(bw, bh) < 8 {
		return false
	}
	if fhdr.Segmentation.Enabled == 0 {
		return true
	}
	seg := fhdr.Segmentation.SegData.D[segID]
	return seg.Ref < 0 && seg.GlobalMV == 0 && seg.Skip == 0
}

func compoundFlagContext(fs *FrameState, bx, by int) int {
	if fs == nil {
		return 1
	}
	top, haveTop := fs.BlockState(bx, by-4)
	left, haveLeft := fs.BlockState(bx-4, by)
	haveTop = haveTop && by > fs.TileY0
	haveLeft = haveLeft && bx > fs.TileX0
	isCompound := func(blk Av1Block) bool { return !blk.Intra && blk.Compound }
	col4, row4 := bx>>2, by>>2
	topBackward := haveTop && col4 >= 0 && col4 < len(fs.AboveRef) && fs.AboveRef[col4] >= 4
	leftBackward := haveLeft && row4 >= 0 && row4 < len(fs.LeftRef) && fs.LeftRef[row4] >= 4
	if haveTop && haveLeft {
		if isCompound(top) {
			if isCompound(left) {
				return 4
			}
			return 2 + btoi(leftBackward)
		}
		if isCompound(left) {
			return 2 + btoi(topBackward)
		}
		return btoi(leftBackward) ^ btoi(topBackward)
	}
	if haveTop {
		if isCompound(top) {
			return 3
		}
		return btoi(topBackward)
	}
	if haveLeft {
		if isCompound(left) {
			return 3
		}
		return btoi(leftBackward)
	}
	return 1
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
		syntax.refFrame = int(blk.RefFrame)
		syntax.refOrder = int(blk.RefOrder)
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

func singleRefModeContexts(fs *FrameState, fhdr *header.FrameHeader, fb *FrameBuf, refSlot, refFrame, bx, by, bw, bh int) (newMVCtx, globalMVCtx, refMVCtx int) {
	result, ok := singleRefSearch(fs, fhdr, fb, refSlot, refFrame, bx, by, bw, bh)
	if !ok {
		return 0, 0, 0
	}
	nearestMatch := boolInt(result.RowMatch) + boolInt(result.ColMatch)
	secondaryMatches := boolInt(result.SecondaryRowMatch) + boolInt(result.SecondaryColMatch)
	refMatchCount := maxInt(nearestMatch, secondaryMatches)
	haveNewMV := boolInt(result.HaveNewMV)
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
	globalMVCtx = result.GlobalMVContext
	return
}

func singleRefSearch(fs *FrameState, fhdr *header.FrameHeader, fb *FrameBuf, refSlot, refFrame, bx, by, bw, bh int) (refmvs.SearchResult, bool) {
	if fs == nil || fs.MVFrame == nil || fhdr == nil || bw <= 0 || bh <= 0 {
		return refmvs.SearchResult{}, false
	}
	if refFrame <= 0 {
		var ok bool
		refFrame, ok = slotRefFrame(fhdr, refSlot)
		if !ok {
			return refmvs.SearchResult{}, false
		}
	}
	if fs.Tracef != nil {
		for _, probe := range []struct {
			name string
			x4   int
			y4   int
		}{{"top", bx >> 2, (by >> 2) - 1}, {"left", (bx >> 2) - 1, by >> 2}, {"top-right", (bx + bw) >> 2, (by >> 2) - 1}} {
			if blk, ok := fs.MVFrame.GridBlock(probe.x4, probe.y4); ok {
				fs.tracef("sym refmv_probe x=%d y=%d side=%s target_ref=%d block_refs=%d,%d mv_y=%d mv_x=%d bs=%d mf=%d",
					bx, by, probe.name, refFrame, blk.Ref[0], blk.Ref[1], blk.MV[0].Y, blk.MV[0].X, blk.BS, blk.MF)
			}
		}
		for _, probe := range []struct {
			name string
			x4   int
			y4   int
		}{{"top-left", (bx >> 2) - 1, (by >> 2) - 1},
			{"row-n2", (bx >> 2) | 1, (((by >> 2) - 3) | 1)},
			{"col-n2", (((bx >> 2) - 3) | 1), (by >> 2) | 1},
			{"row-n3", (bx >> 2) | 1, (((by >> 2) - 5) | 1)},
			{"col-n3", (((bx >> 2) - 5) | 1), (by >> 2) | 1}} {
			if blk, ok := fs.MVFrame.GridBlock(probe.x4, probe.y4); ok {
				fs.tracef("sym refmv_probe x=%d y=%d side=%s target_ref=%d block_refs=%d,%d mv_y=%d mv_x=%d bs=%d mf=%d",
					bx, by, probe.name, refFrame, blk.Ref[0], blk.Ref[1], blk.MV[0].Y, blk.MV[0].X, blk.BS, blk.MF)
			}
		}
	}
	return refmvs.Find(refmvs.SearchConfig{
		Frame: fs.MVFrame, TemporalSource: fbRefMV(fb, refSlot), Ref: int8(refFrame), TargetSlot: refSlot,
		GlobalMV: fallbackGlobalMV(fhdr, refSlot),
		Bx4:      bx >> 2, By4: by >> 2, Bw4: (bw + 3) >> 2, Bh4: (bh + 3) >> 2,
		TileX0: fs.TileX0 >> 2, TileY0: fs.TileY0 >> 2,
		TileX1: fs.TileX1 >> 2, TileY1: fs.TileY1 >> 2, BlockDims: refMVBlockDims[:],
	}), true
}

func neighbourSingleRefFrame(fs *FrameState, fhdr *header.FrameHeader, bx, by int, top bool) (int, bool) {
	if fs == nil || fhdr == nil {
		return 0, false
	}
	var blk Av1Block
	var ok bool
	if top {
		col4 := bx >> 2
		if by <= fs.TileY0 || col4 < 0 || col4 >= len(fs.AbovePresent) || fs.AbovePresent[col4] == 0 {
			return 0, false
		}
		blk, ok = fs.BlockState(bx, by-4)
	} else {
		row4 := by >> 2
		if bx <= fs.TileX0 || row4 < 0 || row4 >= len(fs.LeftPresent) || fs.LeftPresent[row4] == 0 {
			return 0, false
		}
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
	if ref, ok := neighbourSingleRefFrame(fs, fhdr, bx, by, true); ok {
		cnt[boolInt(ref >= 4)]++
	}
	if ref, ok := neighbourSingleRefFrame(fs, fhdr, bx, by, false); ok {
		cnt[boolInt(ref >= 4)]++
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

func decodeSingleRefReferenceSlot(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, fhdr *header.FrameHeader, bx, by int) (int, int, int, bool) {
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
	return refSlot, refFrame + 1, refFrame, ok
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

func singleRefHasSubpelFilter(fhdr *header.FrameHeader, st interState, bw, bh int) bool {
	if st.skipMode {
		return false
	}
	if st.interMode != InterModeGlobalMV {
		return true
	}
	if minInt((bw+3)>>2, (bh+3)>>2) == 1 {
		return true
	}
	return fhdr != nil && st.refOrder >= 0 && st.refOrder < len(fhdr.GMV) &&
		fhdr.GMV[st.refOrder].Type == header.WMTypeTranslation
}

func decodeSingleRefFilterMode(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, fhdr *header.FrameHeader, seq *header.SequenceHeader, st interState, bx, by, bw, bh int) (header.FilterMode, header.FilterMode) {
	if fhdr == nil {
		return header.FilterMode8TapRegular, header.FilterMode8TapRegular
	}
	if fhdr.SubpelFilterMode != header.FilterModeSwitchable {
		return fhdr.SubpelFilterMode, fhdr.SubpelFilterMode
	}
	if m == nil || ctx == nil || st.refSlot < 0 {
		return header.FilterMode8TapRegular, header.FilterMode8TapRegular
	}
	if !singleRefHasSubpelFilter(fhdr, st, bw, bh) {
		return header.FilterMode8TapRegular, header.FilterMode8TapRegular
	}
	ctx1 := getInterFilterCtx(fs, 0, st.refSlot, bx, by)
	before0 := ctx.FilterCDF[0][ctx1]
	f0 := header.FilterMode(m.SymbolAdaptDav1d(ctx.FilterCDF[0][ctx1][:], int(header.NumSwitchableFilters)-1))
	f1 := f0
	ms := m.State()
	fs.tracef("sym inter_filter_dir x=%d y=%d dir=0 ctx=%d val=%d cdf=%v->%v rng=%d cnt=%d off=%d",
		bx, by, ctx1, f0, before0, ctx.FilterCDF[0][ctx1], ms.Range, ms.Count, ms.BufferPosition)
	if seq != nil && seq.DualFilter {
		ctx2 := getInterFilterCtx(fs, 1, st.refSlot, bx, by)
		before1 := ctx.FilterCDF[1][ctx2]
		f1 = header.FilterMode(m.SymbolAdaptDav1d(ctx.FilterCDF[1][ctx2][:], int(header.NumSwitchableFilters)-1))
		ms = m.State()
		fs.tracef("sym inter_filter_dir x=%d y=%d dir=1 ctx=%d val=%d cdf=%v->%v rng=%d cnt=%d off=%d",
			bx, by, ctx2, f1, before1, ctx.FilterCDF[1][ctx2], ms.Range, ms.Count, ms.BufferPosition)
	}
	return f0, f1
}

func candidateWeights(stack [8]interCandidate, cnt int) []int {
	weights := make([]int, clampInt(cnt, 0, len(stack)))
	for i := range weights {
		weights[i] = stack[i].weight
	}
	return weights
}

func decodeSingleRefDRLIndex(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, fhdr *header.FrameHeader, refSlot, refFrame, bx, by, bw, bh, base int) int {
	cnt, stack := singleRefInterCandidates(fs, fhdr, nil, refSlot, refFrame, bx, by, bw, bh)
	if cnt <= 1 {
		// NEAR starts at DRL index 1 even when the explicit stack has only one
		// entry; dav1d fills the missing slot with the reference global MV.
		return base
	}
	drlIdx := base
	weights := candidateWeights(stack, cnt)
	readDRL := func(refIdx int) int {
		drlCtx := refmvs.DRLContext(weights, refIdx)
		if os.Getenv("GOAV1_TRACE_DRL_TRIAL") != "" {
			for trialCtx := range ctx.DRLBitCDF {
				trialMSAC := m.Clone()
				trialTileCtx := ctx.Clone()
				trialVal := trialMSAC.BoolAdapt(trialTileCtx.DRLBitCDF[trialCtx][:])
				fs.tracef("sym drl_trial x=%d y=%d ref_idx=%d ctx=%d val=%d rng=%d",
					bx, by, refIdx, trialCtx, trialVal, trialMSAC.State().Range)
			}
		}
		before := ctx.DRLBitCDF[drlCtx]
		v := int(m.BoolAdapt(ctx.DRLBitCDF[drlCtx][:]))
		ms := m.State()
		fs.tracef("sym drl x=%d y=%d ref_idx=%d ctx=%d val=%d weights=%v cdf=%v->%v rng=%d",
			bx, by, refIdx, drlCtx, v, weights, before, ctx.DRLBitCDF[drlCtx], ms.Range)
		return v
	}
	if base == 0 {
		drlIdx += readDRL(0)
		if drlIdx == 1 && cnt > 2 {
			drlIdx += readDRL(1)
		}
		return clampInt(drlIdx, 0, cnt-1)
	}
	if cnt > 2 {
		drlIdx += readDRL(1)
		if drlIdx == 2 && cnt > 3 {
			drlIdx += readDRL(2)
		}
	}
	return clampInt(drlIdx, 0, cnt-1)
}

func readMVComponentDiff(m *bitstream.MSAC, ctx *TileCtx, comp, mvPrec int) int16 {
	sign := m.BoolAdapt(ctx.MVSignCDF[comp][:])
	cl := int(m.SymbolAdaptDav1d(ctx.MVClassesCDF[comp][:], 10))
	up := 0
	fp := 3
	hp := 1

	if cl == 0 {
		up = int(m.BoolAdapt(ctx.MVClass0CDF[comp][:]))
		if mvPrec >= 0 {
			fp = int(m.SymbolAdaptDav1d(ctx.MVClass0FPCDF[comp][up][:], 3))
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
			fp = int(m.SymbolAdaptDav1d(ctx.MVClassNFPCDF[comp][:], 3))
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
	joint := int(m.SymbolAdaptDav1d(ctx.MVJointCDF[:], 3))
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
	if syntax.modeHint == interModeHintNew {
		// An empty spatial/temporal stack still has the spec-defined global MV
		// fallback (zero for identity). NEWMV residuals must not be discarded
		// merely because our explicit candidate list is empty.
		st.interMode = InterModeNewMV
		st.mv = composeNewMV(st.baseMV, st.deltaMV)
	}
	return st
}

func decodeSingleRefInterBlockWithSyntax(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState, fhdr *header.FrameHeader, seq *header.SequenceHeader, fb *FrameBuf,
	blkSt blockSyntaxState, bx, by, bw, bh int, syntax singleRefInterSyntax) interState {
	ctxBW := blkSt.ctxBW
	ctxBH := blkSt.ctxBH
	if ctxBW <= 0 {
		ctxBW = bw
	}
	if ctxBH <= 0 {
		ctxBH = bh
	}
	st := singleRefInterStateFromSyntax(fs, fb, fhdr, blkSt.segID, blkSt.skip, bx, by, syntax)
	if m != nil {
		ms := m.State()
		fs.tracef("sym inter_motion x=%d y=%d mode=%d base_y=%d base_x=%d delta_y=%d delta_x=%d mv_y=%d mv_x=%d candidates=%d rng=%d",
			bx, by, st.interMode, st.baseMV.Y, st.baseMV.X, st.deltaMV.Y, st.deltaMV.X,
			st.mv.Y, st.mv.X, st.candCnt, ms.Range)
		cnt, stack := singleRefInterCandidates(fs, fhdr, fb, st.refSlot, st.refFrame, bx, by, bw, bh)
		for i := 0; i < cnt; i++ {
			fs.tracef("sym inter_candidate x=%d y=%d idx=%d ref=%d mv_y=%d mv_x=%d weight=%d",
				bx, by, i, stack[i].refSlot, stack[i].mv.Y, stack[i].mv.X, stack[i].weight)
		}
	}
	st.filterMode, st.filterModeV = decodeSingleRefFilterMode(m, ctx, fs, fhdr, seq, st, bx, by, bw, bh)
	if m != nil {
		ms := m.State()
		fs.tracef("sym inter_filter x=%d y=%d h=%d v=%d rng=%d cnt=%d off=%d",
			bx, by, st.filterMode, st.filterModeV, ms.Range, ms.Count, ms.BufferPosition)
	}
	traceInterPrediction(fs, fb, st, bx, by, bw, bh)
	txSt := decodeInterTransformState(m, ctx, fs, fhdr, seq, bx, by, bw, bh, blkSt)
	if m != nil {
		ms := m.State()
		fs.tracef("sym inter_tx x=%d y=%d tx=%d split0=%d split1=%d rng=%d cnt=%d off=%d",
			bx, by, txSt.block.Tx, txSt.block.TxSplit0, txSt.block.TxSplit1,
			ms.Range, ms.Count, ms.BufferPosition)
	}
	blk := buildInterBlockStateForRect(blkSt.segID, blkSt.skip, ctxBW, ctxBH, st)
	blk.Tx = txSt.block.Tx
	blk.MaxYTx = txSt.block.MaxYTx
	blk.Uvtx = txSt.block.Uvtx
	blk.TxSplit0 = txSt.block.TxSplit0
	blk.TxSplit1 = txSt.block.TxSplit1
	blk.LFDelta = blkSt.lfDelta
	predicted := false
	if syntax.isCompound && syntax.compMode == 6 {
		predicted = applyGlobalCompoundState(fb, fhdr, seq, bx, by, bw, bh, st, syntax)
	}
	if !predicted {
		predicted = applyInterState(fb, seq, bx, by, bw, bh, st)
	}
	if !predicted {
		if !copySelectedInterRefBlock(fb, seq, bx, by, bw, bh, st) && !copyInterRefBlock(fb, seq, bx, by, bw, bh) {
			fillDC128(fb, seq, bx, by, bw, bh)
		}
	}
	if syntax.isCompound {
		blk.Compound = true
		blk.RefSlot2 = int8(syntax.refSlot2)
		blk.RefFrame2 = int8(syntax.refFrame2)
		blk.RefOrder2 = int8(syntax.refOrder2)
		mv2 := fallbackGlobalMV(fhdr, syntax.refSlot2)
		blk.MV2 = [2]int16{mv2.Y, mv2.X}
	}
	if fs != nil && fs.Tracef != nil {
		fs.tracef("sym inter_prediction x=%d y=%d w=%d h=%d hash=%08x",
			bx, by, bw, bh, planeBlockHash(fb.Y, fb.StrideY, fb.Width, fb.Height, bx, by, bw, bh))
	}
	decodeInterResidual(m, ctx, fs, fhdr, seq, fb, blkSt, txSt, bx, by, bw, bh)
	commitInterTxState(fs, bx, by, ctxBW, ctxBH, txSt)
	if blkSt.hasChroma {
		fs.SetChromaBlockState(bx, by, ctxBW, ctxBH, blk)
	}
	fs.CommitInterBlock(bx, by, ctxBW, ctxBH, blk, st.refFrame)
	return st
}

func applyGlobalCompoundState(fb *FrameBuf, fhdr *header.FrameHeader, seq *header.SequenceHeader,
	bx, by, bw, bh int, first interState, syntax singleRefInterSyntax) bool {
	if fb == nil || fhdr == nil || first.ref == nil || syntax.refSlot2 < 0 || syntax.refSlot2 >= len(fb.Refs) {
		return false
	}
	second := fb.Refs[syntax.refSlot2]
	if second == nil {
		return false
	}
	mv2 := fallbackGlobalMV(fhdr, syntax.refSlot2)
	compoundPredictPlane(fb.Y, fb.StrideY, fb.Width, fb.Height,
		first.ref.Y, first.ref.StrideY, first.ref.Width, first.ref.Height,
		second.Y, second.StrideY, second.Width, second.Height,
		bx, by, bw, bh, first.mv, mv2, first.filterMode, first.filterModeV)
	if fb.Monochrome || first.ref.Monochrome || second.Monochrome || seq == nil {
		return true
	}
	ssHor, ssVer := int(seq.SsHor), int(seq.SsVer)
	cmv1 := refmvs.MV{X: int16(floorDivPow2(int(first.mv.X), ssHor)), Y: int16(floorDivPow2(int(first.mv.Y), ssVer))}
	cmv2 := refmvs.MV{X: int16(floorDivPow2(int(mv2.X), ssHor)), Y: int16(floorDivPow2(int(mv2.Y), ssVer))}
	cbx, cby, cbw, cbh := chromaRect(seq, bx, by, bw, bh)
	compoundPredictPlane(fb.U, fb.StrideUV, fb.ChromaW, fb.ChromaH,
		first.ref.U, first.ref.StrideUV, first.ref.ChromaW, first.ref.ChromaH,
		second.U, second.StrideUV, second.ChromaW, second.ChromaH,
		cbx, cby, cbw, cbh, cmv1, cmv2, first.filterMode, first.filterModeV)
	compoundPredictPlane(fb.V, fb.StrideUV, fb.ChromaW, fb.ChromaH,
		first.ref.V, first.ref.StrideUV, first.ref.ChromaW, first.ref.ChromaH,
		second.V, second.StrideUV, second.ChromaW, second.ChromaH,
		cbx, cby, cbw, cbh, cmv1, cmv2, first.filterMode, first.filterModeV)
	return true
}

func compoundPredictPlane(dst []byte, dstStride, dstW, dstH int,
	src1 []byte, stride1, width1, height1 int, src2 []byte, stride2, width2, height2 int,
	bx, by, bw, bh int, mv1, mv2 refmvs.MV, modeH, modeV header.FilterMode) {
	if len(dst) == 0 || len(src1) == 0 || len(src2) == 0 || bw <= 0 || bh <= 0 {
		return
	}
	bw = minInt(bw, dstW-bx)
	bh = minInt(bh, dstH-by)
	if bw <= 0 || bh <= 0 {
		return
	}
	prep := func(src []byte, stride, width, height int, mv refmvs.MV) []int16 {
		mv = refmvs.ClampMV(mv, bx>>2, by>>2, (bw+3)>>2, (bh+3)>>2, (width+3)>>2, (height+3)>>2)
		px, mx := splitMV8(int(mv.X))
		py, my := splitMV8(int(mv.Y))
		padStride, padH := bw+7, bh+7
		pad := make([]byte, padStride*padH)
		for y := 0; y < padH; y++ {
			sy := clampInt(by+py-3+y, 0, height-1)
			for x := 0; x < padStride; x++ {
				sx := clampInt(bx+px-3+x, 0, width-1)
				pad[y*padStride+x] = src[sy*stride+sx]
			}
		}
		tmp := make([]int16, bw*bh)
		base := 3*padStride + 3
		if interFilter2D(modeH, modeV) == predinter.Filter2DBilinear {
			predinter.PrepBilin(tmp, pad, base, padStride, bw, bh, mx, my)
		} else {
			predinter.Prep8Tap(tmp, pad, base, padStride, bw, bh, mx, my, interFilter2D(modeH, modeV))
		}
		return tmp
	}
	tmp1 := prep(src1, stride1, width1, height1, mv1)
	tmp2 := prep(src2, stride2, width2, height2, mv2)
	predinter.Avg(dst[by*dstStride+bx:], dstStride, tmp1, tmp2, bw, bh)
}

func traceInterPrediction(fs *FrameState, fb *FrameBuf, st interState, bx, by, bw, bh int) {
	if fs == nil || fs.Tracef == nil || fb == nil || st.ref == nil {
		return
	}
	refHint := -1
	if st.refSlot >= 0 && st.refSlot < len(fb.RefMVs) && fb.RefMVs[st.refSlot] != nil {
		refHint = fb.RefMVs[st.refSlot].OrderHint
	}
	fs.tracef("sym inter_predict x=%d y=%d w=%d h=%d slot=%d ref_frame=%d ref_hint=%d mv_y=%d mv_x=%d hfilter=%d vfilter=%d ref_hash=%08x",
		bx, by, bw, bh, st.refSlot, st.refFrame, refHint, st.mv.Y, st.mv.X,
		st.filterMode, st.filterModeV, planeRectHash(st.ref.Y, st.ref.StrideY, st.ref.Width, st.ref.Height, bx, by, bw, bh))
}

func planeRectHash(plane []byte, stride, width, height, x, y, w, h int) uint32 {
	const offset32 = uint32(2166136261)
	const prime32 = uint32(16777619)
	hash := offset32
	if len(plane) == 0 || stride <= 0 || width <= 0 || height <= 0 {
		return hash
	}
	for row := -3; row < h+4; row++ {
		sy := clampInt(y+row, 0, height-1)
		for col := -3; col < w+4; col++ {
			sx := clampInt(x+col, 0, width-1)
			hash ^= uint32(plane[sy*stride+sx])
			hash *= prime32
		}
	}
	return hash
}

func planeBlockHash(plane []byte, stride, width, height, x, y, w, h int) uint32 {
	const offset32 = uint32(2166136261)
	const prime32 = uint32(16777619)
	hash := offset32
	if len(plane) == 0 || stride <= 0 || width <= 0 || height <= 0 {
		return hash
	}
	for row := 0; row < h && y+row < height; row++ {
		for col := 0; col < w && x+col < width; col++ {
			hash ^= uint32(plane[(y+row)*stride+x+col])
			hash *= prime32
		}
	}
	return hash
}

func decodeSingleRefInterBlock(fs *FrameState, fhdr *header.FrameHeader, seq *header.SequenceHeader, fb *FrameBuf,
	segID uint8, skip bool, bx, by, bw, bh, modeHint int) interState {
	return decodeSingleRefInterBlockWithSyntax(nil, nil, fs, fhdr, seq, fb, blockSyntaxState{
		segID: segID,
		skip:  skip,
		ctxBW: bw,
		ctxBH: bh,
	}, bx, by, bw, bh, singleRefInterSyntax{
		modeHint:     modeHint,
		motionSource: interMotionSourceAuto,
		refSlot:      -1,
		drlIdx:       -1,
	})
}

func singleRefInterStateWithHint(fs *FrameState, fb *FrameBuf, fhdr *header.FrameHeader, segID uint8, skip bool, bx, by int, syntax singleRefInterSyntax) interState {
	if syntax.bw <= 0 {
		syntax.bw = 4
	}
	if syntax.bh <= 0 {
		syntax.bh = 4
	}
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
	if st.refFrame <= 0 {
		if rf, ok := slotRefFrame(fhdr, st.refSlot); ok {
			st.refFrame = rf
			st.refOrder = rf - 1
		}
	}
	if st.refSlot >= 0 && st.refSlot < len(fb.Refs) {
		st.ref = fb.Refs[st.refSlot]
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
	st.refFrame = 0
	updateInterRefState(st, fhdr, fb)
	if syntax.refFrame > 0 {
		st.refFrame, st.refOrder = syntax.refFrame, syntax.refOrder
	}
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
			st.refFrame = 0
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
	if st == nil || fs == nil || fb == nil || fhdr == nil || fhdr.UseRefFrameMVs == 0 || st.skipMode ||
		st.refSlot < 0 || st.refSlot >= len(fb.RefMVs) || fb.Refs[st.refSlot] == nil {
		return false
	}
	tMV, ok := refmvs.FindTemporal(fs.MVFrame, fb.RefMVs[st.refSlot], st.refSlot, bx>>2, by>>2)
	if !ok {
		return false
	}
	st.mv = tMV
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

func applyCandidateInterMotion(st *interState, fs *FrameState, fb *FrameBuf, fhdr *header.FrameHeader, bx, by, bw, bh, modeHint, drlIdx int) bool {
	if st == nil || fs == nil || fhdr == nil {
		return false
	}
	cnt, stack := singleRefInterCandidates(fs, fhdr, fb, st.refSlot, st.refFrame, bx, by, bw, bh)
	if cnt <= 0 {
		return false
	}
	st.candCnt = cnt
	mode, pick := selectInterCandidateMode(modeHint, cnt)
	if modeHint == interModeHintNear && drlIdx >= cnt {
		cand := stack[0]
		cand.mv = fallbackGlobalMV(fhdr, cand.refSlot)
		applyInterCandidate(st, fhdr, fb, cand, InterModeNearMV)
		return true
	}
	if drlIdx >= 0 && drlIdx < cnt && (modeHint == interModeHintNearest || drlIdx > 0) {
		pick = drlIdx
	}
	applyInterCandidate(st, fhdr, fb, stack[pick], mode)
	return true
}

func fallbackGlobalMV(fhdr *header.FrameHeader, refSlot int) refmvs.MV {
	if fhdr == nil {
		return refmvs.MV{}
	}
	for i, slot := range fhdr.Refidx {
		if int(slot) != refSlot || fhdr.GMV[i].Type != header.WMTypeTranslation {
			continue
		}
		shift := 13
		if fhdr.HP == 0 {
			shift = 14
		}
		return refmvs.MV{
			X: int16(fhdr.GMV[i].Matrix[0] >> shift),
			Y: int16(fhdr.GMV[i].Matrix[1] >> shift),
		}
	}
	return refmvs.MV{}
}

func applySkipModeMotion(st *interState, fs *FrameState, fb *FrameBuf, fhdr *header.FrameHeader, bx, by, bw, bh int) bool {
	if st == nil || fs == nil || fhdr == nil || !st.skipMode {
		return false
	}
	cnt, stack := singleRefInterCandidates(fs, fhdr, fb, st.refSlot, st.refFrame, bx, by, bw, bh)
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
		if applySkipModeMotion(st, fs, fb, fhdr, bx, by, syntax.bw, syntax.bh) {
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
		if applyCandidateInterMotion(st, fs, fb, fhdr, bx, by, syntax.bw, syntax.bh, syntax.modeHint, syntax.drlIdx) {
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
		if st.refOrder >= 0 && st.refOrder < len(fhdr.GMV) && fhdr.GMV[st.refOrder].Type == header.WMTypeIdentity {
			st.interMode = InterModeGlobalMV
			st.mv = refmvs.MV{}
			return
		}
	}
	if applyGlobalInterMV(st, fhdr, segID) {
		return
	}
	if applyTemporalInterMV(st, fs, fb, fhdr, bx, by) {
		return
	}
	if st.mv == (refmvs.MV{}) && applyCandidateInterMotion(st, fs, fb, fhdr, bx, by, syntax.bw, syntax.bh, syntax.modeHint, syntax.drlIdx) {
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
	case InterModeNearestMV:
		st.mv = cand.mv
		st.interMode = InterModeNearestMV
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
