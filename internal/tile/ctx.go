package tile

import "reflect"

// TileCtx holds all adaptive CDF state for one tile.
// It is initialised from the multi-context default tables in cdfs_full.go
// and discarded after the tile is decoded (cross-tile CDF propagation is
// disabled in the current implementation).
//
// Context index conventions (mirroring dav1d):
//   - Partition:  ctx = hasAbove | (hasLeft<<1)  (0-3)
//   - Skip:       ctx = aboveSkip + leftSkip       (0-2)
//   - KFY mode:   ctx = [topMode][leftMode]         (0-4 each)
//   - UV mode:    ctx = [cfl_allowed][y_mode]
//   - TX type:    ctx = [tx_class][y_mode]
//   - EOB bin:    ctx = [plane(0=luma,1=chroma)][ctx2]
//   - DC sign:    ctx = [plane][sign_ctx]
type TileCtx struct {
	CoefQCat int

	// -----------------------------------------------------------------------
	// Partition CDFs — 4 contexts, different sizes per block level.
	//   [128x128]: 8  symbols + counter = [4][9]
	//   [64/32/16]: 10 symbols + counter = [4][11]
	//   [8x8]:      4  symbols + counter = [4][5]
	// -----------------------------------------------------------------------
	Partition128CDF [4][9]uint16
	Partition64CDF  [4][11]uint16
	Partition32CDF  [4][11]uint16
	Partition16CDF  [4][11]uint16
	Partition8CDF   [4][5]uint16

	// -----------------------------------------------------------------------
	// Intra prediction mode CDFs.
	// KFY: key-frame luma [top_mode][left_mode][13+counter]
	// Y:   inter/intra-only luma [size_ctx][13+counter]
	// UV:  [cfl_allowed][y_mode][NUVIntraModes+counter]
	// -----------------------------------------------------------------------
	KFYModeCDF [NKFYMCtx][NKFYMCtx][NIntraPredModes + 1]uint16
	YModeCDF   [4][NIntraPredModes + 1]uint16
	UVModeCDF  [2][NIntraPredModes][NUVIntraModes + 1]uint16

	// -----------------------------------------------------------------------
	// Skip CDF: 3 contexts × {2 symbols + counter}.
	// -----------------------------------------------------------------------
	SkipCDF [3][3]uint16

	// Intra decision in inter/switch frames and intrabc flag.
	IntraCDF         [4][2]uint16
	IntrabcCDF       [2]uint16
	CompCDF          [5][2]uint16
	CompDirCDF       [5][2]uint16
	CompFwdRefCDF    [3][3][2]uint16
	CompBwdRefCDF    [2][3][2]uint16
	CompUniRefCDF    [3][3][2]uint16
	CompInterModeCDF [8][9]uint16

	// -----------------------------------------------------------------------
	// Transform type CDFs.
	// Intra1 (6 symbols): [2 tx_class][13 y_mode][7]
	// Intra2 (4 symbols): [3 tx_class][13 y_mode][5]
	// Inter1 (15 coded branches -> 16 symbols): [2 tx_class][17]
	// Inter2 (11 coded branches -> 12 symbols): [13]
	// Inter3 (bool): [4 tx_class][2]
	// -----------------------------------------------------------------------
	TxTypeIntra1CDF [2][NIntraPredModes][TxTypeIntra1Symbols + 1]uint16
	TxTypeIntra2CDF [3][NIntraPredModes][TxTypeIntra2Symbols + 1]uint16
	TxTypeInter1CDF [2][TxTypeInter1Symbols + 1]uint16
	TxTypeInter2CDF [TxTypeInter2Symbols + 1]uint16
	TxTypeInter3CDF [4][2]uint16

	// -----------------------------------------------------------------------
	// EOB point CDFs — [2 plane][2 ctx][symbols+counter]
	// Plane 0=luma, 1=chroma. ctx ∈ {0,1}.
	// -----------------------------------------------------------------------
	EobBin16CDF   [2][2][5]uint16
	EobBin32CDF   [2][2][6]uint16
	EobBin64CDF   [2][2][7]uint16
	EobBin128CDF  [2][2][8]uint16
	EobBin256CDF  [2][2][9]uint16
	EobBin512CDF  [2][10]uint16
	EobBin1024CDF [2][11]uint16

	// -----------------------------------------------------------------------
	// Coefficient token CDFs (simplified single-context approximation).
	// Retained for the M7 decode path; M8 Task 2 will switch decodeCoefficients
	// to the *Full fields below which match dav1d storage exactly.
	// -----------------------------------------------------------------------
	CoeffBaseTokCDF [4][5]uint16 // [ctx][4 syms+counter]
	CoeffBaseEobCDF [4][4]uint16 // [ctx][3 syms+counter]
	CoeffBrTokCDF   [4][5]uint16 // [ctx][4 syms+counter]

	// DC sign CDF [2 plane][3 ctx][2 syms+counter].
	DCSignCDF [2][3][2]uint16

	// -----------------------------------------------------------------------
	// dav1d-shape coefficient CDFs (M8 Task 1: shape only, data filled in T2).
	//
	// Source: dav1d/src/cdf.h struct CdfCoefContext.
	//   eob_bin_*[2 plane][2 is_1d][bin_count + padding]
	//   eob_base_tok[N_TX_SIZES][2 plane][4 ctx][4]
	//   base_tok[N_TX_SIZES][2 plane][41 ctx][4]
	//   br_tok[4 tx_ctx][2 plane][21 ctx][4]
	//   eob_hi_bit[N_TX_SIZES][2 plane][9 bin][2]
	//   coef_skip[N_TX_SIZES][13 skip_ctx][2]    // res_ctx==0 a.k.a. all-skip
	//   dc_sign[2 plane][3 ctx][2]               // alias of DCSignCDF above
	// -----------------------------------------------------------------------
	EobBin16Full   [2][2][8]uint16  // 4 syms + 4 padding (dav1d ALIGN(16))
	EobBin32Full   [2][2][8]uint16  // 5 syms + 3
	EobBin64Full   [2][2][8]uint16  // 6 syms + 2
	EobBin128Full  [2][2][8]uint16  // 7 syms + 1
	EobBin256Full  [2][2][16]uint16 // 8 syms + 8
	EobBin512Full  [2][16]uint16    // 9 syms + 7
	EobBin1024Full [2][16]uint16    // 10 syms + 6
	// Note: dav1d stores these as [4] (3 explicit + sentinel/counter combined).
	// Our SymbolAdapt requires cdf[n-1]=0 sentinel AND cdf[n]=counter, so the
	// last dim is one larger than dav1d's storage. The shape is otherwise
	// identical; data still ports cleanly from dav1d/src/cdf.c.
	EobBaseTokFull [N_TX_SIZES][2][4][4]uint16  // 2 sym + sent + counter
	BaseTokFull    [N_TX_SIZES][2][41][5]uint16 // 3 sym + sent + counter
	BrTokFull      [4][2][21][5]uint16          // 3 sym + sent + counter
	EobHiBitFull   [N_TX_SIZES][2][9][2]uint16  // bool: 1 prob + counter
	CoefSkipFull   [N_TX_SIZES][13][2]uint16    // bool

	// -----------------------------------------------------------------------
	// CFL CDFs.
	// -----------------------------------------------------------------------
	CFLSignCDF  [8]uint16
	CFLAlphaCDF [6][16]uint16

	// -----------------------------------------------------------------------
	// Segmentation CDFs.
	// -----------------------------------------------------------------------
	SegPredCDF [3][2]uint16
	SegIDCDF   [3][8]uint16

	// Delta-q / delta-lf CDFs and tile-local state.
	DeltaQCDF     [5]uint16
	DeltaLFCDF    [5][5]uint16
	LastQIdx      int
	LastQIdxValid bool
	LastDeltaLF   [4]int8

	// -----------------------------------------------------------------------
	// Filter-intra CDFs (A1).
	//   UseFilterIntraCDF[bs][2]    : binary, read after y_mode==DC_PRED.
	//   FilterIntraModeCDF[6]       : 5 modes + counter.
	// -----------------------------------------------------------------------
	UseFilterIntraCDF  [NBlockSizes][2]uint16
	FilterIntraModeCDF [6]uint16

	// -----------------------------------------------------------------------
	// Palette CDFs (A2).
	//   PaletteYCDF[sz_ctx=7][pal_ctx=3][2] : binary use_palette_y.
	//   PaletteUVCDF[pal_ctx=2][2]          : binary use_palette_uv.
	// Read only when frame_hdr.allow_screen_content_tools && y/uv_mode==DC_PRED
	// && imax(bw4,bh4)<=16 && bw4+bh4>=4. sz_ctx = log2(bw4)+log2(bh4)-2.
	// -----------------------------------------------------------------------
	PaletteYCDF    [7][3][2]uint16
	PaletteUVCDF   [2][2]uint16
	PaletteSizeCDF [2][7][8]uint16
	ColorMapCDF    [2][7][5][8]uint16

	// -----------------------------------------------------------------------
	// Tx-size CDFs (A3).
	//   TxSzCDF[tx_max-1=4][tctx=3][n_syms+1=4]: depth symbol read when
	//   txfm_mode==SWITCHABLE && t_dim.Max > TX_4x4. n_syms = imin(max,2)+1.
	//   For max=1 (TX_8x8) only cdf[0..1] is used (2 syms); for max>=2 the
	//   full cdf[0..3] is used (3 syms).
	// -----------------------------------------------------------------------
	TxSzCDF [4][3][4]uint16

	// -----------------------------------------------------------------------
	// Var-tx partition CDFs.
	//   TxPartCDF[cat=7][ctx=3][2]: binary split flag used by read_tx_tree.
	// -----------------------------------------------------------------------
	TxPartCDF [7][3][2]uint16

	// -----------------------------------------------------------------------
	// Angle-delta CDFs (A6).
	//   AngleDeltaCDF[mode-VERT_PRED][8] : 7 syms (delta = val-3) + counter.
	// Read only when bw4*bh4 >= 4 (i.e. !(4x4|4x8|8x4)) and the intra mode
	// is in [VERT_PRED..VERT_LEFT_PRED]. Used for both luma (after y_mode)
	// and chroma (after uv_mode, when uv is not CFL).
	// -----------------------------------------------------------------------
	AngleDeltaCDF [8][8]uint16

	// -----------------------------------------------------------------------
	// Minimal single-ref inter syntax CDFs.
	//   NewMVModeCDF[6][2]
	//   GlobalMVModeCDF[2][2]
	//   RefMVModeCDF[6][2]
	//   DRLBitCDF[3][2]
	// -----------------------------------------------------------------------
	NewMVModeCDF    [6][2]uint16
	GlobalMVModeCDF [2][2]uint16
	RefMVModeCDF    [6][2]uint16
	DRLBitCDF       [3][2]uint16

	// -----------------------------------------------------------------------
	// Minimal motion-vector residual CDFs.
	// -----------------------------------------------------------------------
	MVJointCDF    [5]uint16
	MVSignCDF     [2][2]uint16
	MVClassesCDF  [2][12]uint16
	MVClass0CDF   [2][2]uint16
	MVClass0FPCDF [2][2][5]uint16
	MVClass0HPCDF [2][2]uint16
	MVClassNCDF   [2][10][2]uint16
	MVClassNFPCDF [2][5]uint16
	MVClassNHPCDF [2][2]uint16

	// Single-ref frame syntax CDFs: ref[0..5][ctx=3][2]
	RefCDF [6][3][2]uint16

	// Switchable interpolation filter CDFs [dir][ctx][3 syms + sentinel]
	FilterCDF [2][8][4]uint16
}

// Clone returns an independent adaptive-CDF context for one tile.
func (ctx *TileCtx) Clone() *TileCtx {
	if ctx == nil {
		return nil
	}
	out := *ctx
	return &out
}

// CloneForFrame copies inherited probabilities and resets adaptation counters,
// matching dav1d_cdf_thread_update at a reference-frame boundary.
func (ctx *TileCtx) CloneForFrame() *TileCtx {
	out := ctx.Clone()
	if out != nil {
		out.resetCDFCounts()
		// Delta-q and delta-lf predictors are tile-local syntax state. They are
		// initialized from the current frame header and must not follow the
		// probability state inherited through primary_ref_frame.
		out.LastQIdx = 0
		out.LastQIdxValid = false
		out.LastDeltaLF = [4]int8{}
	}
	return out
}

func zeroInnermostUint16ArrayTails(v reflect.Value) {
	switch v.Kind() {
	case reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint16 {
			v.Index(v.Len() - 1).SetUint(0)
			return
		}
		for i := 0; i < v.Len(); i++ {
			zeroInnermostUint16ArrayTails(v.Index(i))
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			zeroInnermostUint16ArrayTails(v.Field(i))
		}
	}
}

func (ctx *TileCtx) resetCDFCounts() {
	// Most Go tables store the counter in the final uint16 slot.
	zeroInnermostUint16ArrayTails(reflect.ValueOf(ctx).Elem())

	// These tables retain dav1d alignment padding or have a dynamic symbol
	// count, so their counter is not necessarily the final array element.
	for i := range ctx.Partition128CDF {
		ctx.Partition128CDF[i][7] = 0
		ctx.Partition64CDF[i][9] = 0
		ctx.Partition32CDF[i][9] = 0
		ctx.Partition16CDF[i][9] = 0
		ctx.Partition8CDF[i][3] = 0
	}
	for i := range ctx.SkipCDF {
		ctx.SkipCDF[i][1] = 0
	}
	for top := range ctx.KFYModeCDF {
		for left := range ctx.KFYModeCDF[top] {
			ctx.KFYModeCDF[top][left][NIntraPredModes-1] = 0
		}
	}
	for size := range ctx.YModeCDF {
		ctx.YModeCDF[size][NIntraPredModes-1] = 0
	}
	// Transform-type arrays include one trailing Go padding slot. The native
	// dav1d counter consumed by SymbolAdaptDav1d is immediately before it.
	for tx := range ctx.TxTypeIntra1CDF {
		for mode := range ctx.TxTypeIntra1CDF[tx] {
			ctx.TxTypeIntra1CDF[tx][mode][TxTypeIntra1Symbols-1] = 0
		}
	}
	for tx := range ctx.TxTypeIntra2CDF {
		for mode := range ctx.TxTypeIntra2CDF[tx] {
			ctx.TxTypeIntra2CDF[tx][mode][TxTypeIntra2Symbols-1] = 0
		}
	}
	for tx := range ctx.TxTypeInter1CDF {
		ctx.TxTypeInter1CDF[tx][TxTypeInter1Symbols-1] = 0
	}
	ctx.TxTypeInter2CDF[TxTypeInter2Symbols-1] = 0
	for i := range ctx.CompInterModeCDF {
		ctx.CompInterModeCDF[i][7] = 0
	}
	for mode := range ctx.AngleDeltaCDF {
		ctx.AngleDeltaCDF[mode][6] = 0
	}
	ctx.FilterIntraModeCDF[4] = 0
	ctx.MVJointCDF[3] = 0
	for comp := range ctx.MVClassesCDF {
		ctx.MVClassesCDF[comp][10] = 0
		ctx.MVClassNFPCDF[comp][3] = 0
		for up := range ctx.MVClass0FPCDF[comp] {
			ctx.MVClass0FPCDF[comp][up][3] = 0
		}
	}
	for cfl := range ctx.UVModeCDF {
		counter := NUVIntraModes - 1
		if cfl == 0 {
			counter--
		}
		for mode := range ctx.UVModeCDF[cfl] {
			ctx.UVModeCDF[cfl][mode][counter] = 0
		}
	}
	for p := range ctx.EobBin16Full {
		for d := range ctx.EobBin16Full[p] {
			ctx.EobBin16Full[p][d][4] = 0
			ctx.EobBin32Full[p][d][5] = 0
			ctx.EobBin64Full[p][d][6] = 0
			ctx.EobBin128Full[p][d][7] = 0
			ctx.EobBin256Full[p][d][8] = 0
		}
		ctx.EobBin512Full[p][9] = 0
		ctx.EobBin1024Full[p][10] = 0
	}
	for tx := range ctx.EobBaseTokFull {
		for p := range ctx.EobBaseTokFull[tx] {
			for c := range ctx.EobBaseTokFull[tx][p] {
				ctx.EobBaseTokFull[tx][p][c][2] = 0
			}
			for c := range ctx.BaseTokFull[tx][p] {
				ctx.BaseTokFull[tx][p][c][3] = 0
			}
		}
	}
	for tx := range ctx.BrTokFull {
		for p := range ctx.BrTokFull[tx] {
			for c := range ctx.BrTokFull[tx][p] {
				ctx.BrTokFull[tx][p][c][3] = 0
			}
		}
	}
	for p := range ctx.PaletteSizeCDF {
		for sz := range ctx.PaletteSizeCDF[p] {
			ctx.PaletteSizeCDF[p][sz][6] = 0
		}
		for palIdx := range ctx.ColorMapCDF[p] {
			counter := palIdx + 1
			for c := range ctx.ColorMapCDF[p][palIdx] {
				ctx.ColorMapCDF[p][palIdx][c][counter] = 0
			}
		}
	}
	for maxIdx := range ctx.TxSzCDF {
		counter := minInt(maxIdx+2, 3) - 1
		for c := range ctx.TxSzCDF[maxIdx] {
			ctx.TxSzCDF[maxIdx][c][counter] = 0
		}
	}
	for d := range ctx.FilterCDF {
		for c := range ctx.FilterCDF[d] {
			ctx.FilterCDF[d][c][2] = 0
		}
	}
}

// NewTileCtx allocates a TileCtx and copies the default CDF values into it.
func NewTileCtx() *TileCtx {
	return NewTileCtxForQIdx(0)
}

func NewTileCtxForQIdx(qidx int) *TileCtx {
	ctx := &TileCtx{}
	ctx.CoefQCat = coefQCatFromQIdx(qidx)

	// Partition
	ctx.Partition128CDF = Partition128CDFDefault
	ctx.Partition64CDF = Partition64CDFDefault
	ctx.Partition32CDF = Partition32CDFDefault
	ctx.Partition16CDF = Partition16CDFDefault
	ctx.Partition8CDF = Partition8CDFDefault

	// Intra modes
	ctx.KFYModeCDF = KFYMCDFDefault
	ctx.YModeCDF = YModeCDFDefault
	ctx.UVModeCDF = UVModeCDFDefault

	// Skip
	ctx.SkipCDF = SkipCDFDefault
	ctx.IntraCDF = DefaultIntraCDF
	ctx.IntrabcCDF = DefaultIntrabcCDF
	ctx.CompCDF = DefaultCompCDF
	ctx.CompDirCDF = CompDirCDFDefault
	ctx.CompFwdRefCDF = CompFwdRefCDFDefault
	ctx.CompBwdRefCDF = CompBwdRefCDFDefault
	ctx.CompUniRefCDF = CompUniRefCDFDefault
	ctx.CompInterModeCDF = CompInterModeCDFDefault

	// TX type
	ctx.TxTypeIntra1CDF = TxTypeIntra1CDFDefault
	ctx.TxTypeIntra2CDF = TxTypeIntra2CDFDefault
	ctx.TxTypeInter1CDF = DefaultTxTypeInter1CDF
	ctx.TxTypeInter2CDF = DefaultTxTypeInter2CDF
	ctx.TxTypeInter3CDF = DefaultTxTypeInter3CDF

	// EOB bin
	ctx.EobBin16CDF = EobBin16Default
	ctx.EobBin32CDF = EobBin32Default
	ctx.EobBin64CDF = EobBin64Default
	ctx.EobBin128CDF = EobBin128Default
	ctx.EobBin256CDF = EobBin256Default
	ctx.EobBin512CDF = EobBin512Default
	ctx.EobBin1024CDF = EobBin1024Default

	// Coeff tokens
	ctx.CoeffBaseTokCDF = CoeffBaseTokDefault
	ctx.CoeffBaseEobCDF = CoeffBaseEobDefault
	ctx.CoeffBrTokCDF = BrTokDefault

	// DC sign
	ctx.DCSignCDF = DCSignDefault

	// CFL
	ctx.CFLSignCDF = CFLSignDefault
	ctx.CFLAlphaCDF = CFLAlphaDefault

	// Segment
	ctx.SegPredCDF = SegPredCDFDefault
	ctx.SegIDCDF = SegIDCDFDefault

	// Delta-q / delta-lf
	ctx.DeltaQCDF = DeltaQCDFDefault
	ctx.DeltaLFCDF = DeltaLFCDFDefault

	// Filter-intra (A1)
	ctx.UseFilterIntraCDF = UseFilterIntraCDFDefault
	ctx.FilterIntraModeCDF = FilterIntraModeCDFDefault

	// Palette flags (A2)
	ctx.PaletteYCDF = PaletteYCDFDefault
	ctx.PaletteUVCDF = PaletteUVCDFDefault
	ctx.PaletteSizeCDF = PaletteSizeCDFDefault
	ctx.ColorMapCDF = ColorMapCDFDefault

	// Tx-size depth (A3)
	ctx.TxSzCDF = TxSzCDFDefault
	ctx.TxPartCDF = TxPartCDFDefault

	// Angle delta (A6)
	ctx.AngleDeltaCDF = AngleDeltaCDFDefault

	// Minimal single-ref inter syntax
	ctx.NewMVModeCDF = NewMVModeCDFDefault
	ctx.GlobalMVModeCDF = GlobalMVModeCDFDefault
	ctx.RefMVModeCDF = RefMVModeCDFDefault
	ctx.DRLBitCDF = DRLBitCDFDefault
	ctx.MVJointCDF = MVJointCDFDefault
	ctx.MVSignCDF = MVSignCDFDefault
	ctx.MVClassesCDF = MVClassesCDFDefault
	ctx.MVClass0CDF = MVClass0CDFDefault
	ctx.MVClass0FPCDF = MVClass0FPCDFDefault
	ctx.MVClass0HPCDF = MVClass0HPCDFDefault
	ctx.MVClassNCDF = MVClassNCDFDefault
	ctx.MVClassNFPCDF = MVClassNFPCDFDefault
	ctx.MVClassNHPCDF = MVClassNHPCDFDefault
	ctx.RefCDF = RefCDFDefault
	ctx.FilterCDF = FilterCDFDefault

	// Sentinel fix-up: several CDFs were imported from dav1d in its native
	// storage form (n explicit values followed by a single counter slot, with
	// the implicit "last symbol" mass absorbed by the counter being <=32).
	// Our MSAC.Symbol/SymbolAdapt loop instead expects cdf[n-1]=0 as an
	// explicit sentinel (where n is the symbol count passed to the call).
	// Force that invariant here so the inner decode loop always terminates
	// inside the array bounds. The probability mass that originally lived in
	// cdf[n-1] is folded into the implicit last symbol.
	for i := range ctx.TxTypeIntra1CDF {
		for j := range ctx.TxTypeIntra1CDF[i] {
			ctx.TxTypeIntra1CDF[i][j][TxTypeIntra1Symbols-1] = 0
		}
	}
	for i := range ctx.TxTypeIntra2CDF {
		for j := range ctx.TxTypeIntra2CDF[i] {
			ctx.TxTypeIntra2CDF[i][j][TxTypeIntra2Symbols-1] = 0
		}
	}
	for i := range ctx.EobBin512CDF {
		// EobBin512 is called with n=9; cdf[8] must be the sentinel.
		ctx.EobBin512CDF[i][8] = 0
	}
	for i := range ctx.EobBin1024CDF {
		// EobBin1024 is called with n=10; cdf[9] must be the sentinel.
		ctx.EobBin1024CDF[i][9] = 0
	}

	// dav1d-shape *Full fields: initialize the live grouped residual defaults
	// as one coherent slice. EOB-bin tables are sourced from the existing exact
	// Go defaults; base/br/eob_base/eob_hi/coef_skip come from the structured
	// qcat=0 defaults derived from dav1d default_coef_cdf[0].
	initCoefLiveDefaults(ctx, ctx.CoefQCat)

	return ctx
}

// initCoefLiveDefaults initializes the live dav1d-shape coefficient state as
// one grouped slice. This keeps the active path out of the old
// "synthetic then overlay" startup pattern and makes the retained baseline
// explicit.
func initCoefLiveDefaults(ctx *TileCtx, qcat int) {
	switch qcat {
	case 0:
		initCoefFullDefaultsQ0(ctx)
	case 1:
		initCoefFullDefaultsQ1(ctx)
	case 2:
		initCoefFullDefaultsQ2(ctx)
	case 3:
		initCoefFullDefaultsQ3(ctx)
	default:
		initCoefFullDefaultsQ0(ctx)
	}
}

func coefQCatFromQIdx(qidx int) int {
	qcat := 0
	if qidx > 20 {
		qcat++
	}
	if qidx > 60 {
		qcat++
	}
	if qidx > 120 {
		qcat++
	}
	return qcat
}

// initCoefEOBBinDefaults seeds the dav1d-shaped EOB-bin storage from the
// existing exact Go tables. These tables are already used live and do not
// depend on the qcat=0 coefficient-prior cutover.
func initCoefEOBBinDefaults(ctx *TileCtx) {
	for p := 0; p < 2; p++ {
		for i := 0; i < 2; i++ {
			copy(ctx.EobBin16Full[p][i][:5], ctx.EobBin16CDF[p][i][:5])
			copy(ctx.EobBin32Full[p][i][:6], ctx.EobBin32CDF[p][i][:6])
			copy(ctx.EobBin64Full[p][i][:7], ctx.EobBin64CDF[p][i][:7])
			copy(ctx.EobBin128Full[p][i][:8], ctx.EobBin128CDF[p][i][:8])
			copy(ctx.EobBin256Full[p][i][:9], ctx.EobBin256CDF[p][i][:9])
		}
		copy(ctx.EobBin512Full[p][:10], ctx.EobBin512CDF[p][:10])
		copy(ctx.EobBin1024Full[p][:11], ctx.EobBin1024CDF[p][:11])
	}
}

// initCoefFullDefaults broadcasts the simplified 4-bucket M7 default CDF
// values across the dav1d-shape *Full fields. This keeps the Task 2 cut-over
// CDF-valid (no zero divisor in MSAC) without requiring the full ~6KB
// default_coef_cdf table from dav1d/src/cdf.c (which Task 7 ports verbatim).
func initCoefFullDefaults(ctx *TileCtx) {
	initCoefEOBBinDefaults(ctx)

	// base_tok: broadcast 4-bucket default across (txSize, plane, 41 ctx).
	// Layout: [val0][val1][val2][sentinel=0][counter=0]
	for t := 0; t < N_TX_SIZES; t++ {
		for p := 0; p < 2; p++ {
			for c := 0; c < 41; c++ {
				// Use ctx[c & 3] to keep some variation across the 41 slots.
				src := ctx.CoeffBaseTokCDF[c&3]
				ctx.BaseTokFull[t][p][c][0] = src[0]
				ctx.BaseTokFull[t][p][c][1] = src[1]
				ctx.BaseTokFull[t][p][c][2] = src[2]
				ctx.BaseTokFull[t][p][c][3] = 0 // sentinel
				ctx.BaseTokFull[t][p][c][4] = 0 // counter
			}
			// eob_base_tok layout: [val0][val1][sentinel][counter]
			for c := 0; c < 4; c++ {
				src := ctx.CoeffBaseEobCDF[c]
				ctx.EobBaseTokFull[t][p][c][0] = src[0]
				ctx.EobBaseTokFull[t][p][c][1] = src[1]
				ctx.EobBaseTokFull[t][p][c][2] = 0 // sentinel
				ctx.EobBaseTokFull[t][p][c][3] = 0 // counter
			}
		}
	}
	// br_tok[4][2][21][5] — same layout as base_tok.
	for s := 0; s < 4; s++ {
		for p := 0; p < 2; p++ {
			for c := 0; c < 21; c++ {
				src := ctx.CoeffBrTokCDF[c&3]
				ctx.BrTokFull[s][p][c][0] = src[0]
				ctx.BrTokFull[s][p][c][1] = src[1]
				ctx.BrTokFull[s][p][c][2] = src[2]
				ctx.BrTokFull[s][p][c][3] = 0 // sentinel
				ctx.BrTokFull[s][p][c][4] = 0 // counter
			}
		}
	}

	// eob_hi_bit[5][2][9][2]: keep a neutral prior until the full coefficient
	// CDF path is switched over coherently. Real qcat=0 values without matching
	// base/br/eob_base tables regress the current stream sharply.
	for t := 0; t < N_TX_SIZES; t++ {
		for p := 0; p < 2; p++ {
			for b := 0; b < 9; b++ {
				ctx.EobHiBitFull[t][p][b][0] = 16384
				ctx.EobHiBitFull[t][p][b][1] = 0
			}
		}
	}

	// coef_skip[5][13][2]: use a neutral prior until the full coefficient CDF
	// path is switched over. Real dav1d skip priors without matching base/br/eob
	// token tables regress the current stream sharply.
	for t := 0; t < N_TX_SIZES; t++ {
		for c := 0; c < 13; c++ {
			ctx.CoefSkipFull[t][c][0] = 16384
			ctx.CoefSkipFull[t][c][1] = 0
		}
	}
}

// initCoefFullDefaultsQ0 loads the full qcat=0 coefficient defaults derived
// from dav1d default_coef_cdf[0]. Keep this separate from initCoefFullDefaults:
// experiments showed that switching only a subset of these tables regresses the
// current stream sharply, so the live path should only move over as one cut.
func initCoefFullDefaultsQ0(ctx *TileCtx) {
	initCoefEOBBinDefaults(ctx)
	for t := 0; t < N_TX_SIZES; t++ {
		for p := 0; p < 2; p++ {
			for c := 0; c < 41; c++ {
				copy(ctx.BaseTokFull[t][p][c][:], BaseTokFullDefaultQ0[t][p][c][:])
			}
			for c := 0; c < 4; c++ {
				copy(ctx.EobBaseTokFull[t][p][c][:], EobBaseTokFullDefaultQ0[t][p][c][:])
			}
			for b := 0; b < 9; b++ {
				copy(ctx.EobHiBitFull[t][p][b][:], EobHiBitFullDefaultQ0[t][p][b][:])
			}
		}
		for c := 0; c < 13; c++ {
			copy(ctx.CoefSkipFull[t][c][:], CoefSkipFullDefaultQ0[t][c][:])
		}
	}
	for s := 0; s < 4; s++ {
		for p := 0; p < 2; p++ {
			for c := 0; c < 21; c++ {
				copy(ctx.BrTokFull[s][p][c][:], BrTokFullDefaultQ0[s][p][c][:])
			}
		}
	}
}

func initCoefFullDefaultsQ3(ctx *TileCtx) {
	for p := 0; p < 2; p++ {
		for i := 0; i < 2; i++ {
			copy(ctx.EobBin16Full[p][i][:], EobBin16FullDefaultQ3[p][i][:])
			copy(ctx.EobBin32Full[p][i][:], EobBin32FullDefaultQ3[p][i][:])
			copy(ctx.EobBin64Full[p][i][:], EobBin64FullDefaultQ3[p][i][:])
			copy(ctx.EobBin128Full[p][i][:], EobBin128FullDefaultQ3[p][i][:])
			copy(ctx.EobBin256Full[p][i][:], EobBin256FullDefaultQ3[p][i][:])
		}
		copy(ctx.EobBin512Full[p][:], EobBin512FullDefaultQ3[p][:])
		copy(ctx.EobBin1024Full[p][:], EobBin1024FullDefaultQ3[p][:])
	}
	for t := 0; t < N_TX_SIZES; t++ {
		for p := 0; p < 2; p++ {
			for c := 0; c < 41; c++ {
				copy(ctx.BaseTokFull[t][p][c][:], BaseTokFullDefaultQ3[t][p][c][:])
			}
			for c := 0; c < 4; c++ {
				copy(ctx.EobBaseTokFull[t][p][c][:], EobBaseTokFullDefaultQ3[t][p][c][:])
			}
			for b := 0; b < 9; b++ {
				copy(ctx.EobHiBitFull[t][p][b][:], EobHiBitFullDefaultQ3[t][p][b][:])
			}
		}
		for c := 0; c < 13; c++ {
			copy(ctx.CoefSkipFull[t][c][:], CoefSkipFullDefaultQ3[t][c][:])
		}
	}
	for s := 0; s < 4; s++ {
		for p := 0; p < 2; p++ {
			for c := 0; c < 21; c++ {
				copy(ctx.BrTokFull[s][p][c][:], BrTokFullDefaultQ3[s][p][c][:])
			}
		}
	}
}

func initCoefFullDefaultsQ1(ctx *TileCtx) {
	for p := 0; p < 2; p++ {
		for i := 0; i < 2; i++ {
			copy(ctx.EobBin16Full[p][i][:], EobBin16FullDefaultQ1[p][i][:])
			copy(ctx.EobBin32Full[p][i][:], EobBin32FullDefaultQ1[p][i][:])
			copy(ctx.EobBin64Full[p][i][:], EobBin64FullDefaultQ1[p][i][:])
			copy(ctx.EobBin128Full[p][i][:], EobBin128FullDefaultQ1[p][i][:])
			copy(ctx.EobBin256Full[p][i][:], EobBin256FullDefaultQ1[p][i][:])
		}
		copy(ctx.EobBin512Full[p][:], EobBin512FullDefaultQ1[p][:])
		copy(ctx.EobBin1024Full[p][:], EobBin1024FullDefaultQ1[p][:])
	}
	for t := 0; t < N_TX_SIZES; t++ {
		for p := 0; p < 2; p++ {
			for c := 0; c < 41; c++ {
				copy(ctx.BaseTokFull[t][p][c][:], BaseTokFullDefaultQ1[t][p][c][:])
			}
			for c := 0; c < 4; c++ {
				copy(ctx.EobBaseTokFull[t][p][c][:], EobBaseTokFullDefaultQ1[t][p][c][:])
			}
			for b := 0; b < 9; b++ {
				copy(ctx.EobHiBitFull[t][p][b][:], EobHiBitFullDefaultQ1[t][p][b][:])
			}
		}
		for c := 0; c < 13; c++ {
			copy(ctx.CoefSkipFull[t][c][:], CoefSkipFullDefaultQ1[t][c][:])
		}
	}
	for s := 0; s < 4; s++ {
		for p := 0; p < 2; p++ {
			for c := 0; c < 21; c++ {
				copy(ctx.BrTokFull[s][p][c][:], BrTokFullDefaultQ1[s][p][c][:])
			}
		}
	}
}

func initCoefFullDefaultsQ2(ctx *TileCtx) {
	for p := 0; p < 2; p++ {
		for i := 0; i < 2; i++ {
			copy(ctx.EobBin16Full[p][i][:], EobBin16FullDefaultQ2[p][i][:])
			copy(ctx.EobBin32Full[p][i][:], EobBin32FullDefaultQ2[p][i][:])
			copy(ctx.EobBin64Full[p][i][:], EobBin64FullDefaultQ2[p][i][:])
			copy(ctx.EobBin128Full[p][i][:], EobBin128FullDefaultQ2[p][i][:])
			copy(ctx.EobBin256Full[p][i][:], EobBin256FullDefaultQ2[p][i][:])
		}
		copy(ctx.EobBin512Full[p][:], EobBin512FullDefaultQ2[p][:])
		copy(ctx.EobBin1024Full[p][:], EobBin1024FullDefaultQ2[p][:])
	}
	for t := 0; t < N_TX_SIZES; t++ {
		for p := 0; p < 2; p++ {
			for c := 0; c < 41; c++ {
				copy(ctx.BaseTokFull[t][p][c][:], BaseTokFullDefaultQ2[t][p][c][:])
			}
			for c := 0; c < 4; c++ {
				copy(ctx.EobBaseTokFull[t][p][c][:], EobBaseTokFullDefaultQ2[t][p][c][:])
			}
			for b := 0; b < 9; b++ {
				copy(ctx.EobHiBitFull[t][p][b][:], EobHiBitFullDefaultQ2[t][p][b][:])
			}
		}
		for c := 0; c < 13; c++ {
			copy(ctx.CoefSkipFull[t][c][:], CoefSkipFullDefaultQ2[t][c][:])
		}
	}
	for s := 0; s < 4; s++ {
		for p := 0; p < 2; p++ {
			for c := 0; c < 21; c++ {
				copy(ctx.BrTokFull[s][p][c][:], BrTokFullDefaultQ2[s][p][c][:])
			}
		}
	}
}
