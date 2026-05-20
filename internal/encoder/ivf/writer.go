// Package ivf implements a minimal IVF (Indeo Video Format) container writer.
//
// IVF is the standard container for raw AV1 bitstream testing.
// File format: 32-byte header + frame records (12-byte header + payload).
package ivf

import (
	"encoding/binary"
	"io"
)

// Writer writes IVF frames to an io.Writer.
type Writer struct {
	w          io.Writer
	width      uint16
	height     uint16
	timeNum    uint32
	timeDen    uint32
	frameCount uint32
	headerDone bool
}

// NewWriter creates an IVF writer.
// The file header is written lazily on the first frame write.
func NewWriter(w io.Writer, width, height int, timebaseNum, timebaseDen int) *Writer {
	return &Writer{
		w:       w,
		width:   uint16(width),
		height:  uint16(height),
		timeNum: uint32(timebaseNum),
		timeDen: uint32(timebaseDen),
	}
}

// writeHeader writes the 32-byte IVF file header.
func (w *Writer) writeHeader() error {
	var hdr [32]byte
	copy(hdr[0:4], "DKIF")                               // signature
	binary.LittleEndian.PutUint16(hdr[4:6], 0)           // version
	binary.LittleEndian.PutUint16(hdr[6:8], 32)          // header length
	copy(hdr[8:12], "AV01")                              // FourCC
	binary.LittleEndian.PutUint16(hdr[12:14], w.width)   // width
	binary.LittleEndian.PutUint16(hdr[14:16], w.height)  // height
	binary.LittleEndian.PutUint32(hdr[16:20], w.timeDen) // timebase denominator (fps num)
	binary.LittleEndian.PutUint32(hdr[20:24], w.timeNum) // timebase numerator (fps den)
	binary.LittleEndian.PutUint32(hdr[24:28], 0)         // frame count (placeholder)
	binary.LittleEndian.PutUint32(hdr[28:32], 0)         // unused

	_, err := w.w.Write(hdr[:])
	w.headerDone = true
	return err
}

// WriteFrame writes one IVF frame record (12-byte header + payload).
func (w *Writer) WriteFrame(data []byte, pts uint64) error {
	if !w.headerDone {
		if err := w.writeHeader(); err != nil {
			return err
		}
	}

	var fhdr [12]byte
	binary.LittleEndian.PutUint32(fhdr[0:4], uint32(len(data)))
	binary.LittleEndian.PutUint64(fhdr[4:12], pts)

	if _, err := w.w.Write(fhdr[:]); err != nil {
		return err
	}
	if _, err := w.w.Write(data); err != nil {
		return err
	}
	w.frameCount++
	return nil
}

// FrameCount returns the number of frames written.
func (w *Writer) FrameCount() uint32 {
	return w.frameCount
}
