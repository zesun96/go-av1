package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zesun96/go-av1/pkg/av1"
)

func TestWaitForTracks(t *testing.T) {
	s := &server{}
	if !s.trackStarted() {
		t.Fatal("track did not start")
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.trackFinished()
	}()
	if !s.waitForTracks(time.Second) {
		t.Fatal("waitForTracks timed out after track finished")
	}

	if !s.trackStarted() {
		t.Fatal("second track did not start")
	}
	if s.waitForTracks(20 * time.Millisecond) {
		t.Fatal("waitForTracks succeeded with an active track")
	}
	s.trackFinished()

	s.stopping.Store(true)
	if s.trackStarted() {
		t.Fatal("track started during shutdown")
	}
}

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

func TestY4MWriterSegmentsResolutionChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.y4m")
	w := &yuvWriter{path: path}
	first := testPicture(2, 2)
	second := testPicture(4, 2)

	if err := w.WriteFrame(first, 0); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(second, 3000); err != nil {
		t.Fatal(err)
	}
	if got := w.Written(); got != 2 {
		t.Fatalf("frames across segments = %d, want 2", got)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	for _, want := range []struct {
		path   string
		width  int
		height int
	}{
		{path: path, width: 2, height: 2},
		{path: yuvSegmentPath(path, 1), width: 4, height: 2},
	} {
		data, err := os.ReadFile(want.path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(data), y4mHeader(want.width, want.height, 30, 1)) {
			t.Fatalf("unexpected segment header in %s", want.path)
		}
		if got := strings.Count(string(data), "FRAME\n"); got != 1 {
			t.Fatalf("frame count in %s = %d, want 1", want.path, got)
		}
	}
}

func testPicture(width, height int) *av1.Picture {
	cw := (width + 1) / 2
	ch := (height + 1) / 2
	return &av1.Picture{
		Y:        make([]byte, width*height),
		U:        make([]byte, cw*ch),
		V:        make([]byte, cw*ch),
		StrideY:  width,
		StrideUV: cw,
		Width:    width,
		Height:   height,
		Chroma:   av1.Chroma420,
	}
}
