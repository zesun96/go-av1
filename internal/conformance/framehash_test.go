package conformance

import (
	"crypto/md5"
	"encoding/hex"
	"testing"

	"github.com/zesun96/go-av1/pkg/av1"
)

func TestDigestPictureVisible420(t *testing.T) {
	pic := &av1.Picture{
		Y: []byte{
			1, 2, 3, 99, 99,
			4, 5, 6, 99, 99,
			7, 8, 9, 99, 99,
		},
		U:        []byte{10, 11, 99, 12, 13, 99},
		V:        []byte{20, 21, 99, 22, 23, 99},
		StrideY:  5,
		StrideUV: 3,
		Width:    3,
		Height:   3,
		BitDepth: 8,
		Chroma:   av1.Chroma420,
		PTS:      42,
	}

	got, err := DigestPicture(pic, 7)
	if err != nil {
		t.Fatal(err)
	}
	wantY := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9}
	wantU := []byte{10, 11, 12, 13}
	wantV := []byte{20, 21, 22, 23}
	if got.Index != 7 || got.PTS != 42 || got.Width != 3 || got.Height != 3 || got.Chroma != "4:2:0" {
		t.Fatalf("metadata = %+v", got)
	}
	if got.YMD5 != sum(wantY) || got.UMD5 != sum(wantU) || got.VMD5 != sum(wantV) {
		t.Fatalf("plane hashes = %s %s %s", got.YMD5, got.UMD5, got.VMD5)
	}
	all := append(append(append([]byte{}, wantY...), wantU...), wantV...)
	if got.FrameMD5 != sum(all) {
		t.Fatalf("frame hash = %s, want %s", got.FrameMD5, sum(all))
	}
}

func TestDigestPictureRejectsShortPlane(t *testing.T) {
	pic := &av1.Picture{Y: make([]byte, 7), StrideY: 4, Width: 4, Height: 2, Chroma: av1.ChromaMonochrome}
	if _, err := DigestPicture(pic, 0); err == nil {
		t.Fatal("expected short-plane error")
	}
}

func sum(data []byte) string {
	h := md5.Sum(data)
	return hex.EncodeToString(h[:])
}
