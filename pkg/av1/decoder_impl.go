// decoder_impl.go contains the concrete Decoder implementation for the pkg/av1
// package. It routes OBUs, manages the reference frame buffer, and applies
// post-processing filters.
//
// Milestone: M7 (tile CABAC decode, intra frames).
// Inter block motion compensation is a DC128 stub; M8 will wire up real MC.
package av1

import (
	"sync"

	"github.com/zesun96/go-av1/internal/cdef"
	"github.com/zesun96/go-av1/internal/header"
	"github.com/zesun96/go-av1/internal/obu"
	"github.com/zesun96/go-av1/internal/tile"
)

// ─── refEntry ─────────────────────────────────────────────────────────────────

// refEntry holds a decoded frame in one slot of the reference buffer.
type refEntry struct {
	fhdr *header.FrameHeader
	pic  *Picture
}

// ─── decoderImpl ──────────────────────────────────────────────────────────────

// decoderImpl is the concrete implementation of the Decoder interface.
type decoderImpl struct {
	mu   sync.Mutex
	opts DecoderOptions

	// logf is the logging function derived from opts.Logger (never nil).
	logf func(string, ...any)

	// seq is the most-recently parsed SequenceHeader.
	seq *header.SequenceHeader

	// refs is the 8-slot decoded-frame reference buffer.
	refs [header.NumRefFrames]refEntry

	// outQ holds fully decoded pictures waiting to be consumed by GetPicture.
	outQ []*Picture

	// pending state for OBUFrameHeader + OBUTileGroup split mode.
	pendingFhdr    *header.FrameHeader
	pendingPic     *Picture
	pendingFhdrRaw []byte // raw payload bytes of the pending frame header

	closed bool
}

// newDecoderImpl constructs a decoderImpl and returns it as a Decoder.
func newDecoderImpl(opts DecoderOptions) (Decoder, error) {
	logf := func(string, ...any) {} // no-op by default
	if opts.Logger != nil {
		logf = opts.Logger.Logf
	}
	return &decoderImpl{opts: opts, logf: logf}, nil
}

// ─── Decoder interface ────────────────────────────────────────────────────────

// SendData feeds one or more size-prefixed OBUs to the decoder.
// Unrecognised OBU types are silently skipped.
func (d *decoderImpl) SendData(packet []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return ErrClosed
	}
	if len(packet) == 0 {
		return nil
	}

	obus, _ := obu.SplitOBUs(packet, obu.ParseOptions{})
	for _, o := range obus {
		if err := d.routeOBU(o); err != nil {
			return err
		}
	}
	return nil
}

// GetPicture returns the next decoded picture from the output queue.
// Returns ErrAgain when no picture is available.
func (d *decoderImpl) GetPicture() (*Picture, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return nil, ErrClosed
	}
	if len(d.outQ) == 0 {
		return nil, ErrAgain
	}
	pic := d.outQ[0]
	d.outQ = d.outQ[1:]
	return pic, nil
}

// Flush clears the reference buffer.
func (d *decoderImpl) Flush() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return ErrClosed
	}
	for i := range d.refs {
		if d.refs[i].pic != nil {
			d.refs[i].pic.Release()
			d.refs[i].pic = nil
			d.refs[i].fhdr = nil
		}
	}
	d.discardPending()
	return nil
}

// Close releases all resources. Safe to call multiple times.
func (d *decoderImpl) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return nil
	}
	d.closed = true

	for _, p := range d.outQ {
		p.Release()
	}
	d.outQ = nil

	d.discardPending()

	for i := range d.refs {
		if d.refs[i].pic != nil {
			d.refs[i].pic.Release()
			d.refs[i].pic = nil
		}
	}
	return nil
}

// discardPending releases any picture allocated for a pending frame header.
// Must be called with d.mu held.
func (d *decoderImpl) discardPending() {
	if d.pendingPic != nil {
		d.pendingPic.Release()
		d.pendingPic = nil
	}
	d.pendingFhdr = nil
	d.pendingFhdrRaw = nil
}

// ─── OBU routing ─────────────────────────────────────────────────────────────

// routeOBU dispatches one parsed OBU to the appropriate handler.
// Must be called with d.mu held.
func (d *decoderImpl) routeOBU(o obu.OBU) error {
	switch o.Header.Type {

	case header.OBUSequenceHeader:
		var seq header.SequenceHeader
		if err := obu.ParseSequenceHeader(o.Payload, &seq, obu.ParseOptions{}); err != nil {
			return err
		}
		d.seq = &seq

	case header.OBUFrameHeader:
		if d.seq == nil {
			return nil
		}
		// Discard any previously pending (incomplete) frame.
		d.discardPending()

		var fhdr header.FrameHeader
		if err := obu.ParseFrameHeader(o.Payload, &fhdr, obu.FrameParseOptions{
			SeqHeader: d.seq,
			Refs:      d.obuRefs(),
		}); err != nil {
			// Best-effort: skip bad frame headers.
			return nil
		}

		if fhdr.ShowExistingFrame != 0 {
			// show_existing_frame: enqueue the referenced frame directly.
			idx := fhdr.ExistingFrameIdx
			if int(idx) < len(d.refs) && d.refs[idx].pic != nil {
				d.outQ = append(d.outQ, d.refs[idx].pic.Retain())
			}
			return nil
		}

		// Allocate picture and wait for the matching OBUTileGroup.
		fhdrCopy := fhdr
		d.pendingFhdr = &fhdrCopy
		d.pendingPic = d.allocPicture(&fhdrCopy)

	case header.OBUTileGroup:
		if d.pendingFhdr == nil || d.pendingPic == nil || d.seq == nil {
			return nil
		}
		// Decode all tiles into the pending picture.
		fb := picToFrameBuf(d.pendingPic)
		tile.DecodeTileGroup(o.Payload, d.pendingFhdr, d.seq, fb, d.logf) //nolint:errcheck
		d.finishFrame(d.pendingPic, d.pendingFhdr)
		d.pendingFhdr = nil
		d.pendingPic = nil

	case header.OBUFrame:
		// OBU_FRAME carries frame_header_obu() + tile_group_obu() concatenated.
		if d.seq == nil {
			return nil
		}
		d.discardPending()

		var fhdr header.FrameHeader
		consumed, err := obu.ParseFrameHeaderEx(o.Payload, &fhdr, obu.FrameParseOptions{
			SeqHeader: d.seq,
			Refs:      d.obuRefs(),
		})
		if err != nil {
			return nil
		}

		if fhdr.ShowExistingFrame != 0 {
			idx := fhdr.ExistingFrameIdx
			if int(idx) < len(d.refs) && d.refs[idx].pic != nil {
				d.outQ = append(d.outQ, d.refs[idx].pic.Retain())
			}
			return nil
		}

		pic := d.allocPicture(&fhdr)
		tilePayload := frameOBUTilePayload(o.Payload, consumed)
		fb := picToFrameBuf(pic)
		tile.DecodeTileGroup(tilePayload, &fhdr, d.seq, fb, d.logf) //nolint:errcheck
		d.finishFrame(pic, &fhdr)

	case header.OBUTemporalDelimiter:
		// Reset pending state at new temporal unit boundary.
		d.discardPending()

	default:
		// All other OBU types (metadata, redundant frame header, etc.) silently ignored.
	}
	return nil
}

// frameOBUTilePayload extracts the tile_group_obu() portion from an OBU_FRAME
// payload. OBU_FRAME = frame_header_obu() (byte-aligned) + tile_group_obu().
// frameHeaderBytes is the number of bytes consumed by the frame header,
// as returned by obu.ParseFrameHeaderEx.
func frameOBUTilePayload(payload []byte, frameHeaderBytes int) []byte {
	if frameHeaderBytes >= len(payload) {
		return nil
	}
	return payload[frameHeaderBytes:]
}

// ─── frame finalisation ───────────────────────────────────────────────────────

// finishFrame applies post-filters, updates the reference buffer, and enqueues
// the picture for output if it is displayable.
// Must be called with d.mu held.
func (d *decoderImpl) finishFrame(pic *Picture, fhdr *header.FrameHeader) {
	d.postFilter(pic, fhdr)
	d.updateRefs(pic, fhdr)
	if fhdr.ShowFrame != 0 || fhdr.ShowableFrame != 0 {
		d.outQ = append(d.outQ, pic.Retain())
	}
	pic.Release()
}

// ─── legacy helpers ───────────────────────────────────────────────────────────

// obuRefs builds the FrameReference array expected by ParseFrameHeader.
func (d *decoderImpl) obuRefs() *[header.NumRefFrames]obu.FrameReference {
	var refs [header.NumRefFrames]obu.FrameReference
	for i, e := range d.refs {
		refs[i].FrameHdr = e.fhdr
	}
	return &refs
}

// picToFrameBuf wraps a *Picture as a tile.FrameBuf so the tile package does
// not need to import pkg/av1 (which would create an import cycle).
func picToFrameBuf(p *Picture) *tile.FrameBuf {
	return &tile.FrameBuf{
		Y:          p.Y,
		StrideY:    p.StrideY,
		Width:      p.Width,
		Height:     p.Height,
		U:          p.U,
		V:          p.V,
		StrideUV:   p.StrideUV,
		ChromaW:    p.ChromaWidth(),
		ChromaH:    p.ChromaHeight(),
		Monochrome: p.Chroma == ChromaMonochrome,
	}
}

// allocPicture creates a new Picture for the given frame header.
func (d *decoderImpl) allocPicture(fhdr *header.FrameHeader) *Picture {
	w := fhdr.Width[0]
	h := fhdr.Height
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}
	strideY := (w + 15) &^ 15
	cw := (w + 1) >> 1
	ch := (h + 1) >> 1
	strideUV := (cw + 15) &^ 15

	pic := &Picture{
		Y:        make([]byte, strideY*h),
		U:        make([]byte, strideUV*ch),
		V:        make([]byte, strideUV*ch),
		StrideY:  strideY,
		StrideUV: strideUV,
		Width:    w,
		Height:   h,
		BitDepth: 8,
		Chroma:   Chroma420,
	}
	// Seed planes with neutral grey so any block that fails to decode shows
	// up as grey rather than pure-green (chroma=0 maps to bright green in
	// YUV→RGB). Y=128, U=V=128 ⇒ mid-grey.
	for i := range pic.Y {
		pic.Y[i] = 128
	}
	for i := range pic.U {
		pic.U[i] = 128
	}
	for i := range pic.V {
		pic.V[i] = 128
	}
	pic.Retain() // initial reference
	return pic
}

// updateRefs stores pic into every reference slot set in RefreshFrameFlags.
func (d *decoderImpl) updateRefs(pic *Picture, fhdr *header.FrameHeader) {
	fhdrCopy := *fhdr
	for i := 0; i < header.NumRefFrames; i++ {
		if fhdr.RefreshFrameFlags&(1<<uint(i)) == 0 {
			continue
		}
		if d.refs[i].pic != nil {
			d.refs[i].pic.Release()
		}
		d.refs[i].fhdr = &fhdrCopy
		d.refs[i].pic = pic.Retain()
	}
}

// ─── post-filter stubs ───────────────────────────────────────────────────────

// postFilter dispatches the three-stage in-loop post-processing chain.
// Each stage is wrapped in its own panic recovery: M7 post-filters are
// best-effort and a crash in any of them must NOT prevent the picture
// (already filled by the tile decoder) from reaching the output queue.
func (d *decoderImpl) postFilter(pic *Picture, fhdr *header.FrameHeader) {
	run := func(name string, fn func()) {
		defer func() {
			if r := recover(); r != nil {
				d.logf("postFilter: %s recovered from panic: %v", name, r)
			}
		}()
		fn()
	}
	if d.opts.InloopFilters&InloopFilterDeblock != 0 {
		run("deblock", func() { d.applyLoopFilter(pic, fhdr) })
	}
	if d.opts.InloopFilters&InloopFilterCDEF != 0 {
		run("cdef", func() { d.applyCDEF(pic, fhdr) })
	}
	if d.opts.InloopFilters&InloopFilterRestoration != 0 {
		run("restoration", func() { d.applyRestoration(pic, fhdr) })
	}
}

// applyLoopFilter applies a simplified horizontal and vertical deblocking
// filter across all 4-pixel-aligned block boundaries using the frame-level
// loop filter levels from the frame header.
// This is a best-effort implementation: it uses a constant filter width of 4
// (narrow) and skips block-level adaptation, which is sufficient to reduce
// block artefacts on intra-only keyframes without requiring per-block metadata.
func (d *decoderImpl) applyLoopFilter(pic *Picture, fhdr *header.FrameHeader) {
	levelY := int(fhdr.LoopFilter.LevelY[0])
	if levelY == 0 {
		return // loop filter disabled
	}
	// Clamp to [0,63].
	if levelY > 63 {
		levelY = 63
	}

	// Compute E and I from level (AV1 spec Table 7.18):
	//   E = 2*(level+2) + sharpness  (simplified, sharpness=0)
	//   I = level + 2                (simplified)
	//   H = 0 (HEV threshold, simplified to 0 = no HEV boost)
	sharpness := int(fhdr.LoopFilter.Sharpness)
	eThresh := 2*(levelY+2) + sharpness
	iThresh := levelY + 2
	// Clamp thresholds to valid 8-bit range.
	if eThresh > 255 {
		eThresh = 255
	}
	if iThresh > 63 {
		iThresh = 63
	}

	deblockPlane(pic.Y, pic.StrideY, pic.Width, pic.Height, 4, eThresh, iThresh)

	levelU := int(fhdr.LoopFilter.LevelU)
	levelV := int(fhdr.LoopFilter.LevelV)
	if levelU == 0 {
		levelU = levelY / 2
	}
	if levelV == 0 {
		levelV = levelY / 2
	}
	cw := pic.ChromaWidth()
	ch := pic.ChromaHeight()
	eU := 2*(levelU+2) + sharpness
	iU := levelU + 2
	eV := 2*(levelV+2) + sharpness
	iV := levelV + 2
	if eU > 255 {
		eU = 255
	}
	if eV > 255 {
		eV = 255
	}
	deblockPlane(pic.U, pic.StrideUV, cw, ch, 4, eU, iU)
	deblockPlane(pic.V, pic.StrideUV, cw, ch, 4, eV, iV)
}

// deblockPlane applies a simple 4-tap deblocking filter on all grid-aligned
// edges (step=4 pixels) of a single plane in both H and V directions.
func deblockPlane(plane []byte, stride, w, h, step, eThresh, iThresh int) {
	if len(plane) == 0 {
		return
	}
	// Horizontal edges (filter vertically across them).
	for y := step; y < h; y += step {
		for x := 0; x < w; x++ {
			off := y*stride + x
			offPrev := (y-1)*stride + x
			if off >= len(plane) || offPrev < 0 {
				continue
			}
			p1 := int(plane[(y-2)*stride+x])
			p0 := int(plane[offPrev])
			q0 := int(plane[off])
			q1 := int(plane[(y+1)*stride+x])
			if (y+1)*stride+x >= len(plane) {
				continue
			}
			// Basic filter mask: |p1-p0|<=I && |q1-q0|<=I && |p0-q0|*2+|p1-q1|/2 <= E
			absP1P0 := p1 - p0
			if absP1P0 < 0 {
				absP1P0 = -absP1P0
			}
			absQ1Q0 := q1 - q0
			if absQ1Q0 < 0 {
				absQ1Q0 = -absQ1Q0
			}
			absP0Q0 := p0 - q0
			if absP0Q0 < 0 {
				absP0Q0 = -absP0Q0
			}
			absP1Q1 := p1 - q1
			if absP1Q1 < 0 {
				absP1Q1 = -absP1Q1
			}
			if absP1P0 > iThresh || absQ1Q0 > iThresh || absP0Q0*2+absP1Q1/2 > eThresh {
				continue
			}
			// Narrow filter.
			f := 3*(q0-p0) + (p1 - q1)
			const limit = 128
			f = clampVal(f, -limit, limit-1)
			f1 := clampVal(f+4, -limit, limit-1) >> 3
			f2 := clampVal(f+3, -limit, limit-1) >> 3
			plane[offPrev] = clampPixel(p0 + f2)
			plane[off] = clampPixel(q0 - f1)
		}
	}
	// Vertical edges (filter horizontally across them).
	for y := 0; y < h; y++ {
		for x := step; x < w; x += step {
			off := y*stride + x
			offPrev := y*stride + (x - 1)
			if off >= len(plane) || offPrev < 0 {
				continue
			}
			p1 := int(plane[y*stride+(x-2)])
			p0 := int(plane[offPrev])
			q0 := int(plane[off])
			q1off := y*stride + (x + 1)
			if q1off >= len(plane) {
				continue
			}
			q1 := int(plane[q1off])
			absP1P0 := p1 - p0
			if absP1P0 < 0 {
				absP1P0 = -absP1P0
			}
			absQ1Q0 := q1 - q0
			if absQ1Q0 < 0 {
				absQ1Q0 = -absQ1Q0
			}
			absP0Q0 := p0 - q0
			if absP0Q0 < 0 {
				absP0Q0 = -absP0Q0
			}
			absP1Q1 := p1 - q1
			if absP1Q1 < 0 {
				absP1Q1 = -absP1Q1
			}
			if absP1P0 > iThresh || absQ1Q0 > iThresh || absP0Q0*2+absP1Q1/2 > eThresh {
				continue
			}
			f := 3*(q0-p0) + (p1 - q1)
			const limit = 128
			f = clampVal(f, -limit, limit-1)
			f1 := clampVal(f+4, -limit, limit-1) >> 3
			f2 := clampVal(f+3, -limit, limit-1) >> 3
			plane[offPrev] = clampPixel(p0 + f2)
			plane[off] = clampPixel(q0 - f1)
		}
	}
}

func clampVal(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampPixel(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// applyCDEF applies CDEF per 8×8 block for luma and 4×4 for chroma.
// It reads the primary and secondary strengths from the frame header.
func (d *decoderImpl) applyCDEF(pic *Picture, fhdr *header.FrameHeader) {
	if fhdr.CDEF.NBits == 0 {
		return // CDEF disabled
	}
	// Use the first CDEF preset (index 0).
	// In a full implementation we'd read the per-superblock CDEF index
	// from the bitstream; here we apply a single global preset.
	priY := int(fhdr.CDEF.YStrength[0])
	secY := priY & 0x3 // lower 2 bits are secondary
	priY >>= 2         // upper 4 bits are primary
	priUV := int(fhdr.CDEF.UVStrength[0])
	secUV := priUV & 0x3
	priUV >>= 2
	damping := int(fhdr.CDEF.Damping)

	applyCDEFPlane(pic.Y, pic.StrideY, pic.Width, pic.Height, 8, priY, secY, damping)
	if pic.Chroma != ChromaMonochrome && len(pic.U) > 0 {
		cw := pic.ChromaWidth()
		ch := pic.ChromaHeight()
		applyCDEFPlane(pic.U, pic.StrideUV, cw, ch, 4, priUV, secUV, damping)
		applyCDEFPlane(pic.V, pic.StrideUV, cw, ch, 4, priUV, secUV, damping)
	}
}

// applyCDEFPlane applies CDEF block-by-block to one plane.
func applyCDEFPlane(plane []byte, stride, w, h, blockSz, priStrength, secStrength, damping int) {
	if len(plane) == 0 || (priStrength == 0 && secStrength == 0) {
		return
	}
	for by := 0; by < h; by += blockSz {
		for bx := 0; bx < w; bx += blockSz {
			bw := blockSz
			bh := blockSz
			if bx+bw > w {
				bw = w - bx
			}
			if by+bh > h {
				bh = h - by
			}
			if bw <= 0 || bh <= 0 {
				continue
			}

			// Build edge flags.
			var edges cdef.EdgeFlags
			if by > 0 {
				edges |= cdef.HaveTop
			}
			if by+bh < h {
				edges |= cdef.HaveBottom
			}
			if bx > 0 {
				edges |= cdef.HaveLeft
			}
			if bx+bw < w {
				edges |= cdef.HaveRight
			}

			// Build left [][2]uint8 (left 2 pixels, h rows).
			left := make([][2]uint8, bh)
			if bx >= 2 {
				for row := 0; row < bh; row++ {
					y := by + row
					if y < h {
						left[row][0] = plane[y*stride+(bx-2)]
						left[row][1] = plane[y*stride+(bx-1)]
					}
				}
			}

			// Top row.
			var top []byte
			topBase := 0
			if by > 0 {
				top = plane[(by-1)*stride:]
				topBase = bx
			} else {
				top = make([]byte, bw)
			}

			// Bottom row.
			var bottom []byte
			bottomBase := 0
			if by+bh < h {
				bottom = plane[(by+bh)*stride:]
				bottomBase = bx
			} else {
				bottom = make([]byte, bw)
			}

			// Find direction.
			dir, _ := cdef.FindDir(plane, by*stride+bx, stride)

			cdef.FilterBlock(
				plane, by*stride+bx, stride,
				left,
				top, topBase, stride,
				bottom, bottomBase, stride,
				priStrength, secStrength, dir, damping, bw, bh,
				edges,
			)
		}
	}
}

// applyRestoration is a stub.
// TODO M8: call looprestoration.WienerFilter / SGR per restoration unit.
func (d *decoderImpl) applyRestoration(_ *Picture, _ *header.FrameHeader) {}
