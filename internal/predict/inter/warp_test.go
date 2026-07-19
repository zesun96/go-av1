package inter

import "testing"

func TestPutWarpAffineIdentity(t *testing.T) {
	const width, height = 24, 24
	src := make([]byte, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			src[y*width+x] = byte((x*3 + y*7) & 0xff)
		}
	}
	dst := make([]byte, 8*8)
	PutWarpAffine(dst, 8, src, width, width, height, 8, 8, 8, 8, 0, 0,
		[6]int32{0, 0, 1 << 16, 0, 0, 1 << 16}, [4]int16{})
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if got, want := dst[y*8+x], src[(y+8)*width+x+8]; got != want {
				t.Fatalf("pixel (%d,%d) = %d, want %d", x, y, got, want)
			}
		}
	}
}
