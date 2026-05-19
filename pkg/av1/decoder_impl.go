// decoder_impl.go contains the concrete Decoder implementation for the pkg/av1
// package. It routes OBUs, manages the reference frame buffer, and applies
// post-processing filters.
//
// Milestone: M6 (pipeline skeleton).
// Tile group CABAC decode is a stub; M7 will fill it in.
package av1

import (
	"sync"

	"github.com/zesun96/go-av1/internal/header"
	"github.com/zesun96/go-av1/internal/obu"
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

	// seq is the most-recently parsed SequenceHeader.
	seq *header.SequenceHeader

	// refs is the 8-slot decoded-frame reference buffer.
	refs [header.NumRefFrames]refEntry

	// outQ holds fully decoded pictures waiting to be consumed by GetPicture.
	outQ []*Picture

	closed bool
}

// newDecoderImpl constructs a decoderImpl and returns it as a Decoder.
func newDecoderImpl(opts DecoderOptions) (Decoder, error) {
	return &decoderImpl{opts: opts}, nil
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

	for i := range d.refs {
		if d.refs[i].pic != nil {
			d.refs[i].pic.Release()
			d.refs[i].pic = nil
		}
	}
	return nil
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
			return nil // no seq header yet, skip
		}
		return d.decodeFrame(o.Payload)

	case header.OBUFrame:
		// OBU_FRAME carries both frame header and tile data.
		// M6: parse frame header only; tile payload is ignored (stub).
		if d.seq == nil {
			return nil
		}
		return d.decodeFrame(o.Payload)

	case header.OBUTileGroup:
		// Handled in M7 (CABAC tile decode).
		return nil

	case header.OBUTemporalDelimiter:
		return nil

	default:
		return nil
	}
	return nil
}

// ─── frame decode ─────────────────────────────────────────────────────────────

// decodeFrame parses the frame header, allocates a Picture, applies post
// filters, updates the reference buffer, and enqueues showable frames.
// Must be called with d.mu held.
func (d *decoderImpl) decodeFrame(payload []byte) error {
	refs := d.obuRefs()

	var fhdr header.FrameHeader
	if err := obu.ParseFrameHeader(payload, &fhdr, obu.FrameParseOptions{
		SeqHeader: d.seq,
		Refs:      refs,
	}); err != nil {
		// Best-effort: non-fatal in M6.
		return nil
	}

	if fhdr.ShowExistingFrame != 0 {
		idx := fhdr.ExistingFrameIdx
		if int(idx) < len(d.refs) && d.refs[idx].pic != nil {
			d.outQ = append(d.outQ, d.refs[idx].pic.Retain())
		}
		return nil
	}

	// Allocate output picture (8-bit, 4:2:0, stride aligned to 16).
	pic := d.allocPicture(&fhdr)

	// Tile group decode stub: picture is zero-filled (black frame).
	// TODO M7: MSAC + block reconstruction.

	// Post-processing filters (all no-ops until M8 wires up parameters).
	d.postFilter(pic, &fhdr)

	// Update reference buffer.
	d.updateRefs(pic, &fhdr)

	// Enqueue if displayable.
	if fhdr.ShowFrame != 0 || fhdr.ShowableFrame != 0 {
		d.outQ = append(d.outQ, pic.Retain())
	}

	// Release the local reference; updateRefs retains for each refreshed slot.
	pic.Release()
	return nil
}

// obuRefs builds the FrameReference array expected by ParseFrameHeader.
func (d *decoderImpl) obuRefs() *[header.NumRefFrames]obu.FrameReference {
	var refs [header.NumRefFrames]obu.FrameReference
	for i, e := range d.refs {
		refs[i].FrameHdr = e.fhdr
	}
	return &refs
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
// Each stage is currently a no-op (M6 stub); M8 will wire up real parameters.
func (d *decoderImpl) postFilter(pic *Picture, fhdr *header.FrameHeader) {
	if d.opts.InloopFilters&InloopFilterDeblock != 0 {
		d.applyLoopFilter(pic, fhdr)
	}
	if d.opts.InloopFilters&InloopFilterCDEF != 0 {
		d.applyCDEF(pic, fhdr)
	}
	if d.opts.InloopFilters&InloopFilterRestoration != 0 {
		d.applyRestoration(pic, fhdr)
	}
}

// applyLoopFilter is a stub.
// TODO M8: call loopfilter.LoopFilterH/V per superblock.
func (d *decoderImpl) applyLoopFilter(_ *Picture, _ *header.FrameHeader) {}

// applyCDEF is a stub.
// TODO M8: call cdef.FilterBlock per 64×64 CDEF unit.
func (d *decoderImpl) applyCDEF(_ *Picture, _ *header.FrameHeader) {}

// applyRestoration is a stub.
// TODO M8: call looprestoration.WienerFilter / SGR per restoration unit.
func (d *decoderImpl) applyRestoration(_ *Picture, _ *header.FrameHeader) {}
