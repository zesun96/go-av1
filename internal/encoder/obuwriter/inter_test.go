package obuwriter

import (
	"testing"

	"github.com/zesun96/go-av1/internal/header"
	"github.com/zesun96/go-av1/internal/obu"
)

func TestWriteInterFrameHeaderRoundTripsThroughParser(t *testing.T) {
	params := &SeqParams{
		Width: 176, Height: 144, BitDepth: 8, ChromaSS: 1, Use128SB: true,
	}
	var seq header.SequenceHeader
	if err := obu.ParseSequenceHeader(WriteSequenceHeader(params), &seq, obu.ParseOptions{}); err != nil {
		t.Fatalf("ParseSequenceHeader: %v", err)
	}

	const qindex = 91
	payload := WriteInterFrameOBU(params, qindex, []byte{0})
	var frame header.FrameHeader
	if err := obu.ParseFrameHeader(payload, &frame, obu.FrameParseOptions{SeqHeader: &seq}); err != nil {
		t.Fatalf("ParseFrameHeader: %v", err)
	}
	if frame.FrameType != header.FrameTypeInter || frame.ShowFrame == 0 {
		t.Fatalf("frame type/show flags: type=%v show=%d", frame.FrameType, frame.ShowFrame)
	}
	if frame.PrimaryRefFrame != header.PrimaryRefNone {
		t.Fatalf("primary_ref_frame=%d, want none", frame.PrimaryRefFrame)
	}
	if frame.RefreshFrameFlags != 0xff {
		t.Fatalf("refresh flags=%#x, want 0xff", frame.RefreshFrameFlags)
	}
	for i, ref := range frame.Refidx {
		if ref != 0 {
			t.Fatalf("refidx[%d]=%d, want 0", i, ref)
		}
	}
	if frame.Quant.YAC != qindex {
		t.Fatalf("qindex=%d, want %d", frame.Quant.YAC, qindex)
	}
	if frame.HP == 0 || frame.SubpelFilterMode != header.FilterMode8TapRegular {
		t.Fatalf("motion precision/filter: hp=%d filter=%d", frame.HP, frame.SubpelFilterMode)
	}
}
