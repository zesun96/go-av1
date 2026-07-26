package obuwriter

import "github.com/zesun96/go-av1/internal/encoder/bitwriter"

// WriteInterFrameOBU serializes an INTER_FRAME header followed by one tile.
// The frame references slot zero for every AV1 reference type and refreshes all
// slots after decoding. PrimaryRefNone keeps the tile CDF initialization
// independent of decoder-side reference context.
func WriteInterFrameOBU(p *SeqParams, qindex int, tileData []byte) []byte {
	bw := bitwriter.New(256)
	writeInterUncompressedHeader(bw, p, qindex)
	bw.ByteAlign()
	bw.DirectWrite(tileData)
	return bw.Bytes()
}

func writeInterUncompressedHeader(bw *bitwriter.BitWriter, p *SeqParams, qindex int) {
	bw.PutBit(0)     // show_existing_frame
	bw.PutBits(1, 2) // frame_type = INTER_FRAME
	bw.PutBit(1)     // show_frame
	bw.PutBit(0)     // error_resilient_mode
	bw.PutBit(0)     // disable_cdf_update
	bw.PutBit(0)     // allow_screen_content_tools
	bw.PutBit(0)     // frame_size_override_flag
	bw.PutBits(7, 3) // primary_ref_frame = PRIMARY_REF_NONE
	bw.PutBits(0xff, 8)
	for range 7 {
		bw.PutBits(0, 3) // ref_frame_idx[i] = slot zero
	}
	bw.PutBit(0) // render_and_frame_size_different
	bw.PutBit(1) // allow_high_precision_mv
	bw.PutBit(0) // interpolation_filter is fixed
	bw.PutBits(0, 2)
	bw.PutBit(0) // is_motion_mode_switchable

	bw.PutBit(0) // disable_frame_end_update_cdf
	writeSingleTileInfo(bw, p)

	bw.PutBits(uint32(qindex), 8)
	bw.PutBit(0) // delta_q_y_dc
	bw.PutBit(0) // delta_q_u_dc
	bw.PutBit(0) // delta_q_u_ac
	bw.PutBit(0) // using_qmatrix
	bw.PutBit(0) // segmentation_enabled
	bw.PutBit(0) // delta_q_present

	bw.PutBits(0, 6) // loop_filter_level[0]
	bw.PutBits(0, 6) // loop_filter_level[1]
	bw.PutBits(0, 3) // loop_filter_sharpness
	bw.PutBit(0)     // loop_filter_delta_enabled

	bw.PutBit(0) // tx_mode = TX_MODE_LARGEST
	bw.PutBit(0) // reference_select = single reference only
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
	out := append([]byte(nil), WriteTemporalDelimiter()...)
	payload := WriteInterFrameOBU(p, qindex, tileData)
	hdr := WriteOBUHeader(OBUFrame, true, false)
	out = append(out, hdr...)
	out = appendLeb128(out, uint32(len(payload)))
	out = append(out, payload...)
	return out
}
