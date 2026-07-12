package tile

import (
	"testing"

	"github.com/zesun96/go-av1/internal/header"
)

func TestDecodeTileGroupRejectsEmptyPayload(t *testing.T) {
	fhdr := &header.FrameHeader{}
	fhdr.Tiling.Cols = 1
	fhdr.Tiling.Rows = 1
	seq := &header.SequenceHeader{}
	fb := &FrameBuf{Width: 8, Height: 8, Y: make([]byte, 64), StrideY: 8, Monochrome: true}
	if err := DecodeTileGroup(nil, fhdr, seq, fb, nil); err == nil {
		t.Fatal("DecodeTileGroup accepted an empty payload")
	}
}

func TestDecodeTileReturnsRecoveredPanicAsError(t *testing.T) {
	// Missing tile bounds make DecodeTile panic before decoding syntax. The
	// recovery boundary must expose that failure to its caller.
	fhdr := &header.FrameHeader{}
	fhdr.Tiling.Cols = 1
	fhdr.Tiling.Rows = 1
	seq := &header.SequenceHeader{}
	fb := &FrameBuf{Width: 8, Height: 8, Y: make([]byte, 64), StrideY: 8, Monochrome: true}
	fs := NewFrameState(8, 8)
	if err := DecodeTile(TileData{Row: 255, Col: 255, Data: []byte{0}}, fhdr, seq, fb, fs, nil); err == nil {
		t.Fatal("DecodeTile swallowed a recovered panic")
	}
}
