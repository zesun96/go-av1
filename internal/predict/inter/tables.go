package inter

// Filter2D identifies the 2-D subpel interpolation filter combination used
// during inter prediction (horizontal × vertical filter pair).
// Values match dav1d enum Filter2d (src/levels.h).
type Filter2D uint8

const (
	Filter2D8TapRegular       Filter2D = iota // 0
	Filter2D8TapRegularSmooth                 // 1
	Filter2D8TapRegularSharp                  // 2
	Filter2D8TapSharpRegular                  // 3
	Filter2D8TapSharpSmooth                   // 4
	Filter2D8TapSharp                         // 5
	Filter2D8TapSmoothRegular                 // 6
	Filter2D8TapSmooth                        // 7
	Filter2D8TapSmoothSharp                   // 8
	Filter2DBilinear                          // 9
	NFilter2D                                 // 10
)

// FilterType identifies an axis-independent filter type used to index into
// McSubpelFilters.  The three primary types are Regular (0), Smooth (1),
// Sharp (2); indices 3–4 are the ≤4-wide variants.
type FilterType uint8

const (
	FilterRegular        FilterType = iota // 0
	FilterSmooth                           // 1
	FilterSharp                            // 2
	FilterRegularSmall                     // 3  (width ≤ 4, regular)
	FilterSmoothSmall                      // 4  (width ≤ 4, smooth)
	FilterBilinearScaled                   // 5  (bilinear for scaled MC)
	NFilterTypes                           // 6
)

// McSubpelFilters is the 8-tap luma subpel filter table.
// Indexed as McSubpelFilters[FilterType][phase 1..15][tap 0..7].
// 1:1 port of dav1d_mc_subpel_filters (src/tables.c).
//
// Each row corresponds to a 1/16-pel phase (phase 0 means integer position,
// so there are 15 non-zero fractional phases).
var McSubpelFilters = [NFilterTypes][15][8]int8{
	// ── Regular (width > 4) ─────────────────────────────────────────────────
	FilterRegular: {
		{0, 1, -3, 63, 4, -1, 0, 0},
		{0, 1, -5, 61, 9, -2, 0, 0},
		{0, 1, -6, 58, 14, -4, 1, 0},
		{0, 1, -7, 55, 19, -5, 1, 0},
		{0, 1, -7, 51, 24, -6, 1, 0},
		{0, 1, -8, 47, 29, -6, 1, 0},
		{0, 1, -7, 42, 33, -6, 1, 0},
		{0, 1, -7, 38, 38, -7, 1, 0},
		{0, 1, -6, 33, 42, -7, 1, 0},
		{0, 1, -6, 29, 47, -8, 1, 0},
		{0, 1, -6, 24, 51, -7, 1, 0},
		{0, 1, -5, 19, 55, -7, 1, 0},
		{0, 1, -4, 14, 58, -6, 1, 0},
		{0, 0, -2, 9, 61, -5, 1, 0},
		{0, 0, -1, 4, 63, -3, 1, 0},
	},
	// ── Smooth (width > 4) ──────────────────────────────────────────────────
	FilterSmooth: {
		{0, 1, 14, 31, 17, 1, 0, 0},
		{0, 0, 13, 31, 18, 2, 0, 0},
		{0, 0, 11, 31, 20, 2, 0, 0},
		{0, 0, 10, 30, 21, 3, 0, 0},
		{0, 0, 9, 29, 22, 4, 0, 0},
		{0, 0, 8, 28, 23, 5, 0, 0},
		{0, -1, 8, 27, 24, 6, 0, 0},
		{0, -1, 7, 26, 26, 7, -1, 0},
		{0, 0, 6, 24, 27, 8, -1, 0},
		{0, 0, 5, 23, 28, 8, 0, 0},
		{0, 0, 4, 22, 29, 9, 0, 0},
		{0, 0, 3, 21, 30, 10, 0, 0},
		{0, 0, 2, 20, 31, 11, 0, 0},
		{0, 0, 2, 18, 31, 13, 0, 0},
		{0, 0, 1, 17, 31, 14, 1, 0},
	},
	// ── Sharp (width > 4) ───────────────────────────────────────────────────
	FilterSharp: {
		{-1, 1, -3, 63, 4, -1, 1, 0},
		{-1, 3, -6, 62, 8, -3, 2, -1},
		{-1, 4, -9, 60, 13, -5, 3, -1},
		{-2, 5, -11, 58, 19, -7, 3, -1},
		{-2, 5, -11, 54, 24, -9, 4, -1},
		{-2, 5, -12, 50, 30, -10, 4, -1},
		{-2, 5, -12, 45, 35, -11, 5, -1},
		{-2, 6, -12, 40, 40, -12, 6, -2},
		{-1, 5, -11, 35, 45, -12, 5, -2},
		{-1, 4, -10, 30, 50, -12, 5, -2},
		{-1, 4, -9, 24, 54, -11, 5, -2},
		{-1, 3, -7, 19, 58, -11, 5, -2},
		{-1, 3, -5, 13, 60, -9, 4, -1},
		{-1, 2, -3, 8, 62, -6, 3, -1},
		{0, 1, -1, 4, 63, -3, 1, -1},
	},
	// ── Regular small (width ≤ 4) ────────────────────────────────────────────
	FilterRegularSmall: {
		{0, 0, -2, 63, 4, -1, 0, 0},
		{0, 0, -4, 61, 9, -2, 0, 0},
		{0, 0, -5, 58, 14, -3, 0, 0},
		{0, 0, -6, 55, 19, -4, 0, 0},
		{0, 0, -6, 51, 24, -5, 0, 0},
		{0, 0, -7, 47, 29, -5, 0, 0},
		{0, 0, -6, 42, 33, -5, 0, 0},
		{0, 0, -6, 38, 38, -6, 0, 0},
		{0, 0, -5, 33, 42, -6, 0, 0},
		{0, 0, -5, 29, 47, -7, 0, 0},
		{0, 0, -5, 24, 51, -6, 0, 0},
		{0, 0, -4, 19, 55, -6, 0, 0},
		{0, 0, -3, 14, 58, -5, 0, 0},
		{0, 0, -2, 9, 61, -4, 0, 0},
		{0, 0, -1, 4, 63, -2, 0, 0},
	},
	// ── Smooth small (width ≤ 4) ─────────────────────────────────────────────
	FilterSmoothSmall: {
		{0, 0, 15, 31, 17, 1, 0, 0},
		{0, 0, 13, 31, 18, 2, 0, 0},
		{0, 0, 11, 31, 20, 2, 0, 0},
		{0, 0, 10, 30, 21, 3, 0, 0},
		{0, 0, 9, 29, 22, 4, 0, 0},
		{0, 0, 8, 28, 23, 5, 0, 0},
		{0, 0, 7, 27, 24, 6, 0, 0},
		{0, 0, 6, 26, 26, 6, 0, 0},
		{0, 0, 6, 24, 27, 7, 0, 0},
		{0, 0, 5, 23, 28, 8, 0, 0},
		{0, 0, 4, 22, 29, 9, 0, 0},
		{0, 0, 3, 21, 30, 10, 0, 0},
		{0, 0, 2, 20, 31, 11, 0, 0},
		{0, 0, 2, 18, 31, 13, 0, 0},
		{0, 0, 1, 17, 31, 15, 0, 0},
	},
	// ── Bilinear-scaled (rarely used; acts as a 2-tap bilinear) ─────────────
	FilterBilinearScaled: {
		{0, 0, 0, 60, 4, 0, 0, 0},
		{0, 0, 0, 56, 8, 0, 0, 0},
		{0, 0, 0, 52, 12, 0, 0, 0},
		{0, 0, 0, 48, 16, 0, 0, 0},
		{0, 0, 0, 44, 20, 0, 0, 0},
		{0, 0, 0, 40, 24, 0, 0, 0},
		{0, 0, 0, 36, 28, 0, 0, 0},
		{0, 0, 0, 32, 32, 0, 0, 0},
		{0, 0, 0, 28, 36, 0, 0, 0},
		{0, 0, 0, 24, 40, 0, 0, 0},
		{0, 0, 0, 20, 44, 0, 0, 0},
		{0, 0, 0, 16, 48, 0, 0, 0},
		{0, 0, 0, 12, 52, 0, 0, 0},
		{0, 0, 0, 8, 56, 0, 0, 0},
		{0, 0, 0, 4, 60, 0, 0, 0},
	},
}

// filter2DToH maps a Filter2D to the horizontal FilterType index.
// For the H axis: bits 1:0 of the filter_type field in dav1d.
var filter2DToH = [NFilter2D]FilterType{
	Filter2D8TapRegular:       FilterRegular,
	Filter2D8TapRegularSmooth: FilterRegular,
	Filter2D8TapRegularSharp:  FilterRegular,
	Filter2D8TapSharpRegular:  FilterSharp,
	Filter2D8TapSharpSmooth:   FilterSharp,
	Filter2D8TapSharp:         FilterSharp,
	Filter2D8TapSmoothRegular: FilterSmooth,
	Filter2D8TapSmooth:        FilterSmooth,
	Filter2D8TapSmoothSharp:   FilterSmooth,
	Filter2DBilinear:          NFilterTypes, // special
}

// filter2DToV maps a Filter2D to the vertical FilterType index.
var filter2DToV = [NFilter2D]FilterType{
	Filter2D8TapRegular:       FilterRegular,
	Filter2D8TapRegularSmooth: FilterSmooth,
	Filter2D8TapRegularSharp:  FilterSharp,
	Filter2D8TapSharpRegular:  FilterRegular,
	Filter2D8TapSharpSmooth:   FilterSmooth,
	Filter2D8TapSharp:         FilterSharp,
	Filter2D8TapSmoothRegular: FilterRegular,
	Filter2D8TapSmooth:        FilterSmooth,
	Filter2D8TapSmoothSharp:   FilterSharp,
	Filter2DBilinear:          NFilterTypes, // special
}

// GetFilters returns the horizontal and vertical 8-tap filter arrays for
// the given Filter2D, block width w, and sub-pixel offsets mx, my.
// Returns nil for an axis if the offset is zero (integer-pixel position).
// Width/height ≤ 4 select the small horizontal/vertical filter variant.
func GetFilters(f Filter2D, w, h, mx, my int) (fh, fv []int8) {
	if f == Filter2DBilinear {
		// Bilinear: handled separately by the caller.
		return nil, nil
	}

	hType := filter2DToH[f]
	vType := filter2DToV[f]

	// Width ≤ 4 uses the small-filter variant on the horizontal axis.
	if w <= 4 {
		switch hType {
		case FilterRegular:
			hType = FilterRegularSmall
		case FilterSmooth:
			hType = FilterSmoothSmall
		}
	}
	if h <= 4 {
		switch vType {
		case FilterRegular:
			vType = FilterRegularSmall
		case FilterSmooth:
			vType = FilterSmoothSmall
		}
	}

	if mx != 0 {
		row := McSubpelFilters[hType][mx-1]
		fh = row[:]
	}
	if my != 0 {
		row := McSubpelFilters[vType][my-1]
		fv = row[:]
	}
	return
}
