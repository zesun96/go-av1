package av1

import (
	"errors"
	"io"
)

// PictureFunc is invoked once per decoded picture by DecodeReader.
//
// Returning false stops iteration. The picture must be Released by the
// callee, either before returning or asynchronously.
type PictureFunc func(p *Picture, err error) bool

// DecodeReader decodes all pictures in r and invokes fn for each one.
// It uses a bulk ReadAll approach for M6; M8 will switch to streaming.
func DecodeReader(r io.Reader, fn PictureFunc) error {
	dec, err := NewDecoder(DecoderOptions{})
	if err != nil {
		return err
	}
	defer dec.Close()

	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if err := dec.SendData(data); err != nil {
		return err
	}
	for {
		p, gerr := dec.GetPicture()
		if errors.Is(gerr, ErrAgain) {
			break
		}
		if fn != nil && !fn(p, gerr) {
			break
		}
		if p != nil {
			p.Release()
		}
		if gerr != nil {
			return gerr
		}
	}
	return dec.Flush()
}
