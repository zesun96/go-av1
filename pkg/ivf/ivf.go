// Package ivf implements a streaming demuxer for the IVF container that
// dav1d, libaom, and the AOMedia test suite use to ship raw AV1 bitstreams.
//
// The container is described informally in dav1d/tools/input/ivf.c. It is
// little-endian throughout:
//
//	0..3  : "DKIF" magic
//	4..5  : version (must be 0)
//	6..7  : header_length (must be 32)
//	8..11 : codec FourCC ("AV01" for AV1)
//	12..13: width
//	14..15: height
//	16..19: timebase numerator (frame rate numerator)
//	20..23: timebase denominator (frame rate denominator)
//	24..27: total frame count (informational)
//	28..31: reserved
//
// Each frame is preceded by a 12-byte record:
//
//	0..3 : frame_size (uint32 LE)
//	4..11: presentation timestamp (uint64 LE)
package ivf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// FileHeader captures the global IVF container header.
type FileHeader struct {
	// Version is the container version field (always 0 in practice).
	Version uint16
	// HeaderLength is the size of the container header (always 32).
	HeaderLength uint16
	// FourCC is the codec four-character code, e.g. "AV01".
	FourCC [4]byte
	// Width is the encoded video width in pixels.
	Width uint16
	// Height is the encoded video height in pixels.
	Height uint16
	// TimebaseNum and TimebaseDen describe the timestamp time-base.
	TimebaseNum uint32
	TimebaseDen uint32
	// FrameCount is the (informational) number of frames in the file.
	FrameCount uint32
}

// FrameHeader is the per-frame record preceding each payload.
type FrameHeader struct {
	// Size is the payload size in bytes.
	Size uint32
	// PTS is the frame presentation timestamp expressed in
	// FileHeader.TimebaseNum / FileHeader.TimebaseDen units.
	PTS uint64
}

// Errors returned by the demuxer.
var (
	// ErrBadMagic is returned when the file does not begin with "DKIF".
	ErrBadMagic = errors.New("ivf: missing DKIF magic")
	// ErrUnsupportedCodec is returned when the FourCC is not "AV01".
	ErrUnsupportedCodec = errors.New("ivf: unsupported codec FourCC")
	// ErrShortHeader is returned when the file ends before the 32-byte
	// header is fully readable.
	ErrShortHeader = errors.New("ivf: short container header")
	// ErrShortFrame is returned when a frame announcement points past EOF.
	ErrShortFrame = errors.New("ivf: short frame record")
)

// Demuxer reads frames from an IVF source one at a time. Construct it with
// NewDemuxer; the global header is read up front.
type Demuxer struct {
	r      io.Reader
	header FileHeader
}

// NewDemuxer reads and validates the 32-byte IVF container header from r and
// returns a Demuxer ready to yield frames.
//
// EnforceAV1 controls whether the demuxer rejects non-AV1 streams. Most
// callers want EnforceAV1=true so that mismatches surface immediately.
func NewDemuxer(r io.Reader, enforceAV1 bool) (*Demuxer, error) {
	var hdr [32]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrShortHeader
		}
		return nil, fmt.Errorf("ivf: read header: %w", err)
	}
	if string(hdr[0:4]) != "DKIF" {
		return nil, ErrBadMagic
	}
	d := &Demuxer{r: r}
	d.header.Version = binary.LittleEndian.Uint16(hdr[4:6])
	d.header.HeaderLength = binary.LittleEndian.Uint16(hdr[6:8])
	copy(d.header.FourCC[:], hdr[8:12])
	d.header.Width = binary.LittleEndian.Uint16(hdr[12:14])
	d.header.Height = binary.LittleEndian.Uint16(hdr[14:16])
	d.header.TimebaseNum = binary.LittleEndian.Uint32(hdr[16:20])
	d.header.TimebaseDen = binary.LittleEndian.Uint32(hdr[20:24])
	d.header.FrameCount = binary.LittleEndian.Uint32(hdr[24:28])
	if enforceAV1 && string(d.header.FourCC[:]) != "AV01" {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedCodec, d.header.FourCC[:])
	}
	return d, nil
}

// Header returns the parsed container header.
func (d *Demuxer) Header() FileHeader { return d.header }

// ReadFrame reads the next frame's record + payload. It returns io.EOF on a
// clean end of stream and ErrShortFrame for a truncated record.
//
// The returned slice is freshly allocated and owned by the caller.
func (d *Demuxer) ReadFrame() (FrameHeader, []byte, error) {
	var rec [12]byte
	n, err := io.ReadFull(d.r, rec[:])
	switch {
	case err == nil:
	case errors.Is(err, io.EOF) && n == 0:
		return FrameHeader{}, nil, io.EOF
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		return FrameHeader{}, nil, ErrShortFrame
	default:
		return FrameHeader{}, nil, fmt.Errorf("ivf: read frame record: %w", err)
	}
	fh := FrameHeader{
		Size: binary.LittleEndian.Uint32(rec[0:4]),
		PTS:  binary.LittleEndian.Uint64(rec[4:12]),
	}
	payload := make([]byte, fh.Size)
	if _, err := io.ReadFull(d.r, payload); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return FrameHeader{}, nil, ErrShortFrame
		}
		return FrameHeader{}, nil, fmt.Errorf("ivf: read frame payload: %w", err)
	}
	return fh, payload, nil
}
