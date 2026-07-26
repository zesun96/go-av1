package encoder_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/zesun96/go-av1/internal/encoder"
	"github.com/zesun96/go-av1/internal/encoder/y4m"
	"github.com/zesun96/go-av1/pkg/av1"
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
	firstPacketBytes := 0
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
		if pkt.Keyframe != (packetCount == 0) {
			t.Fatalf("packet %d keyframe=%t, want %t", packetCount, pkt.Keyframe, packetCount == 0)
		}
		if packetCount == 0 {
			firstPacketBytes = len(pkt.Data)
		} else if len(pkt.Data) >= firstPacketBytes {
			t.Fatalf("show-existing packet %d has %d bytes, key frame has %d",
				packetCount, len(pkt.Data), firstPacketBytes)
		}
		totalBytes += len(pkt.Data)
		packetCount++
	}

	if packetCount != 3 {
		t.Fatalf("expected 3 packets, got %d", packetCount)
	}
	t.Logf("encoded 3 frames: %d total bytes (avg %.1f bytes/frame)", totalBytes, float64(totalBytes)/3.0)
}

func TestEncodeDecodeRepeatedFrameReference(t *testing.T) {
	const width, height = 64, 64
	y := make([]byte, width*height)
	for row := 0; row < height; row++ {
		for col := 0; col < width; col++ {
			y[row*width+col] = byte(row*3 + col*5)
		}
	}
	uv := bytes.Repeat([]byte{128}, width*height/4)
	enc, err := encoder.NewImpl(encoder.Options{
		Width: width, Height: height, BitDepth: 8, CRF: 30,
	})
	if err != nil {
		t.Fatalf("NewImpl: %v", err)
	}
	pic := &encoder.RawPicture{Y: y, U: uv, V: uv, Width: width, Height: height}
	if err := enc.SendPicture(pic); err != nil {
		t.Fatalf("first SendPicture: %v", err)
	}
	if err := enc.SendPicture(pic); err != nil {
		t.Fatalf("second SendPicture: %v", err)
	}
	key, err := enc.ReceivePacket()
	if err != nil {
		t.Fatalf("key ReceivePacket: %v", err)
	}
	repeat, err := enc.ReceivePacket()
	if err != nil {
		t.Fatalf("repeat ReceivePacket: %v", err)
	}
	if !key.Keyframe || repeat.Keyframe {
		t.Fatalf("key flags first=%t repeat=%t", key.Keyframe, repeat.Keyframe)
	}
	if len(repeat.Data) >= len(key.Data) {
		t.Fatalf("repeat packet=%d bytes, key packet=%d", len(repeat.Data), len(key.Data))
	}

	dec, err := av1.NewDecoder(av1.DecoderOptions{})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer dec.Close()
	if err := dec.SendData(key.Data); err != nil {
		t.Fatalf("decode key: %v", err)
	}
	first, err := dec.GetPicture()
	if err != nil {
		t.Fatalf("GetPicture key: %v", err)
	}
	firstY := append([]byte(nil), first.Y...)
	first.Release()
	if err := dec.SendData(repeat.Data); err != nil {
		t.Fatalf("decode show-existing: %v", err)
	}
	second, err := dec.GetPicture()
	if err != nil {
		t.Fatalf("GetPicture show-existing: %v", err)
	}
	defer second.Release()
	if !bytes.Equal(second.Y, firstY) {
		t.Fatal("show-existing output differs from referenced key frame")
	}
}

func TestEncodeDecodeChangedInterFrame(t *testing.T) {
	const width, height = 64, 64
	values := []byte{64, 160, 32, 220}
	enc, err := encoder.NewImpl(encoder.Options{
		Width: width, Height: height, BitDepth: 8, CRF: 30,
	})
	if err != nil {
		t.Fatalf("NewImpl: %v", err)
	}
	for i, value := range values {
		uv := bytes.Repeat([]byte{128 + byte(i)*4}, width*height/4)
		pic := &encoder.RawPicture{
			Y: bytes.Repeat([]byte{value}, width*height), U: uv, V: uv,
			Width: width, Height: height,
		}
		if err := enc.SendPicture(pic); err != nil {
			t.Fatalf("SendPicture %d: %v", i, err)
		}
	}
	packets := make([]*encoder.Packet, len(values))
	for i := range packets {
		packets[i], err = enc.ReceivePacket()
		if err != nil {
			t.Fatalf("ReceivePacket %d: %v", i, err)
		}
		if packets[i].Keyframe != (i == 0) {
			t.Fatalf("packet %d keyframe=%t", i, packets[i].Keyframe)
		}
	}

	dec, err := av1.NewDecoder(av1.DecoderOptions{})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer dec.Close()
	means := make([]float64, len(values))
	for i, pkt := range packets {
		if err := dec.SendData(pkt.Data); err != nil {
			t.Fatalf("SendData %d: %v", i, err)
		}
		pic, err := dec.GetPicture()
		if err != nil {
			t.Fatalf("GetPicture %d: %v", i, err)
		}
		var sum uint64
		for row := 0; row < height; row++ {
			for _, sample := range pic.Y[row*pic.StrideY : row*pic.StrideY+width] {
				sum += uint64(sample)
			}
		}
		means[i] = float64(sum) / float64(width*height)
		pic.Release()
	}
	for i, mean := range means {
		if diff := mean - float64(values[i]); diff < -8 || diff > 8 {
			t.Fatalf("frame %d reconstruction mean %.2f, want near %d (all means %v)",
				i, mean, values[i], means)
		}
	}
}

func TestEncodeDecodeChangedInterFrameSizes(t *testing.T) {
	for _, size := range [][2]int{{8, 8}, {16, 18}, {65, 63}, {196, 198}} {
		w, h := size[0], size[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			cw, ch := (w+1)/2, (h+1)/2
			enc, err := encoder.NewImpl(encoder.Options{
				Width: w, Height: h, BitDepth: 8, CRF: 30,
			})
			if err != nil {
				t.Fatalf("NewImpl: %v", err)
			}
			for _, value := range []byte{48, 192} {
				if err := enc.SendPicture(&encoder.RawPicture{
					Y:     bytes.Repeat([]byte{value}, w*h),
					U:     bytes.Repeat([]byte{128}, cw*ch),
					V:     bytes.Repeat([]byte{128}, cw*ch),
					Width: w, Height: h,
				}); err != nil {
					t.Fatalf("SendPicture: %v", err)
				}
			}
			dec, err := av1.NewDecoder(av1.DecoderOptions{})
			if err != nil {
				t.Fatalf("NewDecoder: %v", err)
			}
			defer dec.Close()
			for i, want := range []byte{48, 192} {
				pkt, err := enc.ReceivePacket()
				if err != nil {
					t.Fatalf("ReceivePacket %d: %v", i, err)
				}
				if err := dec.SendData(pkt.Data); err != nil {
					t.Fatalf("SendData %d: %v", i, err)
				}
				pic, err := dec.GetPicture()
				if err != nil {
					t.Fatalf("GetPicture %d: %v", i, err)
				}
				var sum uint64
				for row := 0; row < h; row++ {
					for _, sample := range pic.Y[row*pic.StrideY : row*pic.StrideY+w] {
						sum += uint64(sample)
					}
				}
				mean := float64(sum) / float64(w*h)
				pic.Release()
				if diff := mean - float64(want); diff < -10 || diff > 10 {
					t.Fatalf("frame %d mean %.2f, want near %d", i, mean, want)
				}
			}
		})
	}
}

func TestEncodeDecodeTranslatedInterFrame(t *testing.T) {
	const width, height = 64, 64
	base := make([]byte, width*height)
	state := uint32(17)
	for i := range base {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		base[i] = byte(state)
	}
	translated := make([]byte, len(base))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			sx, sy := x+2, y-1
			if sx >= width {
				sx = width - 1
			}
			if sy < 0 {
				sy = 0
			}
			translated[y*width+x] = base[sy*width+sx]
		}
	}
	uv := bytes.Repeat([]byte{128}, width*height/4)
	enc, err := encoder.NewImpl(encoder.Options{
		Width: width, Height: height, BitDepth: 8, CRF: 10,
	})
	if err != nil {
		t.Fatalf("NewImpl: %v", err)
	}
	for _, y := range [][]byte{base, translated} {
		if err := enc.SendPicture(&encoder.RawPicture{
			Y: y, U: uv, V: uv, Width: width, Height: height,
		}); err != nil {
			t.Fatalf("SendPicture: %v", err)
		}
	}
	dec, err := av1.NewDecoder(av1.DecoderOptions{})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer dec.Close()
	var packetSizes [2]int
	for frame := 0; frame < 2; frame++ {
		pkt, err := enc.ReceivePacket()
		if err != nil {
			t.Fatalf("ReceivePacket %d: %v", frame, err)
		}
		packetSizes[frame] = len(pkt.Data)
		if err := dec.SendData(pkt.Data); err != nil {
			t.Fatalf("SendData %d: %v", frame, err)
		}
		pic, err := dec.GetPicture()
		if err != nil {
			t.Fatalf("GetPicture %d: %v", frame, err)
		}
		if frame == 1 {
			var sse uint64
			for row := 0; row < height; row++ {
				for col := 0; col < width; col++ {
					diff := int(pic.Y[row*pic.StrideY+col]) - int(translated[row*width+col])
					sse += uint64(diff * diff)
				}
			}
			if mse := float64(sse) / float64(width*height); mse > 2500 {
				t.Fatalf("translated inter reconstruction MSE %.2f is too high", mse)
			}
		}
		pic.Release()
	}
	if packetSizes[1] >= packetSizes[0] {
		t.Fatalf("translated inter packet=%d bytes, key packet=%d", packetSizes[1], packetSizes[0])
	}
}

// TestEncodeDecodeRoundTripSizes guards the M11 exit criterion: packets from
// the encoder must be consumable by the strict decoder, including partial
// superblocks at the right and bottom frame edges.
func TestEncodeDecodeRoundTripSizes(t *testing.T) {
	sizes := [][2]int{
		{8, 8},
		{16, 18},
		{32, 32},
		{64, 64},
		{65, 63},
		{128, 96},
		{196, 198},
	}
	for _, size := range sizes {
		w, h := size[0], size[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			enc, err := encoder.NewImpl(encoder.Options{
				Width: w, Height: h, BitDepth: 8, CRF: 30,
			})
			if err != nil {
				t.Fatalf("NewImpl: %v", err)
			}
			pic := &encoder.RawPicture{
				Y:     make([]byte, w*h),
				U:     make([]byte, ((w+1)/2)*((h+1)/2)),
				V:     make([]byte, ((w+1)/2)*((h+1)/2)),
				Width: w, Height: h,
			}
			if err := enc.SendPicture(pic); err != nil {
				t.Fatalf("SendPicture: %v", err)
			}
			pkt, err := enc.ReceivePacket()
			if err != nil {
				t.Fatalf("ReceivePacket: %v", err)
			}

			dec, err := av1.NewDecoder(av1.DecoderOptions{})
			if err != nil {
				t.Fatalf("NewDecoder: %v", err)
			}
			defer dec.Close()
			if err := dec.SendData(pkt.Data); err != nil {
				t.Fatalf("decode packet: %v", err)
			}
			got, err := dec.GetPicture()
			if err != nil {
				t.Fatalf("GetPicture: %v", err)
			}
			defer got.Release()
			if got.Width != w || got.Height != h {
				t.Fatalf("decoded dimensions = %dx%d, want %dx%d", got.Width, got.Height, w, h)
			}
		})
	}
}

func TestEncodeDecodeStressPatterns(t *testing.T) {
	cases := []struct {
		w, h int
		crf  int
		seed uint32
	}{
		{1, 1, 0, 1},
		{7, 7, 1, 2},
		{8, 9, 30, 3},
		{9, 8, 63, 4},
		{17, 33, 0, 5},
		{63, 65, 63, 6},
		{127, 129, 1, 7},
		{257, 131, 30, 8},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%dx%d_crf%d", tc.w, tc.h, tc.crf), func(t *testing.T) {
			y := make([]byte, tc.w*tc.h)
			state := tc.seed
			for row := 0; row < tc.h; row++ {
				for col := 0; col < tc.w; col++ {
					state ^= state << 13
					state ^= state >> 17
					state ^= state << 5
					// Combine hard edges, gradients and deterministic noise.
					y[row*tc.w+col] = byte(state + uint32(row*29+col*17+(col/4)*91))
				}
			}
			cw, ch := (tc.w+1)/2, (tc.h+1)/2
			u := make([]byte, cw*ch)
			v := make([]byte, cw*ch)
			for i := range u {
				u[i] = byte(16 + (i*37)%220)
				v[i] = byte(235 - (i*53)%220)
			}

			enc, err := encoder.NewImpl(encoder.Options{
				Width: tc.w, Height: tc.h, BitDepth: 8, CRF: tc.crf,
			})
			if err != nil {
				t.Fatalf("NewImpl: %v", err)
			}
			if err := enc.SendPicture(&encoder.RawPicture{
				Y: y, U: u, V: v, Width: tc.w, Height: tc.h,
			}); err != nil {
				t.Fatalf("SendPicture: %v", err)
			}
			pkt, err := enc.ReceivePacket()
			if err != nil {
				t.Fatalf("ReceivePacket: %v", err)
			}
			if len(pkt.Data) == 0 {
				t.Fatal("empty encoded packet")
			}

			dec, err := av1.NewDecoder(av1.DecoderOptions{})
			if err != nil {
				t.Fatalf("NewDecoder: %v", err)
			}
			defer dec.Close()
			if err := dec.SendData(pkt.Data); err != nil {
				t.Fatalf("SendData: %v", err)
			}
			pic, err := dec.GetPicture()
			if err != nil {
				t.Fatalf("GetPicture: %v", err)
			}
			defer pic.Release()
			if pic.Width != tc.w || pic.Height != tc.h {
				t.Fatalf("decoded dimensions=%dx%d, want %dx%d",
					pic.Width, pic.Height, tc.w, tc.h)
			}
		})
	}
}

func TestEncodeDecodePreservesDCContrast(t *testing.T) {
	const width, height = 128, 128
	decodeMean := func(value byte) float64 {
		enc, err := encoder.NewImpl(encoder.Options{
			Width: width, Height: height, BitDepth: 8, CRF: 30,
		})
		if err != nil {
			t.Fatalf("NewImpl: %v", err)
		}
		y := bytes.Repeat([]byte{value}, width*height)
		uv := bytes.Repeat([]byte{128}, width*height/4)
		if err := enc.SendPicture(&encoder.RawPicture{
			Y: y, U: uv, V: uv, Width: width, Height: height,
		}); err != nil {
			t.Fatalf("SendPicture: %v", err)
		}
		pkt, err := enc.ReceivePacket()
		if err != nil {
			t.Fatalf("ReceivePacket: %v", err)
		}
		dec, err := av1.NewDecoder(av1.DecoderOptions{})
		if err != nil {
			t.Fatalf("NewDecoder: %v", err)
		}
		defer dec.Close()
		if err := dec.SendData(pkt.Data); err != nil {
			t.Fatalf("SendData: %v", err)
		}
		pic, err := dec.GetPicture()
		if err != nil {
			t.Fatalf("GetPicture: %v", err)
		}
		defer pic.Release()
		var sum uint64
		for row := 0; row < height; row++ {
			for _, sample := range pic.Y[row*pic.StrideY : row*pic.StrideY+width] {
				sum += uint64(sample)
			}
		}
		return float64(sum) / float64(width*height)
	}

	dark := decodeMean(16)
	bright := decodeMean(235)
	if dark >= bright {
		t.Fatalf("decoded DC contrast lost: dark mean %.2f, bright mean %.2f", dark, bright)
	}
	if dark == 128 && bright == 128 {
		t.Fatal("encoder still emits the old all-skipped gray reconstruction")
	}
}

func TestEncodeDecodePreservesEightByEightDetail(t *testing.T) {
	const width, height = 64, 64
	y := make([]byte, width*height)
	for by := 0; by < height/8; by++ {
		for bx := 0; bx < width/8; bx++ {
			value := byte(16)
			if (bx+by)&1 != 0 {
				value = 235
			}
			for row := 0; row < 8; row++ {
				for col := 0; col < 8; col++ {
					y[(by*8+row)*width+bx*8+col] = value
				}
			}
		}
	}
	uv := bytes.Repeat([]byte{128}, width*height/4)
	enc, err := encoder.NewImpl(encoder.Options{
		Width: width, Height: height, BitDepth: 8, CRF: 30,
	})
	if err != nil {
		t.Fatalf("NewImpl: %v", err)
	}
	if err := enc.SendPicture(&encoder.RawPicture{
		Y: y, U: uv, V: uv, Width: width, Height: height,
	}); err != nil {
		t.Fatalf("SendPicture: %v", err)
	}
	pkt, err := enc.ReceivePacket()
	if err != nil {
		t.Fatalf("ReceivePacket: %v", err)
	}
	dec, err := av1.NewDecoder(av1.DecoderOptions{})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer dec.Close()
	if err := dec.SendData(pkt.Data); err != nil {
		t.Fatalf("SendData: %v", err)
	}
	pic, err := dec.GetPicture()
	if err != nil {
		t.Fatalf("GetPicture: %v", err)
	}
	defer pic.Release()
	blockMean := func(bx, by int) float64 {
		sum := 0
		for row := 0; row < 8; row++ {
			for _, value := range pic.Y[(by*8+row)*pic.StrideY+bx*8 : (by*8+row)*pic.StrideY+bx*8+8] {
				sum += int(value)
			}
		}
		return float64(sum) / 64
	}
	dark := blockMean(0, 0)
	bright := blockMean(1, 0)
	if bright-dark < 2 {
		t.Fatalf("8x8 detail lost: dark %.2f bright %.2f", dark, bright)
	}
}

func TestEncodeDecodePreservesDetailWithinEightByEightBlock(t *testing.T) {
	const width, height = 64, 64
	y := make([]byte, width*height)
	for row := 0; row < height; row++ {
		for col := 0; col < width; col++ {
			if col&7 < 4 {
				y[row*width+col] = 16
			} else {
				y[row*width+col] = 235
			}
		}
	}
	uv := bytes.Repeat([]byte{128}, width*height/4)
	enc, err := encoder.NewImpl(encoder.Options{
		Width: width, Height: height, BitDepth: 8, CRF: 30,
	})
	if err != nil {
		t.Fatalf("NewImpl: %v", err)
	}
	if err := enc.SendPicture(&encoder.RawPicture{
		Y: y, U: uv, V: uv, Width: width, Height: height,
	}); err != nil {
		t.Fatalf("SendPicture: %v", err)
	}
	pkt, err := enc.ReceivePacket()
	if err != nil {
		t.Fatalf("ReceivePacket: %v", err)
	}
	dec, err := av1.NewDecoder(av1.DecoderOptions{})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer dec.Close()
	if err := dec.SendData(pkt.Data); err != nil {
		t.Fatalf("SendData: %v", err)
	}
	pic, err := dec.GetPicture()
	if err != nil {
		t.Fatalf("GetPicture: %v", err)
	}
	defer pic.Release()

	halfMean := func(x0 int) float64 {
		sum := 0
		for row := 0; row < 8; row++ {
			for col := x0; col < x0+4; col++ {
				sum += int(pic.Y[row*pic.StrideY+col])
			}
		}
		return float64(sum) / 32
	}
	left, right := halfMean(0), halfMean(4)
	if right-left < 2 {
		t.Fatalf("intra-block AC detail lost: left %.2f right %.2f", left, right)
	}
}

func TestEncodeDecodePreservesChromaDetailWithinFourByFourBlock(t *testing.T) {
	const width, height = 64, 64
	y := bytes.Repeat([]byte{128}, width*height)
	const cw, ch = width / 2, height / 2
	u := make([]byte, cw*ch)
	v := bytes.Repeat([]byte{128}, cw*ch)
	for row := 0; row < ch; row++ {
		for col := 0; col < cw; col++ {
			if col&3 < 2 {
				u[row*cw+col] = 16
			} else {
				u[row*cw+col] = 235
			}
		}
	}
	enc, err := encoder.NewImpl(encoder.Options{
		Width: width, Height: height, BitDepth: 8, CRF: 30,
	})
	if err != nil {
		t.Fatalf("NewImpl: %v", err)
	}
	if err := enc.SendPicture(&encoder.RawPicture{
		Y: y, U: u, V: v, Width: width, Height: height,
	}); err != nil {
		t.Fatalf("SendPicture: %v", err)
	}
	pkt, err := enc.ReceivePacket()
	if err != nil {
		t.Fatalf("ReceivePacket: %v", err)
	}
	dec, err := av1.NewDecoder(av1.DecoderOptions{})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer dec.Close()
	if err := dec.SendData(pkt.Data); err != nil {
		t.Fatalf("SendData: %v", err)
	}
	pic, err := dec.GetPicture()
	if err != nil {
		t.Fatalf("GetPicture: %v", err)
	}
	defer pic.Release()
	halfMean := func(x0 int) float64 {
		sum := 0
		for row := 0; row < 4; row++ {
			for col := x0; col < x0+2; col++ {
				sum += int(pic.U[row*pic.StrideUV+col])
			}
		}
		return float64(sum) / 8
	}
	left, right := halfMean(0), halfMean(2)
	if right-left < 2 {
		t.Fatalf("chroma AC detail lost: left %.2f right %.2f", left, right)
	}
}

func TestEncodeDecodeLowQPreservesWholeOddFrame(t *testing.T) {
	const width, height = 65, 63
	cw, ch := (width+1)/2, (height+1)/2
	y := make([]byte, width*height)
	u := make([]byte, cw*ch)
	v := make([]byte, cw*ch)
	for row := 0; row < height; row++ {
		for col := 0; col < width; col++ {
			y[row*width+col] = byte((row*3 + col*5 + (col/7)*19) & 255)
		}
	}
	for row := 0; row < ch; row++ {
		for col := 0; col < cw; col++ {
			u[row*cw+col] = byte((64 + row*5 + col*7) & 255)
			v[row*cw+col] = byte((192 + row*9 - col*3) & 255)
		}
	}
	enc, err := encoder.NewImpl(encoder.Options{
		Width: width, Height: height, BitDepth: 8, CRF: 0,
	})
	if err != nil {
		t.Fatalf("NewImpl: %v", err)
	}
	if err := enc.SendPicture(&encoder.RawPicture{
		Y: y, U: u, V: v, Width: width, Height: height,
	}); err != nil {
		t.Fatalf("SendPicture: %v", err)
	}
	pkt, err := enc.ReceivePacket()
	if err != nil {
		t.Fatalf("ReceivePacket: %v", err)
	}
	dec, err := av1.NewDecoder(av1.DecoderOptions{})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer dec.Close()
	if err := dec.SendData(pkt.Data); err != nil {
		t.Fatalf("SendData: %v", err)
	}
	pic, err := dec.GetPicture()
	if err != nil {
		t.Fatalf("GetPicture: %v", err)
	}
	defer pic.Release()

	checkMSE := func(name string, want, got []byte, w, h, stride int) {
		var squared uint64
		for row := 0; row < h; row++ {
			for col := 0; col < w; col++ {
				diff := int(want[row*w+col]) - int(got[row*stride+col])
				squared += uint64(diff * diff)
			}
		}
		mse := float64(squared) / float64(w*h)
		if mse > 1 {
			t.Fatalf("%s whole-frame MSE %.4f, want <=1", name, mse)
		}
	}
	checkMSE("Y", y, pic.Y, width, height, pic.StrideY)
	checkMSE("U", u, pic.U, cw, ch, pic.StrideUV)
	checkMSE("V", v, pic.V, cw, ch, pic.StrideUV)
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
