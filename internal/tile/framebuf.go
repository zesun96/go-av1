// framebuf.go defines the FrameBuf type, a lightweight frame buffer descriptor
// used internally by the tile package to avoid an import cycle with pkg/av1.
package tile

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

	// Chroma planes.
	U, V     []byte
	StrideUV int
	ChromaW  int
	ChromaH  int

	// Monochrome: if true, U/V are nil.
	Monochrome bool

	// Refs holds decoded reference frames, indexed by AV1 reference-frame slot.
	// Nil entries indicate unavailable references.
	Refs [8]*PlaneBuf
}
