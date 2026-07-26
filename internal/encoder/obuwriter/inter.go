package obuwriter

import "github.com/zesun96/go-av1/internal/encoder/bitwriter"

// InterFrameParams controls reference mapping and refresh for an inter frame.
type InterFrameParams struct {
	RefIdx         [7]uint8
	RefreshFlags   uint8
	EnableCompound bool
	EnableOBMC     bool
}

func defaultInterFrameParams() InterFrameParams {
	return InterFrameParams{RefreshFlags: 0xff}
}

// WriteInterFrameOBU serializes an INTER_FRAME header followed by one tile.
// The frame references slot zero for every AV1 reference type and refreshes all
// slots after decoding. PrimaryRefNone keeps the tile CDF initialization
// independent of decoder-side reference context.
func WriteInterFrameOBU(p *SeqParams, qindex int, tileData []byte) []byte {
	return WriteInterFrameOBUWithParams(p, qindex, tileData, defaultInterFrameParams())
}

// WriteInterFrameOBUWithParams serializes an INTER_FRAME with explicit
// reference-slot mappings.
func WriteInterFrameOBUWithParams(p *SeqParams, qindex int, tileData []byte, fp InterFrameParams) []byte {
	bw := bitwriter.New(256)
	writeInterUncompressedHeader(bw, p, qindex, fp)
	bw.ByteAlign()
	bw.DirectWrite(tileData)
	return bw.Bytes()
}

func writeInterUncompressedHeader(bw *bitwriter.BitWriter, p *SeqParams, qindex int, fp InterFrameParams) {
	bw.PutBit(0)     // show_existing_frame
	bw.PutBits(1, 2) // frame_type = INTER_FRAME
	bw.PutBit(1)     // show_frame
	bw.PutBit(0)     // error_resilient_mode
	bw.PutBit(0)     // disable_cdf_update
	bw.PutBit(0)     // allow_screen_content_tools
	bw.PutBit(0)     // frame_size_override_flag
	bw.PutBits(7, 3) // primary_ref_frame = PRIMARY_REF_NONE
	bw.PutBits(uint32(fp.RefreshFlags), 8)
	for _, slot := range fp.RefIdx {
		bw.PutBits(uint32(slot&7), 3)
	}
	bw.PutBit(0) // render_and_frame_size_different
	bw.PutBit(1) // allow_high_precision_mv
	bw.PutBit(0) // interpolation_filter is fixed
	bw.PutBits(0, 2)
	if fp.EnableOBMC {
		bw.PutBit(1) // is_motion_mode_switchable
	} else {
		bw.PutBit(0)
	}

	bw.PutBit(0) // disable_frame_end_update_cdf
	writeSingleTileInfo(bw, p)

	bw.PutBits(uint32(qindex), 8)
	bw.PutBit(0) // delta_q_y_dc
	bw.PutBit(0) // delta_q_u_dc
	bw.PutBit(0) // delta_q_u_ac
	bw.PutBit(0) // using_qmatrix
	bw.PutBit(0) // segmentation_enabled
	bw.PutBit(0) // delta_q_present

	lfLevel := encoderLoopFilterLevel(qindex)
	bw.PutBits(uint32(lfLevel), 6)
	bw.PutBits(uint32(lfLevel), 6)
	if lfLevel != 0 {
		bw.PutBits(uint32(lfLevel), 6)
		bw.PutBits(uint32(lfLevel), 6)
	}
	bw.PutBits(0, 3) // loop_filter_sharpness
	bw.PutBit(0)     // loop_filter_delta_enabled

	bw.PutBit(0) // tx_mode = TX_MODE_LARGEST
	if fp.EnableCompound {
		bw.PutBit(1) // reference_select = selectable single/compound
	} else {
		bw.PutBit(0)
	}
	bw.PutBit(1) // reduced_tx_set
	for range 7 {
		bw.PutBit(0) // identity global motion for each reference type
	}
}

func writeSingleTileInfo(bw *bitwriter.BitWriter, p *SeqParams) {
	bw.PutBit(1) // uniform_tile_spacing_flag
	sbSize := 64
	if p.Use128SB {
		sbSize = 128
	}
	sbw := (p.Width + sbSize - 1) / sbSize
	sbh := (p.Height + sbSize - 1) / sbSize
	minLog2Cols := tileLog2(4096/sbSize, sbw)
	maxLog2Cols := tileLog2(1, minInt(sbw, 64))
	maxLog2Rows := tileLog2(1, minInt(sbh, 64))
	maxTileAreaSB := 4096 * 2304 / (sbSize * sbSize)
	minLog2Tiles := maxInt(tileLog2(maxTileAreaSB, sbw*sbh), minLog2Cols)
	if minLog2Cols < maxLog2Cols {
		bw.PutBit(0)
	}
	minLog2Rows := maxInt(minLog2Tiles-minLog2Cols, 0)
	if minLog2Rows < maxLog2Rows {
		bw.PutBit(0)
	}
}

// BuildInterTemporalUnit assembles a temporal delimiter and an INTER_FRAME OBU.
// The sequence header must already have been emitted by a preceding key frame.
func BuildInterTemporalUnit(p *SeqParams, qindex int, tileData []byte) []byte {
	return BuildInterTemporalUnitWithParams(p, qindex, tileData, defaultInterFrameParams())
}

// BuildInterTemporalUnitWithParams assembles an inter temporal unit with
// explicit reference mappings.
func BuildInterTemporalUnitWithParams(p *SeqParams, qindex int, tileData []byte, fp InterFrameParams) []byte {
	out := append([]byte(nil), WriteTemporalDelimiter()...)
	payload := WriteInterFrameOBUWithParams(p, qindex, tileData, fp)
	hdr := WriteOBUHeader(OBUFrame, true, false)
	out = append(out, hdr...)
	out = appendLeb128(out, uint32(len(payload)))
	out = append(out, payload...)
	return out
}
