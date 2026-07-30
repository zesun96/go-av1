//go:build !amd64 || purego

package transform

func addDC8SIMD(dst []uint8, stride, w, h, dc int) bool {
	return false
}
