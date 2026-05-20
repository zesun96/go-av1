// Package obuwriter serializes AV1 OBU (Open Bitstream Unit) structures.
//
// For the minimum viable encoder (M10):
//   - Sequence Header OBU (seq_profile=0, 8-bit, mono_chrome=0, 4:2:0)
//   - Frame OBU (KEY_FRAME, show_frame=1, disable_cdf_update=1)
//   - Tile Data inside Frame OBU
package obuwriter

import (
	"github.com/zesun96/go-av1/internal/encoder/bitwriter"
)

// OBU type constants (AV1 spec section 6.2.2).
const (
	OBUSequenceHeader = 1
	OBUTemporalDel    = 2
	OBUFrameHeader    = 3
	OBUTileGroup      = 4
	OBUFrame          = 6 // combined frame header + tile group
)

// SeqParams holds sequence-level parameters for OBU writing.
type SeqParams struct {
	Width      int
	Height     int
	BitDepth   int // 8, 10, or 12
	ChromaSS   int // 0=mono, 1=420, 2=422, 3=444
	FrameRateN int
	FrameRateD int
}

// WriteOBUHeader writes the obu_header() syntax (1 or 2 bytes).
// Returns the OBU header bytes.
func WriteOBUHeader(obuType int, hasSize bool, extensionFlag bool) []byte {
	bw := bitwriter.New(2)
	bw.PutBit(0)                   // obu_forbidden_bit
	bw.PutBits(uint32(obuType), 4) // obu_type
	if extensionFlag {
		bw.PutBit(1) // obu_extension_flag
	} else {
		bw.PutBit(0)
	}
	if hasSize {
		bw.PutBit(1) // obu_has_size_field
	} else {
		bw.PutBit(0)
	}
	bw.PutBit(0) // obu_reserved_1bit
	return bw.Bytes()
}

// WriteSequenceHeader serializes a complete Sequence Header OBU.
func WriteSequenceHeader(p *SeqParams) []byte {
	bw := bitwriter.New(64)

	// sequence_header_obu()
	bw.PutBits(0, 3) // seq_profile = 0 (Main)
	bw.PutBit(0)     // still_picture = 0
	bw.PutBit(0)     // reduced_still_picture_header = 0

	// timing_info_present_flag = 0
	bw.PutBit(0)
	// initial_display_delay_present_flag = 0
	bw.PutBit(0)

	// operating_points_cnt_minus_1 = 0
	bw.PutBits(0, 5)
	// operating_point_idc[0] = 0
	bw.PutBits(0, 12)
	// seq_level_idx[0] = 0 (level 2.0, minimum)
	bw.PutBits(0, 5)
	// seq_tier not present when level < 4

	// frame_width_bits_minus_1 and frame_height_bits_minus_1
	wBits := bitsNeeded(p.Width)
	hBits := bitsNeeded(p.Height)
	bw.PutBits(uint32(wBits-1), 4)
	bw.PutBits(uint32(hBits-1), 4)
	// max_frame_width_minus_1
	bw.PutBits(uint32(p.Width-1), wBits)
	// max_frame_height_minus_1
	bw.PutBits(uint32(p.Height-1), hBits)

	// frame_id_numbers_present_flag = 0
	bw.PutBit(0)

	// use_128x128_superblock = 0 (use 64x64 SB)
	bw.PutBit(0)
	// enable_filter_intra = 0
	bw.PutBit(0)
	// enable_intra_edge_filter = 0
	bw.PutBit(0)

	// enable_interintra_compound = 0
	bw.PutBit(0)
	// enable_masked_compound = 0
	bw.PutBit(0)
	// enable_warped_motion = 0
	bw.PutBit(0)
	// enable_dual_filter = 0
	bw.PutBit(0)
	// enable_order_hint = 0
	bw.PutBit(0)
	// Since enable_order_hint=0: no jnt_comp, no ref_frame_mvs

	// seq_choose_screen_content_tools = 1 (SELECT_SCREEN_CONTENT_TOOLS)
	bw.PutBit(1)
	// seq_choose_integer_mv = 1
	bw.PutBit(1)

	// enable_superres = 0
	bw.PutBit(0)
	// enable_cdef = 0
	bw.PutBit(0)
	// enable_restoration = 0
	bw.PutBit(0)

	// color_config()
	writeColorConfig(bw, p)

	// film_grain_params_present = 0
	bw.PutBit(0)

	// trailing_bits()
	bw.TrailingBits()

	return bw.Bytes()
}

// writeColorConfig writes the color_config() syntax.
func writeColorConfig(bw *bitwriter.BitWriter, p *SeqParams) {
	// high_bitdepth
	if p.BitDepth > 8 {
		bw.PutBit(1)
		if p.BitDepth == 12 {
			bw.PutBit(1) // twelve_bit
		} else {
			bw.PutBit(0)
		}
	} else {
		bw.PutBit(0) // 8-bit
	}

	// mono_chrome = 0 (profile 0 doesn't support mono)
	// For seq_profile == 0, mono_chrome is not signaled; it's inferred as 0.

	// color_description_present_flag = 0
	bw.PutBit(0)
	// Inferred: color_primaries=CP_UNSPECIFIED, transfer=TC_UNSPECIFIED, matrix=MC_UNSPECIFIED

	// color_range = 0 (studio/limited range)
	bw.PutBit(0)

	// For profile 0 (4:2:0): subsampling_x=1, subsampling_y=1 are inferred.
	// chroma_sample_position = CSP_UNKNOWN (0)
	bw.PutBits(0, 2)

	// separate_uv_delta_q = 0
	bw.PutBit(0)
}

// WriteFrameOBU serializes a complete Frame OBU (header + tile group) for a
// KEY_FRAME. tileData is the raw MSAC-encoded tile bytes.
func WriteFrameOBU(p *SeqParams, qindex int, tileData []byte) []byte {
	bw := bitwriter.New(256)

	// uncompressed_header()
	writeUncompressedHeader(bw, p, qindex)

	// tile_group_obu() - single tile
	// For a single tile (NumTiles=1), the tile_start_and_end_present_flag
	// is not signaled (inferred 0), and there's no tile_size.
	// The tile data follows directly.
	bw.ByteAlign()
	bw.DirectWrite(tileData)

	return bw.Bytes()
}

// writeUncompressedHeader writes frame_header_obu() / uncompressed_header().
func writeUncompressedHeader(bw *bitwriter.BitWriter, p *SeqParams, qindex int) {
	// show_existing_frame = 0
	bw.PutBit(0)
	// frame_type = KEY_FRAME (0)
	bw.PutBits(0, 2)
	// show_frame = 1
	bw.PutBit(1)
	// For KEY_FRAME with show_frame=1: error_resilient_mode is inferred as 1.

	// disable_cdf_update = 1 (crucial for M10: fixed CDFs)
	bw.PutBit(1)
	// allow_screen_content_tools: if seq_choose_screen_content_tools=1,
	// this is not present in bitstream (inferred as SELECT_SCREEN_CONTENT_TOOLS).
	// force_integer_mv: similarly inferred.

	// frame_size() - for key frame, uses full size from sequence header
	// frame_size_override_flag = 0 (use sequence max dimensions)
	bw.PutBit(0)

	// render_and_frame_size_different = 0
	bw.PutBit(0)

	// Since KEY_FRAME: refresh_frame_flags not needed for single-frame use,
	// but spec requires it: all 8 bits set to allow subsequent frames to reference.
	bw.PutBits(0xFF, 8) // refresh all reference frames

	// quantization_params()
	bw.PutBits(uint32(qindex), 8) // base_q_idx
	bw.PutBit(0)                  // DeltaQYDc (delta_coded = 0, meaning delta = 0)
	// For 4:2:0 with separate_uv_delta_q=0:
	bw.PutBit(0) // DeltaQUDc = 0
	bw.PutBit(0) // DeltaQUAc = 0
	// No V deltas since separate_uv_delta_q = 0

	// using_qmatrix = 0
	bw.PutBit(0)

	// segmentation_enabled = 0
	bw.PutBit(0)

	// delta_q_present = 0
	bw.PutBit(0)

	// loop_filter_params() - all zeros for no filtering
	bw.PutBits(0, 6) // loop_filter_level[0] = 0
	bw.PutBits(0, 6) // loop_filter_level[1] = 0
	// Since both levels are 0, no more loop filter params needed.

	// cdef_params() - enable_cdef=0 in seq header, so not present

	// lr_params() - enable_restoration=0 in seq header, so not present

	// read_tx_mode(): for KEY_FRAME with lossless=false
	// tx_mode_select = 0 (TX_MODE_LARGEST, all 8x8 since our SB decomposition goes to 8x8)
	bw.PutBit(0)

	// frame_reference_mode(): not present for KEY_FRAME

	// skip_mode_params(): not present for KEY_FRAME (no references)

	// allow_warped_motion not present for KEY_FRAME
	// reduced_tx_set = 1 (use reduced transform set)
	bw.PutBit(1)

	// global_motion_params(): not present for KEY_FRAME

	// film_grain_params(): film_grain_params_present=0 in seq header, not present

	// trailing_bits for frame header portion
	bw.TrailingBits()
}

// WriteTemporalDelimiter writes a Temporal Delimiter OBU (empty payload, 2 bytes total).
func WriteTemporalDelimiter() []byte {
	hdr := WriteOBUHeader(OBUTemporalDel, true, false)
	// Append size = 0 (LEB128 encoding of 0 is a single byte 0x00)
	return append(hdr, 0x00)
}

// BuildTemporalUnit assembles a complete AV1 temporal unit (one access unit)
// consisting of: TD + Sequence Header (if key frame) + Frame OBU.
func BuildTemporalUnit(p *SeqParams, qindex int, tileData []byte, isKeyFrame bool) []byte {
	var out []byte

	// 1. Temporal Delimiter
	out = append(out, WriteTemporalDelimiter()...)

	// 2. Sequence Header (only for key frames)
	if isKeyFrame {
		seqPayload := WriteSequenceHeader(p)
		seqHdr := WriteOBUHeader(OBUSequenceHeader, true, false)
		out = append(out, seqHdr...)
		out = appendLeb128(out, uint32(len(seqPayload)))
		out = append(out, seqPayload...)
	}

	// 3. Frame OBU
	framePayload := WriteFrameOBU(p, qindex, tileData)
	frameHdr := WriteOBUHeader(OBUFrame, true, false)
	out = append(out, frameHdr...)
	out = appendLeb128(out, uint32(len(framePayload)))
	out = append(out, framePayload...)

	return out
}

// bitsNeeded returns the minimum number of bits to represent val.
func bitsNeeded(val int) int {
	n := 0
	v := val
	for v > 0 {
		n++
		v >>= 1
	}
	if n == 0 {
		return 1
	}
	return n
}

// appendLeb128 appends a LEB128-encoded uint32 to buf.
func appendLeb128(buf []byte, v uint32) []byte {
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		buf = append(buf, b)
		if v == 0 {
			break
		}
	}
	return buf
}
