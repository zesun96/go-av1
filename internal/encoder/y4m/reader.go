// Package y4m implements a minimal Y4M (YUV4MPEG2) file reader for the encoder.
package y4m

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Header contains parsed Y4M stream parameters.
type Header struct {
	Width      int
	Height     int
	FrameRate  [2]int // numerator, denominator
	Interlaced bool
	ChromaSS   string // "420", "422", "444", "mono"
	BitDepth   int    // 8, 10, or 12
}

// Reader reads Y4M frames sequentially.
type Reader struct {
	r       *bufio.Reader
	Header  Header
	frameN  int
	frameSz int // bytes per frame (Y + Cb + Cr)
}

// NewReader creates a Y4M reader from the given io.Reader.
// It parses the file header and prepares for frame reading.
func NewReader(r io.Reader) (*Reader, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	line, err := br.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("y4m: failed to read header: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "YUV4MPEG2") {
		return nil, errors.New("y4m: missing YUV4MPEG2 signature")
	}

	hdr := Header{
		FrameRate: [2]int{30, 1},
		ChromaSS:  "420",
		BitDepth:  8,
	}

	parts := strings.Fields(line)
	for _, p := range parts[1:] {
		if len(p) < 2 {
			continue
		}
		switch p[0] {
		case 'W':
			hdr.Width, _ = strconv.Atoi(p[1:])
		case 'H':
			hdr.Height, _ = strconv.Atoi(p[1:])
		case 'F':
			if idx := strings.IndexByte(p[1:], ':'); idx >= 0 {
				hdr.FrameRate[0], _ = strconv.Atoi(p[1 : 1+idx])
				hdr.FrameRate[1], _ = strconv.Atoi(p[2+idx:])
			}
		case 'I':
			hdr.Interlaced = p[1] != 'p'
		case 'C':
			cs := p[1:]
			switch {
			case strings.HasPrefix(cs, "420p10"):
				hdr.ChromaSS = "420"
				hdr.BitDepth = 10
			case strings.HasPrefix(cs, "420p12"):
				hdr.ChromaSS = "420"
				hdr.BitDepth = 12
			case strings.HasPrefix(cs, "420"):
				hdr.ChromaSS = "420"
			case strings.HasPrefix(cs, "422"):
				hdr.ChromaSS = "422"
			case strings.HasPrefix(cs, "444"):
				hdr.ChromaSS = "444"
			case strings.HasPrefix(cs, "mono"):
				hdr.ChromaSS = "mono"
			}
		}
	}

	if hdr.Width <= 0 || hdr.Height <= 0 {
		return nil, fmt.Errorf("y4m: invalid dimensions %dx%d", hdr.Width, hdr.Height)
	}

	// Calculate frame size
	bpp := 1
	if hdr.BitDepth > 8 {
		bpp = 2
	}
	lumaSize := hdr.Width * hdr.Height * bpp
	var chromaSize int
	switch hdr.ChromaSS {
	case "420":
		chromaSize = lumaSize / 2 // two chroma planes, each 1/4 of luma
	case "422":
		chromaSize = lumaSize // two chroma planes, each 1/2 of luma
	case "444":
		chromaSize = lumaSize * 2 // two chroma planes, each full
	case "mono":
		chromaSize = 0
	default:
		chromaSize = lumaSize / 2
	}

	rd := &Reader{
		r:       br,
		Header:  hdr,
		frameSz: lumaSize + chromaSize,
	}
	return rd, nil
}

// Frame holds one Y4M frame's raw pixel data.
type Frame struct {
	Y  []byte // luma plane
	Cb []byte // chroma-blue plane (nil for mono)
	Cr []byte // chroma-red plane (nil for mono)
}

// ReadFrame reads the next frame. Returns io.EOF when no more frames.
func (r *Reader) ReadFrame() (*Frame, error) {
	// Read FRAME header line
	line, err := r.r.ReadString('\n')
	if err != nil {
		if err == io.EOF && len(line) == 0 {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("y4m: frame %d header: %w", r.frameN, err)
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "FRAME") {
		return nil, fmt.Errorf("y4m: frame %d: expected FRAME, got %q", r.frameN, line)
	}

	// Read frame data
	data := make([]byte, r.frameSz)
	if _, err := io.ReadFull(r.r, data); err != nil {
		return nil, fmt.Errorf("y4m: frame %d data: %w", r.frameN, err)
	}

	bpp := 1
	if r.Header.BitDepth > 8 {
		bpp = 2
	}
	lumaSize := r.Header.Width * r.Header.Height * bpp

	f := &Frame{
		Y: data[:lumaSize],
	}
	if r.Header.ChromaSS != "mono" {
		chromaPlaneSize := (r.frameSz - lumaSize) / 2
		f.Cb = data[lumaSize : lumaSize+chromaPlaneSize]
		f.Cr = data[lumaSize+chromaPlaneSize:]
	}

	r.frameN++
	return f, nil
}

// FrameNumber returns the number of frames read so far.
func (r *Reader) FrameNumber() int {
	return r.frameN
}
