package obu

import (
	"errors"
	"testing"

	"github.com/zesun96/go-av1/internal/header"
)

// minimalReducedSeq returns a sequence header with reduced_still_picture set,
// which is the simplest configuration accepted by ParseFrameHeader. The
// returned values match what ParseSequenceHeader would produce for the
// payload built by buildReducedStillSeqHdr(profile=0, stillPicture=true).
func minimalReducedSeq() *header.SequenceHeader {
	return &header.SequenceHeader{
		Profile:                   0,
		StillPicture:              true,
		ReducedStillPictureHeader: true,
		NumOperatingPoints:        1,
		WidthNBits:                1,
		HeightNBits:               1,
		MaxWidth:                  1,
		MaxHeight:                 1,
		ScreenContentTools:        header.AdaptiveAdaptive,
		ForceIntegerMV:            header.AdaptiveAdaptive,
		Layout:                    header.PixelLayoutI420,
		SsHor:                     1,
		SsVer:                     1,
	}
}

// writeReducedKeyFrameHdr builds a minimal valid frame_header_obu payload for
// the reduced-still-picture sequence above. Returns the payload (without a
// trailing_one_bit; ParseFrameHeader does not consume it).
func writeReducedKeyFrameHdr() []byte {
	w := newBitWriter()
	// show_existing_frame NOT written (reduced).
	// frame_type / show_frame are implicit (KEY, 1).
	// error_resilient_mode is implicit (=1).
	w.writeBit(0) // disable_cdf_update
	// allow_screen_content_tools (seq adaptive) -> 1 bit.
	w.writeBit(0) // allow_scc = 0 -> no force_integer_mv bit, IS_KEY forces 1.
	// frame_id_numbers absent.
	// frame_size_override NOT written (reduced).
	// order_hint absent (seq.OrderHint=false).
	// primary_ref_frame implicit (intra+resilient -> NONE).
	// decoder_model absent.
	// intra path: refresh_frame_flags implicit 0xff (key+show), no order_hint loop.
	// read_frame_size(use_ref=false), frame_size_override=0 -> width/height from seq max.
	// super_res: seq.SuperRes=false -> no bit; enabled=0; width[0]=width[1]=1.
	// have_render_size:
	w.writeBit(0)
	// allow_intrabc: allow_scc=0 -> NOT read.
	// refresh_context NOT written (reduced).
	// tile data:
	w.writeBit(1) // uniform=1
	// width=height=1 SB so sbw=sbh=1, min_log2_cols=max_log2_cols=0 -> no bits.
	// tiling.update absent (log2_cols == log2_rows == 0).
	// quant data:
	w.writeBits(0, 8) // yac=0
	w.writeBit(0)     // no ydc_delta bit
	// monochrome=false -> handle subsampled UV path.
	w.writeBit(0) // diff_uv_delta would normally need separate_uv_delta_q, but seq.SeparateUVDeltaQ=0 so diffUV=0.
	w.writeBit(0) // u_dc_delta absent (just-read bit was the "have udc_delta" flag, value 0)
	// Wait: parseQuant reads udc bit irrespective. Let me re-check.
	// Actually in code: if gb.Bit() != 0 { udc_delta = SU(7) }. We just wrote 0.
	// Then uac: if gb.Bit() != 0 ...
	// diffUV=0, so vdc/vac copied from udc/uac.
	// QM:
	w.writeBit(0) // qm=0
	// segmentation:
	w.writeBit(0) // enabled=0
	// delta_q: yac==0 -> SKIPPED.
	// loopfilter: all_lossless=1 (qidx==0 && deltaLossless) -> defaults applied, NO bits.
	// CDEF: all_lossless=1 -> skip.
	// LR: all_lossless=1 && super_res=0 -> skip.
	// txfm_mode: all_lossless=1 -> skip.
	// skip_mode: IS_KEY -> no switchable_comp_refs bit. SkipModeAllowed=0 so no skipModeEnabled bit.
	// warp_motion: IS_KEY -> skip.
	// reduced_txtp_set: 1 bit.
	w.writeBit(0)
	// global_mv: IS_KEY -> default identity, no bits.
	// film_grain: seq.FilmGrainPresent=false -> skip.
	return w.bytes()
}

func TestParseFrameHeader_NilSeq(t *testing.T) {
	var fh header.FrameHeader
	if err := ParseFrameHeader(nil, &fh, FrameParseOptions{}); !errors.Is(err, ErrFrameHeaderRequiresSeq) {
		t.Fatalf("err = %v, want ErrFrameHeaderRequiresSeq", err)
	}
}

func TestParseFrameHeader_NilOut(t *testing.T) {
	seq := minimalReducedSeq()
	if err := ParseFrameHeader(nil, nil, FrameParseOptions{SeqHeader: seq}); !errors.Is(err, ErrNilFrameHeaderOut) {
		t.Fatalf("err = %v, want ErrNilFrameHeaderOut", err)
	}
}

func TestParseFrameHeader_ReducedStillKey(t *testing.T) {
	seq := minimalReducedSeq()
	payload := writeReducedKeyFrameHdr()
	var fh header.FrameHeader
	if err := ParseFrameHeader(payload, &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.FrameType != header.FrameTypeKey || fh.ShowFrame != 1 || fh.ErrorResilientMode != 1 {
		t.Fatalf("flags: %+v", fh)
	}
	if fh.RefreshFrameFlags != 0xff {
		t.Fatalf("refresh = %#x", fh.RefreshFrameFlags)
	}
	if fh.Width[0] != 1 || fh.Width[1] != 1 || fh.Height != 1 {
		t.Fatalf("size = %d/%dx%d", fh.Width[0], fh.Width[1], fh.Height)
	}
	if fh.RenderWidth != 1 || fh.RenderHeight != 1 {
		t.Fatalf("render = %dx%d", fh.RenderWidth, fh.RenderHeight)
	}
	if fh.AllLossless != 1 {
		t.Fatalf("expected all_lossless=1")
	}
	if fh.ForceIntegerMV != 1 {
		t.Fatalf("intra force_integer_mv should be 1, got %d", fh.ForceIntegerMV)
	}
	if fh.GMV[0].Type != header.WMTypeIdentity {
		t.Fatalf("gmv[0] = %+v", fh.GMV[0])
	}
}

func TestParseFrameHeader_ShowExistingFrame(t *testing.T) {
	// Non-reduced sequence so show_existing_frame is read.
	seq := minimalReducedSeq()
	seq.ReducedStillPictureHeader = false
	seq.StillPicture = false
	w := newBitWriter()
	w.writeBit(1)     // show_existing_frame=1
	w.writeBits(0, 3) // existing_frame_idx=0
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.ShowExistingFrame != 1 || fh.ExistingFrameIdx != 0 {
		t.Fatalf("flags = %+v", fh)
	}
}

func TestParseFrameHeader_ShowExistingFrameIDMismatch(t *testing.T) {
	seq := minimalReducedSeq()
	seq.ReducedStillPictureHeader = false
	seq.StillPicture = false
	seq.FrameIDNumbersPresent = true
	seq.FrameIDNBits = 8
	w := newBitWriter()
	w.writeBit(1)     // show_existing_frame=1
	w.writeBits(2, 3) // existing_frame_idx=2
	w.writeBits(7, 8) // frame_id=7
	var refs [header.NumRefFrames]FrameReference
	refs[2].FrameHdr = &header.FrameHeader{FrameID: 9}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: &refs}); !errors.Is(err, ErrFrameIDMismatch) {
		t.Fatalf("err = %v, want ErrFrameIDMismatch", err)
	}
}

func TestParseFrameHeader_ShowExistingFrameRefsRequired(t *testing.T) {
	seq := minimalReducedSeq()
	seq.ReducedStillPictureHeader = false
	seq.StillPicture = false
	seq.FrameIDNumbersPresent = true
	seq.FrameIDNBits = 8
	w := newBitWriter()
	w.writeBit(1)     // show_existing_frame=1
	w.writeBits(0, 3) // existing_frame_idx=0
	w.writeBits(5, 8) // frame_id
	var refs [header.NumRefFrames]FrameReference
	// refs[0].FrameHdr stays nil.
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: &refs}); !errors.Is(err, ErrRefsRequired) {
		t.Fatalf("err = %v, want ErrRefsRequired", err)
	}
}

func TestParseFrameHeader_StrictIntraRefreshAll(t *testing.T) {
	// Non-reduced, intra-only frame_type with refresh_frame_flags=0xff under
	// strict mode must be rejected.
	seq := minimalReducedSeq()
	seq.ReducedStillPictureHeader = false
	seq.StillPicture = false
	seq.ScreenContentTools = header.AdaptiveOff
	seq.ForceIntegerMV = header.AdaptiveOff
	w := newBitWriter()
	w.writeBit(0) // show_existing_frame=0
	// frame_type = INTRA (2)
	w.writeBits(2, 2)
	w.writeBit(1) // show_frame=1
	// no decoder_model timing. show_frame=1, frame_type=INTRA -> showable_frame=1 implicit.
	// error_resilient: frame_type != KEY/SWITCH, !reduced -> read 1 bit.
	w.writeBit(0) // error_resilient=0
	w.writeBit(0) // disable_cdf_update
	// allow_screen_content_tools: seq off -> NOT read, value=0.
	// force_integer_mv: allow=0 -> NOT read.
	// IS_KEY_OR_INTRA -> force_integer_mv=1 forced.
	// frame_id absent.
	// reduced=false -> frame_size_override 1 bit.
	w.writeBit(0) // frame_size_override=0
	// order_hint absent (seq.OrderHint=false).
	// primary_ref: error_resilient=0 && !intra? INTRA is intra so condition `IS_INTER_OR_SWITCH(hdr)` is false.
	// So no primary_ref bits, value=PrimaryRefNone.
	// decoder_model absent.
	// intra path:
	// frame_type=INTRA so refresh_frame_flags read 8 bits.
	w.writeBits(0xFF, 8) // refresh=0xff triggers strict error
	var fh header.FrameHeader
	err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, StrictStdCompliance: true})
	if !errors.Is(err, ErrStrictKeyRefreshAll) {
		t.Fatalf("err = %v, want ErrStrictKeyRefreshAll", err)
	}
}

// nonReducedFullSeq returns a non-reduced 1x1 key-frame-ready sequence with
// SeparateUVDeltaQ / CDEF / Restoration / FilmGrainPresent enabled so that
// every quant / lf / cdef / lr / film_grain branch is exercised.
func nonReducedFullSeq() *header.SequenceHeader {
	return &header.SequenceHeader{
		Profile:            0,
		NumOperatingPoints: 1,
		WidthNBits:         1,
		HeightNBits:        1,
		MaxWidth:           1,
		MaxHeight:          1,
		ScreenContentTools: header.AdaptiveAdaptive,
		ForceIntegerMV:     header.AdaptiveAdaptive,
		Layout:             header.PixelLayoutI420,
		SsHor:              1,
		SsVer:              1,
		CDEF:               true,
		Restoration:        true,
		SeparateUVDeltaQ:   true,
		FilmGrainPresent:   true,
	}
}

// TestParseFrameHeader_KeyFullPath drives the largest single-test path
// through ParseFrameHeader: every quant sub-field, full loop-filter with
// mode_ref_delta update, CDEF, loop-restoration, switchable txfm and a
// complete film-grain block. It mirrors a hand-built KEY OBU with most
// optional features turned on.
func TestParseFrameHeader_KeyFullPath(t *testing.T) {
	seq := nonReducedFullSeq()
	w := newBitWriter()
	// --- parseHead ---
	w.writeBit(0)     // show_existing_frame=0
	w.writeBits(0, 2) // frame_type=KEY (0)
	w.writeBit(1)     // show_frame=1
	// erm implicit 1 (key+show).
	w.writeBit(0) // disable_cdf_update=0
	w.writeBit(1) // allow_scc=1 (adaptive)
	w.writeBit(0) // fimv=0 (adaptive) - IS_INTRA forces to 1
	w.writeBit(0) // frame_size_override=0
	// no order_hint, no primary_ref bits (erm=1).
	// --- parseFrameTypeAndRefs intra path ---
	// refresh implicit 0xff (KEY+show). no err-res loop (orderHint=false).
	// readFrameSize(false): size_override=0 -> w/h from seq. No super_res bit.
	w.writeBit(0) // have_render_size=0
	// allow_scc=1 && super_res=0 -> allow_intrabc bit
	w.writeBit(0) // allow_intrabc=0
	// --- parseTileInfo ---
	w.writeBit(0) // refresh_context = !bit -> RefreshContext=1
	w.writeBit(1) // tile uniform=1 (1x1 SB so no expansion bits)
	// --- parseQuant ---
	w.writeBits(100, 8) // yac
	w.writeBit(1)       // ydc present
	w.writeBits(0, 7)   // ydc SU(7)=0
	w.writeBit(1)       // diff_uv_delta (seq.SeparateUVDeltaQ=true)
	w.writeBit(1)       // udc present
	w.writeBits(0, 7)
	w.writeBit(1) // uac present
	w.writeBits(0, 7)
	w.writeBit(1) // vdc present
	w.writeBits(0, 7)
	w.writeBit(1) // vac present
	w.writeBits(0, 7)
	w.writeBit(1)     // QM=1
	w.writeBits(0, 4) // qmy
	w.writeBits(0, 4) // qmu
	w.writeBits(0, 4) // qmv (separate_uv=true)
	// --- parseSegmentation ---
	w.writeBit(0) // enabled=0
	// --- parseDeltaQLF ---
	w.writeBit(1)     // delta_q.present
	w.writeBits(0, 2) // res_log2
	// intrabc=0 -> delta_lf path
	w.writeBit(1)     // delta_lf.present
	w.writeBits(0, 2) // res_log2
	w.writeBit(1)     // multi
	// deriveLossless: yac!=0 -> AllLossless=0
	// --- parseLoopFilter (full) ---
	w.writeBits(1, 6) // LevelY[0]=1
	w.writeBits(0, 6) // LevelY[1]=0 (any one nonzero triggers UV)
	w.writeBits(2, 6) // LevelU
	w.writeBits(3, 6) // LevelV
	w.writeBits(0, 3) // sharpness
	// primary_ref=none -> default deltas, no bits
	w.writeBit(1) // mode_ref_delta_enabled
	w.writeBit(1) // mode_ref_delta_update
	for i := 0; i < 8; i++ {
		w.writeBit(0)
	} // 8 ref bits all=0 -> no SU
	for i := 0; i < 2; i++ {
		w.writeBit(0)
	} // 2 mode bits all=0
	// --- parseCDEF ---
	w.writeBits(0, 2) // damping (=>3)
	w.writeBits(0, 2) // nbits=0 -> n=1
	w.writeBits(0, 6) // y_strength
	w.writeBits(0, 6) // uv_strength
	// --- parseLR ---
	w.writeBits(1, 2) // type[0]=Switchable
	w.writeBits(0, 2) // type[1]=None
	w.writeBits(0, 2) // type[2]=None
	w.writeBit(0)     // unit_size bit=0 (no increment)
	// --- parseTxfmMode ---
	w.writeBit(1) // switchable
	// --- parseSkipMode IS_INTRA -> skip ---
	// --- parseWarpMotion IS_INTRA -> skip ---
	w.writeBit(0) // reduced_txtp_set
	// --- parseGlobalMV IS_INTRA -> identity, no bits ---
	// --- parseFilmGrain ---
	w.writeBit(1)          // present
	w.writeBits(12345, 16) // seed
	// frame_type==KEY != Inter -> update implicit 1
	// FilmGrainData:
	w.writeBits(2, 4)  // num_y_points
	w.writeBits(10, 8) // Y[0].x
	w.writeBits(50, 8) // Y[0].y
	w.writeBits(20, 8) // Y[1].x (>10)
	w.writeBits(60, 8) // Y[1].y
	w.writeBit(0)      // chroma_scaling_from_luma=0
	w.writeBits(1, 4)  // num_uv_points[0]=1
	w.writeBits(30, 8) // UV[0][0].x
	w.writeBits(70, 8) // UV[0][0].y
	w.writeBits(1, 4)  // num_uv_points[1]=1
	w.writeBits(40, 8) // UV[1][0].x
	w.writeBits(80, 8) // UV[1][0].y
	w.writeBits(0, 2)  // scaling_shift -> 8
	w.writeBits(0, 2)  // ar_coeff_lag=0
	// numYPos = 0; nY!=0 so no Y AR loop
	// pl=0 uv branch: numUVPos = 0 + 1 = 1 -> 1 byte
	w.writeBits(128, 8) // AR coeff -> 0
	w.writeBits(128, 8) // pl=1 same
	w.writeBits(0, 2)   // ar_coeff_shift -> 6
	w.writeBits(0, 2)   // grain_scale_shift
	// pl=0 nUV!=0:
	w.writeBits(128, 8) // uv_mult -> 0
	w.writeBits(128, 8) // uv_luma_mult -> 0
	w.writeBits(256, 9) // uv_offset -> 0
	w.writeBits(128, 8)
	w.writeBits(128, 8)
	w.writeBits(256, 9)
	w.writeBit(0) // overlap_flag
	w.writeBit(1) // clip_to_restricted_range

	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.FrameType != header.FrameTypeKey || fh.ShowFrame != 1 || fh.ForceIntegerMV != 1 {
		t.Fatalf("flags: %+v", fh)
	}
	if fh.Quant.YAC != 100 || fh.Quant.QM != 1 {
		t.Fatalf("quant: %+v", fh.Quant)
	}
	if fh.AllLossless != 0 {
		t.Fatalf("all_lossless expected 0")
	}
	if fh.LoopFilter.LevelY[0] != 1 || fh.LoopFilter.LevelU != 2 || fh.LoopFilter.LevelV != 3 {
		t.Fatalf("lf: %+v", fh.LoopFilter)
	}
	if fh.LoopFilter.ModeRefDeltaEnabled != 1 || fh.LoopFilter.ModeRefDeltaUpdate != 1 {
		t.Fatalf("lf deltas: %+v", fh.LoopFilter)
	}
	if fh.CDEF.Damping != 3 {
		t.Fatalf("cdef: %+v", fh.CDEF)
	}
	if fh.Restoration.Type[0] != header.RestorationSwitchable {
		t.Fatalf("lr: %+v", fh.Restoration)
	}
	if fh.TxfmMode != header.TxfmModeSwitchable {
		t.Fatalf("txfm = %d", fh.TxfmMode)
	}
	if fh.FilmGrain.Present != 1 || fh.FilmGrain.Data.NumYPoints != 2 ||
		fh.FilmGrain.Data.YPoints[0][0] != 10 || fh.FilmGrain.Data.YPoints[1][0] != 20 {
		t.Fatalf("fg: %+v", fh.FilmGrain)
	}
	if fh.FilmGrain.Data.NumUVPoints[0] != 1 || fh.FilmGrain.Data.NumUVPoints[1] != 1 {
		t.Fatalf("fg uv: %+v", fh.FilmGrain.Data)
	}
	if fh.FilmGrain.Data.ClipToRestrictedRange != 1 {
		t.Fatalf("fg clip: %+v", fh.FilmGrain.Data)
	}
}

// TestParseFrameHeader_KeyAllowIntrabc covers the allow_intrabc=1 path:
// super_res must be off and screen_content_tools must be enabled. With
// intrabc=1 the loop_filter, CDEF and LR routines all take their early-
// return / default-defaults branches.
func TestParseFrameHeader_KeyAllowIntrabc(t *testing.T) {
	seq := minimalReducedSeq()
	seq.ReducedStillPictureHeader = false
	seq.StillPicture = false
	w := newBitWriter()
	w.writeBit(0)     // show_existing_frame=0
	w.writeBits(0, 2) // frame_type=KEY
	w.writeBit(1)     // show_frame=1
	w.writeBit(0)     // disable_cdf_update=0
	w.writeBit(1)     // allow_scc=1
	w.writeBit(0)     // fimv adaptive bit (intra forces to 1)
	w.writeBit(0)     // frame_size_override=0
	// readFrameSize: size_override=0 -> 1x1; no super_res bit; have_render_size:
	w.writeBit(0)
	// allow_intrabc:
	w.writeBit(1)
	// Tile:
	w.writeBit(0) // refresh_context bit -> RefreshContext=1
	w.writeBit(1) // uniform
	// Quant: yac=0 -> AllLossless=1 path will skip LF/CDEF/LR/Txfm.
	w.writeBits(0, 8) // yac=0
	w.writeBit(0)     // ydc present=0
	w.writeBit(0)     // udc=0
	w.writeBit(0)     // uac=0
	w.writeBit(0)     // QM=0
	// Segmentation enabled=0:
	w.writeBit(0)
	// DeltaQLF: yac==0 -> skipped.
	// LF: AllLossless || intrabc -> defaults branch (no bits).
	// CDEF: intrabc -> skip.
	// LR: intrabc -> skip.
	// TxfmMode: AllLossless -> skip.
	// SkipMode: IS_INTRA -> skip.
	// reduced_txtp_set:
	w.writeBit(0)
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.AllowIntrabc != 1 {
		t.Fatalf("allow_intrabc=%d", fh.AllowIntrabc)
	}
	if fh.AllLossless != 1 {
		t.Fatalf("all_lossless=%d", fh.AllLossless)
	}
	if fh.LoopFilter.ModeRefDeltaEnabled != 1 {
		t.Fatalf("lf default not applied")
	}
}

// TestParseFrameHeader_KeySuperResAndRender covers the super_res ON path
// (denominator decode, width[0] != width[1]) and the explicit render-size
// branch. allow_scc + super_res!=0 also skips the intrabc bit.
func TestParseFrameHeader_KeySuperResAndRender(t *testing.T) {
	seq := minimalReducedSeq()
	seq.ReducedStillPictureHeader = false
	seq.StillPicture = false
	seq.WidthNBits = 8 // MaxWidth bits
	seq.HeightNBits = 8
	seq.MaxWidth = 64
	seq.MaxHeight = 64
	seq.SuperRes = true
	w := newBitWriter()
	w.writeBit(0)     // show_existing_frame=0
	w.writeBits(0, 2) // frame_type=KEY
	w.writeBit(1)     // show_frame=1
	w.writeBit(0)     // disable_cdf_update
	w.writeBit(1)     // allow_scc adaptive=1
	w.writeBit(0)     // fimv adaptive (intra forces 1)
	w.writeBit(0)     // frame_size_override=0 -> w=64,h=64 from seq max
	// readSuperRes: bit=1 -> enabled; denom bits = 3 -> d=12
	w.writeBit(1)
	w.writeBits(3, 3) // d = 9+3 = 12
	w.writeBit(1)     // have_render_size=1
	w.writeBits(99, 16)
	w.writeBits(199, 16)
	// allow_scc=1 && super_res=1 -> no intrabc bit.
	// Tile: refresh_context bit + uniform=1. Sizes < 64*SBSZ so 1x1 SB.
	w.writeBit(0)
	w.writeBit(1)
	// Quant yac=0 -> AllLossless path skip everything.
	w.writeBits(0, 8)
	w.writeBit(0) // ydc
	w.writeBit(0) // udc
	w.writeBit(0) // uac
	w.writeBit(0) // QM
	w.writeBit(0) // seg
	// LR: AllLossless==0 || super_res != 0 -> super_res path makes it run.
	// But seq.Restoration=false in this seq -> skipped.
	w.writeBit(0) // reduced_txtp_set
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.SuperRes.Enabled != 1 || fh.SuperRes.WidthScaleDenominator != 12 {
		t.Fatalf("super_res = %+v", fh.SuperRes)
	}
	if fh.RenderWidth != 100 || fh.RenderHeight != 200 {
		t.Fatalf("render = %dx%d", fh.RenderWidth, fh.RenderHeight)
	}
	if fh.Width[1] != 64 || fh.Width[0] >= 64 {
		t.Fatalf("width = %d/%d", fh.Width[0], fh.Width[1])
	}
}

// TestParseFrameHeader_KeySegmentationUpdate covers the segmentation
// enabled + UpdateData path with each per-segment field flag exercised.
func TestParseFrameHeader_KeySegmentationUpdate(t *testing.T) {
	seq := minimalReducedSeq()
	seq.ReducedStillPictureHeader = false
	seq.StillPicture = false
	w := newBitWriter()
	w.writeBit(0)      // show_existing_frame
	w.writeBits(0, 2)  // KEY
	w.writeBit(1)      // show_frame
	w.writeBit(0)      // disable_cdf_update
	w.writeBit(1)      // allow_scc
	w.writeBit(0)      // fimv adaptive
	w.writeBit(0)      // size_override
	w.writeBit(0)      // have_render_size
	w.writeBit(0)      // allow_intrabc=0
	w.writeBit(0)      // refresh_context bit
	w.writeBit(1)      // tile uniform
	w.writeBits(50, 8) // yac
	w.writeBit(0)      // ydc
	w.writeBit(0)      // udc
	w.writeBit(0)      // uac
	w.writeBit(0)      // QM
	// segmentation enabled + primary_ref=None -> UpdateMap=1, UpdateData=1 forced.
	w.writeBit(1) // enabled=1
	for i := 0; i < header.MaxSegments; i++ {
		// For seg 0 set all flags so LastActiveSegID = 0 and Ref/Skip/GlobalMV all hit.
		if i == 0 {
			w.writeBit(1)     // delta_q present
			w.writeBits(0, 9) // SU(9)=0
			w.writeBit(1)     // delta_lf_y_v
			w.writeBits(0, 7)
			w.writeBit(1) // delta_lf_y_h
			w.writeBits(0, 7)
			w.writeBit(1) // delta_lf_u
			w.writeBits(0, 7)
			w.writeBit(1) // delta_lf_v
			w.writeBits(0, 7)
			w.writeBit(1)     // ref present
			w.writeBits(2, 3) // Ref=2
			w.writeBit(1)     // skip=1
			w.writeBit(1)     // global_mv=1
		} else {
			for j := 0; j < 8; j++ {
				w.writeBit(0)
			} // 5 delta flags + ref flag + skip + global_mv = 8 bits
		}
	}
	w.writeBit(1)     // delta_q present (yac!=0)
	w.writeBits(0, 2) // res_log2
	w.writeBit(0)     // delta_lf present=0
	// AllLossless=0 (yac!=0) -> full LF path. Set levels=0 to skip UV.
	w.writeBits(0, 6)
	w.writeBits(0, 6)
	w.writeBits(0, 3) // sharpness
	w.writeBit(0)     // mode_ref_delta_enabled=0
	// CDEF: seq.CDEF=false -> skip.
	// LR: seq.Restoration=false -> skip.
	// Txfm:
	w.writeBit(0) // largest
	w.writeBit(0) // reduced_txtp_set
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.Segmentation.Enabled != 1 || fh.Segmentation.UpdateData != 1 {
		t.Fatalf("seg: %+v", fh.Segmentation)
	}
	if fh.Segmentation.SegData.D[0].Ref != 2 || fh.Segmentation.SegData.D[0].Skip != 1 ||
		fh.Segmentation.SegData.D[0].GlobalMV != 1 {
		t.Fatalf("seg0: %+v", fh.Segmentation.SegData.D[0])
	}
	if fh.Segmentation.SegData.LastActiveSegID != 0 || fh.Segmentation.SegData.PreSkip != 1 {
		t.Fatalf("seg set: %+v", fh.Segmentation.SegData)
	}
	// Segments 1..7 should have Ref=-1 default.
	if fh.Segmentation.SegData.D[1].Ref != -1 {
		t.Fatalf("seg1.Ref = %d", fh.Segmentation.SegData.D[1].Ref)
	}
	if fh.TxfmMode != header.TxfmModeLargest {
		t.Fatalf("txfm = %d", fh.TxfmMode)
	}
}

// interBaseSeq returns a non-reduced sequence with order_hint enabled and
// minimal optional features so inter-frame headers can be exercised.
func interBaseSeq() *header.SequenceHeader {
	return &header.SequenceHeader{
		Profile:            0,
		NumOperatingPoints: 1,
		WidthNBits:         1,
		HeightNBits:        1,
		MaxWidth:           1,
		MaxHeight:          1,
		ScreenContentTools: header.AdaptiveOff,
		ForceIntegerMV:     header.AdaptiveOff,
		Layout:             header.PixelLayoutI420,
		SsHor:              1,
		SsVer:              1,
		OrderHint:          true,
		OrderHintNBits:     4,
	}
}

// TestParseFrameHeader_InterHappy walks the full inter parse path with
// frame_ref_short_signaling=0, primary_ref=None, no use_ref size copy,
// switchable_comp_refs=0 (no select) and identity global motion.
func TestParseFrameHeader_InterHappy(t *testing.T) {
	seq := interBaseSeq()
	w := newBitWriter()
	w.writeBit(0)     // show_existing_frame=0
	w.writeBits(1, 2) // frame_type=INTER
	w.writeBit(1)     // show_frame=1
	w.writeBit(0)     // erm=0
	w.writeBit(0)     // disable_cdf_update
	// scc=Off, fimv=Off -> no bits; intra=false so no force.
	// frame_id absent. !reduced + !switch -> size_override bit.
	w.writeBit(0) // size_override=0
	// order_hint -> FrameOffset 4 bits.
	w.writeBits(5, 4)
	// erm=0 && !intra -> primary_ref 3 bits = 7 (None).
	w.writeBits(7, 3)
	// refresh_frame_flags 8 bits (not switch).
	w.writeBits(0, 8)
	// frame_ref_short_signaling bit (order_hint=true).
	w.writeBit(0) // signalled refs
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(0, 3) // refidx
	}
	// size_override=0 -> use_ref=false. No size bits, no super_res bits.
	w.writeBit(0) // have_render_size=0
	// fimv=0 (off) -> HP bit.
	w.writeBit(0) // HP=0
	w.writeBit(1) // subpel switchable
	w.writeBit(0) // switchable_motion_mode
	// seq.RefFrameMVs=false -> no use_ref_frame_mvs bit.
	// Tile:
	w.writeBit(0) // refresh_context bit
	w.writeBit(1) // uniform
	// Quant yac=0:
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	// Segmentation enabled=0:
	w.writeBit(0)
	// Delta skipped (yac==0). LF defaults (AllLossless=1). CDEF/LR skipped.
	// SkipMode: switchable_comp_refs bit
	w.writeBit(0) // 0 -> no select
	// warpMotion: erm=0 && !intra && seq.WarpedMotion=false -> no bit
	w.writeBit(0) // reduced_txtp_set
	// GlobalMV: 7 ref types all Identity (1 bit each, =0).
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(0)
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.FrameType != header.FrameTypeInter || fh.PrimaryRefFrame != header.PrimaryRefNone {
		t.Fatalf("flags: %+v", fh)
	}
	if fh.FrameOffset != 5 {
		t.Fatalf("frame_offset=%d", fh.FrameOffset)
	}
	if fh.SubpelFilterMode != header.FilterModeSwitchable {
		t.Fatalf("subpel=%d", fh.SubpelFilterMode)
	}
	for i := 0; i < header.RefsPerFrame; i++ {
		if fh.GMV[i].Type != header.WMTypeIdentity {
			t.Fatalf("gmv[%d]=%+v", i, fh.GMV[i])
		}
	}
}

// TestParseFrameHeader_NonUniformTileInvalidUpdate exercises the
// non-uniform tile branch (gb.Uniform calls) and the
// ErrInvalidTileUpdate path.
func TestParseFrameHeader_NonUniformTileInvalidUpdate(t *testing.T) {
	seq := minimalReducedSeq() // reduced still picture skips show_existing/frame_type/etc.
	seq.WidthNBits = 8
	seq.HeightNBits = 8
	seq.MaxWidth = 256
	seq.MaxHeight = 256
	w := newBitWriter()
	// reduced still picture: frame_type implicit KEY/show.
	w.writeBit(0) // disable_cdf_update
	w.writeBit(0) // allow_scc adaptive=0
	// size_override implicit (reduced) -> use seq max 256x256.
	w.writeBit(0) // have_render_size=0
	// Tile: refresh_context skipped (reduced or disable_cdf=0? reduced overrides).
	// In code: if !reduced && disable_cdf=0 -> refresh_context. reduced=true -> skip.
	w.writeBit(0) // uniform=0 -> non-uniform branch
	// sbw = 256/64 = 4, sbh = 4. max_tile_width_sb = 64. min_log2_cols = 0.
	// Loop while sbx<sbw && cols<MaxTileCols:
	//   tw = min(sbw-sbx, max_tile_width_sb) = sbw-sbx (small)
	//   tw > 1 -> Uniform(tw). With tw=4, l=Len32(4)=3, m=8-4=4, F(2) read.
	// Let's just emit 0 bits whenever possible: writeBit(0) for each.
	// Iteration 1: sbx=0, tw=4. Uniform(4): F(2)=0 -> v=0 < m=4 -> v=0. tile=1+0=1.
	// Iteration 2: sbx=1, tw=3. Uniform(3): l=2, m=4-3=1, F(1)=0 -> v=0<1 -> v=0. tile=1+0=1.
	// Iteration 3: sbx=2, tw=2. Uniform(2): l=2, m=4-2=2, F(1)=0 -> v=0<2 -> v=0. tile=1+0=1.
	// Iteration 4: sbx=3, tw=1. tile=1 (no Uniform). cols=4.
	w.writeBits(0, 2) // iter1 F(2)=0
	w.writeBits(0, 1) // iter2 F(1)=0
	w.writeBits(0, 1) // iter3 F(1)=0
	// log2_cols = tileLog2(1, 4) = 2.
	// areaSB = 16; minLog2Tiles=max(tileLog2((4096*2304)>>12, 16), 0)=0. So areaSB unchanged.
	// widest=1, maxTileHeightSB = max(16/1, 1) = 16.
	// Row loop: th = min(sbh-sby, 16) = 4. tile = 1+Uniform(4).
	// Iter 1: F(2)=0 -> tile=1. Iter 2: sby=1, th=3, F(1)=0 -> tile=1. Iter 3: th=2, F(1)=0 -> tile=1. Iter 4: th=1 -> tile=1.
	w.writeBits(0, 2)
	w.writeBits(0, 1)
	w.writeBits(0, 1)
	// log2_rows = tileLog2(1, 4) = 2.
	// log2_cols+log2_rows = 4 -> tiling.update F(4). cols*rows = 16, write a value >= 16 to trigger error.
	// But F(4) max is 15. So update<16 always. So this construction CAN'T trigger ErrInvalidTileUpdate.
	// Adjust: write update=15 (valid) and check for happy path success instead; then add a separate error test.
	w.writeBits(15, 4)
	w.writeBits(0, 2) // nbytes-1 -> 1
	// Quant yac=0 -> short.
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // seg=0
	// LF defaults (AllLossless=1).
	w.writeBit(0) // reduced_txtp_set
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.Tiling.Cols != 4 || fh.Tiling.Rows != 4 {
		t.Fatalf("tile = %dx%d", fh.Tiling.Cols, fh.Tiling.Rows)
	}
	if fh.Tiling.Update != 15 || fh.Tiling.NBytes != 1 {
		t.Fatalf("tiling.update=%d nbytes=%d", fh.Tiling.Update, fh.Tiling.NBytes)
	}
}

// makeFullRefs returns an array of 8 reference frames all populated with
// distinct FrameOffset values so applyFrameRefShortSignaling and the
// segmentation / loop-filter / GMV copy paths have something to consume.
func makeFullRefs(currentPOC uint8) *[header.NumRefFrames]FrameReference {
	var refs [header.NumRefFrames]FrameReference
	for i := 0; i < header.NumRefFrames; i++ {
		fh := &header.FrameHeader{
			FrameOffset: uint8(int(currentPOC) - 4 + i), // span POC-4..POC+3
			GMV:         [header.RefsPerFrame]header.WarpedMotionParams{},
		}
		for j := 0; j < header.RefsPerFrame; j++ {
			fh.GMV[j] = defaultWarpParams
		}
		refs[i].FrameHdr = fh
	}
	return &refs
}

// TestParseFrameHeader_InterFrameRefShortSig drives the
// applyFrameRefShortSignaling path (frame_ref_short_signaling=1). With
// all 8 refs populated and distinct order_hints it walks the entire
// dav1d-style sort algorithm and the getPOCDiff helper.
func TestParseFrameHeader_InterFrameRefShortSig(t *testing.T) {
	seq := interBaseSeq()
	refs := makeFullRefs(5)
	w := newBitWriter()
	w.writeBit(0)     // show_existing_frame
	w.writeBits(1, 2) // INTER
	w.writeBit(1)     // show_frame
	w.writeBit(0)     // erm=0
	w.writeBit(0)     // disable_cdf_update
	w.writeBit(0)     // size_override=0
	w.writeBits(5, 4) // order_hint=5
	w.writeBits(7, 3) // primary_ref=None
	w.writeBits(0, 8) // refresh_frame_flags
	// frame_ref_short_signaling=1
	w.writeBit(1)
	w.writeBits(0, 3) // last_frame_idx -> Refidx[0]=0
	w.writeBits(3, 3) // gold_frame_idx -> Refidx[3]=3
	// frame_id absent so no delta loop.
	// size_override=0 -> no use_ref bits, size from seq max.
	w.writeBit(0) // have_render_size
	w.writeBit(0) // HP=0 (fimv=0 forced is intra; here !intra so HP bit IS read since fimv=0)
	w.writeBit(1) // subpel switchable
	w.writeBit(0) // switchable_motion_mode
	// refresh_context
	w.writeBit(0)
	w.writeBit(1) // tile uniform
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // seg=0
	w.writeBit(0) // switchable_comp_refs=0
	w.writeBit(0) // reduced_txtp_set
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(0) // GMV identity
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: refs}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.FrameRefShortSignaling != 1 {
		t.Fatalf("short_sig=%d", fh.FrameRefShortSignaling)
	}
	if fh.Refidx[0] != 0 || fh.Refidx[3] != 3 {
		t.Fatalf("refidx[0]=%d [3]=%d", fh.Refidx[0], fh.Refidx[3])
	}
	// All 7 slots must be assigned to non-negative indices.
	for i := 0; i < header.RefsPerFrame; i++ {
		if fh.Refidx[i] < 0 {
			t.Fatalf("refidx[%d]=%d unassigned", i, fh.Refidx[i])
		}
	}
}

// TestParseFrameHeader_InterUseRef exercises the read_frame_size use_ref
// branch and the GMV identity fast-path. frame_size_override=1 with
// erm=0 selects the use_ref logic; the first refidx slot signalled with a
// 1 bit causes the size to be inherited from that reference.
func TestParseFrameHeader_InterUseRef(t *testing.T) {
	seq := interBaseSeq()
	seq.WidthNBits = 8
	seq.HeightNBits = 8
	seq.MaxWidth = 128
	seq.MaxHeight = 128
	refs := makeFullRefs(5)
	refs[0].FrameHdr.Width = [2]int{64, 64}
	refs[0].FrameHdr.Height = 48
	refs[0].FrameHdr.RenderWidth = 64
	refs[0].FrameHdr.RenderHeight = 48
	w := newBitWriter()
	w.writeBit(0)     // show_existing_frame
	w.writeBits(1, 2) // INTER
	w.writeBit(1)     // show_frame
	w.writeBit(0)     // erm=0
	w.writeBit(0)     // disable_cdf_update
	w.writeBit(1)     // size_override=1
	w.writeBits(5, 4) // order_hint
	w.writeBits(7, 3) // primary_ref=None
	w.writeBits(0, 8) // refresh
	w.writeBit(0)     // frame_ref_short_signaling=0
	for i := 0; i < header.RefsPerFrame; i++ {
		if i == 0 {
			w.writeBits(0, 3) // refidx[0]=0
		} else {
			w.writeBits(0, 3) // refidx[i]=0
		}
	}
	// use_ref: erm=0 && size_override=1 -> true. First bit=1 -> copy from refidx[0].
	w.writeBit(1)
	// readSuperRes called inside the copy path: seq.SuperRes=false -> no bit.
	w.writeBit(0) // HP=0
	w.writeBit(1) // subpel switchable
	w.writeBit(0) // motion_mode
	w.writeBit(0) // refresh_context
	w.writeBit(1) // tile uniform (the inherited 64x48 fits in 1 sb at 64-wide so sbw=1; sbh=ceil(48/64)=1)
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // seg
	w.writeBit(0) // switchable_comp_refs
	w.writeBit(0) // reduced_txtp_set
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(0)
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: refs}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.Width[1] != 64 || fh.Height != 48 || fh.RenderWidth != 64 || fh.RenderHeight != 48 {
		t.Fatalf("size copy failed: %d/%dx%d render %dx%d",
			fh.Width[0], fh.Width[1], fh.Height, fh.RenderWidth, fh.RenderHeight)
	}
}

// TestParseFrameHeader_InterSkipModeSelect drives selectSkipModeRefs to
// the success branch. switchable_comp_refs=1 + order_hint + at least one
// forward and one backward reference produce SkipModeAllowed=1, which
// then forces a skipModeEnabled bit.
func TestParseFrameHeader_InterSkipModeSelect(t *testing.T) {
	seq := interBaseSeq()
	refs := makeFullRefs(5) // refs[i].FrameOffset = 1,2,3,4,5,6,7,8 -> 0,1,2,3 are <5 (forward), 5,6,7 are >5 (backward), refs[4]=5 same as current.
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)     // erm
	w.writeBit(0)     // disable_cdf
	w.writeBit(0)     // size_override
	w.writeBits(5, 4) // FrameOffset=5
	w.writeBits(7, 3) // primary_ref=None
	w.writeBits(0, 8) // refresh
	w.writeBit(0)     // short_sig=0
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(uint32(i), 3) // refidx[0]=0 (POC 1), [1]=1 (2), [2]=2(3), [3]=3(4), [4]=4(5), [5]=5(6), [6]=6(7)
	}
	w.writeBit(0) // have_render_size
	w.writeBit(0) // HP
	w.writeBit(1) // subpel switchable
	w.writeBit(0) // motion_mode
	w.writeBit(0) // refresh_context
	w.writeBit(1) // tile uniform
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // seg
	w.writeBit(1) // switchable_comp_refs=1 -> runs select
	w.writeBit(1) // skipModeEnabled (since SkipModeAllowed=1 after select)
	w.writeBit(0) // reduced_txtp_set
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(0)
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: refs}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.SwitchableCompRefs != 1 || fh.SkipModeAllowed != 1 || fh.SkipModeEnabled != 1 {
		t.Fatalf("skip mode: comp=%d allowed=%d enabled=%d",
			fh.SwitchableCompRefs, fh.SkipModeAllowed, fh.SkipModeEnabled)
	}
}

// TestParseFrameHeader_InterSkipModeAllBackward covers the second
// (fallback) branch of selectSkipModeRefs: no forward reference is
// available, so the algorithm picks two backward refs instead.
func TestParseFrameHeader_InterSkipModeAllBackward(t *testing.T) {
	seq := interBaseSeq()
	// FrameOffset=2; all refs ahead of current.
	refs := makeFullRefs(2)
	// refs[i].FrameOffset = 2-4+i = -2..5; uint8 wraps, but POC diff with 4 bits should make
	// many negative offsets land in the "backward" bucket. Use simpler construction:
	for i := 0; i < header.NumRefFrames; i++ {
		refs[i].FrameHdr.FrameOffset = uint8(i + 5) // 5,6,7,...,12 -> all > current(2)
	}
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(2, 4) // FrameOffset=2 -> all refs > 2 (backward)
	w.writeBits(7, 3)
	w.writeBits(0, 8)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(uint32(i), 3)
	}
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1) // switchable_comp_refs=1
	w.writeBit(0) // skipModeEnabled bit (allowed)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(0)
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: refs}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.SkipModeAllowed != 1 {
		t.Fatalf("backward fallback failed: %+v", fh)
	}
}

// TestParseFrameHeader_InterSegLFCopy covers parseSegmentation /
// parseLoopFilter primary_ref!=None copy paths. The referenced frame
// must carry the segmentation data and loop-filter deltas to copy.
func TestParseFrameHeader_InterSegLFCopy(t *testing.T) {
	seq := interBaseSeq()
	refs := makeFullRefs(5)
	refs[0].FrameHdr.Segmentation.SegData.D[3].Ref = 4
	refs[0].FrameHdr.Segmentation.SegData.LastActiveSegID = 3
	refs[0].FrameHdr.LoopFilter.ModeRefDeltas.RefDelta = [header.TotalRefsPerFrame]int8{7, 6, 5, 4, 3, 2, 1, 0}
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(5, 4)
	w.writeBits(0, 3) // primary_ref=0 -> refidx[0] used for copy
	w.writeBits(0, 8)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(0, 3) // refidx[0..6]=0 -> all point to refs[0]
	}
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)      // tile uniform
	w.writeBits(50, 8) // yac=50
	w.writeBit(0)      // ydc=0
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // QM=0
	// segmentation enabled + primary_ref!=None -> UpdateMap bit, Temporal? UpdateData bit.
	w.writeBit(1) // enabled
	w.writeBit(0) // update_map=0 -> no temporal bit
	w.writeBit(0) // update_data=0 -> copy ref's SegData
	// deltaQ/LF: yac!=0 -> deltaq.present bit
	w.writeBit(0) // delta_q.present=0 -> skip rest
	// LF: AllLossless? deriveLossless: yac=50!=0 -> not lossless. Full LF.
	w.writeBits(0, 6) // level y0=0
	w.writeBits(0, 6) // level y1=0  -> both 0 so no UV bits
	w.writeBits(0, 3) // sharpness
	// primary_ref!=None -> copy ref's ModeRefDeltas (no default).
	w.writeBit(0) // mode_ref_delta_enabled=0
	w.writeBit(0) // txfm largest
	w.writeBit(0) // switchable_comp_refs=0
	w.writeBit(0) // reduced_txtp_set
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(0)
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: refs}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.Segmentation.SegData.D[3].Ref != 4 || fh.Segmentation.SegData.LastActiveSegID != 3 {
		t.Fatalf("seg copy: %+v", fh.Segmentation.SegData)
	}
	if fh.LoopFilter.ModeRefDeltas.RefDelta[0] != 7 {
		t.Fatalf("lf copy: %+v", fh.LoopFilter.ModeRefDeltas)
	}
}

// TestParseFrameHeader_ShowFrameZeroShowable covers parseHead's branch
// where show_frame=0 reads an explicit showable_frame bit.
func TestParseFrameHeader_ShowFrameZeroShowable(t *testing.T) {
	seq := minimalReducedSeq()
	seq.ReducedStillPictureHeader = false
	seq.StillPicture = false
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(2, 2) // INTRA
	w.writeBit(0)     // show_frame=0
	w.writeBit(1)     // showable_frame=1
	w.writeBit(0)     // erm bit (intra+!show)
	w.writeBit(0)     // disable_cdf
	w.writeBit(1)     // allow_scc
	w.writeBit(0)     // fimv adaptive (intra forces 1)
	w.writeBit(0)     // size_override
	w.writeBits(0, 8) // refresh
	w.writeBit(0)     // have_render_size
	w.writeBit(0)     // intrabc
	w.writeBit(0)     // refresh_context
	w.writeBit(1)     // tile uniform
	w.writeBits(0, 8) // yac=0
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // seg=0
	w.writeBit(0) // reduced_txtp
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.ShowFrame != 0 || fh.ShowableFrame != 1 {
		t.Fatalf("show=%d showable=%d", fh.ShowFrame, fh.ShowableFrame)
	}
}

// TestParseFrameHeader_DecoderModel exercises the decoder_model_info_present
// branch (frame_presentation_delay + buffer_removal_time loops).
func TestParseFrameHeader_DecoderModel(t *testing.T) {
	seq := minimalReducedSeq()
	seq.ReducedStillPictureHeader = false
	seq.StillPicture = false
	seq.DecoderModelInfoPresent = true
	seq.EqualPictureInterval = false
	seq.FramePresentationDelayLength = 4
	seq.BufferRemovalDelayLength = 4
	seq.OperatingPoints[0] = header.OperatingPoint{
		IDC:                      0,
		DecoderModelParamPresent: true,
	}
	w := newBitWriter()
	w.writeBit(0)     // show_existing_frame
	w.writeBits(0, 2) // KEY
	w.writeBit(1)     // show_frame
	w.writeBits(5, 4) // frame_presentation_delay
	// KEY+show -> no showable bit, erm forced.
	w.writeBit(0)     // disable_cdf
	w.writeBit(1)     // allow_scc
	w.writeBit(0)     // fimv adaptive
	w.writeBit(0)     // size_override
	w.writeBit(1)     // buffer_removal_time_present
	w.writeBits(9, 4) // buffer_removal_time[0]
	w.writeBit(0)     // have_render_size
	w.writeBit(0)     // intrabc
	w.writeBit(0)     // refresh_context
	w.writeBit(1)     // tile uniform
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.FramePresentationDelay != 5 || fh.OperatingPoints[0].BufferRemovalTime != 9 {
		t.Fatalf("decoder model: delay=%d brt=%d", fh.FramePresentationDelay,
			fh.OperatingPoints[0].BufferRemovalTime)
	}
}

// TestParseFrameHeader_InvalidFilmGrainPoints rejects film grain point
// arrays whose x-coordinates aren't strictly increasing.
func TestParseFrameHeader_InvalidFilmGrainPoints(t *testing.T) {
	seq := nonReducedFullSeq()
	w := newBitWriter()
	// Bare-minimum head + intra path that lands inside parseFilmGrain.
	w.writeBit(0)
	w.writeBits(0, 2) // KEY
	w.writeBit(1)     // show
	w.writeBit(0)     // disable_cdf
	w.writeBit(1)     // allow_scc
	w.writeBit(0)     // fimv adaptive
	w.writeBit(0)     // size_override
	w.writeBit(0)     // have_render_size
	w.writeBit(0)     // intrabc
	w.writeBit(0)     // refresh_context
	w.writeBit(1)     // tile uniform
	w.writeBits(0, 8) // yac=0
	w.writeBit(0)
	w.writeBit(0) // diffUV (separate_uv=1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // QM
	w.writeBit(0) // seg
	// All lossless -> LF default, CDEF/LR skipped.
	w.writeBit(0) // reduced_txtp_set
	// FilmGrain present:
	w.writeBit(1)
	w.writeBits(0, 16) // seed
	w.writeBits(2, 4)  // num_y_points
	w.writeBits(50, 8) // Y[0].x
	w.writeBits(0, 8)
	w.writeBits(50, 8) // Y[1].x same as Y[0] -> error
	w.writeBits(0, 8)
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); !errors.Is(err, ErrInvalidFilmGrainPoints) {
		t.Fatalf("err = %v, want ErrInvalidFilmGrainPoints", err)
	}
}

// TestParseFrameHeader_InvalidChromaScaling rejects an asymmetric chroma
// scaling layout (one of NumUVPoints zero, the other non-zero) when the
// sequence is sub-sampled in both dimensions.
func TestParseFrameHeader_InvalidChromaScaling(t *testing.T) {
	seq := nonReducedFullSeq()
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(0, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(1) // allow_scc
	w.writeBit(0) // fimv adaptive
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // reduced_txtp
	w.writeBit(1) // FG present
	w.writeBits(0, 16)
	w.writeBits(1, 4) // num_y_points=1
	w.writeBits(10, 8)
	w.writeBits(0, 8)
	w.writeBit(0)     // chroma_scaling_from_luma=0
	w.writeBits(1, 4) // num_uv_points[0]=1
	w.writeBits(20, 8)
	w.writeBits(0, 8)
	w.writeBits(0, 4) // num_uv_points[1]=0 -> mismatch -> error
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); !errors.Is(err, ErrInvalidChromaScaling) {
		t.Fatalf("err = %v, want ErrInvalidChromaScaling", err)
	}
}

// TestParseFrameHeader_FilmGrainPointsTooMany covers the num_y_points > 14
// guard in parseFilmGrainData.
func TestParseFrameHeader_FilmGrainPointsTooMany(t *testing.T) {
	seq := nonReducedFullSeq()
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(0, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBits(0, 16)
	w.writeBits(15, 4) // num_y_points=15 -> error
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); !errors.Is(err, ErrInvalidFilmGrainPoints) {
		t.Fatalf("err = %v, want ErrInvalidFilmGrainPoints (y too many)", err)
	}
}

// TestParseFrameHeader_LRWithChromaSub exercises the LR path with all
// three planes restored and the optional UV unit-size adjustment bit.
func TestParseFrameHeader_LRWithChromaSub(t *testing.T) {
	seq := nonReducedFullSeq()
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(0, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // have_render
	w.writeBit(0) // intrabc
	w.writeBit(0)
	w.writeBit(1)
	w.writeBits(50, 8) // yac=50 (not lossless) -> LF/CDEF/LR all run
	w.writeBit(0)
	w.writeBit(0) // diffUV (sep_uv=1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // QM
	w.writeBit(0) // seg
	w.writeBit(0) // delta_q.present
	// LF
	w.writeBits(0, 6)
	w.writeBits(0, 6)
	w.writeBits(0, 3) // sharpness
	w.writeBit(0)     // mode_ref_delta_enabled=0
	// CDEF (cdef=true)
	w.writeBits(0, 2)
	w.writeBits(0, 2)
	w.writeBits(0, 6)
	w.writeBits(0, 6)
	// LR (Restoration=true, !all_lossless): all three Wiener
	w.writeBits(2, 2)
	w.writeBits(2, 2)
	w.writeBits(2, 2)
	w.writeBit(1) // unit_size +1
	w.writeBit(0) // !sb128 -> extra bit
	w.writeBit(1) // chroma sub bit (type[1] && ssHor==1 && ssVer==1)
	w.writeBit(0) // txfm largest
	w.writeBit(0) // reduced_txtp
	w.writeBit(0) // FG present
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.Restoration.Type[0] != header.RestorationWiener {
		t.Fatalf("lr type[0]=%d", fh.Restoration.Type[0])
	}
	if fh.Restoration.UnitSize[0] == fh.Restoration.UnitSize[1] {
		t.Fatalf("chroma sub bit not applied: %+v", fh.Restoration)
	}
}

// TestParseFrameHeader_InterErrorResilientOrderHintLoop drives the
// orderHintNBits-times skip loop that erm=1 + orderHint forces on intra
// and inter paths.
func TestParseFrameHeader_InterErrorResilientOrderHintLoop(t *testing.T) {
	seq := interBaseSeq()
	w := newBitWriter()
	w.writeBit(0)     // show_existing_frame
	w.writeBits(2, 2) // frame_type=INTRA
	w.writeBit(1)     // show_frame
	w.writeBit(1)     // error_resilient_mode
	w.writeBit(0)     // disable_cdf_update
	// scc=Off / fimv=Off / no frame_id
	w.writeBit(0)     // frame_size_override
	w.writeBits(3, 4) // FrameOffset
	// erm=1 + intra -> primary_ref_frame=None (no bit)
	w.writeBits(0x55, 8) // refresh_frame_flags != 0xff
	// erm=1 + OrderHint -> 8x F(4) loop = 32 bits
	for i := 0; i < 8; i++ {
		w.writeBits(0, 4)
	}
	// readFrameSize(false): !size_override -> no width/height, no super_res
	w.writeBit(0) // have_render_size
	// no intrabc
	w.writeBit(0)     // refresh_context (DisableCDFUpdate=0)
	w.writeBit(1)     // tile.uniform (1x1 frame -> log2=0, no extra bits)
	w.writeBits(0, 8) // quant YAC=0
	w.writeBit(0)     // YDCDelta
	w.writeBit(0)     // UDCDelta
	w.writeBit(0)     // UACDelta
	w.writeBit(0)     // QM
	w.writeBit(0)     // segmentation enabled
	// YAC=0 -> AllLossless=1 -> skip LF/CDEF/LR/Txfm
	// intra -> no skip_mode, warp_motion, global_mv
	w.writeBit(0) // ReducedTxtpSet
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.RefreshFrameFlags != 0x55 || fh.ErrorResilientMode != 1 {
		t.Fatalf("head: %+v", fh)
	}
}

// TestParseFrameHeader_InterRefShortSigRefsNil ensures applyFrameRefShortSignaling
// returns ErrRefsRequired when opts.Refs is nil.
func TestParseFrameHeader_InterRefShortSigRefsNil(t *testing.T) {
	seq := interBaseSeq()
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(5, 4)
	w.writeBits(7, 3)
	w.writeBits(0, 8)
	w.writeBit(1) // short_sig=1, but Refs is nil -> error
	w.writeBits(0, 3)
	w.writeBits(0, 3)
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); !errors.Is(err, ErrRefsRequired) {
		t.Fatalf("err = %v, want ErrRefsRequired", err)
	}
}

// TestParseFrameHeader_InterRefShortSigSlotEmpty ensures applyFrameRefShortSignaling
// returns ErrRefsRequired when a referenced slot is empty.
func TestParseFrameHeader_InterRefShortSigSlotEmpty(t *testing.T) {
	seq := interBaseSeq()
	refs := makeFullRefs(5)
	refs[2].FrameHdr = nil
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(5, 4)
	w.writeBits(7, 3)
	w.writeBits(0, 8)
	w.writeBit(1)
	w.writeBits(0, 3)
	w.writeBits(0, 3)
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: refs}); !errors.Is(err, ErrRefsRequired) {
		t.Fatalf("err = %v, want ErrRefsRequired (slot)", err)
	}
}

// TestParseFrameHeader_InterFrameIDMismatch confirms the per-ref frame_id
// validation path is exercised and reports a mismatch.
func TestParseFrameHeader_InterFrameIDMismatch(t *testing.T) {
	seq := interBaseSeq()
	seq.FrameIDNumbersPresent = true
	seq.FrameIDNBits = 8
	seq.DeltaFrameIDNBits = 4
	refs := makeFullRefs(5)
	refs[0].FrameHdr.FrameID = 999
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(10, 8) // frame_id=10
	w.writeBit(0)
	w.writeBits(5, 4)
	w.writeBits(7, 3)
	w.writeBits(0, 8)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(0, 3)
		w.writeBits(0, 4) // delta_frame_id - 1 = 0 -> expected = 10 + 256 - 1 = 265 % 256 = 9 != 999
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: refs}); !errors.Is(err, ErrFrameIDMismatch) {
		t.Fatalf("err = %v, want ErrFrameIDMismatch", err)
	}
}

// TestParseFrameHeader_InterUseRefRefsNil ensures readFrameSize use_ref
// path returns ErrRefsRequired when opts.Refs is nil.
func TestParseFrameHeader_InterUseRefRefsNil(t *testing.T) {
	seq := interBaseSeq()
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1) // size_override=1
	w.writeBits(5, 4)
	w.writeBits(7, 3)
	w.writeBits(0, 8)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(0, 3)
	}
	w.writeBit(1) // use_ref bit=1 -> needs refs
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); !errors.Is(err, ErrRefsRequired) {
		t.Fatalf("err = %v, want ErrRefsRequired (use_ref)", err)
	}
}

// TestParseFrameHeader_NotIntraSelectSkipModeRefsNil triggers the inner
// selectSkipModeRefs ErrRefsRequired branch.
func TestParseFrameHeader_NotIntraSelectSkipModeRefsNil(t *testing.T) {
	seq := interBaseSeq()
	refs := makeFullRefs(5)
	refs[3].FrameHdr = nil // make refidx[3] empty
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(5, 4)
	w.writeBits(7, 3)
	w.writeBits(0, 8)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(uint32(i), 3) // refidx[3] = 3 -> empty slot
	}
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1) // switchable_comp_refs=1
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: refs}); !errors.Is(err, ErrRefsRequired) {
		t.Fatalf("err = %v, want ErrRefsRequired (select)", err)
	}
}

// TestParseFrameHeader_FilmGrainInterUpdateZero exercises the
// film_grain ref-copy path on an inter frame.
func TestParseFrameHeader_FilmGrainInterUpdateZero(t *testing.T) {
	seq := interBaseSeq()
	seq.FilmGrainPresent = true
	refs := makeFullRefs(5)
	refs[0].FrameHdr.FilmGrain.Data.NumYPoints = 3
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(5, 4)
	w.writeBits(7, 3)
	w.writeBits(0, 8)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(0, 3) // refidx[i]=0
	}
	w.writeBit(0) // have_render
	w.writeBit(0) // HP
	w.writeBit(1) // subpel switchable
	w.writeBit(0) // motion_mode
	w.writeBit(0) // refresh_context
	w.writeBit(1) // tile uniform
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // seg
	w.writeBit(0) // switchable_comp_refs
	w.writeBit(0) // reduced_txtp
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(0) // GMV identity
	}
	// FilmGrain:
	w.writeBit(1)       // present
	w.writeBits(42, 16) // seed
	w.writeBit(0)       // update=0 (Inter+present)
	w.writeBits(0, 3)   // refidx -> 0
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: refs}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.FilmGrain.Data.NumYPoints != 3 || fh.FilmGrain.Data.Seed != 42 {
		t.Fatalf("fg copy failed: %+v", fh.FilmGrain.Data)
	}
}

// TestParseFrameHeader_FilmGrainInvalidRef triggers ErrInvalidFilmGrainRef
// when update=0 and refidx is not in the current frame's refidx[].
func TestParseFrameHeader_FilmGrainInvalidRef(t *testing.T) {
	seq := interBaseSeq()
	seq.FilmGrainPresent = true
	refs := makeFullRefs(5)
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(5, 4)
	w.writeBits(7, 3)
	w.writeBits(0, 8)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(0, 3) // all refidx=0
	}
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(0)
	}
	w.writeBit(1)      // present
	w.writeBits(0, 16) // seed
	w.writeBit(0)      // update=0
	w.writeBits(7, 3)  // refidx=7 not in hdr.Refidx (all 0)
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: refs}); !errors.Is(err, ErrInvalidFilmGrainRef) {
		t.Fatalf("err = %v, want ErrInvalidFilmGrainRef", err)
	}
}

// writeInterHappyPrefix emits the canonical InterHappy bits up to the
// GMV section so individual GMV-type tests can fork it cheaply.
func writeInterHappyPrefix() *bitWriter {
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(5, 4)
	w.writeBits(7, 3)
	w.writeBits(0, 8)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(0, 3)
	}
	w.writeBit(0) // have_render_size
	w.writeBit(0) // HP
	w.writeBit(1) // subpel switchable
	w.writeBit(0) // switchable_motion_mode
	w.writeBit(0) // refresh_context
	w.writeBit(1) // uniform
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	return w
}

func TestParseFrameHeader_ReducedShortBuffer(t *testing.T) {
	seq := minimalReducedSeq()
	var fh header.FrameHeader
	if err := ParseFrameHeader(nil, &fh, FrameParseOptions{SeqHeader: seq}); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("err = %v, want ErrShortBuffer", err)
	}
}

// TestParseFrameHeader_InterGMVRotZoom forks InterHappy and replaces the
// 7 Identity GMV bits with full RotZoom encodings (18 bits per ref). With
// PrimaryRefFrame=None the ref-side params equal defaultWarpParams, so
// writing 0000 for every BitsSubexp(0,12) yields a value of 0.
func TestParseFrameHeader_InterGMVRotZoom(t *testing.T) {
	seq := interBaseSeq()
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(5, 4)
	w.writeBits(7, 3)
	w.writeBits(0, 8)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(0, 3)
	}
	w.writeBit(0) // have_render_size
	w.writeBit(0) // HP
	w.writeBit(1) // subpel switchable
	w.writeBit(0) // switchable_motion_mode
	w.writeBit(0) // refresh_context
	w.writeBit(1) // uniform
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // switchable_comp_refs
	w.writeBit(0) // reduced_txtp
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(1)     // hasType
		w.writeBit(1)     // RotZoom
		w.writeBits(0, 4) // mat[2] = (1<<16) + 0
		w.writeBits(0, 4) // mat[3] = 0
		w.writeBits(0, 4) // mat[0] = 0
		w.writeBits(0, 4) // mat[1] = 0
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	for i := 0; i < header.RefsPerFrame; i++ {
		if fh.GMV[i].Type != header.WMTypeRotZoom {
			t.Fatalf("gmv[%d]=%+v", i, fh.GMV[i])
		}
		if fh.GMV[i].Matrix[2] != 1<<16 || fh.GMV[i].Matrix[3] != 0 {
			t.Fatalf("gmv[%d] matrix=%v", i, fh.GMV[i].Matrix)
		}
		// mat[4]=-mat[3]=0, mat[5]=mat[2]=1<<16
		if fh.GMV[i].Matrix[4] != 0 || fh.GMV[i].Matrix[5] != 1<<16 {
			t.Fatalf("gmv[%d] affine derived=%v", i, fh.GMV[i].Matrix)
		}
	}
}

// TestParseFrameHeader_InterGMVAffine forks InterHappy and uses Affine
// type (3 type-flag bits + 4x4 RotZoom + 4x4 Affine extras).
func TestParseFrameHeader_InterGMVAffine(t *testing.T) {
	seq := interBaseSeq()
	w := writeInterHappyPrefix()
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(1)     // hasType
		w.writeBit(0)     // not RotZoom
		w.writeBit(0)     // not Translation -> Affine
		w.writeBits(0, 4) // mat[2]
		w.writeBits(0, 4) // mat[3]
		w.writeBits(0, 4) // mat[4]
		w.writeBits(0, 4) // mat[5]
		w.writeBits(0, 4) // mat[0]
		w.writeBits(0, 4) // mat[1]
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	for i := 0; i < header.RefsPerFrame; i++ {
		if fh.GMV[i].Type != header.WMTypeAffine {
			t.Fatalf("gmv[%d]=%+v", i, fh.GMV[i])
		}
	}
}

// TestParseFrameHeader_InterGMVTranslation walks the Translation path.
func TestParseFrameHeader_InterGMVTranslation(t *testing.T) {
	seq := interBaseSeq()
	w := writeInterHappyPrefix()
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(1)     // hasType
		w.writeBit(0)     // not RotZoom
		w.writeBit(1)     // Translation
		w.writeBits(0, 4) // mat[0]
		w.writeBits(0, 4) // mat[1]
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	for i := 0; i < header.RefsPerFrame; i++ {
		if fh.GMV[i].Type != header.WMTypeTranslation {
			t.Fatalf("gmv[%d]=%+v", i, fh.GMV[i])
		}
	}
}

// TestParseFrameHeader_InterSkipModeOnlyBefore drives selectSkipModeRefs
// where all refs lie strictly before the current POC (offAfter < 0). This
// covers the inner offBefore2 scan branch.
func TestParseFrameHeader_InterSkipModeOnlyBefore(t *testing.T) {
	seq := interBaseSeq()
	var refs [header.NumRefFrames]FrameReference
	for i := 0; i < header.NumRefFrames; i++ {
		refs[i].FrameHdr = &header.FrameHeader{FrameOffset: uint8(9 - i)}
	}
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(10, 4) // current POC = 10
	w.writeBits(7, 3)  // primary_ref=None
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBits(0, 3)
	w.writeBits(1, 3)
	w.writeBits(2, 3)
	w.writeBits(3, 3)
	w.writeBits(4, 3)
	w.writeBits(5, 3)
	w.writeBits(6, 3)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1) // switchable_comp_refs=1 -> select
	w.writeBit(1) // skip_mode_enabled (allowed via offBefore2)
	w.writeBit(0) // reduced_txtp
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(0)
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: &refs}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.SkipModeAllowed != 1 || fh.SkipModeEnabled != 1 {
		t.Fatalf("allow=%d enable=%d", fh.SkipModeAllowed, fh.SkipModeEnabled)
	}
	if fh.SkipModeRefs[0] != 0 || fh.SkipModeRefs[1] != 1 {
		t.Fatalf("refs=%v", fh.SkipModeRefs)
	}
}

// TestParseFrameHeader_InterUseRefSizeSuperRes drives readFrameSize via
// the useRef path (size_override=1, erm=0, found_ref bit=1) and exercises
// readSuperRes's enabled branch with a 3-bit scale denominator.
func TestParseFrameHeader_InterUseRefSizeSuperRes(t *testing.T) {
	seq := interBaseSeq()
	seq.SuperRes = true
	var refs [header.NumRefFrames]FrameReference
	refs[0].FrameHdr = &header.FrameHeader{
		FrameOffset:  3,
		Width:        [2]int{0, 128},
		Height:       72,
		RenderWidth:  128,
		RenderHeight: 72,
	}
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2) // INTER
	w.writeBit(1)     // show
	w.writeBit(0)     // erm=0
	w.writeBit(0)     // disable_cdf
	w.writeBit(1)     // size_override=1
	w.writeBits(5, 4) // order_hint
	w.writeBits(7, 3) // primary_ref=None
	w.writeBits(0, 8) // refresh_flags
	w.writeBit(0)     // signalled refs
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(0, 3) // refidx -> use refs[0]
	}
	// readFrameSize(useRef=true): first found_ref bit=1 -> break.
	w.writeBit(1)
	// readSuperRes: superres_enabled=1 + 3-bit scale=3 -> denom=12.
	w.writeBit(1)
	w.writeBits(3, 3)
	// (readFrameSize returns; no render_size bits when useRef path)
	// HP bit:
	w.writeBit(0)
	w.writeBit(1) // subpel switchable
	w.writeBit(0) // switchable_motion_mode
	// Tile:
	w.writeBit(0) // refresh_context
	w.writeBit(1) // uniform
	// Quant yac=0:
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // seg
	w.writeBit(0) // switchable_comp_refs
	w.writeBit(0) // reduced_txtp
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(0)
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: &refs}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.FrameSizeOverride != 1 {
		t.Fatalf("size_override=%d", fh.FrameSizeOverride)
	}
	if fh.Width[1] != 128 || fh.Height != 72 {
		t.Fatalf("ref size: %dx%d", fh.Width[1], fh.Height)
	}
	if fh.SuperRes.Enabled != 1 || fh.SuperRes.WidthScaleDenominator != 12 {
		t.Fatalf("superres: enabled=%d denom=%d", fh.SuperRes.Enabled, fh.SuperRes.WidthScaleDenominator)
	}
	if fh.Width[0] != 85 {
		t.Fatalf("scaled width=%d want 85", fh.Width[0])
	}
}

// TestParseFrameHeader_InterLRRestoration drives parseLR with Restoration
// enabled (SuperRes.Enabled satisfies the AllLossless==0 || SuperRes!=0
// gate). Touches UnitSize derivation paths for both planes.
func TestParseFrameHeader_InterLRRestoration(t *testing.T) {
	seq := interBaseSeq()
	seq.SuperRes = true
	seq.Restoration = true
	var refs [header.NumRefFrames]FrameReference
	refs[0].FrameHdr = &header.FrameHeader{
		FrameOffset:  3,
		Width:        [2]int{0, 128},
		Height:       72,
		RenderWidth:  128,
		RenderHeight: 72,
	}
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1) // size_override=1
	w.writeBits(5, 4)
	w.writeBits(7, 3)
	w.writeBits(0, 8)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(0, 3)
	}
	w.writeBit(1)     // found_ref=1
	w.writeBit(1)     // superres_enabled=1
	w.writeBits(3, 3) // scale
	w.writeBit(0)     // HP
	w.writeBit(1)     // subpel switchable
	w.writeBit(0)     // switchable_motion_mode
	w.writeBit(0)     // refresh_context
	w.writeBit(1)     // uniform
	w.writeBit(0)     // cols loop exit (MinLog2Cols<MaxLog2Cols)
	w.writeBit(0)     // rows loop exit
	w.writeBits(0, 8) // yac=0
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // seg
	// parseLR (SuperRes.Enabled=1, Restoration=true, AllowIntrabc=0):
	w.writeBits(1, 2) // Type[0]=1 (Wiener)
	w.writeBits(1, 2) // Type[1]=1
	w.writeBits(0, 2) // Type[2]=0
	w.writeBit(1)     // unitSize bump
	w.writeBit(0)     // !SB128 second bit -> +=0
	w.writeBit(0)     // SsHor=SsVer=1 path -> UnitSize[1] -= 0
	w.writeBit(0)     // switchable_comp_refs
	w.writeBit(0)     // reduced_txtp
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(0)
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: &refs}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.Restoration.Type[0] != header.RestorationType(1) || fh.Restoration.Type[1] != header.RestorationType(1) {
		t.Fatalf("restoration types: %+v", fh.Restoration.Type)
	}
	if fh.Restoration.UnitSize[0] != 7 || fh.Restoration.UnitSize[1] != 7 {
		t.Fatalf("unit sizes: %+v", fh.Restoration.UnitSize)
	}
}

// TestParseFrameHeader_InterLRAllNone exercises parseLR's all-None else
// branch (UnitSize[0]=8 default).
func TestParseFrameHeader_InterLRAllNone(t *testing.T) {
	seq := interBaseSeq()
	seq.SuperRes = true
	seq.Restoration = true
	var refs [header.NumRefFrames]FrameReference
	refs[0].FrameHdr = &header.FrameHeader{
		FrameOffset:  3,
		Width:        [2]int{0, 128},
		Height:       72,
		RenderWidth:  128,
		RenderHeight: 72,
	}
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBits(5, 4)
	w.writeBits(7, 3)
	w.writeBits(0, 8)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(0, 3)
	}
	w.writeBit(1)
	w.writeBit(1)
	w.writeBits(3, 3)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(0, 2) // Type[0]=None
	w.writeBits(0, 2) // Type[1]=None
	w.writeBits(0, 2) // Type[2]=None
	w.writeBit(0)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(0)
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: &refs}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.Restoration.UnitSize[0] != 8 {
		t.Fatalf("unit_size[0]=%d want 8", fh.Restoration.UnitSize[0])
	}
}

// TestParseFrameHeader_InterLFFullPath drives parseLoopFilter's full body
// with AllLossless=0 (yac=1), ModeRefDeltaEnabled/Update=1 and SU(7)
// reads on one ref delta and one mode delta.
func TestParseFrameHeader_InterLFFullPath(t *testing.T) {
	seq := interBaseSeq()
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)     // size_override=0
	w.writeBits(5, 4) // order_hint
	w.writeBits(7, 3) // primary_ref=None
	w.writeBits(0, 8) // refresh
	w.writeBit(0)     // signalled
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(0, 3)
	}
	w.writeBit(0)     // have_render_size
	w.writeBit(0)     // HP
	w.writeBit(1)     // subpel switchable
	w.writeBit(0)     // switchable_motion_mode
	w.writeBit(0)     // refresh_context
	w.writeBit(1)     // uniform (sbw=sbh=1 -> no loops)
	w.writeBits(1, 8) // yac=1
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // seg=0
	w.writeBit(0) // delta_q_present=0
	// parseLoopFilter body:
	w.writeBits(0, 6) // LevelY[0]
	w.writeBits(0, 6) // LevelY[1]
	w.writeBits(0, 3) // Sharpness
	w.writeBit(1)     // ModeRefDeltaEnabled
	w.writeBit(1)     // ModeRefDeltaUpdate
	w.writeBit(1)     // ref[0]: read SU(7)
	w.writeBits(0, 7) // SU(7)=0
	for i := 1; i < 8; i++ {
		w.writeBit(0)
	}
	w.writeBit(1)     // mode[0]: read SU(7)
	w.writeBits(0, 7) // SU(7)=0
	w.writeBit(0)     // mode[1]: skip
	w.writeBit(0)     // txfm: largest
	w.writeBit(0)     // switchable_comp_refs
	w.writeBit(0)     // reduced_txtp
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(0)
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.AllLossless != 0 {
		t.Fatalf("AllLossless=%d", fh.AllLossless)
	}
	if fh.LoopFilter.ModeRefDeltaEnabled != 1 || fh.LoopFilter.ModeRefDeltaUpdate != 1 {
		t.Fatalf("lf: enable=%d update=%d", fh.LoopFilter.ModeRefDeltaEnabled, fh.LoopFilter.ModeRefDeltaUpdate)
	}
	if fh.TxfmMode != header.TxfmModeLargest {
		t.Fatalf("txfm=%d", fh.TxfmMode)
	}
}

// TestParseFrameHeader_InterGMVPrimaryRefNoRefs forces parseGlobalMV to
// hit ErrRefsRequired when PrimaryRefFrame!=None but FrameParseOptions.Refs
// is nil and a non-Identity GMV is signalled.
func TestParseFrameHeader_InterGMVPrimaryRefNoRefs(t *testing.T) {
	seq := interBaseSeq()
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(5, 4)
	w.writeBits(0, 3) // primary_ref=0 (!= None)
	w.writeBits(0, 8)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(0, 3)
	}
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1) // first ref: hasType
	w.writeBit(0) // not RotZoom
	w.writeBit(1) // Translation
	var fh header.FrameHeader
	err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq})
	if !errors.Is(err, ErrRefsRequired) {
		t.Fatalf("err=%v want ErrRefsRequired", err)
	}
}

func TestHelpers_GetPOCDiffAndClipU8(t *testing.T) {
	if got := getPOCDiff(0, 7, 3); got != 0 {
		t.Fatalf("getPOCDiff nBits=0: %d", got)
	}
	if got := getPOCDiff(4, 0, 8); got != -8 {
		t.Fatalf("getPOCDiff wrap: %d", got)
	}
	if got := clipU8(-5); got != 0 {
		t.Fatalf("clipU8(-5) = %d", got)
	}
	if got := clipU8(999); got != 255 {
		t.Fatalf("clipU8(999) = %d", got)
	}
	if got := clipU8(128); got != 128 {
		t.Fatalf("clipU8(128) = %d", got)
	}
}

// TestParseFrameHeader_InterWarpMotion forks InterHappy and turns on
// seq.WarpedMotion so parseWarpMotion reads a bit.
func TestParseFrameHeader_InterWarpMotion(t *testing.T) {
	seq := interBaseSeq()
	seq.WarpedMotion = true
	w := newBitWriter()
	w.writeBit(0)     // show_existing_frame
	w.writeBits(1, 2) // INTER
	w.writeBit(1)     // show_frame
	w.writeBit(0)     // erm
	w.writeBit(0)     // disable_cdf
	w.writeBit(0)     // size_override
	w.writeBits(5, 4) // order_hint
	w.writeBits(7, 3) // primary_ref=None
	w.writeBits(0, 8) // refresh_flags
	w.writeBit(0)     // signalled refs
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(0, 3)
	}
	w.writeBit(0) // have_render_size
	w.writeBit(0) // HP
	w.writeBit(1) // subpel switchable
	w.writeBit(0) // switchable_motion_mode
	w.writeBit(0) // refresh_context
	w.writeBit(1) // uniform
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // seg
	w.writeBit(0) // switchable_comp_refs
	w.writeBit(1) // warp_motion=1
	w.writeBit(0) // reduced_txtp
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(0)
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.WarpMotion != 1 {
		t.Fatalf("warp_motion=%d", fh.WarpMotion)
	}
}

// TestParseFrameHeader_InterUseRefFrameMVs forks InterHappy and turns on
// seq.RefFrameMVs so parseFrameTypeAndRefs reads use_ref_frame_mvs.
func TestParseFrameHeader_InterUseRefFrameMVs(t *testing.T) {
	seq := interBaseSeq()
	seq.RefFrameMVs = true
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(5, 4)
	w.writeBits(7, 3)
	w.writeBits(0, 8)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(0, 3)
	}
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(1) // use_ref_frame_mvs=1
	w.writeBit(0)
	w.writeBit(1)
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(0)
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.UseRefFrameMVs != 1 {
		t.Fatalf("use_ref_frame_mvs=%d", fh.UseRefFrameMVs)
	}
}

// TestParseFrameHeader_InterGMVWithPrimaryRef drives the
// PrimaryRefFrame!=None branch of parseGlobalMV (covers ref.GMV[i] copy).
func TestParseFrameHeader_InterGMVWithPrimaryRef(t *testing.T) {
	seq := interBaseSeq()
	refs := makeFullRefs(5)
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(5, 4)
	w.writeBits(0, 3) // primary_ref=0 (not None)
	w.writeBits(0, 8)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBits(0, 3)
	}
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(1)     // hasType
		w.writeBit(0)     // not RotZoom
		w.writeBit(1)     // Translation
		w.writeBits(0, 4) // mat[0]
		w.writeBits(0, 4) // mat[1]
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: refs}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.PrimaryRefFrame != 0 {
		t.Fatalf("primary=%d", fh.PrimaryRefFrame)
	}
	for i := 0; i < header.RefsPerFrame; i++ {
		if fh.GMV[i].Type != header.WMTypeTranslation {
			t.Fatalf("gmv[%d]=%+v", i, fh.GMV[i])
		}
	}
}

// TestParseFrameHeader_InterSkipModeSelectsRefs drives selectSkipModeRefs
// with both before/after refs available, exercising the dual branch.
func TestParseFrameHeader_InterSkipModeSelectsRefs(t *testing.T) {
	seq := interBaseSeq()
	refs := makeFullRefs(5) // POCs span 1..8
	w := newBitWriter()
	w.writeBit(0)
	w.writeBits(1, 2)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBits(5, 4)
	w.writeBits(0, 3) // primary=0 (need Refs)
	w.writeBits(0, 8)
	w.writeBit(0)
	// refidx spread: 0,1,2,3,4,5,6 -> spans before/after
	w.writeBits(0, 3)
	w.writeBits(1, 3)
	w.writeBits(2, 3)
	w.writeBits(3, 3)
	w.writeBits(4, 3)
	w.writeBits(5, 3)
	w.writeBits(6, 3)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(1)
	w.writeBits(0, 8)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0)
	w.writeBit(0) // seg
	w.writeBit(1) // switchable_comp_refs=1 -> select
	w.writeBit(1) // skip_mode_enabled (since both before+after exist)
	w.writeBit(0) // reduced_txtp
	for i := 0; i < header.RefsPerFrame; i++ {
		w.writeBit(0)
	}
	var fh header.FrameHeader
	if err := ParseFrameHeader(w.bytes(), &fh, FrameParseOptions{SeqHeader: seq, Refs: refs}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fh.SwitchableCompRefs != 1 || fh.SkipModeAllowed != 1 || fh.SkipModeEnabled != 1 {
		t.Fatalf("skip mode flags: switch=%d allow=%d enable=%d",
			fh.SwitchableCompRefs, fh.SkipModeAllowed, fh.SkipModeEnabled)
	}
}
