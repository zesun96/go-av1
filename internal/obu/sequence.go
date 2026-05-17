package obu

import (
	"errors"

	"github.com/zesun96/go-av1/internal/bitstream"
	"github.com/zesun96/go-av1/internal/header"
)

// Errors returned by sequence header parsing. Each one mirrors a discrete
// "goto error" in dav1d's parse_seq_hdr.
var (
	ErrInvalidProfile            = errors.New("obu: sequence header has invalid profile")
	ErrReducedStillRequiresStill = errors.New("obu: reduced_still_picture_header without still_picture")
	ErrZeroTickInStrict          = errors.New("obu: zero num_units_in_tick/time_scale in strict mode")
	ErrZeroDecodingTickInStrict  = errors.New("obu: zero num_units_in_decoding_tick in strict mode")
	ErrInvalidNumTicksPerPicture = errors.New("obu: invalid num_ticks_per_picture")
	ErrInvalidOperatingPointIDC  = errors.New("obu: invalid operating_point_idc")
	ErrInvalidColorConfig        = errors.New("obu: invalid color configuration for profile")
	ErrMonochromeIdentityInvalid = errors.New("obu: mtrx=identity requires 4:4:4 layout in strict mode")
	ErrTrailingBits              = errors.New("obu: invalid trailing bits")
)

// ParseSequenceHeader decodes a sequence_header_obu payload and fills out
// with the result. payload is the OBU body (header + leb128 size already
// consumed by the caller).
//
// This is a 1:1 port of dav1d's parse_seq_hdr in src/obu.c (lines 72-300)
// plus the obu-level check_trailing_bits invocation in dav1d_parse_obus.
func ParseSequenceHeader(payload []byte, out *header.SequenceHeader, opts ParseOptions) error {
	if out == nil {
		return errors.New("obu: nil SequenceHeader")
	}
	*out = header.SequenceHeader{}
	gb := bitstream.NewGetBits(payload)

	out.Profile = uint8(gb.F(3))
	if out.Profile > 2 {
		return ErrInvalidProfile
	}

	out.StillPicture = gb.Bit() != 0
	out.ReducedStillPictureHeader = gb.Bit() != 0
	if out.ReducedStillPictureHeader && !out.StillPicture {
		return ErrReducedStillRequiresStill
	}

	if out.ReducedStillPictureHeader {
		out.NumOperatingPoints = 1
		out.OperatingPoints[0].MajorLevel = uint8(gb.F(3))
		out.OperatingPoints[0].MinorLevel = uint8(gb.F(2))
		out.OperatingPoints[0].InitialDisplayDelay = 10
	} else {
		out.TimingInfoPresent = gb.Bit() != 0
		if out.TimingInfoPresent {
			out.NumUnitsInTick = gb.F(32)
			out.TimeScale = gb.F(32)
			if opts.StrictStdCompliance && (out.NumUnitsInTick == 0 || out.TimeScale == 0) {
				return ErrZeroTickInStrict
			}
			out.EqualPictureInterval = gb.Bit() != 0
			if out.EqualPictureInterval {
				v := gb.VLC()
				if v == 0xFFFFFFFF {
					return ErrInvalidNumTicksPerPicture
				}
				out.NumTicksPerPicture = v + 1
			}

			out.DecoderModelInfoPresent = gb.Bit() != 0
			if out.DecoderModelInfoPresent {
				out.EncoderDecoderBufferDelayLength = uint8(gb.F(5)) + 1
				out.NumUnitsInDecodingTick = gb.F(32)
				if opts.StrictStdCompliance && out.NumUnitsInDecodingTick == 0 {
					return ErrZeroDecodingTickInStrict
				}
				out.BufferRemovalDelayLength = uint8(gb.F(5)) + 1
				out.FramePresentationDelayLength = uint8(gb.F(5)) + 1
			}
		}

		out.DisplayModelInfoPresent = gb.Bit() != 0
		out.NumOperatingPoints = uint8(gb.F(5)) + 1
		for i := 0; i < int(out.NumOperatingPoints); i++ {
			op := &out.OperatingPoints[i]
			op.IDC = uint16(gb.F(12))
			if op.IDC != 0 && (op.IDC&0xff == 0 || op.IDC&0xf00 == 0) {
				return ErrInvalidOperatingPointIDC
			}
			op.MajorLevel = 2 + uint8(gb.F(3))
			op.MinorLevel = uint8(gb.F(2))
			if op.MajorLevel > 3 {
				op.Tier = uint8(gb.Bit())
			}
			if out.DecoderModelInfoPresent {
				op.DecoderModelParamPresent = gb.Bit() != 0
				if op.DecoderModelParamPresent {
					n := int(out.EncoderDecoderBufferDelayLength)
					opi := &out.OperatingParameterInfo[i]
					opi.DecoderBufferDelay = gb.F(n)
					opi.EncoderBufferDelay = gb.F(n)
					opi.LowDelayMode = gb.Bit() != 0
				}
			}
			if out.DisplayModelInfoPresent {
				op.DisplayModelParamPresent = gb.Bit() != 0
			}
			if op.DisplayModelParamPresent {
				op.InitialDisplayDelay = uint8(gb.F(4)) + 1
			} else {
				op.InitialDisplayDelay = 10
			}
		}
	}

	out.WidthNBits = uint8(gb.F(4)) + 1
	out.HeightNBits = uint8(gb.F(4)) + 1
	out.MaxWidth = int(gb.F(int(out.WidthNBits))) + 1
	out.MaxHeight = int(gb.F(int(out.HeightNBits))) + 1

	if !out.ReducedStillPictureHeader {
		out.FrameIDNumbersPresent = gb.Bit() != 0
		if out.FrameIDNumbersPresent {
			out.DeltaFrameIDNBits = uint8(gb.F(4)) + 2
			out.FrameIDNBits = uint8(gb.F(3)) + out.DeltaFrameIDNBits + 1
		}
	}

	out.SB128 = gb.Bit() != 0
	out.FilterIntra = gb.Bit() != 0
	out.IntraEdgeFilter = gb.Bit() != 0
	if out.ReducedStillPictureHeader {
		out.ScreenContentTools = header.AdaptiveAdaptive
		out.ForceIntegerMV = header.AdaptiveAdaptive
	} else {
		out.InterIntra = gb.Bit() != 0
		out.MaskedCompound = gb.Bit() != 0
		out.WarpedMotion = gb.Bit() != 0
		out.DualFilter = gb.Bit() != 0
		out.OrderHint = gb.Bit() != 0
		if out.OrderHint {
			out.JntComp = gb.Bit() != 0
			out.RefFrameMVs = gb.Bit() != 0
		}
		// screen_content_tools: 1 bit (1 => ADAPTIVE) or 1+1 bits
		// (0,x => OFF/ON).
		if gb.Bit() != 0 {
			out.ScreenContentTools = header.AdaptiveAdaptive
		} else {
			out.ScreenContentTools = header.AdaptiveBoolean(gb.Bit())
		}
		// force_integer_mv mirrors screen_content_tools encoding when
		// screen_content_tools != OFF; otherwise the field is absent
		// and dav1d stores the sentinel value 2.
		if out.ScreenContentTools != header.AdaptiveOff {
			if gb.Bit() != 0 {
				out.ForceIntegerMV = header.AdaptiveAdaptive
			} else {
				out.ForceIntegerMV = header.AdaptiveBoolean(gb.Bit())
			}
		} else {
			out.ForceIntegerMV = header.AdaptiveAdaptive
		}
		if out.OrderHint {
			out.OrderHintNBits = uint8(gb.F(3)) + 1
		}
	}
	out.SuperRes = gb.Bit() != 0
	out.CDEF = gb.Bit() != 0
	out.Restoration = gb.Bit() != 0

	// color_config().
	out.HBD = uint8(gb.Bit())
	if out.Profile == 2 && out.HBD != 0 {
		out.HBD += uint8(gb.Bit())
	}
	if out.Profile != 1 {
		out.Monochrome = gb.Bit() != 0
	}
	out.ColorDescriptionPresent = gb.Bit() != 0
	if out.ColorDescriptionPresent {
		out.Pri = header.ColorPrimaries(gb.F(8))
		out.TRC = header.TransferCharacteristics(gb.F(8))
		out.Mtrx = header.MatrixCoefficients(gb.F(8))
	} else {
		out.Pri = header.ColorPriUnknown
		out.TRC = header.TRCUnknown
		out.Mtrx = header.MCUnknown
	}
	if out.Monochrome {
		out.ColorRange = gb.Bit() != 0
		out.Layout = header.PixelLayoutI400
		out.SsHor, out.SsVer = 1, 1
		out.Chr = header.ChromaUnknown
	} else if out.Pri == header.ColorPriBT709 &&
		out.TRC == header.TRCSRGB &&
		out.Mtrx == header.MCIdentity {
		out.Layout = header.PixelLayoutI444
		out.ColorRange = true
		if out.Profile != 1 && !(out.Profile == 2 && out.HBD == 2) {
			return ErrInvalidColorConfig
		}
	} else {
		out.ColorRange = gb.Bit() != 0
		switch out.Profile {
		case 0:
			out.Layout = header.PixelLayoutI420
			out.SsHor, out.SsVer = 1, 1
		case 1:
			out.Layout = header.PixelLayoutI444
		case 2:
			if out.HBD == 2 {
				out.SsHor = uint8(gb.Bit())
				if out.SsHor != 0 {
					out.SsVer = uint8(gb.Bit())
				}
			} else {
				out.SsHor = 1
			}
			switch {
			case out.SsHor != 0 && out.SsVer != 0:
				out.Layout = header.PixelLayoutI420
			case out.SsHor != 0:
				out.Layout = header.PixelLayoutI422
			default:
				out.Layout = header.PixelLayoutI444
			}
		}
		if out.SsHor != 0 && out.SsVer != 0 {
			out.Chr = header.ChromaSamplePosition(gb.F(2))
		} else {
			out.Chr = header.ChromaUnknown
		}
	}
	if opts.StrictStdCompliance &&
		out.Mtrx == header.MCIdentity && out.Layout != header.PixelLayoutI444 {
		return ErrMonochromeIdentityInvalid
	}
	if !out.Monochrome {
		out.SeparateUVDeltaQ = gb.Bit() != 0
	}
	out.FilmGrainPresent = gb.Bit() != 0

	if gb.Err() {
		return ErrShortBuffer
	}
	return checkTrailingBits(gb, opts.StrictStdCompliance)
}

// checkTrailingBits mirrors dav1d's check_trailing_bits. It must be called
// once per OBU after the parser is done with the bit reader.
func checkTrailingBits(gb *bitstream.GetBits, strict bool) error {
	one := gb.Bit()
	if gb.Err() {
		return ErrShortBuffer
	}
	if !strict {
		return nil
	}
	// In strict mode the trailing bit must be 1 and every unread bit in
	// the current byte plus every byte after the OBU must be zero.
	if one != 1 || gb.State() != 0 {
		return ErrTrailingBits
	}
	for _, b := range gb.RemainingBytes() {
		if b != 0 {
			return ErrTrailingBits
		}
	}
	return nil
}
