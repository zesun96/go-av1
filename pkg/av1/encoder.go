package av1

// EncoderOptions configures an Encoder. Field semantics will solidify during
// the M10+ encoder phase; today this struct exists so external callers can
// already write code against the type.
type EncoderOptions struct {
	// Width and Height are the input luma resolution.
	Width  int
	Height int

	// FrameRate expressed as numerator / denominator.
	FrameRateNum int
	FrameRateDen int

	// BitDepth is 8 in the first encoder release.
	BitDepth int

	// Chroma subsampling. Only Chroma420 is supported initially.
	Chroma ChromaFormat

	// Threads is the worker pool size. Zero means runtime.NumCPU().
	Threads int

	// Preset selects the speed / quality trade-off. The interpretation will
	// loosely follow SVT-AV1 presets (0 = slowest / best, 13 = fastest).
	Preset int

	// TargetBitrateKbps is the desired average bitrate. Zero implies CRF
	// rate control using CRF.
	TargetBitrateKbps int

	// CRF is the constant-rate-factor when TargetBitrateKbps is zero.
	CRF int
}

// EncodedPacket is one OBU temporal unit produced by Encoder.
type EncodedPacket struct {
	// Data is the encoded bytes; the buffer is owned by the encoder until
	// the next call to ReceivePacket.
	Data []byte

	// PTS is the presentation timestamp echoed from the input picture.
	PTS int64

	// Keyframe is true if Data starts with a key frame.
	Keyframe bool
}

// Encoder consumes raw pictures and produces AV1 bitstream packets.
//
// The state machine mirrors the decoder: feed pictures with SendPicture,
// drain packets with ReceivePacket, finish with Flush.
type Encoder interface {
	// SendPicture queues a raw picture for encoding.
	SendPicture(p *Picture) error

	// ReceivePacket returns the next encoded packet, or ErrAgain when more
	// input is needed.
	ReceivePacket() (*EncodedPacket, error)

	// Flush signals the end of input and lets the encoder drain buffered
	// frames.
	Flush() error

	// Close releases resources.
	Close() error
}

// NewEncoder constructs an Encoder. The encoder pipeline does not exist yet,
// so this always returns ErrNotImplemented at M0.
func NewEncoder(opts EncoderOptions) (Encoder, error) {
	_ = opts
	return nil, ErrNotImplemented
}
