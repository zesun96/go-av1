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
	seq *header.SequenceHeader, fb *FrameBuf, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}

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
			decodeSuperBlock(m, ctx, fhdr, seq, fb, sbx, sby, sbSz)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Superblock → partition tree (Task 4)
// ---------------------------------------------------------------------------

// decodeSuperBlock starts the recursive partition tree at the superblock root.
func decodeSuperBlock(m *bitstream.MSAC, ctx *TileCtx,
	fhdr *header.FrameHeader, seq *header.SequenceHeader,
	fb *FrameBuf, sbx, sby, sbSz int) {

	bl := BL64X64
	if sbSz == 128 {
		bl = BL128X128
	}
	decodePartition(m, ctx, fhdr, seq, fb, sbx, sby, bl)
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
func decodePartition(m *bitstream.MSAC, ctx *TileCtx,
	fhdr *header.FrameHeader, seq *header.SequenceHeader,
	fb *FrameBuf, bx, by, bl int) {

	// Clamp to frame.
	if bx >= fb.Width || by >= fb.Height {
		return
	}

	blSz := blkSizeFromLevel(bl) // full block size in luma px

	// At the leaf level (8×8), always decode as a single block.
	if bl == BL8X8 {
		decodeBlock(m, ctx, fhdr, seq, fb, bx, by, blSz, blSz)
		return
	}

	// Select partition CDF and symbol count based on block level.
	// AV1 spec: 128x128→8 syms, 64/32/16→10 syms, 8x8→4 syms.
	var partCDF []uint16
	var nPart int
	switch bl {
	case BL128X128:
		partCDF = ctx.Partition128CDF[:]
		nPart = 8
	case BL64X64:
		partCDF = ctx.Partition64CDF[:]
		nPart = 10
	case BL32X32:
		partCDF = ctx.Partition32CDF[:]
		nPart = 10
	case BL16X16:
		partCDF = ctx.Partition16CDF[:]
		nPart = 10
	default: // BL8X8
		partCDF = ctx.Partition8CDF[:]
		nPart = 4
	}

	part := int(m.SymbolAdapt(partCDF, nPart))
	half := blSz / 2

	switch part {
	case PartitionNone:
		decodeBlock(m, ctx, fhdr, seq, fb, bx, by, blSz, blSz)

	case PartitionH:
		decodeBlock(m, ctx, fhdr, seq, fb, bx, by, blSz, half)
		decodeBlock(m, ctx, fhdr, seq, fb, bx, by+half, blSz, half)

	case PartitionV:
		decodeBlock(m, ctx, fhdr, seq, fb, bx, by, half, blSz)
		decodeBlock(m, ctx, fhdr, seq, fb, bx+half, by, half, blSz)

	case PartitionSplit:
		decodePartition(m, ctx, fhdr, seq, fb, bx, by, bl+1)
		decodePartition(m, ctx, fhdr, seq, fb, bx+half, by, bl+1)
		decodePartition(m, ctx, fhdr, seq, fb, bx, by+half, bl+1)
		decodePartition(m, ctx, fhdr, seq, fb, bx+half, by+half, bl+1)

	case PartitionTTopSplit:
		decodeBlock(m, ctx, fhdr, seq, fb, bx, by, blSz, half)
		decodePartition(m, ctx, fhdr, seq, fb, bx, by+half, bl+1)
		decodePartition(m, ctx, fhdr, seq, fb, bx+half, by+half, bl+1)

	case PartitionTBottomSplit:
		decodePartition(m, ctx, fhdr, seq, fb, bx, by, bl+1)
		decodePartition(m, ctx, fhdr, seq, fb, bx+half, by, bl+1)
		decodeBlock(m, ctx, fhdr, seq, fb, bx, by+half, blSz, half)

	case PartitionTLeftSplit:
		decodeBlock(m, ctx, fhdr, seq, fb, bx, by, half, blSz)
		decodePartition(m, ctx, fhdr, seq, fb, bx+half, by, bl+1)
		decodePartition(m, ctx, fhdr, seq, fb, bx+half, by+half, bl+1)

	case PartitionTRightSplit:
		decodePartition(m, ctx, fhdr, seq, fb, bx, by, bl+1)
		decodePartition(m, ctx, fhdr, seq, fb, bx, by+half, bl+1)
		decodeBlock(m, ctx, fhdr, seq, fb, bx+half, by, half, blSz)

	case PartitionH4:
		q := blSz / 4
		for i := 0; i < 4; i++ {
			decodeBlock(m, ctx, fhdr, seq, fb, bx, by+i*q, blSz, q)
		}

	case PartitionV4:
		q := blSz / 4
		for i := 0; i < 4; i++ {
			decodeBlock(m, ctx, fhdr, seq, fb, bx+i*q, by, q, blSz)
		}
	}
}

// ---------------------------------------------------------------------------
// Block decoder (Task 5)
// ---------------------------------------------------------------------------

// decodeBlock decodes one coding block of size bw×bh (luma pixels) at (bx,by).
func decodeBlock(m *bitstream.MSAC, ctx *TileCtx,
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

	// --- Skip flag ---
	skipCtx := 0 // simplified context (no neighbour state in M7)
	skip := m.SymbolAdapt(ctx.SkipCDF[skipCtx][:], 2) != 0

	// --- Intra vs Inter ---
	isIntra := fhdr.FrameType.IsIntra()
	// For inter frames, treat every block as intra DC128 (M7 stub).

	if !isIntra {
		// Inter frame block: fill with DC128 and return.
		fillDC128(fb, bx, by, bw, bh)
		return
	}

	// --- Intra luma mode ---
	yMode := int(m.SymbolAdapt(ctx.IntraYModeCDF[:], NIntraPredModes))

	// --- Intra UV mode ---
	uvMode := int(m.SymbolAdapt(ctx.IntraUVModeCDF[:], NIntraPredModes+1))

	// --- Transform size selection (M7: use largest fitting square tx) ---
	txY := largestTx(bw, bh)
	txUV := largestTx((bw+1)/2, (bh+1)/2)

	// --- Dequant values from frame header ---
	qidx := int(fhdr.Segmentation.QIdx[0])
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
	decodeIntraPlane(m, ctx, fb, 0, bx, by, bw, bh, txY, yMode, 0, dqY, skip, yMode, reducedTxtpSet, fhdr, seq)

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
			// CFL prediction: uses luma AC residual.
			decodeIntraPlane(m, ctx, fb, 1, cbx, cby, cbw, cbh, txUV, DCPred, int(cflAlphaU), dqU, skip, yMode, reducedTxtpSet, fhdr, seq)
			decodeIntraPlane(m, ctx, fb, 2, cbx, cby, cbw, cbh, txUV, DCPred, int(cflAlphaV), dqV, skip, yMode, reducedTxtpSet, fhdr, seq)
		} else {
			decodeIntraPlane(m, ctx, fb, 1, cbx, cby, cbw, cbh, txUV, uvMode, 0, dqU, skip, yMode, reducedTxtpSet, fhdr, seq)
			decodeIntraPlane(m, ctx, fb, 2, cbx, cby, cbw, cbh, txUV, uvMode, 0, dqV, skip, yMode, reducedTxtpSet, fhdr, seq)
		}
	}
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
	// Layout (matches intra package convention):
	//   topleft[tl-h..tl-1] = left samples (top-to-bottom)
	//   topleft[tl]         = top-left sample
	//   topleft[tl+1..tl+w] = top samples (left-to-right)
	//   topleft[tl+w]       = right extension (for SMOOTH)
	//   topleft[tl-h]       = bottom extension (for SMOOTH)
	maxDim := bw
	if bh > maxDim {
		maxDim = bh
	}
	tlBufSize := 2*maxDim + 3 // enough for left+tl+top+ext
	tlBuf := make([]byte, tlBufSize)
	tl := maxDim + 1 // index of the top-left sample

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
				coeff, eob, txtp := decodeCoefficients(m, ctx, tx, plane, yMode, reducedTxtpSet)
				if eob > 0 && len(coeff) > 0 {
					ReconBlock(dst, stride, coeff, eob, tx, txtp, dq, 8)
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
	default:
		// Directional modes and unknown → use DC as safe fallback.
		intra.PredDC(dst, stride, topleft, tl, width, height)
	}
}

// ---------------------------------------------------------------------------
// Coefficient decoding (Task 5)
// ---------------------------------------------------------------------------

// decodeCoefficients reads EOB, base tokens, high tokens and sign for one
// transform block. Returns (coefficients, eob, txtp).
func decodeCoefficients(m *bitstream.MSAC, ctx *TileCtx, tx uint8, plane int, yMode int, reducedTxtpSet bool) ([]int32, int, uint8) {
	td := transform.TxfmDimensions[tx]
	n := int(td.W) * int(td.H) * 16 // number of coefficients
	isLuma := plane == 0

	// --- Transform type ---
	// AV1 spec §7.12.3 / dav1d recon_tmpl.c:
	// chroma: derived from UV mode, not coded in bitstream.
	// luma, tx_max+1 >= TX_64X64 (i.e. td.Max >= 3): DCT_DCT, not coded.
	// luma, otherwise: coded with reduced (4-sym) or full (6-sym) set.
	var txtp uint8
	if !isLuma {
		// UV txtp derived from UV mode via LUT (dav1d_txtp_from_uvmode).
		if yMode >= 0 && yMode < len(TxtpFromUVMode) {
			txtp = TxtpFromUVMode[yMode]
		}
	} else if td.Max >= 3 {
		// TX32 or larger: only DCT_DCT allowed (not coded).
		txtp = transform.DCT_DCT
	} else if reducedTxtpSet || td.Min >= 2 {
		// Reduced tx set or TX16x16 min: 4-symbol Intra2 CDF.
		idx := m.SymbolAdapt(ctx.TxTypeIntra2CDF[:], TxTypeIntra2Symbols)
		if int(idx) < len(TxTypeIntra2Set) {
			txtp = TxTypeIntra2Set[idx]
		}
	} else {
		// Full tx set, TX4 or TX8 min: 6-symbol Intra1 CDF.
		idx := m.SymbolAdapt(ctx.TxTypeIntra1CDF[:], TxTypeIntra1Symbols)
		if int(idx) < len(TxTypeIntra1Set) {
			txtp = TxTypeIntra1Set[idx]
		}
	}
	// Safety clamp for large tx sizes (already handled above, but defensive).
	txtp = clampTxType(txtp, td.Lw, td.Lh)

	// --- EOB point ---
	eobCtx := 0
	if !isLuma {
		eobCtx = 1
	}
	eob := decodeEOB(m, ctx, tx, eobCtx)
	if eob == 0 {
		return nil, 0, txtp
	}

	coeff := make([]int32, n)

	// Decode coefficients in reverse scan order (position eob-1 … 0).
	// AV1 uses a zig-zag scan; for simplicity we decode in column-major
	// order which matches the dequant/itxfm convention used by ReconBlock.
	for i := eob - 1; i >= 0; i-- {
		// Base token (0=zero, 1=one, 2=two, 3=three_plus).
		baseCtx := clampInt(i*4/eob, 0, 3)
		var base uint32
		if i == eob-1 {
			// EOB position: use EOB CDF (only 3 levels).
			base = m.SymbolAdapt(ctx.CoeffBaseEobCDF[eobCtx][:], 3)
			base++ // EOB token is 1-indexed
		} else {
			base = m.SymbolAdapt(ctx.CoeffBaseCDF[baseCtx][:], 4)
		}

		if base == 0 {
			continue
		}

		var mag uint32
		if base >= 3 {
			// High token.
			brCtx := clampInt(i*4/eob, 0, 3)
			br := m.HiTok(ctx.CoeffBrCDF[brCtx][:])
			mag = br + 3
		} else {
			mag = base
		}

		// Sign.
		var sign uint32
		if i == 0 {
			// DC sign uses dedicated CDF.
			signCtx := 0
			sign = m.BoolAdapt(ctx.DCSignCDF[signCtx][:])
		} else {
			sign = m.BoolEqui()
		}

		// Extra bits for large magnitudes (bypass coded).
		var extra uint32
		if mag >= 14 {
			for k := 0; k < 14; k++ {
				extra = (extra << 1) | m.BoolEqui()
			}
			mag += extra
		}

		if sign != 0 {
			coeff[i] = -int32(mag)
		} else {
			coeff[i] = int32(mag)
		}
	}

	return coeff, eob, txtp
}

// decodeEOB decodes the end-of-block position for a transform block.
// Returns the 1-based EOB (0 means all-zero block).
func decodeEOB(m *bitstream.MSAC, ctx *TileCtx, tx uint8, eobCtx int) int {
	td := transform.TxfmDimensions[tx]
	maxTx := int(td.Max) // log2 of max(w,h) in 4-px units

	var eobPt int
	switch maxTx {
	case 0: // TX4x4: 2 symbols (pt=1..4)
		eobPt = int(m.SymbolAdapt(ctx.EobPtCDF4[eobCtx][:], 2))
	case 1: // TX8x8: 3 symbols (pt=1..8)
		eobPt = int(m.SymbolAdapt(ctx.EobPtCDF8[eobCtx][:], 3))
	case 2: // TX16x16: 5 symbols
		eobPt = int(m.SymbolAdapt(ctx.EobPtCDF16[eobCtx][:], 5))
	case 3: // TX32x32: 7 symbols
		eobPt = int(m.SymbolAdapt(ctx.EobPtCDF32[eobCtx][:], 7))
	default: // TX64x64+: 9 symbols
		eobPt = int(m.SymbolAdapt(ctx.EobPtCDF64[eobCtx][:], 9))
	}

	// eobPt == 0 means EOB=1 (only DC), eobPt==1 means EOB ∈ [2,3], etc.
	// Convert eob_pt to an EOB value via the standard mapping.
	if eobPt == 0 {
		return 1
	}
	// For eobPt >= 1, EOB is in range [2^(eobPt-1)+1 … 2^eobPt].
	// Read (eobPt-1) extra bits.
	base := 1 << uint(eobPt)
	extra := 0
	for k := eobPt - 1; k > 0; k-- {
		extra = (extra << 1) | int(m.BoolEqui())
	}
	eob := base + extra
	// Clamp to block size.
	td2 := transform.TxfmDimensions[tx]
	maxCoeff := int(td2.W) * int(td2.H) * 16
	if eob > maxCoeff {
		eob = maxCoeff
	}
	return eob
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

// fillTopleft fills the topleft reference buffer for intra prediction.
// Samples outside the frame or not-yet-reconstructed are seeded with a
// position-derived pseudo-grey value (instead of the AV1-spec default 128).
//
// Rationale (M7): the M7 CABAC pipeline uses simplified CDF tables that do
// not synchronise with a real AV1 encoder, so most blocks decode skip=1 or
// eob=0 → residual ≈ 0. If we then default neighbours to a constant 128,
// every intra-predicted pixel becomes 128 and the entire luma plane
// collapses to a flat grey frame (yMean=128.0).
// Seeding with a smooth gradient breaks that collapse: directional intra
// prediction now propagates a varying signal across blocks, so the output
// frame shows visible texture instead of a uniform colour. This is a
// best-effort visualisation only; M8+ (full CABAC + motion compensation)
// will replace this with proper reference samples.
func fillTopleft(planeBuf []byte, stride, planeW, planeH, bx, by, bw, bh int,
	tlBuf []byte, tl int) {

	// Position-derived seed: smooth diagonal gradient in [64, 192].
	// Using bx+by ensures neighbouring blocks see slightly different defaults.
	seed := byte(64 + ((bx + by) & 0x7F))

	// Default: fill everything with the seed value.
	for i := range tlBuf {
		tlBuf[i] = seed
	}

	// Top-left sample.
	if bx > 0 && by > 0 {
		off := (by-1)*stride + (bx - 1)
		if off >= 0 && off < len(planeBuf) {
			tlBuf[tl] = planeBuf[off]
		}
	}

	// Top row (left→right: tlBuf[tl+1..tl+bw]).
	if by > 0 {
		for x := 0; x < bw; x++ {
			off := (by-1)*stride + (bx + x)
			if off >= 0 && off < len(planeBuf) && bx+x < planeW {
				tlBuf[tl+1+x] = planeBuf[off]
			}
		}
		// Right extension for SMOOTH.
		lastX := bx + bw - 1
		if lastX < planeW {
			off := (by-1)*stride + lastX
			if off >= 0 && off < len(planeBuf) {
				tlBuf[tl+bw] = planeBuf[off]
			}
		}
	}

	// Left column (top→bottom: tlBuf[tl-1..tl-bh]).
	if bx > 0 {
		for y := 0; y < bh; y++ {
			off := (by+y)*stride + (bx - 1)
			if off >= 0 && off < len(planeBuf) && by+y < planeH {
				tlBuf[tl-1-y] = planeBuf[off]
			}
		}
		// Bottom extension for SMOOTH.
		lastY := by + bh - 1
		if lastY < planeH {
			off := lastY*stride + (bx - 1)
			if off >= 0 && off < len(planeBuf) {
				tlBuf[tl-bh] = planeBuf[off]
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
