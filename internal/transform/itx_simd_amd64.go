//go:build amd64 && !purego

package transform

import "github.com/zesun96/go-av1/internal/dispatch"

// SSE2 is part of the Go amd64 baseline.
var haveAddDCSSE2 = true

func addDC8SIMD(dst []uint8, stride, w, h, dc int) bool {
	if !haveAddDCSSE2 || dispatch.GenericForced() ||
		w < 8 || w&7 != 0 || h <= 0 {
		return false
	}
	if dc > 255 {
		dc = 255
	} else if dc < -255 {
		dc = -255
	}
	addDC8SSE2(&dst[0], stride, w, h, dc)
	return true
}

//go:noescape
func addDC8SSE2(dst *uint8, stride, w, h, dc int)
