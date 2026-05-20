package encoder

import (
	"errors"

	"github.com/zesun96/go-av1/internal/encoder/core"
)

// RawPicture is the encoder-internal picture representation.
// pkg/av1 wraps this to avoid import cycles.
type RawPicture struct {
	Y      []byte
	U      []byte
	V      []byte
	Width  int
	Height int
}

// Packet is one encoded output unit.
type Packet struct {
	Data     []byte
	PTS      int64
	Keyframe bool
}

// Options configures the encoder (internal mirror of av1.EncoderOptions).
type Options struct {
	Width        int
	Height       int
	FrameRateNum int
	FrameRateDen int
	BitDepth     int
	CRF          int
}

// ErrAgain is returned when no packet is available.
var ErrAgain = errors.New("encoder: try again")

// Impl is the concrete encoder implementation.
type Impl struct {
	opts     Options
	fe       *core.FrameEncoder
	frameNum int
	flushing bool
	packets  []*Packet
}

// NewImpl creates a new encoder implementation.
func NewImpl(opts Options) (*Impl, error) {
	if opts.Width <= 0 || opts.Height <= 0 {
		return nil, errors.New("encoder: invalid dimensions")
	}
	if opts.BitDepth == 0 {
		opts.BitDepth = 8
	}
	if opts.BitDepth != 8 {
		return nil, errors.New("encoder: only 8-bit supported in M10")
	}
	if opts.FrameRateNum == 0 {
		opts.FrameRateNum = 30
	}
	if opts.FrameRateDen == 0 {
		opts.FrameRateDen = 1
	}

	// Map CRF to qindex: CRF 0 -> qindex 0 (lossless-ish), CRF 63 -> qindex 255
	qindex := opts.CRF * 4
	if qindex > 255 {
		qindex = 255
	}
	if qindex < 1 {
		qindex = 1
	}

	fe := &core.FrameEncoder{
		Width:    opts.Width,
		Height:   opts.Height,
		QIndex:   qindex,
		BitDepth: 0, // 8-bit -> hbd index 0
	}

	return &Impl{
		opts: opts,
		fe:   fe,
	}, nil
}

// SendPicture queues a raw picture for encoding.
func (e *Impl) SendPicture(p *RawPicture) error {
	if e.flushing {
		return errors.New("encoder: cannot send after flush")
	}
	if p == nil {
		return errors.New("encoder: nil picture")
	}

	// Encode the frame
	data := e.fe.EncodeFrame(p.Y, p.U, p.V, e.frameNum)

	pkt := &Packet{
		Data:     data,
		PTS:      int64(e.frameNum),
		Keyframe: true, // M10: all key frames
	}
	e.packets = append(e.packets, pkt)
	e.frameNum++
	return nil
}

// ReceivePacket returns the next encoded packet.
func (e *Impl) ReceivePacket() (*Packet, error) {
	if len(e.packets) == 0 {
		return nil, ErrAgain
	}
	pkt := e.packets[0]
	e.packets = e.packets[1:]
	return pkt, nil
}

// Flush signals end of input.
func (e *Impl) Flush() error {
	e.flushing = true
	return nil
}

// Close releases resources.
func (e *Impl) Close() error {
	e.packets = nil
	return nil
}
