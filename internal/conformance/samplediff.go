package conformance

import (
	"fmt"

	"github.com/zesun96/go-av1/pkg/av1"
)

// NativeFrame owns tightly packed visible planes in Y, U, V order.
type NativeFrame struct {
	Width, Height int
	BitDepth      int
	Chroma        string
	Y, U, V       []byte
}

// SampleDifference locates the first differing native sample.
type SampleDifference struct {
	Plane      string `json:"plane"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
	GoValue    uint16 `json:"go_value"`
	Dav1dValue uint16 `json:"dav1d_value"`
}

// CopyPicture copies visible samples out of a reference-counted picture.
func CopyPicture(pic *av1.Picture) (NativeFrame, error) {
	if _, err := DigestPicture(pic, 0); err != nil {
		return NativeFrame{}, err
	}
	frame := NativeFrame{Width: pic.Width, Height: pic.Height, BitDepth: pic.BitDepth, Chroma: pic.Chroma.String()}
	frame.Y = copyPlane(pic.Y, pic.StrideY, pic.Width, pic.Height)
	if pic.Chroma != av1.ChromaMonochrome {
		cw, ch := pic.ChromaWidth(), pic.ChromaHeight()
		frame.U = copyPlane(pic.U, pic.StrideUV, cw, ch)
		frame.V = copyPlane(pic.V, pic.StrideUV, cw, ch)
	}
	return frame, nil
}

func copyPlane(data []byte, stride, width, height int) []byte {
	out := make([]byte, width*height)
	for row := 0; row < height; row++ {
		copy(out[row*width:(row+1)*width], data[row*stride:row*stride+width])
	}
	return out
}

// FirstDifferentSample compares dav1d's tightly packed YUV output with frame.
func FirstDifferentSample(frame NativeFrame, dav1dRaw []byte) (*SampleDifference, error) {
	if frame.BitDepth != 8 {
		return nil, fmt.Errorf("conformance: sample diagnostics support 8-bit output, got %d-bit", frame.BitDepth)
	}
	planes := []struct {
		name          string
		width, height int
		goData        []byte
	}{
		{name: "Y", width: frame.Width, height: frame.Height, goData: frame.Y},
	}
	cw, ch, err := chromaDimensions(frame.Width, frame.Height, frame.Chroma)
	if err != nil {
		return nil, err
	}
	if frame.Chroma != "mono" {
		planes = append(planes,
			struct {
				name          string
				width, height int
				goData        []byte
			}{name: "U", width: cw, height: ch, goData: frame.U},
			struct {
				name          string
				width, height int
				goData        []byte
			}{name: "V", width: cw, height: ch, goData: frame.V},
		)
	}
	offset := 0
	for _, plane := range planes {
		size := plane.width * plane.height
		if len(plane.goData) != size || len(dav1dRaw) < offset+size {
			return nil, fmt.Errorf("conformance: invalid %s plane size", plane.name)
		}
		for i, value := range plane.goData {
			if value != dav1dRaw[offset+i] {
				return &SampleDifference{Plane: plane.name, X: i % plane.width, Y: i / plane.width, GoValue: uint16(value), Dav1dValue: uint16(dav1dRaw[offset+i])}, nil
			}
		}
		offset += size
	}
	if len(dav1dRaw) != offset {
		return nil, fmt.Errorf("conformance: dav1d frame has %d bytes, expected %d", len(dav1dRaw), offset)
	}
	return nil, nil
}

func frameByteSize(frame FrameDigest) (int, error) {
	if frame.BitDepth != 8 {
		return 0, fmt.Errorf("conformance: raw dav1d diagnostics support 8-bit output, got %d-bit", frame.BitDepth)
	}
	cw, ch, err := chromaDimensions(frame.Width, frame.Height, frame.Chroma)
	if err != nil {
		return 0, err
	}
	size := frame.Width * frame.Height
	if frame.Chroma != "mono" {
		size += 2 * cw * ch
	}
	return size, nil
}

func chromaDimensions(width, height int, chroma string) (int, int, error) {
	switch chroma {
	case "4:2:0":
		return (width + 1) >> 1, (height + 1) >> 1, nil
	case "4:2:2":
		return (width + 1) >> 1, height, nil
	case "4:4:4":
		return width, height, nil
	case "mono":
		return 0, 0, nil
	default:
		return 0, 0, fmt.Errorf("conformance: unsupported chroma format %q", chroma)
	}
}
