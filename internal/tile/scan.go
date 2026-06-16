// scan.go — dav1d-aligned coefficient scan / context lookup tables.
//
// Sources (referenced by file path so the port can be re-checked):
//   - dav1d/src/levels.h         : N_TX_SIZES, N_RECT_TX_SIZES, TxClass enum
//   - dav1d/src/tables.c         : dav1d_tx_type_class, dav1d_lo_ctx_offsets,
//     dav1d_skip_ctx, dav1d_txtp_from_uvmode
//   - dav1d/src/scan.c           : dav1d_scans[N_RECT_TX_SIZES][...]
//   - dav1d/src/recon_tmpl.c     : decode_coefs (uses all of the above)
//
// This file is part of M8 Task 1 (table-shape alignment). The scan tables
// themselves are generated lazily on first use (raster fallback) — Task 2
// will replace the lazy generator with the hard-coded tables from
// dav1d/src/scan.c when needed.
package tile

import "github.com/zesun96/go-av1/internal/transform"

// ---------------------------------------------------------------------------
// Enum: TxClass — decides how get_lo_ctx / scan / stride are computed.
// ---------------------------------------------------------------------------

// TxClass mirrors dav1d enum TxClass (recon_tmpl.c).
type TxClass uint8

const (
	TxClass2D TxClass = 0
	TxClassH  TxClass = 1
	TxClassV  TxClass = 2
)

// N_TX_SIZES is the number of square tx sizes (TX4..TX64) — 5.
// Mirrors dav1d/src/levels.h enum RectTxfmSize.
const N_TX_SIZES = 5

// N_RECT_TX_SIZES = 19 (5 square + 14 rectangular).
const N_RECT_TX_SIZES = 19

// N_TX_TYPES_PLUS_LL = 17 (16 base tx types + WHT_WHT).
const N_TX_TYPES_PLUS_LL = 17

// ---------------------------------------------------------------------------
// dav1d_tx_type_class — maps TxfmType (DCT_DCT..WHT_WHT) → TxClass.
// Source: dav1d/src/tables.c dav1d_tx_type_class[N_TX_TYPES_PLUS_LL].
//
// Indexed by transform.TxfmType constant (DCT_DCT=0 .. H_FLIPADST=15, WHT_WHT=16).
// ---------------------------------------------------------------------------

var DAV1DTxTypeClass = [N_TX_TYPES_PLUS_LL]TxClass{
	TxClass2D, // DCT_DCT
	TxClass2D, // ADST_DCT
	TxClass2D, // DCT_ADST
	TxClass2D, // ADST_ADST
	TxClass2D, // FLIPADST_DCT
	TxClass2D, // DCT_FLIPADST
	TxClass2D, // FLIPADST_FLIPADST
	TxClass2D, // ADST_FLIPADST
	TxClass2D, // FLIPADST_ADST
	TxClass2D, // IDTX
	TxClassV,  // V_DCT
	TxClassH,  // H_DCT
	TxClassV,  // V_ADST
	TxClassH,  // H_ADST
	TxClassV,  // V_FLIPADST
	TxClassH,  // H_FLIPADST
	TxClass2D, // WHT_WHT
}

// ---------------------------------------------------------------------------
// dav1d_lo_ctx_offsets[3][5][5] — base-token lo-context spatial offset map.
// Source: dav1d/src/tables.c dav1d_lo_ctx_offsets.
//
// Index 0: shape ∈ {w==h, w>h, w<h}.
// Index 1, 2: spatial position (y, x) within a 5×5 neighbourhood window.
// Returns the lo-context offset in [0, 41) used to index base_tok / br_tok.
// ---------------------------------------------------------------------------

var DAV1DLoCtxOffsets = [3][5][5]uint8{
	{ // w == h
		{0, 1, 6, 6, 21},
		{1, 6, 6, 21, 21},
		{6, 6, 21, 21, 21},
		{6, 21, 21, 21, 21},
		{21, 21, 21, 21, 21},
	},
	{ // w > h
		{0, 16, 6, 6, 21},
		{16, 16, 6, 21, 21},
		{16, 16, 21, 21, 21},
		{16, 16, 21, 21, 21},
		{16, 16, 21, 21, 21},
	},
	{ // w < h
		{0, 11, 11, 11, 11},
		{11, 11, 11, 11, 11},
		{6, 6, 21, 21, 21},
		{6, 21, 21, 21, 21},
		{21, 21, 21, 21, 21},
	},
}

// ---------------------------------------------------------------------------
// dav1d_skip_ctx[5][5] — coef.skip context derivation from above/left
// signed-token magnitudes (clamped to [0,4]).
// Source: dav1d/src/tables.c dav1d_skip_ctx.
// ---------------------------------------------------------------------------

var DAV1DSkipCtx = [5][5]uint8{
	{1, 2, 2, 2, 3},
	{2, 4, 4, 4, 5},
	{2, 4, 4, 4, 5},
	{2, 4, 4, 4, 5},
	{3, 5, 5, 5, 6},
}

// ---------------------------------------------------------------------------
// scan tables — populated lazily.
//
// dav1d/src/scan.c provides hard-coded zig-zag / row-major / column-major
// orders for every (lw, lh) combination. Hard-coding all 19 tables is
// ~6000 uint16 entries; Task 1 keeps things compilable by generating
// raster scans on demand. Task 2 will swap in real dav1d tables when
// pattern-mismatch becomes the dominant error term.
// ---------------------------------------------------------------------------

// scanCache is keyed by (txClass<<8)|(lw<<4)|lh.
var scanCache = map[uint16][]uint16{}
var lastNonzeroColCache = map[uint8][]uint8{}

// GetScan returns a coefficient scan order for a transform of log2-size
// (lw, lh) and tx_class. The returned slice has length (4<<lw)*(4<<lh).
// For TX_CLASS_2D each entry is dav1d's packed rc value: (x << shift) | y,
// where shift=lh+2. For 1D transforms the caller does not use this table.
//
// Small and medium transforms use verbatim dav1d scan tables. dav1d also
// aliases TX64x64/RTX32x64/RTX64x32 to scan_32x32 and RTX16x64/RTX64x16 to
// scan_16x32/scan_32x16 respectively; keep those aliases here so Largest-tx
// streams do not fall back to raster order.
func GetScan(lw, lh uint8, cls TxClass) []uint16 {
	key := (uint16(cls) << 8) | (uint16(lw) << 4) | uint16(lh)
	if s, ok := scanCache[key]; ok {
		return s
	}

	if cls == TxClass2D {
		if tx, ok := rectTxSizeForLogs(lw, lh); ok {
			if exact := Scans[tx]; len(exact) != 0 {
				scanCache[key] = exact
				return exact
			}
		}
	}

	w := 4 << lw
	h := 4 << lh
	out := make([]uint16, w*h)
	n := 0
	if cls == TxClass2D {
		shift := lh + 2
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				out[n] = uint16((x << shift) | y)
				n++
			}
		}
	} else {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				out[n] = uint16(y*w + x)
				n++
			}
		}
	}
	scanCache[key] = out
	return out
}

// LastNonzeroColFromEOB returns dav1d's last_nonzero_col_from_eob value for
// exact 2D-scan-backed transforms. The bool is false when no exact table is
// available for the given transform size.
func LastNonzeroColFromEOB(tx uint8, eob int) (int, bool) {
	if eob < 0 {
		return 0, false
	}

	if cols, ok := lastNonzeroColCache[tx]; ok {
		if eob >= len(cols) {
			return len(cols) - 1, true
		}
		return int(cols[eob]), true
	}

	if int(tx) >= len(Scans) {
		return 0, false
	}
	scan := Scans[tx]
	if len(scan) == 0 {
		return 0, false
	}

	td := transform.TxfmDimensions[tx]
	sh := int(td.H) * 4
	if sh > 32 {
		sh = 32
	}
	mask := sh - 1
	cols := make([]uint8, len(scan))
	maxCol := 0
	for i, rc := range scan {
		col := int(rc) & mask
		if col > maxCol {
			maxCol = col
		}
		cols[i] = uint8(maxCol)
	}
	lastNonzeroColCache[tx] = cols
	if eob >= len(cols) {
		return len(cols) - 1, true
	}
	return int(cols[eob]), true
}

func rectTxSizeForLogs(lw, lh uint8) (uint8, bool) {
	switch {
	case lw == 0 && lh == 0:
		return transform.TX4x4, true
	case lw == 1 && lh == 1:
		return transform.TX8x8, true
	case lw == 2 && lh == 2:
		return transform.TX16x16, true
	case lw == 3 && lh == 3:
		return transform.TX32x32, true
	case lw == 4 && lh == 4:
		return transform.TX64x64, true
	case lw == 0 && lh == 1:
		return transform.RTX4x8, true
	case lw == 1 && lh == 0:
		return transform.RTX8x4, true
	case lw == 1 && lh == 2:
		return transform.RTX8x16, true
	case lw == 2 && lh == 1:
		return transform.RTX16x8, true
	case lw == 2 && lh == 3:
		return transform.RTX16x32, true
	case lw == 3 && lh == 2:
		return transform.RTX32x16, true
	case lw == 3 && lh == 4:
		return transform.RTX32x64, true
	case lw == 4 && lh == 3:
		return transform.RTX64x32, true
	case lw == 0 && lh == 2:
		return transform.RTX4x16, true
	case lw == 2 && lh == 0:
		return transform.RTX16x4, true
	case lw == 1 && lh == 3:
		return transform.RTX8x32, true
	case lw == 3 && lh == 1:
		return transform.RTX32x8, true
	case lw == 2 && lh == 4:
		return transform.RTX16x64, true
	case lw == 4 && lh == 2:
		return transform.RTX64x16, true
	default:
		return 0, false
	}
}

// ---------------------------------------------------------------------------
// is1D helper — TxClass != TxClass2D.
// ---------------------------------------------------------------------------

// IsTx1D returns true when the tx_class is row-1D (H) or col-1D (V).
func IsTx1D(cls TxClass) bool { return cls != TxClass2D }
