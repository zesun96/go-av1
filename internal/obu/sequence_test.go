package obu

import (
	"errors"
	"testing"

	"github.com/zesun96/go-av1/internal/bitstream"
	"github.com/zesun96/go-av1/internal/header"
)

// buildReducedStillSeqHdr writes a minimal reduced-still-picture sequence
// header for the given profile / HBD / monochrome configuration. It
// returns the OBU payload (no trailing bytes appended beyond the spec's
// trailing_one_bit).
type seqOpts struct {
	profile       uint32
	stillPicture  bool
	hbd           uint32 // 0 or 1; ignored when profile != 2
	monochrome    bool
	withColorDesc bool
	pri           uint32
	trc           uint32
	mtrx          uint32
	colorRange    bool
}

func buildReducedStillSeqHdr(_ *testing.T, o seqOpts) []byte {
	w := newBitWriter()
	w.writeBits(o.profile, 3)
	// still_picture / reduced_still_picture_header
	if o.stillPicture {
		w.writeBit(1)
		w.writeBit(1)
	} else {
		w.writeBit(0)
		w.writeBit(0)
	}
	// If reduced still picture header, write 3+2 level bits + skip
	// timing/operating-points groups.
	if o.stillPicture {
		w.writeBits(0, 3) // major_level
		w.writeBits(0, 2) // minor_level
	} else {
		// timing_info_present=0, display_model_info_present=0,
		// num_operating_points_minus_1=0 + one operating point.
		w.writeBit(0)      // timing_info_present
		w.writeBit(0)      // display_model_info_present
		w.writeBits(0, 5)  // num_operating_points_minus_1
		w.writeBits(0, 12) // operating_point_idc
		w.writeBits(0, 3)  // major_level
		w.writeBits(0, 2)  // minor_level
		// major_level==2, so tier bit is NOT written.
	}
	// frame size bits (minimal: 1x1).
	w.writeBits(0, 4) // width_n_bits - 1 => 1 bit
	w.writeBits(0, 4) // height_n_bits - 1 => 1 bit
	w.writeBits(0, 1) // max_width_minus_1
	w.writeBits(0, 1) // max_height_minus_1
	if !o.stillPicture {
		w.writeBit(0) // frame_id_numbers_present
	}
	w.writeBit(0) // sb128
	w.writeBit(0) // filter_intra
	w.writeBit(0) // intra_edge_filter
	if !o.stillPicture {
		// inter_intra/masked_compound/warped_motion/dual_filter
		// (4 bits), order_hint=0 (skip jnt/mvs), screen_content_tools
		// path: write 1=>ADAPTIVE, force_integer_mv: 1=>ADAPTIVE.
		w.writeBits(0, 4)
		w.writeBit(0) // order_hint
		w.writeBit(1) // screen_content_tools => ADAPTIVE
		w.writeBit(1) // force_integer_mv => ADAPTIVE
		// order_hint=0, so order_hint_n_bits absent.
	}
	w.writeBit(0) // super_res
	w.writeBit(0) // cdef
	w.writeBit(0) // restoration
	// color_config().
	w.writeBit(o.hbd & 1) // hbd low bit
	if o.profile == 2 && o.hbd != 0 {
		// extra hbd bit, here always 0 (=> hbd=1 still).
		w.writeBit(0)
	}
	if o.profile != 1 {
		if o.monochrome {
			w.writeBit(1)
		} else {
			w.writeBit(0)
		}
	}
	if o.withColorDesc {
		w.writeBit(1)
		w.writeBits(o.pri, 8)
		w.writeBits(o.trc, 8)
		w.writeBits(o.mtrx, 8)
	} else {
		w.writeBit(0)
	}
	if o.monochrome {
		// color_range
		if o.colorRange {
			w.writeBit(1)
		} else {
			w.writeBit(0)
		}
	} else if o.withColorDesc && o.pri == uint32(header.ColorPriBT709) &&
		o.trc == uint32(header.TRCSRGB) && o.mtrx == uint32(header.MCIdentity) {
		// sRGB shortcut writes no further color bits.
	} else {
		// color_range + chr (if ss_hor & ss_ver).
		if o.colorRange {
			w.writeBit(1)
		} else {
			w.writeBit(0)
		}
		switch o.profile {
		case 0:
			w.writeBits(0, 2) // chr
		case 1:
			// no subsampling bits, no chr (ss_hor=0)
		case 2:
			if o.hbd == 2 {
				w.writeBits(0, 1) // ss_hor=0
				// ss_ver omitted
				// no chr
			} else {
				// ss_hor=1, ss_ver=0 implied (422); no chr.
			}
		}
	}
	if !o.monochrome {
		w.writeBit(0) // separate_uv_delta_q
	}
	w.writeBit(0) // film_grain_present
	return w.finishWithTrailingBit()
}

func TestParseSequenceHeader_ReducedStillMinimal(t *testing.T) {
	payload := buildReducedStillSeqHdr(t, seqOpts{
		profile: 0, stillPicture: true,
	})
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{StrictStdCompliance: true}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if hdr.Profile != 0 || !hdr.StillPicture || !hdr.ReducedStillPictureHeader {
		t.Fatalf("flags wrong: %+v", hdr)
	}
	if hdr.NumOperatingPoints != 1 {
		t.Fatalf("num operating points = %d", hdr.NumOperatingPoints)
	}
	if hdr.OperatingPoints[0].InitialDisplayDelay != 10 {
		t.Fatalf("default display delay = %d", hdr.OperatingPoints[0].InitialDisplayDelay)
	}
	if hdr.MaxWidth != 1 || hdr.MaxHeight != 1 {
		t.Fatalf("size = %dx%d", hdr.MaxWidth, hdr.MaxHeight)
	}
	if hdr.Layout != header.PixelLayoutI420 {
		t.Fatalf("layout = %d", hdr.Layout)
	}
	if hdr.ScreenContentTools != header.AdaptiveAdaptive ||
		hdr.ForceIntegerMV != header.AdaptiveAdaptive {
		t.Fatalf("adaptive defaults wrong: %+v", hdr)
	}
}

func TestParseSequenceHeader_FullPath(t *testing.T) {
	payload := buildReducedStillSeqHdr(t, seqOpts{profile: 0})
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if hdr.Profile != 0 || hdr.StillPicture || hdr.ReducedStillPictureHeader {
		t.Fatalf("flags wrong: %+v", hdr)
	}
	if hdr.NumOperatingPoints != 1 {
		t.Fatalf("num operating points = %d", hdr.NumOperatingPoints)
	}
	if hdr.OperatingPoints[0].MajorLevel != 2 {
		t.Fatalf("major_level = %d, want 2", hdr.OperatingPoints[0].MajorLevel)
	}
	if hdr.OperatingPoints[0].InitialDisplayDelay != 10 {
		t.Fatalf("display delay = %d", hdr.OperatingPoints[0].InitialDisplayDelay)
	}
	if hdr.ScreenContentTools != header.AdaptiveAdaptive ||
		hdr.ForceIntegerMV != header.AdaptiveAdaptive {
		t.Fatalf("screen/force tri-state wrong: %+v", hdr)
	}
}

func TestParseSequenceHeader_NilOut(t *testing.T) {
	if err := ParseSequenceHeader([]byte{0}, nil, ParseOptions{}); err == nil {
		t.Fatal("nil out should error")
	}
}

func TestParseSequenceHeader_InvalidProfile(t *testing.T) {
	w := newBitWriter()
	w.writeBits(3, 3) // profile=3 is invalid
	payload := w.finishWithTrailingBit()
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("err = %v, want ErrInvalidProfile", err)
	}
}

func TestParseSequenceHeader_ReducedWithoutStill(t *testing.T) {
	w := newBitWriter()
	w.writeBits(0, 3) // profile=0
	w.writeBit(0)     // still_picture=0
	w.writeBit(1)     // reduced_still_picture_header=1 (invalid)
	payload := w.finishWithTrailingBit()
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); !errors.Is(err, ErrReducedStillRequiresStill) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseSequenceHeader_ShortBuffer(t *testing.T) {
	// Only one byte; cannot complete the header.
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader([]byte{0x00}, &hdr, ParseOptions{}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err = %v, want ErrShortBuffer", err)
	}
}

func TestParseSequenceHeader_TrailingBitsStrict(t *testing.T) {
	payload := buildReducedStillSeqHdr(t, seqOpts{profile: 0, stillPicture: true})
	// Corrupt: append a non-zero trailing byte.
	bad := append([]byte{}, payload...)
	bad = append(bad, 0xFF)
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(bad, &hdr, ParseOptions{StrictStdCompliance: true}); !errors.Is(err, ErrTrailingBits) {
		t.Fatalf("err = %v, want ErrTrailingBits", err)
	}
	// Non-strict accepts the same payload.
	if err := ParseSequenceHeader(bad, &hdr, ParseOptions{}); err != nil {
		t.Fatalf("non-strict err = %v", err)
	}
}

func TestParseSequenceHeader_TimingInfoZeroStrict(t *testing.T) {
	// Build a non-reduced header with timing_info_present=1 and zero
	// num_units_in_tick / time_scale to trigger the strict check.
	w := newBitWriter()
	w.writeBits(0, 3) // profile=0
	w.writeBit(0)     // still_picture
	w.writeBit(0)     // reduced
	w.writeBit(1)     // timing_info_present
	w.writeBits(0, 32)
	w.writeBits(0, 32)
	payload := w.bytes()
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{StrictStdCompliance: true}); !errors.Is(err, ErrZeroTickInStrict) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseSequenceHeader_MonochromePath(t *testing.T) {
	payload := buildReducedStillSeqHdr(t, seqOpts{
		profile: 0, stillPicture: true,
		monochrome: true, colorRange: true,
	})
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !hdr.Monochrome || hdr.Layout != header.PixelLayoutI400 {
		t.Fatalf("monochrome flags wrong: %+v", hdr)
	}
	if hdr.SsHor != 1 || hdr.SsVer != 1 {
		t.Fatalf("ss = (%d,%d)", hdr.SsHor, hdr.SsVer)
	}
	if !hdr.ColorRange {
		t.Fatalf("color_range should be true")
	}
}

func TestParseSequenceHeader_ColorDescPresent(t *testing.T) {
	payload := buildReducedStillSeqHdr(t, seqOpts{
		profile: 0, stillPicture: true,
		withColorDesc: true,
		pri:           uint32(header.ColorPriBT2020),
		trc:           uint32(header.TRCSMPTE2084),
		mtrx:          uint32(header.MCBT2020NCL),
	})
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if hdr.Pri != header.ColorPriBT2020 || hdr.TRC != header.TRCSMPTE2084 ||
		hdr.Mtrx != header.MCBT2020NCL {
		t.Fatalf("color desc lost: %+v", hdr)
	}
}

func TestParseSequenceHeader_StrictMtrxIdentityRejectsSubsampled(t *testing.T) {
	// profile=0 forces 4:2:0; declaring mtrx=IDENTITY in strict mode must
	// fail because the spec requires 4:4:4 for the BT709/sRGB/Identity
	// shortcut.
	payload := buildReducedStillSeqHdr(t, seqOpts{
		profile: 0, stillPicture: true,
		withColorDesc: true,
		pri:           uint32(header.ColorPriBT2020), // not BT709 -> avoids the sRGB shortcut
		trc:           uint32(header.TRCSMPTE2084),
		mtrx:          uint32(header.MCIdentity),
	})
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{StrictStdCompliance: true}); !errors.Is(err, ErrMonochromeIdentityInvalid) {
		t.Fatalf("err = %v", err)
	}
}

// writeMinimalTail writes the post-color-config bytes: separate_uv_delta_q
// (if not monochrome) and film_grain_present, then a trailing one bit.
func writeMinimalTail(w *bitWriter, monochrome bool) {
	if !monochrome {
		w.writeBit(0)
	}
	w.writeBit(0)
}

// writeFullPathPrefix writes the common prefix for non-reduced sequence
// headers up to and including super_res/cdef/restoration. The caller
// controls order_hint / screen_content_tools / force_integer_mv via the
// inline parameters.
func writeFullPathPrefix(w *bitWriter, orderHint, sctIsAdaptive, sctOn, fimvOn uint32, scIsOff bool) {
	w.writeBits(0, 3)     // profile=0
	w.writeBit(0)         // still_picture
	w.writeBit(0)         // reduced_still_picture_header
	w.writeBit(0)         // timing_info_present
	w.writeBit(0)         // display_model_info_present
	w.writeBits(0, 5)     // num_operating_points_minus_1
	w.writeBits(0, 12)    // op_idc
	w.writeBits(0, 3)     // major_level
	w.writeBits(0, 2)     // minor_level
	w.writeBits(0, 4)     // width_n_bits-1
	w.writeBits(0, 4)     // height_n_bits-1
	w.writeBits(0, 1)     // max_w-1
	w.writeBits(0, 1)     // max_h-1
	w.writeBit(0)         // frame_id_numbers_present
	w.writeBit(0)         // sb128
	w.writeBit(0)         // filter_intra
	w.writeBit(0)         // intra_edge_filter
	w.writeBits(0, 4)     // inter_intra/masked/warped/dual
	w.writeBit(orderHint) // order_hint
	if orderHint != 0 {
		w.writeBit(0) // jnt_comp
		w.writeBit(0) // ref_frame_mvs
	}
	if sctIsAdaptive != 0 {
		w.writeBit(1)
	} else {
		w.writeBit(0)
		w.writeBit(sctOn)
	}
	if !scIsOff {
		w.writeBit(0)      // force_integer_mv => OFF or ON
		w.writeBit(fimvOn) // (only when first bit was 0)
	}
	if orderHint != 0 {
		w.writeBits(0, 3) // order_hint_n_bits-1
	}
	w.writeBit(0) // super_res
	w.writeBit(0) // cdef
	w.writeBit(0) // restoration
}

func TestParseSequenceHeader_OrderHintEnabled(t *testing.T) {
	w := newBitWriter()
	writeFullPathPrefix(w, 1, 1, 0, 0, false)
	// color_config: hbd=0, mono=0, no color desc, profile=0 default
	w.writeBit(0)     // hbd
	w.writeBit(0)     // monochrome
	w.writeBit(0)     // color_description_present
	w.writeBit(0)     // color_range
	w.writeBits(0, 2) // chr
	writeMinimalTail(w, false)
	payload := w.finishWithTrailingBit()

	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !hdr.OrderHint || hdr.OrderHintNBits != 1 {
		t.Fatalf("order_hint=%v nbits=%d", hdr.OrderHint, hdr.OrderHintNBits)
	}
}

func TestParseSequenceHeader_ScreenContentToolsOff(t *testing.T) {
	w := newBitWriter()
	writeFullPathPrefix(w, 0, 0, 0, 0, true)
	// color_config minimal
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(0, 2)
	writeMinimalTail(w, false)
	payload := w.finishWithTrailingBit()

	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if hdr.ScreenContentTools != header.AdaptiveOff {
		t.Fatalf("sct = %v", hdr.ScreenContentTools)
	}
	if hdr.ForceIntegerMV != header.AdaptiveAdaptive {
		t.Fatalf("force_integer_mv default should be ADAPTIVE when sct=OFF, got %v", hdr.ForceIntegerMV)
	}
}

func TestParseSequenceHeader_ScreenContentToolsOnFiMvOn(t *testing.T) {
	w := newBitWriter()
	writeFullPathPrefix(w, 0, 0, 1, 1, false)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(0, 2)
	writeMinimalTail(w, false)
	payload := w.finishWithTrailingBit()

	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if hdr.ScreenContentTools != header.AdaptiveOn {
		t.Fatalf("sct = %v", hdr.ScreenContentTools)
	}
	if hdr.ForceIntegerMV != header.AdaptiveOn {
		t.Fatalf("force_integer_mv = %v", hdr.ForceIntegerMV)
	}
}

func TestParseSequenceHeader_TimingFullPath(t *testing.T) {
	w := newBitWriter()
	w.writeBits(0, 3) // profile=0
	w.writeBit(0)     // still
	w.writeBit(0)     // reduced
	w.writeBit(1)     // timing_info_present
	w.writeBits(1, 32)
	w.writeBits(1, 32)
	w.writeBit(1) // equal_picture_interval
	w.writeVLC(2)
	w.writeBit(1)      // decoder_model_info_present
	w.writeBits(4, 5)  // encoder_decoder_buffer_delay_length - 1 = 4 (=> 5)
	w.writeBits(1, 32) // num_units_in_decoding_tick
	w.writeBits(0, 5)  // buffer_removal_delay_length - 1
	w.writeBits(0, 5)  // frame_presentation_delay_length - 1
	w.writeBit(1)      // display_model_info_present
	w.writeBits(0, 5)  // num_operating_points_minus_1 -> 1 op
	w.writeBits(0, 12) // idc
	w.writeBits(2, 3)  // major_level: +2 = 4 (>3 so tier follows)
	w.writeBits(0, 2)  // minor_level
	w.writeBit(0)      // tier
	w.writeBit(1)      // decoder_model_param_present
	w.writeBits(0, 5)  // decoder_buffer_delay (5 bits)
	w.writeBits(0, 5)  // encoder_buffer_delay
	w.writeBit(0)      // low_delay_mode
	w.writeBit(1)      // display_model_param_present
	w.writeBits(3, 4)  // initial_display_delay_minus_1 => 4
	// frame size
	w.writeBits(0, 4)
	w.writeBits(0, 4)
	w.writeBits(0, 1)
	w.writeBits(0, 1)
	w.writeBit(1) // frame_id_numbers_present
	w.writeBits(0, 4)
	w.writeBits(0, 3)
	w.writeBit(0) // sb128
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(0, 4)
	w.writeBit(0)     // order_hint
	w.writeBit(1)     // sct adaptive
	w.writeBit(1)     // fimv adaptive (because sct != OFF)
	w.writeBit(0)     // super_res
	w.writeBit(0)     // cdef
	w.writeBit(0)     // restoration
	w.writeBit(0)     // hbd
	w.writeBit(0)     // mono
	w.writeBit(0)     // color_desc_present
	w.writeBit(0)     // color_range
	w.writeBits(0, 2) // chr
	w.writeBit(0)     // separate_uv_delta_q
	w.writeBit(0)     // film_grain_present
	payload := w.finishWithTrailingBit()

	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !hdr.TimingInfoPresent || !hdr.EqualPictureInterval ||
		hdr.NumTicksPerPicture != 3 || !hdr.DecoderModelInfoPresent ||
		hdr.EncoderDecoderBufferDelayLength != 5 || !hdr.FrameIDNumbersPresent {
		t.Fatalf("timing parse mismatch: %+v", hdr)
	}
	if hdr.OperatingPoints[0].MajorLevel != 4 || hdr.OperatingPoints[0].Tier != 0 {
		t.Fatalf("op[0] = %+v", hdr.OperatingPoints[0])
	}
	if !hdr.OperatingPoints[0].DecoderModelParamPresent ||
		!hdr.OperatingPoints[0].DisplayModelParamPresent {
		t.Fatalf("op[0] decoder/display params lost")
	}
	if hdr.OperatingPoints[0].InitialDisplayDelay != 4 {
		t.Fatalf("display delay = %d", hdr.OperatingPoints[0].InitialDisplayDelay)
	}
}

func TestParseSequenceHeader_InvalidOpIDC(t *testing.T) {
	w := newBitWriter()
	w.writeBits(0, 3)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // timing_info_present
	w.writeBit(0)
	w.writeBits(0, 5)      // num_op-1=0
	w.writeBits(0x001, 12) // bad idc: low byte zero, high nibble non-zero? actually low byte!=0 but high nibble == 0
	payload := w.finishWithTrailingBit()
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); !errors.Is(err, ErrInvalidOperatingPointIDC) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseSequenceHeader_DecodingTickZeroStrict(t *testing.T) {
	w := newBitWriter()
	w.writeBits(0, 3)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)      // timing_info_present
	w.writeBits(1, 32) // num_units_in_tick
	w.writeBits(1, 32) // time_scale
	w.writeBit(0)      // equal_picture_interval
	w.writeBit(1)      // decoder_model_info_present
	w.writeBits(0, 5)
	w.writeBits(0, 32) // num_units_in_decoding_tick = 0 -> strict error
	payload := w.finishWithTrailingBit()
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{StrictStdCompliance: true}); !errors.Is(err, ErrZeroDecodingTickInStrict) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseSequenceHeader_NumTicksOverflow(t *testing.T) {
	w := newBitWriter()
	w.writeBits(0, 3)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)      // timing_info_present
	w.writeBits(1, 32) // num_units_in_tick
	w.writeBits(1, 32) // time_scale
	w.writeBit(1)      // equal_picture_interval
	// 32 leading zeros to trigger VLC overflow (=0xFFFFFFFF return)
	for i := 0; i < 32; i++ {
		w.writeBit(0)
	}
	w.writeBit(1)
	payload := w.finishWithTrailingBit()
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); !errors.Is(err, ErrInvalidNumTicksPerPicture) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseSequenceHeader_SRGBShortcutProfile1(t *testing.T) {
	w := newBitWriter()
	w.writeBits(1, 3) // profile=1
	w.writeBit(0)     // still
	w.writeBit(0)     // reduced
	w.writeBit(0)     // timing_info_present
	w.writeBit(0)     // display_model_info_present
	w.writeBits(0, 5) // num_op-1
	w.writeBits(0, 12)
	w.writeBits(0, 3)
	w.writeBits(0, 2)
	w.writeBits(0, 4)
	w.writeBits(0, 4)
	w.writeBits(0, 1)
	w.writeBits(0, 1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(0, 4)
	w.writeBit(0) // order_hint
	w.writeBit(1) // sct adaptive
	w.writeBit(1) // fimv adaptive
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // hbd=0
	// profile==1 => monochrome bit NOT written, monochrome=false implicit
	w.writeBit(1) // color_description_present
	w.writeBits(uint32(header.ColorPriBT709), 8)
	w.writeBits(uint32(header.TRCSRGB), 8)
	w.writeBits(uint32(header.MCIdentity), 8)
	// sRGB shortcut: no color_range / subsampling / chr bits.
	w.writeBit(0) // separate_uv_delta_q
	w.writeBit(0) // film_grain_present
	payload := w.finishWithTrailingBit()

	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if hdr.Layout != header.PixelLayoutI444 || !hdr.ColorRange {
		t.Fatalf("sRGB shortcut wrong: %+v", hdr)
	}
}

func TestParseSequenceHeader_SRGBShortcutInvalidProfile0(t *testing.T) {
	payload := buildReducedStillSeqHdr(t, seqOpts{
		profile: 0, stillPicture: true,
		withColorDesc: true,
		pri:           uint32(header.ColorPriBT709),
		trc:           uint32(header.TRCSRGB),
		mtrx:          uint32(header.MCIdentity),
	})
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); !errors.Is(err, ErrInvalidColorConfig) {
		t.Fatalf("err = %v, want ErrInvalidColorConfig", err)
	}
}

func TestCheckTrailingBits_ShortBuffer(t *testing.T) {
	// Drop the trailing byte from a valid payload so the trailing_one_bit
	// read overruns and yields ErrShortBuffer.
	payload := buildReducedStillSeqHdr(t, seqOpts{profile: 0, stillPicture: true})
	trunc := payload[:len(payload)-1]
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(trunc, &hdr, ParseOptions{}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err = %v, want ErrShortBuffer", err)
	}
}

// TestCheckTrailingBits_DirectShortBuffer exercises checkTrailingBits when
// the bit reader is already empty (so the trailing-one-bit read itself
// flags an error). The wrapper test above stops earlier in ParseSequenceHeader
// before reaching checkTrailingBits.
func TestCheckTrailingBits_DirectShortBuffer(t *testing.T) {
	gb := bitstream.NewGetBits(nil)
	if err := checkTrailingBits(gb, false); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err = %v, want ErrShortBuffer", err)
	}
}

// TestCheckTrailingBits_StateNonzeroStrict feeds a payload whose trailing
// one-bit is followed by a non-zero bit inside the same byte; strict mode
// must reject it via ErrTrailingBits.
func TestCheckTrailingBits_StateNonzeroStrict(t *testing.T) {
	// 0b10000001: trailing_one_bit=1, then 6 zeros, then a stray 1.
	gb := bitstream.NewGetBits([]byte{0x81})
	if err := checkTrailingBits(gb, true); !errors.Is(err, ErrTrailingBits) {
		t.Fatalf("err = %v, want ErrTrailingBits", err)
	}
}

// TestParseSequenceHeader_Profile1NonSRGB exercises the profile=1 branch of
// the color_config() layout switch (always I444, no subsampling bits).
func TestParseSequenceHeader_Profile1NonSRGB(t *testing.T) {
	payload := buildReducedStillSeqHdr(t, seqOpts{
		profile:       1,
		withColorDesc: true,
		pri:           uint32(header.ColorPriUnknown),
		trc:           uint32(header.TRCUnknown),
		mtrx:          uint32(header.MCUnknown),
	})
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if hdr.Profile != 1 || hdr.Layout != header.PixelLayoutI444 {
		t.Fatalf("profile=%d layout=%v", hdr.Profile, hdr.Layout)
	}
	if hdr.SsHor != 0 || hdr.SsVer != 0 {
		t.Fatalf("ss = %d/%d, want 0/0", hdr.SsHor, hdr.SsVer)
	}
}

// writeProfile2ColorTail writes the post-restoration bits for a non-reduced,
// profile=2 sequence header with the given hbd/sub-sampling settings. The
// caller must have written up to and including restoration bits already.
func writeProfile2ColorTail(w *bitWriter, hbd, ssHor, ssVer uint32, withChr bool, chr uint32) {
	// color_config(): hbd first bit + extra bit (profile==2 && hbd!=0).
	w.writeBit(1) // hbd low bit -> hbd>=1
	if hbd == 2 {
		w.writeBit(1)
	} else {
		w.writeBit(0)
	}
	w.writeBit(0) // monochrome=false (profile != 1)
	w.writeBit(0) // color_description_present=false
	w.writeBit(0) // color_range
	if hbd == 2 {
		w.writeBits(ssHor, 1)
		if ssHor != 0 {
			w.writeBits(ssVer, 1)
		}
	}
	// ss_hor==1 && ss_ver==1 -> chr is read; otherwise chr is implicit.
	if ssHor != 0 && ssVer != 0 {
		w.writeBits(chr, 2)
	}
	w.writeBit(0) // separate_uv_delta_q
	_ = withChr
	w.writeBit(0) // film_grain_present
}

// writeProfile2Prefix writes the bytes from start-of-payload up to the first
// color_config() bit for a profile=2, non-reduced, single-op header.
func writeProfile2Prefix(w *bitWriter) {
	w.writeBits(2, 3) // profile=2
	w.writeBit(0)     // still
	w.writeBit(0)     // reduced
	w.writeBit(0)     // timing_info_present
	w.writeBit(0)     // display_model_info_present
	w.writeBits(0, 5) // num_op-1
	w.writeBits(0, 12)
	w.writeBits(0, 3) // major_level=2 (no tier)
	w.writeBits(0, 2)
	w.writeBits(0, 4) // width_n_bits
	w.writeBits(0, 4) // height_n_bits
	w.writeBits(0, 1)
	w.writeBits(0, 1)
	w.writeBit(0) // frame_id_numbers_present
	w.writeBit(0) // sb128
	w.writeBit(0) // filter_intra
	w.writeBit(0) // intra_edge_filter
	w.writeBits(0, 4)
	w.writeBit(0) // order_hint
	w.writeBit(1) // sct adaptive
	w.writeBit(1) // fimv adaptive
	w.writeBit(0) // super_res
	w.writeBit(0) // cdef
	w.writeBit(0) // restoration
}

// TestParseSequenceHeader_Profile2HBD2_I422 covers profile=2 hbd=2 ss_hor=1
// ss_ver=0 (I422) and the implicit ChromaUnknown branch.
func TestParseSequenceHeader_Profile2HBD2_I422(t *testing.T) {
	w := newBitWriter()
	writeProfile2Prefix(w)
	writeProfile2ColorTail(w, 2, 1, 0, false, 0)
	payload := w.finishWithTrailingBit()
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if hdr.HBD != 2 || hdr.Layout != header.PixelLayoutI422 ||
		hdr.Chr != header.ChromaUnknown {
		t.Fatalf("hbd=%d layout=%v chr=%v", hdr.HBD, hdr.Layout, hdr.Chr)
	}
}

// TestParseSequenceHeader_Profile2HBD2_I420WithChr covers profile=2 hbd=2
// ss_hor=1 ss_ver=1 (I420) plus an explicit chroma_sample_position.
func TestParseSequenceHeader_Profile2HBD2_I420WithChr(t *testing.T) {
	w := newBitWriter()
	writeProfile2Prefix(w)
	writeProfile2ColorTail(w, 2, 1, 1, true, uint32(header.ChromaColocated))
	payload := w.finishWithTrailingBit()
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if hdr.HBD != 2 || hdr.Layout != header.PixelLayoutI420 ||
		hdr.Chr != header.ChromaColocated {
		t.Fatalf("hbd=%d layout=%v chr=%v", hdr.HBD, hdr.Layout, hdr.Chr)
	}
}

// TestParseSequenceHeader_Profile2HBD2_I444 covers profile=2 hbd=2 ss_hor=0
// (so ss_ver is not read) -> I444.
func TestParseSequenceHeader_Profile2HBD2_I444(t *testing.T) {
	w := newBitWriter()
	writeProfile2Prefix(w)
	writeProfile2ColorTail(w, 2, 0, 0, false, 0)
	payload := w.finishWithTrailingBit()
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if hdr.HBD != 2 || hdr.Layout != header.PixelLayoutI444 {
		t.Fatalf("hbd=%d layout=%v", hdr.HBD, hdr.Layout)
	}
}

// TestParseSequenceHeader_Profile2HBD1_DefaultI422 covers the
// profile=2 hbd!=2 fallback that forces ss_hor=1 without reading any
// subsampling bits (yielding I422).
func TestParseSequenceHeader_Profile2HBD1_DefaultI422(t *testing.T) {
	w := newBitWriter()
	writeProfile2Prefix(w)
	writeProfile2ColorTail(w, 1, 0, 0, false, 0)
	payload := w.finishWithTrailingBit()
	var hdr header.SequenceHeader
	if err := ParseSequenceHeader(payload, &hdr, ParseOptions{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if hdr.HBD != 1 || hdr.Layout != header.PixelLayoutI422 ||
		hdr.SsHor == 0 || hdr.SsVer != 0 {
		t.Fatalf("hbd=%d layout=%v ss=%d/%d", hdr.HBD, hdr.Layout, hdr.SsHor, hdr.SsVer)
	}
}
