// Package refmvs implements AV1 reference motion vector (MV) prediction.
//
// This file defines the core data types and MV utility functions needed for
// inter prediction.  The full temporal MV prediction pipeline (save_tmvs /
// load_tmvs) and the MV candidate stack search are scaffolded here; the
// complete implementation will land in a later milestone.
//
// Reference: dav1d/src/refmvs.{c,h}
package refmvs

// ─────────────────────────────────────────────────────────────────────────────
// Core types
// ─────────────────────────────────────────────────────────────────────────────

// MV is a motion vector stored in 1/8-pel units.
// Matches dav1d `mv` (int16 x, y).
type MV struct {
	Y int16 // vertical component (row direction)
	X int16 // horizontal component (col direction)
}

// InvalidMV is the sentinel value dav1d uses for "no MV" (INVALID_MV=0x80008000).
var InvalidMV = MV{Y: -32768, X: -32768}

// IsInvalid reports whether mv equals the invalid sentinel.
func (m MV) IsInvalid() bool { return m == InvalidMV }

// MVPair holds up to two motion vectors (single-ref or compound).
type MVPair [2]MV

// RefPair holds up to two reference frame indices.
//   - ref[0] == 0  → intra block
//   - ref[1] == -1 → single-reference inter
//   - both ≥ 0     → compound inter
type RefPair [2]int8

// IsIntra reports whether this is an intra block.
func (r RefPair) IsIntra() bool { return r[0] == 0 }

// IsCompound reports whether compound inter prediction is used.
func (r RefPair) IsCompound() bool { return r[0] > 0 && r[1] >= 0 }

// TemporalBlock is the per-4×4 entry stored for temporal MV projection.
// Matches dav1d refmvs_temporal_block (packed 5 bytes).
type TemporalBlock struct {
	MV  MV
	Ref uint8
}

// Block holds the MV pair, references, block size, and motion-field flags
// for a single coded block.  Written to the MV buffer after each block.
// Matches dav1d refmvs_block (12 bytes, packed).
type Block struct {
	MV  MVPair
	Ref RefPair
	BS  uint8 // BlockSize enum value
	MF  uint8 // motion-field flags: 1=global/affine, 2=newmv
}

// Candidate is one entry in the MV candidate stack returned by Find.
type Candidate struct {
	MV     MVPair
	Weight int
}

// ─────────────────────────────────────────────────────────────────────────────
// MV arithmetic
// ─────────────────────────────────────────────────────────────────────────────

// clampMVComponent clamps a single MV component to the frame-extended range.
// AV1 spec §7.9.3: MV range is [-MV_BORDER, MV_BORDER-1] in 1/8-pel units.
// For a frame of size (dim4 × 4) pixels the clamp range (with border) is:
//
//	[-(border + dim4*4)*8 , (border + dim4*4)*8 - 1]
//
// where border = 128*8 = 1024 (in 1/8-pel).
func clampMVComponent(v, border int) int {
	if v < -border {
		return -border
	}
	if v > border-1 {
		return border - 1
	}
	return v
}

// clampMVBorder is the standard AV1 MV border: 128 pixels × 8 = 1024 (1/8-pel).
const clampMVBorder = 128 * 8

// ClampMV clamps an MV so that it stays within the tile region extended by
// border (in 1/8-pel units).  bx4, by4 are the block position in 4-pel units;
// bw4, bh4 are the block size in 4-pel units; iw4, ih4 are the coded frame
// dimensions in 4-pel units.
//
// Mirrors the dav1d clamp_mv_row / clamp_mv logic.
func ClampMV(mv MV, bx4, by4, bw4, bh4, iw4, ih4 int) MV {
	// Horizontal: left border = -(bx4*4 + border)*8, right = ((iw4-bx4)*4 + border)*8
	hborder := clampMVBorder + bw4*16 // extra slack for the block width
	minX := -(bx4*32 + hborder)
	maxX := (iw4-bx4)*32 + hborder

	// Vertical
	vborder := clampMVBorder + bh4*16
	minY := -(by4*32 + vborder)
	maxY := (ih4-by4)*32 + vborder

	cx := int(mv.X)
	if cx < minX {
		cx = minX
	} else if cx > maxX-1 {
		cx = maxX - 1
	}

	cy := int(mv.Y)
	if cy < minY {
		cy = minY
	} else if cy > maxY-1 {
		cy = maxY - 1
	}

	return MV{Y: int16(cy), X: int16(cx)}
}

// ScaleMV scales a motion vector from one reference frame distance to another.
// td = dst_distance, tr = ref_distance (both in frame-order units).
// Matches dav1d scale_mv (inline in refmvs.c).
//
// Returns InvalidMV if tr is zero (avoid division by zero).
func ScaleMV(mv MV, td, tr int) MV {
	if tr == 0 {
		return InvalidMV
	}
	// Clamp ratio to [-4096, 4096] per AV1 spec.
	ratio := clamp(4096*td/tr, -4096, 4096)
	return MV{
		Y: int16(scaleMVComp(int(mv.Y), ratio)),
		X: int16(scaleMVComp(int(mv.X), ratio)),
	}
}

func scaleMVComp(v, ratio int) int {
	// round-towards-zero with sign-aware rounding.
	scaled := v * ratio
	if scaled >= 0 {
		return (scaled + 2048) >> 12
	}
	return -((-scaled + 2048) >> 12)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ─────────────────────────────────────────────────────────────────────────────
// MV merging (for the candidate stack)
// ─────────────────────────────────────────────────────────────────────────────

// MVEqual reports whether two MVs are equal.
func MVEqual(a, b MV) bool { return a == b }

// MVPairEqual reports whether two MV pairs are equal.
func MVPairEqual(a, b MVPair) bool { return a[0] == b[0] && a[1] == b[1] }

// AddCandidate adds cand to the stack if its MVPair is not already present.
// Returns the updated count.
func AddCandidate(stack []Candidate, cnt int, mv MVPair, weight int) int {
	if cnt >= len(stack) {
		return cnt
	}
	for i := 0; i < cnt; i++ {
		if MVPairEqual(stack[i].MV, mv) {
			stack[i].Weight += weight
			return cnt
		}
	}
	stack[cnt] = Candidate{MV: mv, Weight: weight}
	return cnt + 1
}

// SortCandidates performs a stable insertion sort of the MV candidate stack
// (descending by Weight), matching dav1d behaviour.
func SortCandidates(stack []Candidate, cnt int) {
	for i := 1; i < cnt; i++ {
		key := stack[i]
		j := i - 1
		for j >= 0 && stack[j].Weight < key.Weight {
			stack[j+1] = stack[j]
			j--
		}
		stack[j+1] = key
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Frame-level MV store (scaffold)
// ─────────────────────────────────────────────────────────────────────────────

// Frame holds the per-frame MV store and metadata needed for temporal MV
// prediction.  The buffer layout mirrors dav1d refmvs_frame.
type Frame struct {
	IW4, IH4 int // frame dimensions in 4-pel units
	IW8, IH8 int // frame dimensions in 8-pel units

	// Temporal MV projection data.
	RPStride int
	RP       []TemporalBlock // current frame's temporal blocks (row-major)

	// Per-block MV store (35 × RPStride entries for the current superblock row).
	R       []Block
	RStride int
}

// NewFrame allocates a Frame for a frame of iw×ih luma pixels.
func NewFrame(iw, ih int) *Frame {
	iw4 := (iw + 3) >> 2
	ih4 := (ih + 3) >> 2
	iw8 := (iw + 7) >> 3
	ih8 := (ih + 7) >> 3
	rStride := iw4 + 4 // 2-pixel border each side
	return &Frame{
		IW4:      iw4,
		IH4:      ih4,
		IW8:      iw8,
		IH8:      ih8,
		RPStride: iw8,
		RP:       make([]TemporalBlock, iw8*ih8),
		R:        make([]Block, 35*rStride),
		RStride:  rStride,
	}
}
