// Package conformance contains deterministic decoder conformance helpers.
package conformance

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"hash"

	"github.com/zesun96/go-av1/pkg/av1"
)

// FrameDigest describes the native visible samples of one output picture.
// Padding bytes at the end of rows and outside the visible dimensions are not
// included, so changing frame dimensions can be compared without resampling.
type FrameDigest struct {
	Index    int    `json:"index"`
	PTS      int64  `json:"pts"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	BitDepth int    `json:"bit_depth"`
	Chroma   string `json:"chroma"`
	YMD5     string `json:"y_md5"`
	UMD5     string `json:"u_md5,omitempty"`
	VMD5     string `json:"v_md5,omitempty"`
	FrameMD5 string `json:"frame_md5"`
}

// DigestPicture hashes the visible rows of pic in native Y, U, V order.
func DigestPicture(pic *av1.Picture, index int) (FrameDigest, error) {
	if pic == nil {
		return FrameDigest{}, fmt.Errorf("conformance: nil picture")
	}
	if pic.Width <= 0 || pic.Height <= 0 || pic.StrideY < pic.Width {
		return FrameDigest{}, fmt.Errorf("conformance: invalid luma geometry %dx%d stride %d", pic.Width, pic.Height, pic.StrideY)
	}

	frameHash := md5.New()
	y, err := hashPlane(pic.Y, pic.StrideY, pic.Width, pic.Height, frameHash)
	if err != nil {
		return FrameDigest{}, fmt.Errorf("conformance: luma: %w", err)
	}

	digest := FrameDigest{
		Index: index, PTS: pic.PTS, Width: pic.Width, Height: pic.Height,
		BitDepth: pic.BitDepth, Chroma: pic.Chroma.String(), YMD5: y,
	}
	if pic.Chroma != av1.ChromaMonochrome {
		cw, ch := pic.ChromaWidth(), pic.ChromaHeight()
		if cw <= 0 || ch <= 0 || pic.StrideUV < cw {
			return FrameDigest{}, fmt.Errorf("conformance: invalid chroma geometry %dx%d stride %d", cw, ch, pic.StrideUV)
		}
		digest.UMD5, err = hashPlane(pic.U, pic.StrideUV, cw, ch, frameHash)
		if err != nil {
			return FrameDigest{}, fmt.Errorf("conformance: chroma U: %w", err)
		}
		digest.VMD5, err = hashPlane(pic.V, pic.StrideUV, cw, ch, frameHash)
		if err != nil {
			return FrameDigest{}, fmt.Errorf("conformance: chroma V: %w", err)
		}
	}
	digest.FrameMD5 = hex.EncodeToString(frameHash.Sum(nil))
	return digest, nil
}

func hashPlane(data []byte, stride, width, height int, frameHash hash.Hash) (string, error) {
	needed := (height-1)*stride + width
	if stride < 0 || width < 0 || height <= 0 || needed < 0 || len(data) < needed {
		return "", fmt.Errorf("buffer has %d bytes, need %d", len(data), needed)
	}
	planeHash := md5.New()
	for row := 0; row < height; row++ {
		visible := data[row*stride : row*stride+width]
		_, _ = planeHash.Write(visible)
		_, _ = frameHash.Write(visible)
	}
	return hex.EncodeToString(planeHash.Sum(nil)), nil
}
