package av1

import "sync/atomic"

// ChromaFormat describes the subsampling of a decoded picture.
type ChromaFormat uint8

// Supported chroma layouts.
const (
	// Chroma420 is 4:2:0 subsampling. The first decoder milestone targets
	// this layout exclusively.
	Chroma420 ChromaFormat = iota
	// Chroma422 is 4:2:2 subsampling. Reserved for post-M8 work.
	Chroma422
	// Chroma444 is 4:4:4 subsampling. Reserved for post-M8 work.
	Chroma444
	// ChromaMonochrome indicates a luma-only picture.
	ChromaMonochrome
)

// String returns a short label such as "4:2:0".
func (c ChromaFormat) String() string {
	switch c {
	case Chroma420:
		return "4:2:0"
	case Chroma422:
		return "4:2:2"
	case Chroma444:
		return "4:4:4"
	case ChromaMonochrome:
		return "mono"
	default:
		return "unknown"
	}
}

// Picture is a decoded AV1 frame.
//
// Picture instances are reference-counted and owned by an internal pool. The
// caller obtains an additional reference with Retain and must call Release
// exactly once for every NewDecoder.GetPicture or Retain it has performed.
//
// The plane slices alias the underlying pool buffers; do not retain them past
// the picture's lifetime.
type Picture struct {
	// Y, U, V are the per-plane sample buffers. Length equals
	// StrideY*Height for Y and StrideUV*ChromaHeight() for U/V.
	Y, U, V []byte

	// StrideY and StrideUV are the byte distances between consecutive rows.
	// They may exceed Width / chroma-width for SIMD alignment.
	StrideY  int
	StrideUV int

	// Width and Height are in luma samples.
	Width  int
	Height int

	// BitDepth is 8 today; 10 / 12 will be unlocked after M8.
	BitDepth int

	// Chroma describes the subsampling.
	Chroma ChromaFormat

	// PTS is the presentation timestamp carried by the OBU temporal unit, in
	// the timebase declared by the surrounding container.
	PTS int64

	// refs counts outstanding references. Picture is invalid once it reaches
	// zero again.
	refs atomic.Int32

	// release is invoked when the reference count drops to zero. The decoder
	// installs a callback that returns the buffer to the pool.
	release func(*Picture)
}

// ChromaWidth returns the chroma plane width in samples.
func (p *Picture) ChromaWidth() int {
	switch p.Chroma {
	case Chroma420, Chroma422:
		return (p.Width + 1) >> 1
	case Chroma444:
		return p.Width
	case ChromaMonochrome:
		return 0
	default:
		return 0
	}
}

// ChromaHeight returns the chroma plane height in samples.
func (p *Picture) ChromaHeight() int {
	switch p.Chroma {
	case Chroma420:
		return (p.Height + 1) >> 1
	case Chroma422, Chroma444:
		return p.Height
	case ChromaMonochrome:
		return 0
	default:
		return 0
	}
}

// Retain increments the reference count and returns the same Picture so it
// can be chained.
func (p *Picture) Retain() *Picture {
	if p == nil {
		return nil
	}
	p.refs.Add(1)
	return p
}

// Release decrements the reference count. When the count drops to zero, the
// installed release callback is invoked. Release on a nil Picture is a no-op.
func (p *Picture) Release() {
	if p == nil {
		return
	}
	if p.refs.Add(-1) == 0 && p.release != nil {
		p.release(p)
	}
}
