package encoder_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/zesun96/go-av1/internal/encoder"
	"github.com/zesun96/go-av1/internal/encoder/y4m"
)

// TestEndToEnd_EncodeGradient verifies the full encode pipeline produces valid output.
func TestEndToEnd_EncodeGradient(t *testing.T) {
	const width, height = 64, 64

	// Create encoder
	enc, err := encoder.NewImpl(encoder.Options{
		Width:        width,
		Height:       height,
		FrameRateNum: 30,
		FrameRateDen: 1,
		BitDepth:     8,
		CRF:          30,
	})
	if err != nil {
		t.Fatalf("NewImpl: %v", err)
	}

	// Create a synthetic gradient frame
	yPlane := make([]byte, width*height)
	for row := 0; row < height; row++ {
		for col := 0; col < width; col++ {
			yPlane[row*width+col] = byte((row*4 + col*4) & 0xFF)
		}
	}

	// 4:2:0 chroma planes (half dimensions)
	chromaW := width / 2
	chromaH := height / 2
	uPlane := make([]byte, chromaW*chromaH)
	vPlane := make([]byte, chromaW*chromaH)
	for i := range uPlane {
		uPlane[i] = 128 // neutral chroma
		vPlane[i] = 128
	}

	// Encode 3 frames
	for i := 0; i < 3; i++ {
		pic := &encoder.RawPicture{
			Y:      yPlane,
			U:      uPlane,
			V:      vPlane,
			Width:  width,
			Height: height,
		}
		if err := enc.SendPicture(pic); err != nil {
			t.Fatalf("SendPicture frame %d: %v", i, err)
		}
	}

	// Receive all packets
	enc.Flush()
	packetCount := 0
	totalBytes := 0
	for {
		pkt, err := enc.ReceivePacket()
		if err != nil {
			break
		}
		if pkt == nil {
			t.Fatal("ReceivePacket returned nil packet with nil error")
		}
		if len(pkt.Data) == 0 {
			t.Fatalf("packet %d has zero bytes", packetCount)
		}
		if !pkt.Keyframe {
			t.Fatalf("packet %d is not keyframe (M10 should be all keyframes)", packetCount)
		}
		totalBytes += len(pkt.Data)
		packetCount++
	}

	if packetCount != 3 {
		t.Fatalf("expected 3 packets, got %d", packetCount)
	}
	t.Logf("encoded 3 frames: %d total bytes (avg %.1f bytes/frame)", totalBytes, float64(totalBytes)/3.0)
}

// TestEndToEnd_Y4MReader verifies the Y4M parser works with a synthetic stream.
func TestEndToEnd_Y4MReader(t *testing.T) {
	const width, height = 16, 16

	// Build a minimal Y4M stream in memory
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "YUV4MPEG2 W%d H%d F30:1 C420\n", width, height)

	// One frame
	buf.WriteString("FRAME\n")
	// Y plane (16x16 = 256 bytes)
	yData := make([]byte, width*height)
	for i := range yData {
		yData[i] = byte(i & 0xFF)
	}
	buf.Write(yData)
	// U and V planes (8x8 = 64 each for 4:2:0)
	chromaSize := (width / 2) * (height / 2)
	uData := make([]byte, chromaSize)
	vData := make([]byte, chromaSize)
	buf.Write(uData)
	buf.Write(vData)

	// Parse it
	reader, err := y4m.NewReader(&buf)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	if reader.Header.Width != width || reader.Header.Height != height {
		t.Fatalf("dimensions: got %dx%d, want %dx%d",
			reader.Header.Width, reader.Header.Height, width, height)
	}
	if reader.Header.ChromaSS != "420" {
		t.Fatalf("chroma: got %q, want 420", reader.Header.ChromaSS)
	}

	frame, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if len(frame.Y) != width*height {
		t.Fatalf("Y plane size: got %d, want %d", len(frame.Y), width*height)
	}
	if len(frame.Cb) != chromaSize {
		t.Fatalf("Cb plane size: got %d, want %d", len(frame.Cb), chromaSize)
	}
	t.Logf("Y4M reader: parsed %dx%d frame successfully", width, height)
}

// TestEndToEnd_OBUFormat checks that encoded output starts with valid OBU structure.
func TestEndToEnd_OBUFormat(t *testing.T) {
	const width, height = 32, 32

	enc, err := encoder.NewImpl(encoder.Options{
		Width:        width,
		Height:       height,
		FrameRateNum: 30,
		FrameRateDen: 1,
		BitDepth:     8,
		CRF:          30,
	})
	if err != nil {
		t.Fatalf("NewImpl: %v", err)
	}

	// Flat grey frame
	yPlane := make([]byte, width*height)
	for i := range yPlane {
		yPlane[i] = 128
	}
	uPlane := make([]byte, width*height/4)
	vPlane := make([]byte, width*height/4)
	for i := range uPlane {
		uPlane[i] = 128
		vPlane[i] = 128
	}

	pic := &encoder.RawPicture{Y: yPlane, U: uPlane, V: vPlane, Width: width, Height: height}
	if err := enc.SendPicture(pic); err != nil {
		t.Fatalf("SendPicture: %v", err)
	}

	pkt, err := enc.ReceivePacket()
	if err != nil {
		t.Fatalf("ReceivePacket: %v", err)
	}

	data := pkt.Data
	if len(data) < 4 {
		t.Fatalf("encoded data too short: %d bytes", len(data))
	}

	// First OBU should be Temporal Delimiter
	// OBU header byte: forbidden(0) | type(4bit) | ext(0) | has_size(1) | reserved(0)
	// TD type = 2 -> 0b0_0010_0_1_0 = 0x12
	obuByte := data[0]
	obuType := (obuByte >> 3) & 0x0F
	if obuType != 2 { // OBU_TEMPORAL_DELIMITER
		t.Fatalf("first OBU type: got %d, want 2 (Temporal Delimiter)", obuType)
	}

	t.Logf("OBU format check passed: %d bytes, starts with TD (type=%d)", len(data), obuType)
}
