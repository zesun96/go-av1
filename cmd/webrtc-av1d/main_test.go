package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zesun96/go-av1/pkg/av1"
)

func TestIVFWriterPreservesRTPTimestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.ivf")
	w := &ivfWriter{path: path}

	if err := w.WriteFrame([]byte{1, 2, 3}, 0); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame([]byte{4, 5}, 9000); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(data[16:20]); got != rtpVideoClock {
		t.Fatalf("IVF rate numerator = %d, want %d", got, rtpVideoClock)
	}
	if got := binary.LittleEndian.Uint32(data[20:24]); got != 1 {
		t.Fatalf("IVF rate denominator = %d, want 1", got)
	}
	if got := binary.LittleEndian.Uint32(data[24:28]); got != 2 {
		t.Fatalf("IVF frame count = %d, want 2", got)
	}
	secondHeader := ivfFileHeaderSize + ivfFrameHeaderSize + 3
	if got := binary.LittleEndian.Uint64(data[secondHeader+4 : secondHeader+12]); got != 9000 {
		t.Fatalf("second frame PTS = %d, want 9000", got)
	}
	if got := w.Written(); got != 0 {
		t.Fatalf("writer retained %d frames after close", got)
	}
}

func TestY4MWriterFinalizesObservedFrameRate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.y4m")
	w := &yuvWriter{path: path}
	pic := &av1.Picture{
		Y:        []byte{10, 20, 30, 40},
		U:        []byte{50},
		V:        []byte{60},
		StrideY:  2,
		StrideUV: 1,
		Width:    2,
		Height:   2,
		Chroma:   av1.Chroma420,
	}

	if err := w.WriteFrame(pic, 0); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(pic, rtpVideoClock); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantHeader := y4mHeader(2, 2, 1, 1)
	if !strings.HasPrefix(string(data), wantHeader) {
		t.Fatalf("Y4M header = %q, want %q", strings.SplitN(string(data), "\n", 2)[0], strings.TrimSpace(wantHeader))
	}
	if got := strings.Count(string(data), "FRAME\n"); got != 2 {
		t.Fatalf("Y4M frame count = %d, want 2", got)
	}
	if got := w.Written(); got != 0 {
		t.Fatalf("writer retained %d frames after close", got)
	}
}
