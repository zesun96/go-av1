// decoder_impl.go contains the concrete Decoder implementation for the pkg/av1
// package. It routes OBUs, manages the reference frame buffer, and applies
// post-processing filters.
//
// Milestone: M7 (tile CABAC decode, intra frames).
// Inter block motion compensation is a DC128 stub; M8 will wire up real MC.
package av1

import (
	"fmt"
	"math/bits"
	"os"
	"sync"

	"github.com/zesun96/go-av1/internal/cdef"
	"github.com/zesun96/go-av1/internal/header"
	"github.com/zesun96/go-av1/internal/loopfilter"
	"github.com/zesun96/go-av1/internal/looprestoration"
	"github.com/zesun96/go-av1/internal/obu"
	"github.com/zesun96/go-av1/internal/refmvs"
	"github.com/zesun96/go-av1/internal/tile"
)

// ─── refEntry ─────────────────────────────────────────────────────────────────

// refEntry holds a decoded frame in one slot of the reference buffer.
type refEntry struct {
	fhdr *header.FrameHeader
	pic  *Picture
	cdf  *tile.TileCtx
	mv   *refmvs.Frame
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

	obus, err := obu.SplitOBUs(packet, obu.ParseOptions{})
	if err != nil && !d.opts.BestEffort {
		return fmt.Errorf("split OBUs: %w: %v", ErrInvalidBitstream, err)
	}
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
		if err := validateSequenceSupport(&seq); err != nil {
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
			if d.opts.BestEffort {
				return nil
			}
			return fmt.Errorf("parse frame header: %w: %v", ErrInvalidBitstream, err)
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
		fb := d.picToFrameBuf(d.pendingPic)
		frameCDF, err := tile.DecodeTileGroupWithContext(o.Payload, d.pendingFhdr, d.seq, fb, d.initialFrameCDF(d.pendingFhdr), d.logf)
		if err != nil {
			if !d.opts.BestEffort {
				d.discardPending()
				return fmt.Errorf("decode tile group: %w: %v", ErrInvalidBitstream, err)
			}
		}
		d.finishFrame(d.pendingPic, d.pendingFhdr, frameCDF, fb.MVFrame, fb.FilterState)
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
			if d.opts.BestEffort {
				return nil
			}
			return fmt.Errorf("parse frame OBU header: %w: %v", ErrInvalidBitstream, err)
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
		fb := d.picToFrameBuf(pic)
		frameCDF, err := tile.DecodeTileGroupWithContext(tilePayload, &fhdr, d.seq, fb, d.initialFrameCDF(&fhdr), d.logf)
		if err != nil {
			if d.opts.BestEffort {
				d.finishFrame(pic, &fhdr, nil, fb.MVFrame, fb.FilterState)
				return nil
			}
			pic.Release()
			return fmt.Errorf("decode frame tile group: %w: %v", ErrInvalidBitstream, err)
		}
		d.finishFrame(pic, &fhdr, frameCDF, fb.MVFrame, fb.FilterState)

	case header.OBUTemporalDelimiter:
		// Reset pending state at new temporal unit boundary.
		d.discardPending()

	default:
		// All other OBU types (metadata, redundant frame header, etc.) silently ignored.
	}
	return nil
}

func validateSequenceSupport(seq *header.SequenceHeader) error {
	if seq.HBD != 0 {
		bitDepth := 10
		if seq.HBD == 2 {
			bitDepth = 12
		}
		return fmt.Errorf("%w: %d-bit sequences", ErrUnsupported, bitDepth)
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
func (d *decoderImpl) finishFrame(pic *Picture, fhdr *header.FrameHeader, cdf *tile.TileCtx, mv *refmvs.Frame, filterState *tile.FrameState) {
	d.postFilter(pic, fhdr, filterState)
	d.updateRefs(pic, fhdr, cdf, mv)
	if fhdr.ShowFrame != 0 {
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
func (d *decoderImpl) picToFrameBuf(p *Picture) *tile.FrameBuf {
	codedW := (p.Width + 7) &^ 7
	codedH := (p.Height + 7) &^ 7
	fb := &tile.FrameBuf{
		Y:            p.Y,
		StrideY:      p.StrideY,
		Width:        p.Width,
		Height:       p.Height,
		CodedWidth:   codedW,
		CodedHeight:  codedH,
		U:            p.U,
		V:            p.V,
		StrideUV:     p.StrideUV,
		ChromaW:      p.ChromaWidth(),
		ChromaH:      p.ChromaHeight(),
		CodedChromaW: (codedW + 1) >> 1,
		CodedChromaH: (codedH + 1) >> 1,
		Monochrome:   p.Chroma == ChromaMonochrome,
	}
	for i, ref := range d.refs {
		fb.RefMVs[i] = ref.mv
		if ref.pic == nil {
			continue
		}
		rp := ref.pic
		fb.Refs[i] = &tile.PlaneBuf{
			Y:          rp.Y,
			StrideY:    rp.StrideY,
			Width:      rp.Width,
			Height:     rp.Height,
			U:          rp.U,
			V:          rp.V,
			StrideUV:   rp.StrideUV,
			ChromaW:    rp.ChromaWidth(),
			ChromaH:    rp.ChromaHeight(),
			Monochrome: rp.Chroma == ChromaMonochrome,
		}
	}
	return fb
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
	codedW := (w + 7) &^ 7
	codedH := (h + 7) &^ 7
	strideY := (codedW + 15) &^ 15
	cw := (w + 1) >> 1
	strideUV := (cw + 15) &^ 15
	codedCh := (codedH + 1) >> 1

	pic := &Picture{
		Y:        make([]byte, strideY*codedH),
		U:        make([]byte, strideUV*codedCh),
		V:        make([]byte, strideUV*codedCh),
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
func (d *decoderImpl) updateRefs(pic *Picture, fhdr *header.FrameHeader, cdf *tile.TileCtx, mv *refmvs.Frame) {
	fhdrCopy := *fhdr
	cdf = d.cdfForReferenceUpdate(fhdr, cdf)
	if os.Getenv("GOAV1_TRACE_FRAMES") != "" {
		d.logf("sym ref_cdf refresh=%02x palette_size_y0=%v", fhdr.RefreshFrameFlags, cdf.PaletteSizeCDF[0][0])
	}
	if fhdr.FrameType.IsIntra() {
		mv = nil
	}
	for i := 0; i < header.NumRefFrames; i++ {
		if fhdr.RefreshFrameFlags&(1<<uint(i)) == 0 {
			continue
		}
		if d.refs[i].pic != nil {
			d.refs[i].pic.Release()
		}
		d.refs[i].fhdr = &fhdrCopy
		d.refs[i].pic = pic.Retain()
		d.refs[i].cdf = cdf.Clone()
		d.refs[i].mv = mv
	}
}

// cdfForReferenceUpdate mirrors dav1d's reference refresh rule. When frame-end
// CDF refresh is disabled, refreshed slots inherit the frame input context,
// not the context adapted while decoding the context-update tile.
func (d *decoderImpl) cdfForReferenceUpdate(fhdr *header.FrameHeader, decoded *tile.TileCtx) *tile.TileCtx {
	if fhdr != nil && fhdr.RefreshContext != 0 && decoded != nil {
		return decoded
	}
	if inherited := d.initialFrameCDF(fhdr); inherited != nil {
		return inherited
	}
	qidx := 0
	if fhdr != nil {
		qidx = int(fhdr.Quant.YAC)
	}
	return tile.NewTileCtxForQIdx(qidx)
}

func (d *decoderImpl) initialFrameCDF(fhdr *header.FrameHeader) *tile.TileCtx {
	if fhdr == nil || fhdr.PrimaryRefFrame == header.PrimaryRefNone || int(fhdr.PrimaryRefFrame) >= len(fhdr.Refidx) {
		return nil
	}
	refSlot := int(fhdr.Refidx[fhdr.PrimaryRefFrame])
	if refSlot < 0 || refSlot >= len(d.refs) {
		return nil
	}
	return d.refs[refSlot].cdf.CloneForFrame()
}

// ─── post-filter stubs ───────────────────────────────────────────────────────

// postFilter dispatches the three-stage in-loop post-processing chain.
// Each stage is wrapped in its own panic recovery: M7 post-filters are
// best-effort and a crash in any of them must NOT prevent the picture
// (already filled by the tile decoder) from reaching the output queue.
func (d *decoderImpl) postFilter(pic *Picture, fhdr *header.FrameHeader, filterState *tile.FrameState) {
	run := func(name string, fn func()) {
		defer func() {
			if r := recover(); r != nil {
				d.logf("postFilter: %s recovered from panic: %v", name, r)
			}
		}()
		fn()
	}
	if d.opts.InloopFilters&InloopFilterDeblock != 0 {
		run("deblock", func() { d.applyLoopFilterWithState(pic, fhdr, filterState) })
	}
	var restorationBoundary [3][]byte
	if d.opts.InloopFilters&InloopFilterRestoration != 0 {
		restorationBoundary[0] = append([]byte(nil), pic.Y...)
		restorationBoundary[1] = append([]byte(nil), pic.U...)
		restorationBoundary[2] = append([]byte(nil), pic.V...)
	}
	if d.opts.InloopFilters&InloopFilterCDEF != 0 {
		run("cdef", func() { d.applyCDEFWithState(pic, fhdr, filterState) })
	}
	if d.opts.InloopFilters&InloopFilterRestoration != 0 {
		run("restoration", func() { d.applyRestoration(pic, fhdr, filterState, restorationBoundary) })
	}
}

// applyLoopFilter applies a simplified horizontal and vertical deblocking
// filter across all 4-pixel-aligned block boundaries using the frame-level
// loop filter levels from the frame header.
// This is a best-effort implementation: it uses a constant filter width of 4
// (narrow) and skips block-level adaptation, which is sufficient to reduce
// block artefacts on intra-only keyframes without requiring per-block metadata.
func (d *decoderImpl) applyLoopFilter(pic *Picture, fhdr *header.FrameHeader) {
	d.applyLoopFilterWithState(pic, fhdr, nil)
}

func (d *decoderImpl) applyLoopFilterWithState(pic *Picture, fhdr *header.FrameHeader, filterState *tile.FrameState) {
	// When both luma levels are zero, AV1 disables deblocking for every
	// plane; mode/reference deltas must not turn filtering back on.
	if fhdr == nil || fhdr.LoopFilter.LevelY[0] == 0 && fhdr.LoopFilter.LevelY[1] == 0 {
		return
	}
	if filterState != nil {
		d.applyLumaLoopFilter(pic, fhdr, filterState)
		d.applyChromaLoopFilter(pic, fhdr, filterState)
	} else {
		sharpness := int(fhdr.LoopFilter.Sharpness)
		levelYV := int(fhdr.LoopFilter.LevelY[0])
		levelYH := int(fhdr.LoopFilter.LevelY[1])
		w, h := pic.codedSize()
		deblockPlaneLevels(pic.Y, pic.StrideY, w, h, 4, levelYH, levelYV, sharpness)
		cw, ch := pic.codedChromaSize()
		deblockPlaneLevels(pic.U, pic.StrideUV, cw, ch, 4, int(fhdr.LoopFilter.LevelU), int(fhdr.LoopFilter.LevelU), sharpness)
		deblockPlaneLevels(pic.V, pic.StrideUV, cw, ch, 4, int(fhdr.LoopFilter.LevelV), int(fhdr.LoopFilter.LevelV), sharpness)
	}
}

func (d *decoderImpl) applyChromaLoopFilter(pic *Picture, fhdr *header.FrameHeader, fs *tile.FrameState) {
	lut := loopfilter.NewFilterLUT(int(fhdr.LoopFilter.Sharpness))
	w, h := pic.codedChromaSize()
	for planeNum, plane := range [][]byte{pic.U, pic.V} {
		if len(plane) == 0 {
			continue
		}
		planeID := planeNum + 1
		for x4 := 1; x4 < fs.CW4; x4++ {
			x := x4 * 4
			for y4 := 0; y4 < fs.CH4 && y4*4+4 <= h; y4++ {
				width, ok := fs.ChromaFilterEdge(x4, y4, true)
				if !ok {
					continue
				}
				width = safeLoopFilterWidth(width, x, w-x)
				if width == 0 {
					continue
				}
				level := fs.ChromaFilterLevel(fhdr, x4, y4, planeID)
				if level == 0 {
					level = fs.ChromaFilterLevel(fhdr, x4-1, y4, planeID)
				}
				d.traceLoopFilterEdge(planeID, "v", x4, y4, width, level)
				loopfilter.FilterEdgeV(plane, y4*4*pic.StrideUV+x, pic.StrideUV, level, width, &lut)
			}
		}
		for y4 := 1; y4 < fs.CH4; y4++ {
			y := y4 * 4
			for x4 := 0; x4 < fs.CW4 && x4*4+4 <= w; x4++ {
				width, ok := fs.ChromaFilterEdge(x4, y4, false)
				if !ok {
					continue
				}
				width = safeLoopFilterWidth(width, y, h-y)
				if width == 0 {
					continue
				}
				level := fs.ChromaFilterLevel(fhdr, x4, y4, planeID)
				if level == 0 {
					level = fs.ChromaFilterLevel(fhdr, x4, y4-1, planeID)
				}
				d.traceLoopFilterEdge(planeID, "h", x4, y4, width, level)
				loopfilter.FilterEdgeH(plane, y*pic.StrideUV+x4*4, pic.StrideUV, level, width, &lut)
			}
		}
	}
}

func (d *decoderImpl) applyLumaLoopFilter(pic *Picture, fhdr *header.FrameHeader, fs *tile.FrameState) {
	lut := loopfilter.NewFilterLUT(int(fhdr.LoopFilter.Sharpness))
	w, h := pic.codedSize()
	visibleW4 := (pic.Width + 3) >> 2
	visibleH4 := (pic.Height + 3) >> 2
	// AV1 applies vertical edges before horizontal edges.
	for x4 := 1; x4 < visibleW4; x4++ {
		x := x4 * 4
		for y4 := 0; y4 < visibleH4 && y4*4+4 <= h; y4++ {
			width, ok := fs.LumaFilterEdge(x4, y4, true)
			if !ok {
				continue
			}
			width = safeLoopFilterWidth(width, x, w-x)
			if width == 0 {
				continue
			}
			level := fs.LumaFilterLevel(fhdr, x4, y4, true)
			if level == 0 {
				level = fs.LumaFilterLevel(fhdr, x4-1, y4, true)
			}
			d.traceLoopFilterEdge(0, "v", x4, y4, width, level)
			loopfilter.FilterEdgeV(pic.Y, y4*4*pic.StrideY+x, pic.StrideY, level, width, &lut)
		}
	}
	for y4 := 1; y4 < visibleH4; y4++ {
		y := y4 * 4
		for x4 := 0; x4 < visibleW4 && x4*4+4 <= w; x4++ {
			width, ok := fs.LumaFilterEdge(x4, y4, false)
			if !ok {
				continue
			}
			width = safeLoopFilterWidth(width, y, h-y)
			if width == 0 {
				continue
			}
			level := fs.LumaFilterLevel(fhdr, x4, y4, false)
			if level == 0 {
				level = fs.LumaFilterLevel(fhdr, x4, y4-1, false)
			}
			d.traceLoopFilterEdge(0, "h", x4, y4, width, level)
			loopfilter.FilterEdgeH(pic.Y, y*pic.StrideY+x4*4, pic.StrideY, level, width, &lut)
		}
	}
}

func (d *decoderImpl) traceLoopFilterEdge(plane int, direction string, x4, y4, width, level int) {
	if os.Getenv("GOAV1_TRACE_LF") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "lf edge plane=%d dir=%s x4=%d y4=%d width=%d level=%d\n", plane, direction, x4, y4, width, level)
}

func safeLoopFilterWidth(width, before, after int) int {
	for _, candidate := range []int{16, 8, 6, 4} {
		if width >= candidate {
			radius := map[int]int{16: 7, 8: 4, 6: 3, 4: 2}[candidate]
			if before >= radius && after >= radius {
				return candidate
			}
		}
	}
	return 0
}

func loopFilterThresholds(level, sharpness int) (eThresh, iThresh int) {
	level = clampVal(level, 0, 63)
	return clampVal(2*(level+2)+sharpness, 0, 255), clampVal(level+2, 0, 63)
}

func deblockPlaneLevels(plane []byte, stride, w, h, step, horizontalLevel, verticalLevel, sharpness int) {
	hE, hI := loopFilterThresholds(horizontalLevel, sharpness)
	vE, vI := loopFilterThresholds(verticalLevel, sharpness)
	deblockPlaneDirections(plane, stride, w, h, step, hE, hI, vE, vI, horizontalLevel != 0, verticalLevel != 0)
}

// deblockPlane applies a simple 4-tap deblocking filter on all grid-aligned
// edges (step=4 pixels) of a single plane in both H and V directions.
func deblockPlane(plane []byte, stride, w, h, step, eThresh, iThresh int) {
	deblockPlaneDirections(plane, stride, w, h, step, eThresh, iThresh, eThresh, iThresh, true, true)
}

func deblockPlaneDirections(plane []byte, stride, w, h, step, hE, hI, vE, vI int, filterH, filterV bool) {
	if len(plane) == 0 {
		return
	}
	// Horizontal edges (filter vertically across them).
	for y := step; filterH && y < h; y += step {
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
			if absP1P0 > hI || absQ1Q0 > hI || absP0Q0*2+absP1Q1/2 > hE {
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
	for y := 0; filterV && y < h; y++ {
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
			if absP1P0 > vI || absQ1Q0 > vI || absP0Q0*2+absP1Q1/2 > vE {
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
	d.applyCDEFWithState(pic, fhdr, nil)
}

func (d *decoderImpl) applyCDEFWithState(pic *Picture, fhdr *header.FrameHeader, fs *tile.FrameState) {
	damping := int(fhdr.CDEF.Damping)
	w, h := pic.codedSize()
	dirW := w / 8
	dirH := h / 8
	dirs := make([]uint8, dirW*dirH)
	variances := make([]uint, dirW*dirH)

	applyCDEFPlane(pic.Y, pic.StrideY, w, h, 8, 0, damping, fs, fhdr, dirs, variances, dirW)
	if pic.Chroma != ChromaMonochrome && len(pic.U) > 0 {
		cw, ch := pic.codedChromaSize()
		applyCDEFPlane(pic.U, pic.StrideUV, cw, ch, 4, 1, damping-1, fs, fhdr, dirs, variances, dirW)
		applyCDEFPlane(pic.V, pic.StrideUV, cw, ch, 4, 2, damping-1, fs, fhdr, dirs, variances, dirW)
	}
}

// applyCDEFPlane applies CDEF block-by-block to one plane.
func applyCDEFPlane(plane []byte, stride, w, h, blockSz, planeID, damping int, fs *tile.FrameState, fhdr *header.FrameHeader, dirs []uint8, variances []uint, dirStride int) {
	if len(plane) == 0 {
		return
	}
	src := append([]byte(nil), plane...)
	work := append([]byte(nil), plane...)
	for by := 0; by < h; by += blockSz {
		for bx := 0; bx < w; bx += blockSz {
			hasNonSkip := cdefBlockHasNonSkip(fs, bx, by, blockSz, blockSz, planeID)
			if fs != nil && !hasNonSkip {
				continue
			}
			preset := 0
			if fs != nil && len(fs.CDEFIndex) != 0 {
				lx, ly := bx, by
				if planeID != 0 {
					lx <<= fs.SsHor
					ly <<= fs.SsVer
				}
				i := (ly/64)*fs.W64 + lx/64
				if i >= 0 && i < len(fs.CDEFIndex) && fs.CDEFIndex[i] >= 0 {
					preset = int(fs.CDEFIndex[i])
				}
			}
			strength := int(fhdr.CDEF.YStrength[preset])
			if planeID != 0 {
				strength = int(fhdr.CDEF.UVStrength[preset])
			}
			priStrength, secStrength := strength>>2, strength&3
			if secStrength == 3 {
				secStrength = 4
			}
			needsChromaDirection := planeID == 0 && int(fhdr.CDEF.UVStrength[preset])>>2 != 0
			if priStrength == 0 && secStrength == 0 && !needsChromaDirection {
				continue
			}
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
						left[row][0] = src[y*stride+(bx-2)]
						left[row][1] = src[y*stride+(bx-1)]
					}
				}
			}

			// Top row.
			var top []byte
			topBase := 0
			if by > 0 {
				top = src[(by-2)*stride:]
				topBase = bx
			} else {
				top = make([]byte, bw)
			}

			// Bottom row.
			var bottom []byte
			bottomBase := 0
			if by+bh < h {
				bottom = src[(by+bh)*stride:]
				bottomBase = bx
			} else {
				bottom = make([]byte, bw)
			}

			// Find direction.
			dirIdx := (by/blockSz)*dirStride + bx/blockSz
			dir := 0
			if planeID == 0 {
				rawPriStrength := priStrength
				uvPriStrength := int(fhdr.CDEF.UVStrength[preset]) >> 2
				var variance uint
				if rawPriStrength != 0 || uvPriStrength != 0 {
					dir, variance = cdef.FindDir(src, by*stride+bx, stride)
					if dirIdx >= 0 && dirIdx < len(dirs) {
						dirs[dirIdx] = uint8(dir)
						variances[dirIdx] = variance
					}
					if rawPriStrength != 0 {
						priStrength = adjustCDEFStrength(rawPriStrength, variance)
					} else {
						dir = 0
					}
				} else {
					dir = 0
				}
			} else {
				dir = chromaCDEFDirection(priStrength, dirIdx, dirs)
			}
			for y := 0; y < bh; y++ {
				copy(work[(by+y)*stride+bx:(by+y)*stride+bx+bw], src[(by+y)*stride+bx:(by+y)*stride+bx+bw])
			}

			cdef.FilterBlock(
				work, by*stride+bx, stride,
				left,
				top, topBase, stride,
				bottom, bottomBase, stride,
				priStrength, secStrength, dir, damping, bw, bh,
				edges,
			)
			for y := 0; y < bh; y++ {
				copy(plane[(by+y)*stride+bx:(by+y)*stride+bx+bw], work[(by+y)*stride+bx:(by+y)*stride+bx+bw])
			}
		}
	}
}

func chromaCDEFDirection(priStrength, dirIdx int, dirs []uint8) int {
	// dav1d passes direction zero for secondary-only chroma filtering.
	if priStrength == 0 || dirIdx < 0 || dirIdx >= len(dirs) {
		return 0
	}
	return int(dirs[dirIdx])
}

func cdefBlockHasNonSkip(fs *tile.FrameState, bx, by, bw, bh, planeID int) bool {
	if fs == nil {
		return true
	}
	if planeID != 0 {
		bx <<= fs.SsHor
		by <<= fs.SsVer
		bw <<= fs.SsHor
		bh <<= fs.SsVer
	}
	x0, y0 := bx/4, by/4
	x1, y1 := minInt((bx+bw+3)/4, fs.W4), minInt((by+bh+3)/4, fs.H4)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if !fs.BlockGrid[y*fs.W4+x].Skip {
				return true
			}
		}
	}
	return false
}

func adjustCDEFStrength(strength int, variance uint) int {
	if variance == 0 {
		return 0
	}
	i := 0
	if variance>>6 != 0 {
		i = minInt(bits.Len(variance>>6)-1, 12)
	}
	return (strength*(4+i) + 8) >> 4
}

func (d *decoderImpl) applyRestoration(pic *Picture, _ *header.FrameHeader, fs *tile.FrameState, boundary [3][]byte) {
	if pic == nil || fs == nil || len(fs.RestorationUnits) == 0 {
		return
	}
	planes := [3][]byte{pic.Y, pic.U, pic.V}
	strides := [3]int{pic.StrideY, pic.StrideUV, pic.StrideUV}
	chromaW, chromaH := pic.ChromaWidth(), pic.ChromaHeight()
	widths := [3]int{pic.Width, chromaW, chromaW}
	heights := [3]int{pic.Height, chromaH, chromaH}
	sources := [3][]byte{}
	for p, plane := range planes {
		sources[p] = append([]byte(nil), plane...)
	}
	for _, unit := range fs.RestorationUnits {
		plane := int(unit.Plane)
		if plane < 0 || plane >= len(planes) || unit.Type == header.RestorationNone {
			continue
		}
		ssV := 0
		if plane > 0 && pic.Chroma == Chroma420 {
			ssV = 1
		}
		applyRestorationUnit(planes[plane], sources[plane], boundary[plane], strides[plane], widths[plane], heights[plane], ssV, unit)
	}
}

func applyRestorationUnit(dst, src, boundary []byte, stride, planeW, planeH, ssV int, unit tile.RestorationUnit) {
	if unit.W <= 0 || unit.H <= 0 || unit.X < 0 || unit.Y < 0 ||
		unit.X+unit.W > planeW || unit.Y+unit.H > planeH {
		return
	}
	regularStripe := 64 >> ssV
	firstStripeEnd := 56 >> ssV
	for sy := unit.Y; sy < unit.Y+unit.H; {
		nextStripe := firstStripeEnd
		if sy >= firstStripeEnd {
			nextStripe += ((sy-firstStripeEnd)/regularStripe + 1) * regularStripe
		}
		h := min(nextStripe-sy, unit.Y+unit.H-sy)
		edges := looprestoration.LrEdgeFlags(0)
		if unit.X > 0 {
			edges |= looprestoration.LrHaveLeft
		}
		if unit.X+unit.W < planeW {
			edges |= looprestoration.LrHaveRight
		}
		if sy > 0 {
			edges |= looprestoration.LrHaveTop
		}
		if sy+h < planeH {
			edges |= looprestoration.LrHaveBottom
		}
		left := restorationLeft(src, stride, unit.X, sy, h)
		if len(boundary) == 0 {
			boundary = src
		}
		lpf, lpfBase, lpfStride := restorationLPF(boundary, stride, planeW, planeH, unit.X, sy, unit.W, h)
		base := sy*stride + unit.X
		switch unit.Type {
		case header.RestorationWiener:
			// Wiener is applied below from the immutable CDEF snapshot. Keep the
			// stripe loop here to share boundary construction with SGR.
		case header.RestorationSGRProj:
			params := restorationSGRParams(unit)
			s0, s1 := restorationSGRStrengths(unit.SGRIndex)
			switch {
			case s0 == 0:
				looprestoration.SGR3x3(dst, base, stride, left, lpf, lpfBase, lpfStride, unit.W, h, &params, edges)
			case s1 == 0:
				looprestoration.SGR5x5(dst, base, stride, left, lpf, lpfBase, lpfStride, unit.W, h, &params, edges)
			}
		}
		sy += h
	}
	if unit.Type == header.RestorationWiener {
		params := restorationWienerParams(unit)
		applyWienerSnapshot(dst, src, stride, planeW, planeH, unit, &params)
		regularStripe, firstStripeEnd := 64>>ssV, 56>>ssV
		unitEnd := unit.Y + unit.H
		for boundaryY := firstStripeEnd; boundaryY < planeH; boundaryY += regularStripe {
			if boundaryY > unit.Y && boundaryY <= unitEnd {
				aboveSrc := append([]byte(nil), src...)
				copyRestorationRows(aboveSrc, boundary, stride, boundaryY, 2, planeH)
				copyRestorationRow(aboveSrc, boundary, stride, boundaryY+2, boundaryY+1, planeH)
				aboveY := max(unit.Y, boundaryY-3)
				if aboveY < boundaryY {
					aboveUnit := unit
					aboveUnit.Y, aboveUnit.H = aboveY, boundaryY-aboveY
					applyWienerSnapshot(dst, aboveSrc, stride, planeW, planeH, aboveUnit, &params)
				}
			}
			if boundaryY >= unit.Y && boundaryY < unitEnd {
				belowSrc := append([]byte(nil), src...)
				copyRestorationRows(belowSrc, boundary, stride, boundaryY-2, 2, planeH)
				copyRestorationRow(belowSrc, boundary, stride, boundaryY-3, boundaryY-2, planeH)
				belowUnit := unit
				belowUnit.Y = boundaryY
				belowUnit.H = min(3, unitEnd-boundaryY)
				applyWienerSnapshot(dst, belowSrc, stride, planeW, planeH, belowUnit, &params)
			}
		}
	}
	if unit.Type == header.RestorationSGRProj {
		s0, _ := restorationSGRStrengths(unit.SGRIndex)
		if s0 == 0 {
			params := restorationSGRParams(unit)
			looprestoration.SGR3x3Snapshot(dst, src, stride, planeW, planeH,
				unit.X, unit.Y, unit.W, unit.H, &params)
			regularStripe, firstStripeEnd := 64>>ssV, 56>>ssV
			unitEnd := unit.Y + unit.H
			for boundaryY := firstStripeEnd; boundaryY < planeH; boundaryY += regularStripe {
				if boundaryY > unit.Y && boundaryY <= unitEnd {
					aboveSrc := append([]byte(nil), src...)
					copyRestorationRows(aboveSrc, boundary, stride, boundaryY, 2, planeH)
					aboveY := max(unit.Y, boundaryY-2)
					if aboveY < boundaryY {
						looprestoration.SGR3x3Snapshot(dst, aboveSrc, stride, planeW, planeH,
							unit.X, aboveY, unit.W, boundaryY-aboveY, &params)
					}
				}
				if boundaryY >= unit.Y && boundaryY < unitEnd {
					belowSrc := append([]byte(nil), src...)
					copyRestorationRows(belowSrc, boundary, stride, boundaryY-2, 2, planeH)
					belowH := min(2, unitEnd-boundaryY)
					looprestoration.SGR3x3Snapshot(dst, belowSrc, stride, planeW, planeH,
						unit.X, boundaryY, unit.W, belowH, &params)
				}
			}
		}
	}
}

func copyRestorationRows(dst, src []byte, stride, first, count, planeH int) {
	if len(src) == 0 {
		return
	}
	for y := max(0, first); y < min(planeH, first+count); y++ {
		copy(dst[y*stride:(y+1)*stride], src[y*stride:(y+1)*stride])
	}
}

func copyRestorationRow(dst, src []byte, stride, dstY, srcY, planeH int) {
	if len(src) == 0 || dstY < 0 || dstY >= planeH || srcY < 0 || srcY >= planeH {
		return
	}
	copy(dst[dstY*stride:(dstY+1)*stride], src[srcY*stride:(srcY+1)*stride])
}

func applyWienerSnapshot(dst, src []byte, stride, planeW, planeH int,
	unit tile.RestorationUnit, params *looprestoration.WienerParams,
) {
	// Horizontal intermediates include three source rows on either side so
	// the vertical seven-tap pass always reads the unmodified CDEF snapshot.
	hRows := unit.H + 6
	hor := make([]uint16, hRows*unit.W)
	fh := params.Filter[0]
	for hy := 0; hy < hRows; hy++ {
		sy := min(planeH-1, max(0, unit.Y+hy-3))
		for ux := 0; ux < unit.W; ux++ {
			sx := unit.X + ux
			sum := (1 << 14) + int(src[sy*stride+sx])*128
			for k := 0; k < 7; k++ {
				px := min(planeW-1, max(0, sx+k-3))
				sum += int(src[sy*stride+px]) * int(fh[k])
			}
			hor[hy*unit.W+ux] = uint16(min(8191, max(0, (sum+4)>>3)))
		}
	}
	fv := params.Filter[1]
	for uy := 0; uy < unit.H; uy++ {
		for ux := 0; ux < unit.W; ux++ {
			sum := -(1 << 18)
			for k := 0; k < 7; k++ {
				sum += int(hor[(uy+k)*unit.W+ux]) * int(fv[k])
			}
			v := min(255, max(0, (sum+1024)>>11))
			dst[(unit.Y+uy)*stride+unit.X+ux] = byte(v)
		}
	}
}

func restorationLeft(src []byte, stride, x, y, h int) [][4]byte {
	if x == 0 {
		return nil
	}
	left := make([][4]byte, h)
	for row := 0; row < h; row++ {
		for i := 0; i < 4; i++ {
			sx := max(0, x-4+i)
			left[row][i] = src[(y+row)*stride+sx]
		}
	}
	return left
}

func restorationLPF(src []byte, stride, planeW, planeH, x, y, w, h int) ([]byte, int, int) {
	// The restoration kernels address top rows at 0..3 and bottom rows at
	// 6..9, matching dav1d's loop-filter boundary buffer layout.
	const pad = 3
	lpfStride := w + 2*pad
	lpf := make([]byte, 10*lpfStride)
	for row := 0; row < 4; row++ {
		topY := max(0, y-2+row)
		bottomY := min(planeH-1, y+h+row)
		for px := -pad; px < w+pad; px++ {
			sx := min(planeW-1, max(0, x+px))
			lpf[row*lpfStride+px+pad] = src[topY*stride+sx]
			lpf[(6+row)*lpfStride+px+pad] = src[bottomY*stride+sx]
		}
	}
	return lpf, pad, lpfStride
}

func restorationWienerParams(unit tile.RestorationUnit) looprestoration.WienerParams {
	var params looprestoration.WienerParams
	for pass, taps := range [2][3]int8{unit.FilterH, unit.FilterV} {
		for i := 0; i < 3; i++ {
			params.Filter[pass][i] = int16(taps[i])
			params.Filter[pass][6-i] = int16(taps[i])
		}
		sum := int16(taps[0]+taps[1]+taps[2]) * 2
		params.Filter[pass][3] = -sum
		if pass == 1 {
			params.Filter[pass][3] += 128
		}
	}
	return params
}

func restorationSGRParams(unit tile.RestorationUnit) looprestoration.SGRParams {
	s0, s1 := restorationSGRStrengths(unit.SGRIndex)
	return looprestoration.SGRParams{
		S0: s0, S1: s1,
		W0: int(unit.SGRWeights[0]),
		W1: 128 - int(unit.SGRWeights[0]) - int(unit.SGRWeights[1]),
	}
}

func restorationSGRStrengths(idx uint8) (uint16, uint16) {
	table := [16][2]uint16{
		{140, 3236}, {112, 2158}, {93, 1618}, {80, 1438},
		{70, 1295}, {58, 1177}, {47, 1079}, {37, 996},
		{30, 925}, {25, 863}, {0, 2589}, {0, 1618},
		{0, 1177}, {0, 925}, {56, 0}, {22, 0},
	}
	return table[idx][0], table[idx][1]
}

func (d *decoderImpl) copyReferenceFallback(dst *Picture, fhdr *header.FrameHeader) bool {
	src := d.firstHeaderReference(fhdr)
	if src == nil {
		return false
	}
	copyPicturePlanes(dst, src)
	return true
}

func (d *decoderImpl) firstHeaderReference(fhdr *header.FrameHeader) *Picture {
	for _, idx := range fhdr.Refidx {
		if idx < 0 || int(idx) >= len(d.refs) {
			continue
		}
		if p := d.refs[idx].pic; p != nil {
			return p
		}
	}
	for i := range d.refs {
		if p := d.refs[i].pic; p != nil {
			return p
		}
	}
	return nil
}

func copyPicturePlanes(dst, src *Picture) {
	copyPlaneRows(dst.Y, dst.StrideY, src.Y, src.StrideY, minInt(dst.Width, src.Width), minInt(dst.Height, src.Height))
	cw := minInt(dst.ChromaWidth(), src.ChromaWidth())
	ch := minInt(dst.ChromaHeight(), src.ChromaHeight())
	copyPlaneRows(dst.U, dst.StrideUV, src.U, src.StrideUV, cw, ch)
	copyPlaneRows(dst.V, dst.StrideUV, src.V, src.StrideUV, cw, ch)
}

func copyPlaneRows(dst []byte, dstStride int, src []byte, srcStride int, w, h int) {
	if w <= 0 || h <= 0 || len(dst) == 0 || len(src) == 0 {
		return
	}
	for y := 0; y < h; y++ {
		doff := y * dstStride
		soff := y * srcStride
		if doff+w > len(dst) || soff+w > len(src) {
			break
		}
		copy(dst[doff:doff+w], src[soff:soff+w])
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
