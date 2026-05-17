package obu

import (
	"errors"

	"github.com/zesun96/go-av1/internal/bitstream"
	"github.com/zesun96/go-av1/internal/header"
)

// Frame header parser errors. They mirror the various "goto error" exits in
// dav1d's parse_frame_hdr.
var (
	ErrFrameHeaderRequiresSeq = errors.New("obu: frame header requires sequence header")
	ErrNilFrameHeaderOut      = errors.New("obu: nil FrameHeader out")
	ErrRefsRequired           = errors.New("obu: parsing path requires reference state")
	ErrFrameIDMismatch        = errors.New("obu: frame_id mismatch with reference")
	ErrStrictKeyRefreshAll    = errors.New("obu: strict mode: intra-only frame must not refresh all refs")
	ErrInvalidTileUpdate      = errors.New("obu: invalid context_update_tile_id")
	ErrInvalidFilmGrainPoints = errors.New("obu: invalid film grain scaling points")
	ErrInvalidChromaScaling   = errors.New("obu: invalid chroma scaling configuration")
	ErrInvalidFilmGrainRef    = errors.New("obu: film grain references unknown frame")
)

// FrameReference represents one slot of the dav1d c->refs[] array. A nil
// FrameHdr indicates that the slot is empty (no previously decoded frame).
type FrameReference struct {
	FrameHdr *header.FrameHeader
}

// FrameParseOptions configures ParseFrameHeader.
//
// SeqHeader is required; the bitstream cannot be interpreted without it.
// Refs is optional: any parsing path that needs a previously decoded frame
// (frame_id validation, frame_ref_short_signaling reordering, use_ref frame
// sizes, primary_ref segmentation/loopfilter/film_grain inheritance,
// skip_mode reference selection, warped motion ref_gmv lookup) will return
// ErrRefsRequired when Refs is nil or the referenced slot is empty.
type FrameParseOptions struct {
	StrictStdCompliance bool
	SeqHeader           *header.SequenceHeader
	Refs                *[header.NumRefFrames]FrameReference
	TemporalID          uint8
	SpatialID           uint8
}

// defaultWarpParams mirrors dav1d_default_wm_params (tables.c).
var defaultWarpParams = header.WarpedMotionParams{
	Type:   header.WMTypeIdentity,
	Matrix: [6]int32{0, 0, 1 << 16, 0, 0, 1 << 16},
}

// ParseFrameHeader decodes a frame_header_obu (or the equivalent fields of an
// OBU_FRAME) payload. This is a 1:1 port of dav1d's parse_frame_hdr in
// src/obu.c (lines 409-1152). It does not consume the trailing_one_bit; the
// caller should invoke checkTrailingBits when this OBU stands alone.
func ParseFrameHeader(payload []byte, out *header.FrameHeader, opts FrameParseOptions) error {
	if opts.SeqHeader == nil {
		return ErrFrameHeaderRequiresSeq
	}
	if out == nil {
		return ErrNilFrameHeaderOut
	}
	*out = header.FrameHeader{}
	gb := bitstream.NewGetBits(payload)
	fp := &frameParser{gb: gb, seq: opts.SeqHeader, hdr: out, opts: opts}
	fp.hdr.TemporalID = opts.TemporalID
	fp.hdr.SpatialID = opts.SpatialID
	if err := fp.parse(); err != nil {
		return err
	}
	if gb.Err() {
		return ErrShortBuffer
	}
	return nil
}

// frameParser carries per-call state across the many sub-parsers below. It is
// the Go analogue of dav1d's (c, gb, seqhdr, hdr) implicit-context style.
type frameParser struct {
	gb   *bitstream.GetBits
	seq  *header.SequenceHeader
	hdr  *header.FrameHeader
	opts FrameParseOptions
}

func (p *frameParser) parse() error {
	if err := p.parseHead(); err != nil {
		return err
	}
	if p.hdr.ShowExistingFrame != 0 {
		return nil
	}
	if err := p.parseFrameTypeAndRefs(); err != nil {
		return err
	}
	if err := p.parseTileInfo(); err != nil {
		return err
	}
	p.parseQuant()
	if err := p.parseSegmentation(); err != nil {
		return err
	}
	p.parseDeltaQLF()
	p.deriveLossless()
	if err := p.parseLoopFilter(); err != nil {
		return err
	}
	p.parseCDEF()
	p.parseLR()
	p.parseTxfmMode()
	if err := p.parseSkipMode(); err != nil {
		return err
	}
	p.parseWarpMotion()
	p.hdr.ReducedTxtpSet = uint8(p.gb.Bit())
	if err := p.parseGlobalMV(); err != nil {
		return err
	}
	return p.parseFilmGrain()
}

// ----------------------------------------------------------------------------
// Front matter: show_existing_frame, frame_type, error_resilient, screen-tool
// flags, frame_id, frame_size_override, order_hint, primary_ref_frame,
// buffer_removal_time_present, refresh_frame_flags, frame-size + inter refs.
// ----------------------------------------------------------------------------

func (p *frameParser) parseHead() error {
	seq, hdr, gb := p.seq, p.hdr, p.gb

	if !seq.ReducedStillPictureHeader {
		hdr.ShowExistingFrame = uint8(gb.Bit())
	}
	if hdr.ShowExistingFrame != 0 {
		hdr.ExistingFrameIdx = uint8(gb.F(3))
		if seq.DecoderModelInfoPresent && !seq.EqualPictureInterval {
			hdr.FramePresentationDelay = gb.F(int(seq.FramePresentationDelayLength))
		}
		if seq.FrameIDNumbersPresent {
			hdr.FrameID = gb.F(int(seq.FrameIDNBits))
			if p.opts.Refs != nil {
				ref := p.opts.Refs[hdr.ExistingFrameIdx].FrameHdr
				if ref == nil {
					return ErrRefsRequired
				}
				if ref.FrameID != hdr.FrameID {
					return ErrFrameIDMismatch
				}
			}
		}
		return nil
	}

	if seq.ReducedStillPictureHeader {
		hdr.FrameType = header.FrameTypeKey
		hdr.ShowFrame = 1
	} else {
		hdr.FrameType = header.FrameType(gb.F(2))
		hdr.ShowFrame = uint8(gb.Bit())
	}
	if hdr.ShowFrame != 0 {
		if seq.DecoderModelInfoPresent && !seq.EqualPictureInterval {
			hdr.FramePresentationDelay = gb.F(int(seq.FramePresentationDelayLength))
		}
		if hdr.FrameType != header.FrameTypeKey {
			hdr.ShowableFrame = 1
		}
	} else {
		hdr.ShowableFrame = uint8(gb.Bit())
	}
	erm := uint8(0)
	if (hdr.FrameType == header.FrameTypeKey && hdr.ShowFrame != 0) ||
		hdr.FrameType == header.FrameTypeSwitch || seq.ReducedStillPictureHeader {
		erm = 1
	} else if gb.Bit() != 0 {
		erm = 1
	}
	hdr.ErrorResilientMode = erm
	hdr.DisableCDFUpdate = uint8(gb.Bit())

	if seq.ScreenContentTools == header.AdaptiveAdaptive {
		hdr.AllowScreenContentTools = uint8(gb.Bit())
	} else {
		hdr.AllowScreenContentTools = uint8(seq.ScreenContentTools)
	}
	if hdr.AllowScreenContentTools != 0 {
		if seq.ForceIntegerMV == header.AdaptiveAdaptive {
			hdr.ForceIntegerMV = uint8(gb.Bit())
		} else {
			hdr.ForceIntegerMV = uint8(seq.ForceIntegerMV)
		}
	}
	if hdr.FrameType.IsIntra() {
		hdr.ForceIntegerMV = 1
	}

	if seq.FrameIDNumbersPresent {
		hdr.FrameID = gb.F(int(seq.FrameIDNBits))
	}
	if !seq.ReducedStillPictureHeader {
		if hdr.FrameType == header.FrameTypeSwitch {
			hdr.FrameSizeOverride = 1
		} else {
			hdr.FrameSizeOverride = uint8(gb.Bit())
		}
	}
	if seq.OrderHint {
		hdr.FrameOffset = uint8(gb.F(int(seq.OrderHintNBits)))
	}
	if hdr.ErrorResilientMode == 0 && !hdr.FrameType.IsIntra() {
		hdr.PrimaryRefFrame = uint8(gb.F(3))
	} else {
		hdr.PrimaryRefFrame = header.PrimaryRefNone
	}

	if seq.DecoderModelInfoPresent {
		hdr.BufferRemovalTimePresent = uint8(gb.Bit())
		if hdr.BufferRemovalTimePresent != 0 {
			for i := 0; i < int(seq.NumOperatingPoints); i++ {
				sop := &seq.OperatingPoints[i]
				op := &hdr.OperatingPoints[i]
				if sop.DecoderModelParamPresent {
					inT := (sop.IDC >> hdr.TemporalID) & 1
					inS := (sop.IDC >> (hdr.SpatialID + 8)) & 1
					if sop.IDC == 0 || (inT != 0 && inS != 0) {
						op.BufferRemovalTime = gb.F(int(seq.BufferRemovalDelayLength))
					}
				}
			}
		}
	}
	return nil
}

func (p *frameParser) parseFrameTypeAndRefs() error {
	seq, hdr, gb := p.seq, p.hdr, p.gb

	if hdr.FrameType.IsIntra() {
		if hdr.FrameType == header.FrameTypeKey && hdr.ShowFrame != 0 {
			hdr.RefreshFrameFlags = 0xff
		} else {
			hdr.RefreshFrameFlags = uint8(gb.F(8))
		}
		if hdr.RefreshFrameFlags != 0xff && hdr.ErrorResilientMode != 0 && seq.OrderHint {
			for i := 0; i < 8; i++ {
				_ = gb.F(int(seq.OrderHintNBits))
			}
		}
		if p.opts.StrictStdCompliance &&
			hdr.FrameType == header.FrameTypeIntra && hdr.RefreshFrameFlags == 0xff {
			return ErrStrictKeyRefreshAll
		}
		if err := p.readFrameSize(false); err != nil {
			return err
		}
		if hdr.AllowScreenContentTools != 0 && hdr.SuperRes.Enabled == 0 {
			hdr.AllowIntrabc = uint8(gb.Bit())
		}
		return nil
	}

	// Inter / switch frame.
	if hdr.FrameType == header.FrameTypeSwitch {
		hdr.RefreshFrameFlags = 0xff
	} else {
		hdr.RefreshFrameFlags = uint8(gb.F(8))
	}
	if hdr.ErrorResilientMode != 0 && seq.OrderHint {
		for i := 0; i < 8; i++ {
			_ = gb.F(int(seq.OrderHintNBits))
		}
	}
	if seq.OrderHint {
		hdr.FrameRefShortSignaling = uint8(gb.Bit())
		if hdr.FrameRefShortSignaling != 0 {
			if err := p.applyFrameRefShortSignaling(); err != nil {
				return err
			}
		}
	}
	for i := 0; i < header.RefsPerFrame; i++ {
		if hdr.FrameRefShortSignaling == 0 {
			hdr.Refidx[i] = int8(gb.F(3))
		}
		if seq.FrameIDNumbersPresent {
			delta := gb.F(int(seq.DeltaFrameIDNBits)) + 1
			expected := (hdr.FrameID + (uint32(1) << seq.FrameIDNBits) - delta) &
				((uint32(1) << seq.FrameIDNBits) - 1)
			if p.opts.Refs != nil {
				ref := p.opts.Refs[hdr.Refidx[i]].FrameHdr
				if ref == nil {
					return ErrRefsRequired
				}
				if ref.FrameID != expected {
					return ErrFrameIDMismatch
				}
			}
		}
	}
	useRef := hdr.ErrorResilientMode == 0 && hdr.FrameSizeOverride != 0
	if err := p.readFrameSize(useRef); err != nil {
		return err
	}
	if hdr.ForceIntegerMV == 0 {
		hdr.HP = uint8(gb.Bit())
	}
	if gb.Bit() != 0 {
		hdr.SubpelFilterMode = header.FilterModeSwitchable
	} else {
		hdr.SubpelFilterMode = header.FilterMode(gb.F(2))
	}
	hdr.SwitchableMotionMode = uint8(gb.Bit())
	if hdr.ErrorResilientMode == 0 && seq.RefFrameMVs && seq.OrderHint && !hdr.FrameType.IsIntra() {
		hdr.UseRefFrameMVs = uint8(gb.Bit())
	}
	return nil
}

// applyFrameRefShortSignaling performs the order-hint-based selection of the
// 7 reference frames described by spec 7.8 / dav1d obu.c lines 519-585. It
// requires the reference state to be available.
func (p *frameParser) applyFrameRefShortSignaling() error {
	hdr, gb := p.hdr, p.gb
	hdr.Refidx[0] = int8(gb.F(3))
	hdr.Refidx[1], hdr.Refidx[2] = -1, -1
	hdr.Refidx[3] = int8(gb.F(3))
	if p.opts.Refs == nil {
		return ErrRefsRequired
	}
	nBits := int(p.seq.OrderHintNBits)
	const intMax = int(^uint(0) >> 1)
	const intMin = -intMax - 1
	frameOffset := make([]int, 8)
	earliestRef := -1
	earliestOffset := intMax
	for i := 0; i < 8; i++ {
		ref := p.opts.Refs[i].FrameHdr
		if ref == nil {
			return ErrRefsRequired
		}
		d := getPOCDiff(nBits, int(ref.FrameOffset), int(hdr.FrameOffset))
		frameOffset[i] = d
		if d < earliestOffset {
			earliestOffset = d
			earliestRef = i
		}
	}
	frameOffset[hdr.Refidx[0]] = intMin
	frameOffset[hdr.Refidx[3]] = intMin
	// refidx[6] - latest forward
	refidx := -1
	latestOffset := 0
	for i := 0; i < 8; i++ {
		if frameOffset[i] >= latestOffset {
			latestOffset = frameOffset[i]
			refidx = i
		}
	}
	frameOffset[refidx] = intMin
	hdr.Refidx[6] = int8(refidx)
	// refidx[4..5] - earliest backwards
	for i := 4; i < 6; i++ {
		earliest := uint32(0xFFFFFFFF)
		refidx = -1
		for j := 0; j < 8; j++ {
			h := uint32(frameOffset[j])
			if h < earliest {
				earliest = h
				refidx = j
			}
		}
		frameOffset[refidx] = intMin
		hdr.Refidx[i] = int8(refidx)
	}
	// Fill remaining slots with latest used.
	for i := 1; i < 7; i++ {
		if hdr.Refidx[i] >= 0 {
			continue
		}
		latest := uint32(0)
		refidx = -1
		for j := 0; j < 8; j++ {
			h := uint32(frameOffset[j])
			if h >= latest {
				latest = h
				refidx = j
			}
		}
		frameOffset[refidx] = intMin
		if refidx >= 0 {
			hdr.Refidx[i] = int8(refidx)
		} else {
			hdr.Refidx[i] = int8(earliestRef)
		}
	}
	return nil
}

func getPOCDiff(nBits, a, b int) int {
	if nBits < 1 {
		return 0
	}
	diff := a - b
	mask := 1 << (nBits - 1)
	return (diff & ((1 << nBits) - 1)) - (diff & mask << 1)
}

// readFrameSize is the Go port of dav1d's read_frame_size. When useRef is true
// the size is inherited from one of the reference frames; otherwise it is
// decoded from the bitstream. Render size / super-res scaling are also handled.
func (p *frameParser) readFrameSize(useRef bool) error {
	seq, hdr, gb := p.seq, p.hdr, p.gb

	if useRef {
		for i := 0; i < header.RefsPerFrame; i++ {
			if gb.Bit() == 0 {
				continue
			}
			if p.opts.Refs == nil {
				return ErrRefsRequired
			}
			ref := p.opts.Refs[hdr.Refidx[i]].FrameHdr
			if ref == nil {
				return ErrRefsRequired
			}
			hdr.Width[1] = ref.Width[1]
			hdr.Height = ref.Height
			hdr.RenderWidth = ref.RenderWidth
			hdr.RenderHeight = ref.RenderHeight
			p.readSuperRes()
			return nil
		}
	}
	if hdr.FrameSizeOverride != 0 {
		hdr.Width[1] = int(gb.F(int(seq.WidthNBits))) + 1
		hdr.Height = int(gb.F(int(seq.HeightNBits))) + 1
	} else {
		hdr.Width[1] = seq.MaxWidth
		hdr.Height = seq.MaxHeight
	}
	p.readSuperRes()
	hdr.HaveRenderSize = uint8(gb.Bit())
	if hdr.HaveRenderSize != 0 {
		hdr.RenderWidth = int(gb.F(16)) + 1
		hdr.RenderHeight = int(gb.F(16)) + 1
	} else {
		hdr.RenderWidth = hdr.Width[1]
		hdr.RenderHeight = hdr.Height
	}
	return nil
}

func (p *frameParser) readSuperRes() {
	seq, hdr, gb := p.seq, p.hdr, p.gb
	if seq.SuperRes && gb.Bit() != 0 {
		hdr.SuperRes.Enabled = 1
	}
	if hdr.SuperRes.Enabled != 0 {
		d := 9 + uint8(gb.F(3))
		hdr.SuperRes.WidthScaleDenominator = d
		w := (hdr.Width[1]*8 + int(d)/2) / int(d)
		min16 := 16
		if hdr.Width[1] < 16 {
			min16 = hdr.Width[1]
		}
		if w < min16 {
			w = min16
		}
		hdr.Width[0] = w
	} else {
		hdr.SuperRes.WidthScaleDenominator = 8
		hdr.Width[0] = hdr.Width[1]
	}
}
