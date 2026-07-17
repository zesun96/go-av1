package main

import (
	"bytes"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
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

func TestFrontendUsesDirectICECandidates(t *testing.T) {
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	if strings.Contains(page, `id="stunEnabled" type="checkbox" checked`) {
		t.Fatal("frontend must keep STUN disabled by default")
	}
	if !strings.Contains(page, "if (!stunEnabled.checked) return undefined;") {
		t.Fatal("frontend does not conditionally enable STUN")
	}
	if !strings.Contains(page, "new RTCPeerConnection(peerConnectionConfig())") {
		t.Fatal("frontend does not apply the selected ICE configuration")
	}
	if !strings.Contains(page, "timeoutMs = 2000") {
		t.Fatal("frontend ICE gathering fallback is not bounded to two seconds")
	}
	for _, want := range []string{"sender.getStats()", "framesEncoded", "framesSent", "qualityLimitationReason"} {
		if !strings.Contains(page, want) {
			t.Fatalf("frontend does not report sender statistic %q", want)
		}
	}
}

func TestTemporalUnitQueueDrainsInOrder(t *testing.T) {
	q := newTemporalUnitQueue()
	for i := 0; i < 3; i++ {
		depth, peak, ok := q.Push(temporalUnit{payload: []byte{byte(i)}, pts: uint64(i * 3000)})
		if !ok || depth != i+1 || peak != i+1 {
			t.Fatalf("push %d = depth %d peak %d ok %v", i, depth, peak, ok)
		}
	}
	if pending, peak := q.Close(); pending != 3 || peak != 3 {
		t.Fatalf("close = pending %d peak %d, want 3 and 3", pending, peak)
	}
	for i := 0; i < 3; i++ {
		tu, ok := q.Pop()
		if !ok || tu.pts != uint64(i*3000) || len(tu.payload) != 1 || tu.payload[0] != byte(i) {
			t.Fatalf("pop %d = %+v, ok %v", i, tu, ok)
		}
	}
	if _, ok := q.Pop(); ok {
		t.Fatal("closed queue returned an item after draining")
	}
	if _, _, ok := q.Push(temporalUnit{}); ok {
		t.Fatal("closed queue accepted an item")
	}
}

func TestBrowserStatsEndpoint(t *testing.T) {
	s := &server{}
	req := httptest.NewRequest(http.MethodPost, "/stats", strings.NewReader(
		`{"captureFPS":30,"encodeFPS":12.5,"sendFPS":12,"width":2560,"height":1440,"limitation":"cpu"}`))
	rec := httptest.NewRecorder()
	s.handleBrowserStats(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("stats status = %d, want %d: %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/stats", nil)
	rec = httptest.NewRecorder()
	s.handleBrowserStats(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET stats status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
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

	stem := strings.TrimSuffix(path, filepath.Ext(path))
	list, err := os.ReadFile(stem + ".ffplay")
	if err != nil {
		t.Fatal(err)
	}
	wantList := "# Y4M segments in playback order\noutput.y4m\noutput-001.y4m\n"
	if string(list) != wantList {
		t.Fatalf("ffplay list = %q, want %q", list, wantList)
	}
	launcher, err := os.ReadFile(stem + "-play.cmd")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(launcher), `ffplay -autoexit -loglevel warning "%~dp0%%F"`) {
		t.Fatalf("unexpected ffplay launcher: %q", launcher)
	}
}

func TestY4MWriterPadsSmallerFramesOnFixedCanvas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.y4m")
	w := &yuvWriter{path: path}
	first := testPicture(6, 6)
	second := testPicture(2, 2)
	for i := range second.Y {
		second.Y[i] = 200
	}
	second.U[0], second.V[0] = 30, 40

	if err := w.WriteFrame(first, 0); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(second, 3000); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(yuvSegmentPath(path, 1)); !os.IsNotExist(err) {
		t.Fatalf("smaller frame unexpectedly created a segment: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parts := bytes.Split(data, []byte("FRAME\n"))
	if len(parts) != 3 {
		t.Fatalf("Y4M frame sections = %d, want 3", len(parts))
	}
	frame := parts[2]
	if len(frame) != 54 { // 6x6 Y plus two 3x3 chroma planes.
		t.Fatalf("padded frame length = %d, want 54", len(frame))
	}
	for y := 0; y < 6; y++ {
		for x := 0; x < 6; x++ {
			want := byte(16)
			if x >= 2 && x < 4 && y >= 2 && y < 4 {
				want = 200
			}
			if got := frame[y*6+x]; got != want {
				t.Fatalf("Y[%d,%d] = %d, want %d", x, y, got, want)
			}
		}
	}
	if got := frame[36+1*3+1]; got != 30 {
		t.Fatalf("centered U = %d, want 30", got)
	}
	if got := frame[45+1*3+1]; got != 40 {
		t.Fatalf("centered V = %d, want 40", got)
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
