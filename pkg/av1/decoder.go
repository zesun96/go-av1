package av1

// InloopFilter is a bitmask selecting which post-filters the decoder applies.
//
// Mirrors dav1d's Dav1dInloopFilterType. Disabling filters speeds up decoding
// at the cost of subjective quality and bit-exact compliance.
type InloopFilter uint8

// Inloop filter bits.
const (
	InloopFilterDeblock     InloopFilter = 1 << 0
	InloopFilterCDEF        InloopFilter = 1 << 1
	InloopFilterRestoration InloopFilter = 1 << 2

	// InloopFilterAll enables every post-filter. Required for bit-exact
	// decoding.
	InloopFilterAll = InloopFilterDeblock | InloopFilterCDEF | InloopFilterRestoration
)

// DecodeFrameType selects which frames the decoder emits. Mirrors dav1d's
// Dav1dDecodeFrameType.
type DecodeFrameType uint8

// Frame selection modes.
const (
	// DecodeFrameAll emits every output frame (default).
	DecodeFrameAll DecodeFrameType = iota
	// DecodeFrameReference emits only frames that are referenced by others.
	DecodeFrameReference
	// DecodeFrameIntra emits intra-only frames including keyframes.
	DecodeFrameIntra
	// DecodeFrameKey emits only keyframes.
	DecodeFrameKey
)

// DecoderOptions configures a Decoder.
//
// The zero value is valid and produces a single-threaded decoder with film
// grain enabled and every in-loop filter active.
type DecoderOptions struct {
	// Threads sets the worker pool size. Zero means runtime.NumCPU().
	Threads int

	// MaxFrameDelay caps how many frames may be decoded ahead of the
	// consumer. Zero defaults to ceil(sqrt(Threads)) like dav1d.
	MaxFrameDelay int

	// AllowFilmGrain enables film grain synthesis. Disable to skip the final
	// post-processing stage; the bitstream remains conformant.
	AllowFilmGrain bool

	// InloopFilters is the bitmask of in-loop filters to apply. Zero defaults
	// to InloopFilterAll unless InloopFiltersSet is true.
	InloopFilters InloopFilter

	// InloopFiltersSet reports whether InloopFilters was explicitly chosen by
	// the caller. This allows zero to mean "disable all filters" instead of
	// always collapsing to the default.
	InloopFiltersSet bool

	// FrameSelection chooses which frames to output. Zero defaults to
	// DecodeFrameAll.
	FrameSelection DecodeFrameType

	// Logger receives diagnostics. Nil disables logging.
	Logger Logger
}

// Logger receives free-form diagnostic messages from the codec.
type Logger interface {
	Logf(format string, args ...any)
}

// Decoder consumes AV1 bitstream packets and emits decoded pictures.
//
// The state machine matches dav1d:
//
//	for { _ = dec.SendData(pkt); for { p, err := dec.GetPicture(); ... } }
//
// SendData returns ErrAgain when its input queue is full. GetPicture returns
// ErrAgain when no completed frame is available yet.
type Decoder interface {
	// SendData feeds a chunk of bitstream into the decoder. The packet may
	// contain one or more OBUs and need not be aligned to a temporal unit
	// boundary, mirroring Dav1dContext.send_data behaviour.
	SendData(packet []byte) error

	// GetPicture returns the next decoded picture. The returned Picture is
	// reference-counted; call Release when done.
	GetPicture() (*Picture, error)

	// Flush forces the decoder to emit every buffered picture and discards
	// internal references.
	Flush() error

	// Close releases all resources.
	Close() error
}

// NewDecoder constructs a Decoder backed by the M6 pipeline.
func NewDecoder(opts DecoderOptions) (Decoder, error) {
	if !opts.InloopFiltersSet && opts.InloopFilters == 0 {
		opts.InloopFilters = InloopFilterAll
	}
	return newDecoderImpl(opts)
}
