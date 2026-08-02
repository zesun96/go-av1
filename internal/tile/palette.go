package tile

import (
	"math/bits"

	"github.com/zesun96/go-av1/internal/bitstream"
	"github.com/zesun96/go-av1/internal/header"
)

var paletteMissingOrder = func() [256][8]uint8 {
	var table [256][8]uint8
	for mask := range table {
		n := 0
		for color := 0; color < 8; color++ {
			if mask&(1<<color) == 0 {
				table[mask][n] = uint8(color)
				n++
			}
		}
	}
	return table
}()

func bitDepthFromSeq(seq *header.SequenceHeader) int {
	switch seq.HBD {
	case 0:
		return 8
	case 1:
		return 10
	default:
		return 12
	}
}

func readPalettePlane(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	seq *header.SequenceHeader, plane, szCtx, bx, by, palSz int,
) [8]uint8 {
	var pal [8]uint8
	bpc := bitDepthFromSeq(seq)
	bx4 := bx >> 2
	by4 := by >> 2

	lCache := 0
	aCache := 0
	if plane == 0 {
		if by4 < len(fs.LeftPalY) {
			lCache = int(fs.LeftPalY[by4])
		}
		if by4&15 != 0 && bx4 < len(fs.AbovePalY) {
			aCache = int(fs.AbovePalY[bx4])
		}
	} else {
		if by4 < len(fs.LeftPalUV) {
			lCache = int(fs.LeftPalUV[by4])
		}
		if by4&15 != 0 && bx4 < len(fs.AbovePalUV) {
			aCache = int(fs.AbovePalUV[bx4])
		}
	}
	if lCache > 8 {
		lCache = 8
	}
	if aCache > 8 {
		aCache = 8
	}

	left := fs.LeftPal[plane][minInt(by4, len(fs.LeftPal[plane])-1)]
	above := [8]uint8{}
	if bx4 >= 0 && bx4 < len(fs.AbovePal[plane]) {
		above = fs.AbovePal[plane][bx4]
	}

	var cache [16]uint8
	nCache := 0
	li := 0
	ai := 0
	for li < lCache && ai < aCache {
		lv := left[li]
		av := above[ai]
		if lv < av {
			if nCache == 0 || cache[nCache-1] != lv {
				cache[nCache] = lv
				nCache++
			}
			li++
		} else {
			if av == lv {
				li++
			}
			if nCache == 0 || cache[nCache-1] != av {
				cache[nCache] = av
				nCache++
			}
			ai++
		}
	}
	for ; li < lCache; li++ {
		lv := left[li]
		if nCache == 0 || cache[nCache-1] != lv {
			cache[nCache] = lv
			nCache++
		}
	}
	for ; ai < aCache; ai++ {
		av := above[ai]
		if nCache == 0 || cache[nCache-1] != av {
			cache[nCache] = av
			nCache++
		}
	}

	var usedCache [8]uint8
	nUsed := 0
	for i := 0; i < nCache && nUsed < palSz; i++ {
		if m.BoolEqui() != 0 {
			usedCache[nUsed] = cache[i]
			nUsed++
		}
	}

	i := nUsed
	if i < palSz {
		prev := int(m.Bools(bpc))
		pal[i] = uint8(prev)
		i++
		if i < palSz {
			bitsUsed := bpc - 3 + int(m.Bools(2))
			max := (1 << bpc) - 1
			step := 0
			if plane == 0 {
				step = 1
			}
			for i < palSz {
				delta := int(m.Bools(bitsUsed))
				prev = minInt(prev+delta+step, max)
				pal[i] = uint8(prev)
				i++
				if prev+step >= max {
					for ; i < palSz; i++ {
						pal[i] = uint8(max)
					}
					break
				}
				bitsUsed = minInt(bitsUsed, 1+ulog2(max-prev-step))
			}
		}

		n := 0
		mid := nUsed
		var merged [8]uint8
		for i := 0; i < palSz; i++ {
			if n < nUsed && (mid >= palSz || usedCache[n] <= pal[mid]) {
				merged[i] = usedCache[n]
				n++
			} else {
				merged[i] = pal[mid]
				mid++
			}
		}
		pal = merged
	} else {
		for i := 0; i < nUsed; i++ {
			pal[i] = usedCache[i]
		}
	}

	_ = szCtx
	_ = ctx
	return pal
}

func readPaletteUV(m *bitstream.MSAC, ctx *TileCtx, fs *FrameState,
	seq *header.SequenceHeader, szCtx, bx, by, palSz int,
) ([8]uint8, [8]uint8) {
	u := readPalettePlane(m, ctx, fs, seq, 1, szCtx, bx, by, palSz)
	var v [8]uint8
	bpc := bitDepthFromSeq(seq)
	max := (1 << bpc) - 1
	if m.BoolEqui() != 0 {
		bitsUsed := bpc - 4 + int(m.Bools(2))
		prev := int(m.Bools(bpc))
		v[0] = uint8(prev)
		for i := 1; i < palSz; i++ {
			delta := int(m.Bools(bitsUsed))
			if delta != 0 && m.BoolEqui() != 0 {
				delta = -delta
			}
			prev = (prev + delta) & max
			v[i] = uint8(prev)
		}
	} else {
		for i := 0; i < palSz; i++ {
			v[i] = uint8(m.Bools(bpc))
		}
	}
	return u, v
}

func readPalIndices(m *bitstream.MSAC, colorMapCDF *[5][8]uint16, palSz, w, h, bw, bh int) []uint8 {
	if palSz <= 0 || w <= 0 || h <= 0 || bw <= 0 || bh <= 0 {
		return nil
	}
	idx := make([]uint8, bw*bh)
	idx[0] = uint8(m.Uniform(uint32(palSz)))

	for i := 1; i < w+h-1; i++ {
		first := minInt(i, w-1)
		last := maxInt(0, i-h+1)
		haveTop := i > first
		for j, off := first, first+(i-first)*bw; j >= last; j, off = j-1, off+bw-1 {
			order, mask, count, context := paletteOrderCompactAt(idx, bw, off, j > 0, haveTop)
			colorIdx := int(m.SymbolAdaptDav1d(colorMapCDF[context][:], palSz-1))
			if colorIdx < int(count) {
				idx[off] = uint8(order >> uint(colorIdx*8))
			} else {
				idx[off] = paletteMissingOrder[mask][colorIdx-int(count)]
			}
			haveTop = true
		}
	}
	palIdxFinish(idx, bw, bh, w, h)
	return idx
}

func paletteOrderCompactAt(palIdx []uint8, stride, off int, haveLeft, haveTop bool) (
	order uint32, mask, count, context uint8,
) {
	add := func(v uint8) {
		order |= uint32(v) << uint(count*8)
		count++
		mask |= 1 << v
	}
	if !haveLeft {
		add(palIdx[off-stride])
	} else if !haveTop {
		add(palIdx[off-1])
	} else {
		l := palIdx[off-1]
		t := palIdx[off-stride]
		tl := palIdx[off-stride-1]
		sameTL := t == l
		sameTTl := t == tl
		sameLTl := l == tl
		switch {
		case sameTL && sameTTl && sameLTl:
			context = 4
			add(t)
		case sameTL:
			context = 3
			add(t)
			add(tl)
		case sameTTl || sameLTl:
			context = 2
			add(tl)
			if sameTTl {
				add(l)
			} else {
				add(t)
			}
		default:
			context = 1
			if t < l {
				add(t)
				add(l)
			} else {
				add(l)
				add(t)
			}
			add(tl)
		}
	}
	return order, mask, count, context
}

func paletteOrderAt(palIdx []uint8, stride, off int, haveLeft, haveTop bool) (row [8]uint8, context uint8) {
	order, mask, count, context := paletteOrderCompactAt(palIdx, stride, off, haveLeft, haveTop)
	for i := 0; i < int(count); i++ {
		row[i] = uint8(order >> uint(i*8))
	}
	missing := &paletteMissingOrder[mask]
	copy(row[count:], missing[:8-count])
	return row, context
}

func orderPalette(palIdx []uint8, stride, i, first, last int, order *[64][8]uint8, ctx *[64]uint8) {
	haveTop := i > first
	base := first + (i-first)*stride
	for j, n, off := first, 0, base; j >= last; j, n, off = j-1, n+1, off+stride-1 {
		order[n], ctx[n] = paletteOrderAt(palIdx, stride, off, j > 0, haveTop)
		haveTop = true
	}
}

func palIdxFinish(idx []uint8, bw, bh, w, h int) {
	for y := 0; y < h; y++ {
		row := idx[y*bw : y*bw+bw]
		fill := row[w-1]
		for x := w; x < bw; x++ {
			row[x] = fill
		}
	}
	if h < bh {
		last := idx[(h-1)*bw : h*bw]
		for y := h; y < bh; y++ {
			copy(idx[y*bw:(y+1)*bw], last)
		}
	}
}

func predictPalette(dst []byte, stride int, pal [8]uint8, idx []uint8, bw, bh, idxStride int) {
	for y := 0; y < bh; y++ {
		dstRow := dst[y*stride:]
		idxRow := idx[y*idxStride:]
		for x := 0; x < bw; x++ {
			dstRow[x] = pal[idxRow[x]]
		}
	}
}

func ulog2(v int) int {
	if v <= 0 {
		return 0
	}
	return bits.Len(uint(v)) - 1
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
