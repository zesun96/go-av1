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
	"github.com/zesun96/go-av1/internal/predict/intra"
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

	// At the leaf level (8×8), always decode as a single block.
	if bl == BL8X8 {
		decodeBlock(m, ctx, fs, fhdr, seq, fb, bx, by, blSz, blSz)
		return
	}

	// Select partition CDF and symbol count based on block level.
	// AV1 spec: 128x128→8 syms, 64/32/16→10 syms, 8x8→4 syms.
	// Context: partCtx = (hasAbove<<1) | hasLeft from FrameState.
	partCtx := fs.PartCtx(bx, by)
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

	part := int(m.SymbolAdapt(partCDF, nPart))
	half := blSz / 2

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
		decodePartition(m, ctx, fs, fhdr, seq, fb, bx, by, bl+1)
		decodePartition(m, ctx, fs, fhdr, seq, fb, bx+half, by, bl+1)
		decodePartition(m, ctx, fs, fhdr, seq, fb, bx, by+half, bl+1)
		decodePartition(m, ctx, fs, fhdr, seq, fb, bx+half, by+half, bl+1)

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
	if fhdr.Segmentation.Enabled != 0 && fhdr.Segmentation.UpdateMap != 0 {
		pred := fs.SegIDFromNeighbours(bx, by)
		segCtx := 0
		haveAbove := by > 0
		haveLeft := bx > 0
		if haveAbove && haveLeft {
			segCtx = 2
		} else if haveAbove || haveLeft {
			segCtx = 1
		}
		// Decode segment id delta from CDF; map back to absolute id.
		delta := int(m.SymbolAdapt(ctx.SegIDCDF[segCtx][:], 7))
		abs := int(pred) + delta
		if abs < 0 {
			abs = 0
		}
		if abs >= int(header.MaxSegments) {
			abs = int(header.MaxSegments) - 1
		}
		segID = uint8(abs)
	}

	// --- Skip flag ---
	skipCtx := fs.SkipCtx(bx, by)
	skip := m.SymbolAdapt(ctx.SkipCDF[skipCtx][:], 2) != 0

	// --- Intra vs Inter ---
	isIntra := fhdr.FrameType.IsIntra()
	// For inter frames, treat every block as intra DC128 (M7 stub).

	if !isIntra {
		// Inter frame block: fill with DC128 and return.
		fs.SetBlockSeg(bx, by, bw, bh, skip, DCPred, segID)
		fillDC128(fb, bx, by, bw, bh)
		return
	}

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

	// --- Intra UV mode ---
	// CFL is gated off for now: our CFL alpha decode does not match the
	// dav1d MSAC contexts, so enabling CFL here produces large chroma
	// excursions (green/magenta block artefacts). Until the alpha CDFs are
	// wired up, force cflAllowed=0 so uvMode never resolves to CFL_PRED.
	cflAllowed := 0
	uvMode := int(m.SymbolAdapt(ctx.UVModeCDF[cflAllowed][yMode][:], NUVIntraModes))

	// --- Transform size selection (M7: use largest fitting square tx) ---
	txY := largestTx(bw, bh)
	txUV := largestTx((bw+1)/2, (bh+1)/2)

	// --- Dequant values from frame header ---
	qidx := int(fhdr.Segmentation.QIdx[segID])
	qidxIsZero := qidx == 0
	lossless := fhdr.Segmentation.Lossless[segID] != 0
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
	decodeIntraPlane(m, ctx, fb, 0, bx, by, bw, bh, txY, yMode, 0, dqY, skip, yMode, reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)

	// --- Chroma planes (skip for monochrome) ---
	if !fb.Monochrome && len(fb.U) > 0 {
		// Chroma block position/size (4:2:0 subsampling).
		cbx := bx / 2
		cby := by / 2
		cbw := (bw + 1) / 2
		cbh := (bh + 1) / 2

		var cflAlphaU, cflAlphaV int8 // CFL alpha parameters

		if uvMode == CFLPred {
			// Decode CFL alpha signs and magnitudes.
			cflAlphaU, cflAlphaV = decodeCFLAlphas(m)
		}

		if uvMode == CFLPred {
			// Build zero-mean luma AC buffer (4:2:0 subsampled from reconstructed Y).
			acCfl := buildCflAc(fb, bx, by, bw, bh, cbw, cbh)
			// CFL prediction: chroma = DC_chroma + (alpha*luma_AC + 32) >> 6.
			decodeIntraPlaneCFL(m, ctx, fb, 1, cbx, cby, cbw, cbh, txUV, int(cflAlphaU), dqU, skip, yMode, reducedTxtpSet, fhdr, seq, qidxIsZero, lossless, acCfl)
			decodeIntraPlaneCFL(m, ctx, fb, 2, cbx, cby, cbw, cbh, txUV, int(cflAlphaV), dqV, skip, yMode, reducedTxtpSet, fhdr, seq, qidxIsZero, lossless, acCfl)
		} else {
			decodeIntraPlane(m, ctx, fb, 1, cbx, cby, cbw, cbh, txUV, uvMode, 0, dqU, skip, yMode, reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)
			decodeIntraPlane(m, ctx, fb, 2, cbx, cby, cbw, cbh, txUV, uvMode, 0, dqV, skip, yMode, reducedTxtpSet, fhdr, seq, qidxIsZero, lossless)
		}
	}

	// Record decoded block state for future neighbour context derivation.
	fs.SetBlockSeg(bx, by, bw, bh, skip, yMode, segID)
}

// decodeCFLAlphas reads two CFL alpha values from the bitstream.
// Simplified: decode sign (1 bit each) then magnitude (3 bits each), clamped to [-16,16].
func decodeCFLAlphas(m *bitstream.MSAC) (int8, int8) {
	signU := m.BoolEqui()
	magU := m.Bools(3)
	signV := m.BoolEqui()
	magV := m.Bools(3)
	alphaU := int8(magU)
	alphaV := int8(magV)
	if signU != 0 {
		alphaU = -alphaU
	}
	if signV != 0 {
		alphaV = -alphaV
	}
	return alphaU, alphaV
}

// buildCflAc constructs a zero-mean luma AC buffer for CFL prediction by
// 4:2:0-subsampling the reconstructed luma block at (bx,by,bw,bh) into a
// cbw×cbh array, then subtracting the mean. The result is in row-major
// layout, length cbw*cbh.
func buildCflAc(fb *FrameBuf, bx, by, bw, bh, cbw, cbh int) []int16 {
	ac := make([]int16, cbw*cbh)
	if len(fb.Y) == 0 || cbw == 0 || cbh == 0 {
		return ac
	}
	stride := fb.StrideY
	planeW := fb.Width
	planeH := fb.Height
	var sum int32
	for cy := 0; cy < cbh; cy++ {
		for cx := 0; cx < cbw; cx++ {
			lx := bx + cx*2
			ly := by + cy*2
			var s int32
			n := 0
			for dy := 0; dy < 2; dy++ {
				yy := ly + dy
				if yy >= planeH {
					continue
				}
				for dx := 0; dx < 2; dx++ {
					xx := lx + dx
					if xx >= planeW {
						continue
					}
					off := yy*stride + xx
					if off < 0 || off >= len(fb.Y) {
						continue
					}
					s += int32(fb.Y[off])
					n++
				}
			}
			var avg int32
			if n > 0 {
				// dav1d uses sum<<3/n for 4:2:0 (8x scale before subtraction).
				avg = (s << 3) / int32(n)
			}
			ac[cy*cbw+cx] = int16(avg)
			sum += avg
		}
	}
	mean := int16(sum / int32(cbw*cbh))
	for i := range ac {
		ac[i] -= mean
	}
	return ac
}

// decodeIntraPlaneCFL decodes a chroma plane using CFL prediction. It is
// a CFL-specialised variant of decodeIntraPlane: prediction is built via
// PredCFL (DC base + alpha*ac), then chroma residual is added on top.
func decodeIntraPlaneCFL(
	m *bitstream.MSAC, ctx *TileCtx,
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

			// CFL prediction: derive DC from neighbours, then add alpha*ac.
			acSlice := cflAcSubBlock(ac, bw, bh, tbx, tby, tw, th)
			intra.PredCFLBoth(predBuf, tw, tlBuf, tl, acSlice, tw, th, cflAlpha)

			for row := 0; row < th; row++ {
				dstRow := (by+tby+row)*stride + (bx + tbx)
				if dstRow+tw > len(planeBuf) {
					break
				}
				copy(planeBuf[dstRow:dstRow+tw], predBuf[row*tw:(row+1)*tw])
			}

			if !skip {
				coeff, eob, txtp := decodeCoefficients(m, ctx, tx, plane, yMode, reducedTxtpSet, qidxIsZero, lossless)
				if eob > 0 && len(coeff) > 0 {
					tdFull := transform.TxfmDimensions[tx]
					twFull := int(tdFull.W) * 4
					thFull := int(tdFull.H) * 4
					maxOff := (thFull-1)*stride + (twFull - 1)
					if dstOff+maxOff < len(planeBuf) {
						ReconBlock(dst, stride, coeff, eob, tx, txtp, dq, 8)
					}
				}
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

// decodeIntraPlane performs intra prediction + coefficient decode + reconstruction
// for one plane within a block.
//
//	plane: 0=Y, 1=U, 2=V
//	mode:  IntraPredMode constant
//	cflAlpha: only used when mode == CFLPred
//	yMode: luma intra prediction mode (used for chroma txtp derivation)
//	reducedTxtpSet: from fhdr.ReducedTxtpSet
func decodeIntraPlane(
	m *bitstream.MSAC, ctx *TileCtx,
	fb *FrameBuf,
	plane, bx, by, bw, bh int,
	tx uint8,
	mode, cflAlpha int,
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
			callIntraPred(mode, predBuf, tw, tlBuf, tl, tw, th)

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
				coeff, eob, txtp := decodeCoefficients(m, ctx, tx, plane, yMode, reducedTxtpSet, qidxIsZero, lossless)
				if eob > 0 && len(coeff) > 0 {
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
func callIntraPred(mode int, dst []byte, stride int, topleft []byte, tl, width, height int) {
	switch mode {
	case DCPred:
		intra.PredDC(dst, stride, topleft, tl, width, height)
	case VertPred:
		intra.PredV(dst, stride, topleft, tl, width, height)
	case HorPred:
		intra.PredH(dst, stride, topleft, tl, width, height)
	case PaethPred:
		intra.PredPaeth(dst, stride, topleft, tl, width, height)
	case SmoothPred:
		// SMOOTH requires right and bottom extensions.
		intra.PredSmooth(dst, stride, topleft, tl, width, height)
	case SmoothVPred:
		intra.PredSmoothV(dst, stride, topleft, tl, width, height)
	case SmoothHPred:
		intra.PredSmoothH(dst, stride, topleft, tl, width, height)
	case DiagDownLeftPred: // D45 — angle=45, Z1
		intra.PredZ1(dst, stride, topleft, tl, width, height, 45)
	case VertLeftPred: // D67 — angle=67, Z1
		intra.PredZ1(dst, stride, topleft, tl, width, height, 67)
	case VertRightPred: // D113 — angle=113, Z2
		intra.PredZ2(dst, stride, topleft, tl, width, height, 113, width, height)
	case DiagDownRightPred: // D135 — angle=135, Z2
		intra.PredZ2(dst, stride, topleft, tl, width, height, 135, width, height)
	case HorDownPred: // D157 — angle=157, Z2
		intra.PredZ2(dst, stride, topleft, tl, width, height, 157, width, height)
	case HorUpPred: // D203 — angle=203, Z3
		intra.PredZ3(dst, stride, topleft, tl, width, height, 203)
	default:
		// CFL or unknown → DC fallback.
		intra.PredDC(dst, stride, topleft, tl, width, height)
	}
}

// ---------------------------------------------------------------------------
// Coefficient decoding (M8 Task 2 — dav1d-aligned)
//
// Mirrors dav1d/src/recon_tmpl.c decode_coefs(). Layout differences:
//   - dav1d cf[rc] is in the (x<<shift)|y / transposed layout that its own
//     itxfm consumes. Our ReconBlock currently expects row-major raster
//     (y*W + x). Task 3 will switch ReconBlock to dav1d's layout; for now we
//     translate (x,y) → row-major when storing tokens, so the dav1d-aligned
//     CDF reads do not require any other downstream change.
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
func decodeCoefficients(m *bitstream.MSAC, ctx *TileCtx, tx uint8, plane int,
	yMode int, reducedTxtpSet bool, qidxIsZero bool, lossless bool,
) ([]int32, int, uint8) {
	td := transform.TxfmDimensions[tx]
	chroma := 0
	if plane > 0 {
		chroma = 1
	}
	blockW := int(td.W) * 4
	blockH := int(td.H) * 4
	n := blockW * blockH

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
		if yMode >= 0 && yMode < len(TxtpFromUVMode) {
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
	if eob == 0 {
		return nil, 0, txtp
	}
	if eob > n {
		eob = n
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

	// rasterIdx maps dav1d (x, y) → col-major index used by
	// transform.InvTxfmAdd (which reads coeff[y + x*sh]). Note: sh is the
	// transform height (blockH); the column-major index is x*blockH + y.
	rasterIdx := func(x, y int) int {
		if x < 0 || x >= blockW || y < 0 || y >= blockH {
			return -1
		}
		return x*blockH + y
	}

	// signCtx for DC — simplified to 0 (Task 7 will wire dav1d's
	// get_dc_sign_ctx using above/left neighbour sign accumulation).
	dcSignCtx := 0

	// Helper that reads sign + golomb extra bits and stores the signed
	// magnitude into coeff. Mirrors the per-coefficient bit-stream order of
	// the previous M7 implementation (sign decoded immediately after the
	// token), keeping byte alignment with the existing pipeline.
	writeSignedCoeff := func(coeffIdx, tok int, isDC bool) {
		if coeffIdx < 0 || tok == 0 {
			return
		}
		var sign uint32
		if isDC {
			sign = m.BoolAdapt(ctx.DCSignCDF[chroma][dcSignCtx][:])
		} else {
			sign = m.BoolEqui()
		}
		mag := tok
		if mag == 15 {
			mag = int(readGolomb(m)) + 15
		}
		if sign != 0 {
			coeff[coeffIdx] = int32(-mag)
		} else {
			coeff[coeffIdx] = int32(mag)
		}
	}

	// EOB position (i = eob-1)
	var x, y, levelIdx int
	if cls == TxClass2D {
		if eob-1 >= len(scan) {
			return coeff, eob, txtp
		}
		rcRaw := int(scan[eob-1])
		x = rcRaw >> shift
		y = rcRaw & mask
		levelIdx = rcRaw
	} else if cls == TxClassH {
		x = (eob - 1) & mask
		y = (eob - 1) >> shift
		levelIdx = x*stride + y
	} else {
		x = (eob - 1) & mask
		y = (eob - 1) >> shift
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
	writeSignedCoeff(rasterIdx(x, y), tok, eob-1 == 0)
	if levelIdx >= 0 && levelIdx < len(levels) {
		levels[levelIdx] = uint8(levelTok)
	}

	// AC tokens: i = eob-2 .. 1
	for i := eob - 2; i > 0; i-- {
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
		writeSignedCoeff(rasterIdx(xi, yi), toki, false)
	}

	// DC token (i = 0)
	var dcTok int
	if cls == TxClass2D {
		dcTok = int(m.SymbolAdapt(loCdf[0][:], 4))
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
	writeSignedCoeff(rasterIdx(0, 0), dcTok, true)

	return coeff, eob, txtp
}

// clampTxType restricts txtp to the 1D transform types supported by the
// given transform dimensions. AV1 spec §7.12.2:
//   - TX32 (lw or lh == 3): only DCT and IDENTITY are valid.
//   - TX64 (lw or lh == 4): only DCT is valid.
//
// If either dimension doesn't support the decoded 1D type, fall back to DCT_DCT.
func clampTxType(txtp, lw, lh uint8) uint8 {
	txtps := transform.Tx1dTypes[txtp]
	row1d := txtps[1] // row transform type
	col1d := txtps[0] // column transform type

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
func fillDC128(fb *FrameBuf, bx, by, bw, bh int) {
	yFill := byte(64 + ((bx + by) & 0x7F))
	cbx := bx / 2
	cby := by / 2
	cbw := (bw + 1) / 2
	cbh := (bh + 1) / 2
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
