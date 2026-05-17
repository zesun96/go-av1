package av1

import "errors"

// Sentinel errors mirror the errno values that dav1d returns.
//
// Wrap them with fmt.Errorf("...: %w", ErrXxx) and compare with errors.Is.
var (
	// ErrAgain is returned when the decoder needs more input data, or when
	// the encoder cannot accept more input until previous output is drained.
	// It corresponds to dav1d's EAGAIN return.
	ErrAgain = errors.New("av1: try again")

	// ErrInvalidBitstream is returned for any bitstream that violates the
	// AV1 specification. It corresponds to dav1d's EINVAL return for parsing
	// errors.
	ErrInvalidBitstream = errors.New("av1: invalid bitstream")

	// ErrUnsupported is returned for syntactically valid bitstreams that use
	// tools go-av1 has not yet implemented. It corresponds to ENOTSUP.
	ErrUnsupported = errors.New("av1: unsupported feature")

	// ErrClosed is returned by methods on a Decoder or Encoder that has
	// already been closed.
	ErrClosed = errors.New("av1: codec closed")

	// ErrNotImplemented is returned by every M0 stub. It will disappear once
	// the corresponding milestone lands the implementation.
	ErrNotImplemented = errors.New("av1: not implemented")
)
