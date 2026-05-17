package av1

import "io"

// PictureFunc is invoked once per decoded picture by DecodeReader.
//
// Returning false stops iteration. The picture must be Released by the
// callee, either before returning or asynchronously.
type PictureFunc func(p *Picture, err error) bool

// DecodeReader is a convenience helper that decodes every picture in r and
// invokes fn for each one.
//
// The Go 1.23 range-over-func iterator form will be added once the project's
// minimum Go version is bumped; the callback shape is wire-compatible with a
// future iter.Seq2[*Picture, error] adapter.
//
// At M0 the helper invokes fn exactly once with (nil, ErrNotImplemented) and
// returns that error.
func DecodeReader(r io.Reader, fn PictureFunc) error {
	_ = r
	if fn != nil {
		fn(nil, ErrNotImplemented)
	}
	return ErrNotImplemented
}
