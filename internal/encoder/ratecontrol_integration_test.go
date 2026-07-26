package encoder

import (
	"bytes"
	"testing"

	"github.com/zesun96/go-av1/internal/encoder/ratecontrol"
)

func TestImplFeedsPacketSizesBackToVBR(t *testing.T) {
	const width, height = 64, 64
	enc, err := NewImpl(Options{
		Width: width, Height: height, BitDepth: 8,
		FrameRateNum: 30, FrameRateDen: 1,
		CRF: 10, RateControl: int(ratecontrol.ModeVBR), TargetKbps: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := enc.fe.QIndex
	uv := bytes.Repeat([]byte{128}, width*height/4)
	for frame := 0; frame < 3; frame++ {
		y := make([]byte, width*height)
		state := uint32(17 + frame)
		for i := range y {
			state ^= state << 13
			state ^= state >> 17
			state ^= state << 5
			y[i] = byte(state)
		}
		if err := enc.SendPicture(&RawPicture{
			Y: y, U: uv, V: uv, Width: width, Height: height,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := enc.rc.QIndex(); got <= initial {
		t.Fatalf("feedback qindex=%d, initial=%d", got, initial)
	}
}
