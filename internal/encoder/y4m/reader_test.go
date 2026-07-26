package y4m_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/zesun96/go-av1/internal/encoder/y4m"
)

func TestReadOddSized420Frame(t *testing.T) {
	const width, height = 127, 129
	lumaSize := width * height
	chromaPlaneSize := ((width + 1) / 2) * ((height + 1) / 2)
	stream := bytes.NewBufferString("YUV4MPEG2 W127 H129 F1:1 Ip C420jpeg\nFRAME\n")
	stream.Write(bytes.Repeat([]byte{0x11}, lumaSize))
	stream.Write(bytes.Repeat([]byte{0x22}, chromaPlaneSize))
	stream.Write(bytes.Repeat([]byte{0x33}, chromaPlaneSize))

	r, err := y4m.NewReader(stream)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	frame, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if len(frame.Y) != lumaSize || len(frame.Cb) != chromaPlaneSize || len(frame.Cr) != chromaPlaneSize {
		t.Fatalf("plane sizes Y=%d Cb=%d Cr=%d, want %d/%d/%d",
			len(frame.Y), len(frame.Cb), len(frame.Cr),
			lumaSize, chromaPlaneSize, chromaPlaneSize)
	}
	if frame.Y[0] != 0x11 || frame.Cb[0] != 0x22 || frame.Cr[0] != 0x33 {
		t.Fatalf("plane split is incorrect: Y=%x Cb=%x Cr=%x",
			frame.Y[0], frame.Cb[0], frame.Cr[0])
	}
	if _, err := r.ReadFrame(); err != io.EOF {
		t.Fatalf("second ReadFrame error=%v, want EOF", err)
	}
}
