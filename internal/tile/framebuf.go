// framebuf.go defines the FrameBuf type, a lightweight frame buffer descriptor
// used internally by the tile package to avoid an import cycle with pkg/av1.
package tile

// FrameBuf holds the sample planes for one decoded frame.
// It mirrors the fields of pkg/av1.Picture that are needed for tile decoding.
type FrameBuf struct {
	// Luma plane.
	Y       []byte
	StrideY int
	Width   int
	Height  int

	// Chroma planes (4:2:0 layout).
	U, V     []byte
	StrideUV int
	ChromaW  int // (Width+1)/2
	ChromaH  int // (Height+1)/2

	// Monochrome: if true, U/V are nil.
	Monochrome bool
}
