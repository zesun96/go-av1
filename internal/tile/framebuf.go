// framebuf.go defines the FrameBuf type, a lightweight frame buffer descriptor
// used internally by the tile package to avoid an import cycle with pkg/av1.
package tile

import "github.com/zesun96/go-av1/internal/refmvs"

// PlaneBuf describes one decoded reference frame in the same layout as
// FrameBuf. It intentionally has no ownership semantics; the caller keeps the
// referenced picture alive while tile decoding runs.
type PlaneBuf struct {
	Y       []byte
	StrideY int
	Width   int
	Height  int

	U, V     []byte
	StrideUV int
	ChromaW  int
	ChromaH  int

	Monochrome bool
}

// FrameBuf holds the sample planes for one decoded frame.
// It mirrors the fields of pkg/av1.Picture that are needed for tile decoding.
type FrameBuf struct {
	// Luma plane.
	Y       []byte
	StrideY int
	Width   int
	Height  int
	// CodedWidth/CodedHeight include the 8x8-aligned reconstruction padding.
	// Width/Height remain the visible frame dimensions.
	CodedWidth  int
	CodedHeight int

	// Chroma planes.
	U, V         []byte
	StrideUV     int
	ChromaW      int
	ChromaH      int
	CodedChromaW int
	CodedChromaH int

	// Monochrome: if true, U/V are nil.
	Monochrome bool

	// Refs holds decoded reference frames, indexed by AV1 reference-frame slot.
	// Nil entries indicate unavailable references.
	Refs    [8]*PlaneBuf
	MVFrame *refmvs.Frame
	RefMVs  [8]*refmvs.Frame

	// FilterState retains full-frame block metadata assembled from the
	// independently decoded tile states. Post-filters consume this after tile
	// entropy and neighbour contexts have gone out of scope.
	FilterState *FrameState
}

func (fb *FrameBuf) codedLumaSize() (int, int) {
	w, h := fb.CodedWidth, fb.CodedHeight
	if w <= 0 {
		w = fb.Width
	}
	if h <= 0 {
		h = fb.Height
	}
	return w, h
}

func (fb *FrameBuf) codedChromaSize() (int, int) {
	w, h := fb.CodedChromaW, fb.CodedChromaH
	if w <= 0 {
		w = fb.ChromaW
	}
	if h <= 0 {
		h = fb.ChromaH
	}
	return w, h
}

func (fb *FrameBuf) codedPlaneSize(plane int) (int, int) {
	if plane == 0 {
		return fb.codedLumaSize()
	}
	return fb.codedChromaSize()
}
