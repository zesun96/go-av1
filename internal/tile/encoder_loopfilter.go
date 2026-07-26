package tile

import (
	"github.com/zesun96/go-av1/internal/header"
	"github.com/zesun96/go-av1/internal/loopfilter"
)

// ApplyEncoderLoopFilter applies the same block-state-driven deblocking pass
// as the decoder, allowing filtered reconstructions to be stored as references.
func ApplyEncoderLoopFilter(fs *FrameState, planes [3][]byte,
	widths, heights [3]int, level int,
) {
	if fs == nil || level <= 0 {
		return
	}
	if level > 63 {
		level = 63
	}
	fhdr := &header.FrameHeader{}
	fhdr.LoopFilter.LevelY = [2]uint8{uint8(level), uint8(level)}
	fhdr.LoopFilter.LevelU = uint8(level)
	fhdr.LoopFilter.LevelV = uint8(level)
	lut := loopfilter.NewFilterLUT(0)

	w, h := widths[0], heights[0]
	for x4 := 1; x4 < (w+3)>>2; x4++ {
		x := x4 * 4
		for y4 := 0; y4 < (h+3)>>2 && y4*4+4 <= h; y4++ {
			filterWidth, ok := fs.LumaFilterEdge(x4, y4, true)
			filterWidth = encoderSafeFilterWidth(filterWidth, x, w-x)
			if !ok || filterWidth == 0 {
				continue
			}
			filterLevel := fs.LumaFilterLevel(fhdr, x4, y4, true)
			loopfilter.FilterEdgeV(planes[0], y4*4*w+x, w, filterLevel, filterWidth, &lut)
		}
	}
	for y4 := 1; y4 < (h+3)>>2; y4++ {
		y := y4 * 4
		for x4 := 0; x4 < (w+3)>>2 && x4*4+4 <= w; x4++ {
			filterWidth, ok := fs.LumaFilterEdge(x4, y4, false)
			filterWidth = encoderSafeFilterWidth(filterWidth, y, h-y)
			if !ok || filterWidth == 0 {
				continue
			}
			filterLevel := fs.LumaFilterLevel(fhdr, x4, y4, false)
			loopfilter.FilterEdgeH(planes[0], y*w+x4*4, w, filterLevel, filterWidth, &lut)
		}
	}

	for plane := 1; plane < 3; plane++ {
		w, h = widths[plane], heights[plane]
		for x4 := 1; x4 < fs.CW4; x4++ {
			x := x4 * 4
			for y4 := 0; y4 < fs.CH4 && y4*4+4 <= h; y4++ {
				filterWidth, ok := fs.ChromaFilterEdge(x4, y4, true)
				filterWidth = encoderSafeFilterWidth(filterWidth, x, w-x)
				if !ok || filterWidth == 0 {
					continue
				}
				filterLevel := fs.ChromaFilterLevel(fhdr, x4, y4, plane)
				loopfilter.FilterEdgeV(planes[plane], y4*4*w+x, w, filterLevel, filterWidth, &lut)
			}
		}
		for y4 := 1; y4 < fs.CH4; y4++ {
			y := y4 * 4
			for x4 := 0; x4 < fs.CW4 && x4*4+4 <= w; x4++ {
				filterWidth, ok := fs.ChromaFilterEdge(x4, y4, false)
				filterWidth = encoderSafeFilterWidth(filterWidth, y, h-y)
				if !ok || filterWidth == 0 {
					continue
				}
				filterLevel := fs.ChromaFilterLevel(fhdr, x4, y4, plane)
				loopfilter.FilterEdgeH(planes[plane], y*w+x4*4, w, filterLevel, filterWidth, &lut)
			}
		}
	}
}

func encoderSafeFilterWidth(width, before, after int) int {
	for _, candidate := range []int{16, 8, 6, 4} {
		if width < candidate {
			continue
		}
		radius := map[int]int{16: 7, 8: 4, 6: 3, 4: 2}[candidate]
		if before >= radius && after >= radius {
			return candidate
		}
	}
	return 0
}
