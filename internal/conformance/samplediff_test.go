package conformance

import (
	"testing"

	"github.com/zesun96/go-av1/pkg/av1"
)

func TestFirstDifferentSampleWithPaddedOdd420(t *testing.T) {
	pic := &av1.Picture{
		Y:       []byte{1, 2, 3, 99, 4, 5, 6, 99, 7, 8, 9, 99},
		U:       []byte{10, 11, 99, 12, 13, 99},
		V:       []byte{20, 21, 99, 22, 23, 99},
		StrideY: 4, StrideUV: 3, Width: 3, Height: 3, BitDepth: 8, Chroma: av1.Chroma420,
	}
	frame, err := CopyPicture(pic)
	if err != nil {
		t.Fatal(err)
	}
	raw := append(append(append([]byte{}, frame.Y...), frame.U...), frame.V...)
	raw[len(frame.Y)+len(frame.U)+3] = 24
	diff, err := FirstDifferentSample(frame, raw)
	if err != nil {
		t.Fatal(err)
	}
	if diff == nil || diff.Plane != "V" || diff.X != 1 || diff.Y != 1 || diff.GoValue != 23 || diff.Dav1dValue != 24 {
		t.Fatalf("difference = %+v", diff)
	}
}

func TestFirstDifferentSampleEqual(t *testing.T) {
	frame := NativeFrame{Width: 2, Height: 1, BitDepth: 8, Chroma: "mono", Y: []byte{1, 2}}
	diff, err := FirstDifferentSample(frame, []byte{1, 2})
	if err != nil || diff != nil {
		t.Fatalf("difference = %+v, error = %v", diff, err)
	}
}
