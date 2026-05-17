package ivf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// buildFile assembles a minimal IVF file with the supplied frames.
func buildFile(t *testing.T, fourcc string, frames []struct {
	pts     uint64
	payload []byte
}) []byte {
	t.Helper()
	var hdr [32]byte
	copy(hdr[0:4], "DKIF")
	binary.LittleEndian.PutUint16(hdr[4:6], 0)
	binary.LittleEndian.PutUint16(hdr[6:8], 32)
	copy(hdr[8:12], fourcc)
	binary.LittleEndian.PutUint16(hdr[12:14], 1280)
	binary.LittleEndian.PutUint16(hdr[14:16], 720)
	binary.LittleEndian.PutUint32(hdr[16:20], 30)
	binary.LittleEndian.PutUint32(hdr[20:24], 1)
	binary.LittleEndian.PutUint32(hdr[24:28], uint32(len(frames)))
	out := append([]byte(nil), hdr[:]...)
	var rec [12]byte
	for _, f := range frames {
		binary.LittleEndian.PutUint32(rec[0:4], uint32(len(f.payload)))
		binary.LittleEndian.PutUint64(rec[4:12], f.pts)
		out = append(out, rec[:]...)
		out = append(out, f.payload...)
	}
	return out
}

func TestNewDemuxer_HappyPath(t *testing.T) {
	frames := []struct {
		pts     uint64
		payload []byte
	}{
		{0, []byte{0x01, 0x02, 0x03}},
		{33, []byte{0xAA, 0xBB}},
	}
	file := buildFile(t, "AV01", frames)
	d, err := NewDemuxer(bytes.NewReader(file), true)
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}
	h := d.Header()
	if string(h.FourCC[:]) != "AV01" || h.Width != 1280 || h.Height != 720 {
		t.Fatalf("header parsed wrong: %+v", h)
	}
	if h.TimebaseNum != 30 || h.TimebaseDen != 1 {
		t.Fatalf("timebase parsed wrong: %+v", h)
	}
	if h.FrameCount != 2 {
		t.Fatalf("frame count = %d, want 2", h.FrameCount)
	}
	for i, want := range frames {
		fh, payload, err := d.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame[%d]: %v", i, err)
		}
		if fh.PTS != want.pts || string(payload) != string(want.payload) {
			t.Fatalf("frame[%d] mismatch: got (%d,%v) want (%d,%v)", i,
				fh.PTS, payload, want.pts, want.payload)
		}
	}
	if _, _, err := d.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("trailing read should be EOF, got %v", err)
	}
}

func TestNewDemuxer_BadMagic(t *testing.T) {
	file := buildFile(t, "AV01", nil)
	file[0] = 'X'
	if _, err := NewDemuxer(bytes.NewReader(file), true); !errors.Is(err, ErrBadMagic) {
		t.Fatalf("err = %v, want ErrBadMagic", err)
	}
}

func TestNewDemuxer_UnsupportedCodec(t *testing.T) {
	file := buildFile(t, "VP90", nil)
	if _, err := NewDemuxer(bytes.NewReader(file), true); !errors.Is(err, ErrUnsupportedCodec) {
		t.Fatalf("err = %v, want ErrUnsupportedCodec", err)
	}
	// EnforceAV1=false accepts the same file.
	d, err := NewDemuxer(bytes.NewReader(file), false)
	if err != nil {
		t.Fatalf("non-enforcing demuxer rejected VP90: %v", err)
	}
	hdrOut := d.Header()
	if string(hdrOut.FourCC[:]) != "VP90" {
		t.Fatalf("FourCC parsed wrong: %q", hdrOut.FourCC[:])
	}
}

func TestNewDemuxer_ShortHeader(t *testing.T) {
	if _, err := NewDemuxer(bytes.NewReader([]byte("DKIF")), true); !errors.Is(err, ErrShortHeader) {
		t.Fatalf("err = %v, want ErrShortHeader", err)
	}
	if _, err := NewDemuxer(bytes.NewReader(nil), true); !errors.Is(err, ErrShortHeader) {
		t.Fatalf("err = %v, want ErrShortHeader on empty input", err)
	}
}

// failingReader returns an arbitrary error on the first Read call.
type failingReader struct{ err error }

func (f *failingReader) Read(p []byte) (int, error) { return 0, f.err }

func TestNewDemuxer_ReaderError(t *testing.T) {
	want := errors.New("boom")
	if _, err := NewDemuxer(&failingReader{err: want}, true); err == nil ||
		errors.Is(err, ErrShortHeader) || !errors.Is(err, want) {
		t.Fatalf("err = %v, want wrap of %v", err, want)
	}
}

func TestReadFrame_ShortRecord(t *testing.T) {
	file := buildFile(t, "AV01", nil)
	// Append a truncated 12-byte record (only 4 bytes).
	file = append(file, 0x10, 0x00, 0x00, 0x00)
	d, err := NewDemuxer(bytes.NewReader(file), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.ReadFrame(); !errors.Is(err, ErrShortFrame) {
		t.Fatalf("err = %v, want ErrShortFrame", err)
	}
}

func TestReadFrame_ShortPayload(t *testing.T) {
	file := buildFile(t, "AV01", nil)
	// Promise 8 bytes of payload but only supply 3.
	var rec [12]byte
	binary.LittleEndian.PutUint32(rec[0:4], 8)
	binary.LittleEndian.PutUint64(rec[4:12], 0)
	file = append(file, rec[:]...)
	file = append(file, 0x01, 0x02, 0x03)
	d, err := NewDemuxer(bytes.NewReader(file), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.ReadFrame(); !errors.Is(err, ErrShortFrame) {
		t.Fatalf("err = %v, want ErrShortFrame", err)
	}
}

// midstreamFailingReader wraps a successful header read with a failing data
// read, to cover the non-EOF wrapping branch of ReadFrame.
type midstreamFailingReader struct {
	header []byte
	off    int
	err    error
}

func (m *midstreamFailingReader) Read(p []byte) (int, error) {
	if m.off < len(m.header) {
		n := copy(p, m.header[m.off:])
		m.off += n
		return n, nil
	}
	return 0, m.err
}

func TestReadFrame_GenericReaderError(t *testing.T) {
	file := buildFile(t, "AV01", nil)
	want := errors.New("disk on fire")
	r := &midstreamFailingReader{header: file, err: want}
	d, err := NewDemuxer(r, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.ReadFrame(); err == nil || !errors.Is(err, want) {
		t.Fatalf("err = %v, want wrap of %v", err, want)
	}
}

// payloadFailingReader serves the header + frame record cleanly and then
// fails midway through the payload with a non-EOF error.
type payloadFailingReader struct {
	prefix []byte
	off    int
	err    error
}

func (p *payloadFailingReader) Read(b []byte) (int, error) {
	if p.off < len(p.prefix) {
		n := copy(b, p.prefix[p.off:])
		p.off += n
		return n, nil
	}
	return 0, p.err
}

func TestReadFrame_PayloadGenericError(t *testing.T) {
	file := buildFile(t, "AV01", nil)
	var rec [12]byte
	binary.LittleEndian.PutUint32(rec[0:4], 16)
	binary.LittleEndian.PutUint64(rec[4:12], 0)
	prefix := append(file, rec[:]...)
	want := errors.New("payload IO blew up")
	r := &payloadFailingReader{prefix: prefix, err: want}
	d, err := NewDemuxer(r, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.ReadFrame(); err == nil || !errors.Is(err, want) {
		t.Fatalf("err = %v, want wrap of %v", err, want)
	}
}
