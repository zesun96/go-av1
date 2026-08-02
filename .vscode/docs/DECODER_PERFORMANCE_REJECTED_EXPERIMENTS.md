# Decoder Performance Experiments Not Retained

This file records decoder optimization experiments that were tested and then
reverted. It exists to prevent repeating locally attractive changes that do
not improve the single-thread end-to-end decoder.

## Acceptance rule

An optimization is retained only when it:

- preserves the 599-frame WebRTC Frame MD5 output;
- passes the relevant unit tests and `go test ./...`;
- improves alternating old/new 120-frame runs by more than normal timing
  noise, with the median as the primary result;
- does not add disproportionate architecture-specific complexity for a
  sub-percent result.

Microbenchmarks are diagnostic only. End-to-end decode time is authoritative.

## Rejected experiments

### Fused scalar 16x16 DCT_DCT driver

- Result: about 7% faster in the transform microbenchmark, but about 0.20%
  slower end-to-end.
- Reason: extra local-buffer traffic and scalar butterfly work outweighed
  generic-driver savings in the real transform-size and EOB distribution.
- Revisit only as a genuine multi-lane SIMD 2D kernel.

### Extra bounds-check guards in DCT8/DCT16

- Result: DCT8 regressed.
- Reason: entry guards and altered compiler code generation cost more than the
  removed dynamic-stride checks.
- Revisit only if the transform is specialized by fixed size/stride or moved
  into assembly.

### Fixed-width integer motion-compensation row copies

- Result: approximately 0.09% improvement, inside noise.
- Reason: replacing Go `copy`/`memmove` did not reduce the required pixel
  traffic and added dispatch/call overhead.
- Revisit only as part of a larger motion-compensation kernel that also avoids
  an intermediate buffer or combines adjacent blocks.

### SSE2 compact multi-tile grid offset

- Result: about 2.44% slower and initially failed zero/sentinel preservation.
- Reason: per-row Go-to-assembly calls dominated, and grid index zero has
  semantic meaning.
- A follow-up used one assembly call for the complete tile rectangle, passed
  1,000 randomized stride/zero-sentinel cases, and improved its dense-grid
  microbenchmark by more than 3x. It still regressed the exact same-build
  end-to-end median by 2.54%; adding all-zero/all-nonzero vector fast paths
  increased the regression to 3.22%.
- Reason for the follow-up rejection: the real grids do not behave like the
  dense microbenchmark, and unconditional vector destination traffic plus
  extra branching/code footprint costs more than the scalar nonzero stores.
- Do not revisit without a profile proving a different grid density or a
  merge design that eliminates the destination remap pass entirely.

### MSAC 2/3-symbol manual unrolling

- Result: about 0.13% improvement for roughly 155 lines of specialized code.
- Reason: added instruction footprint and maintenance cost for a noise-level
  gain.
- Revisit only with generated or assembly kernels covering the dominant CDF
  families together.

### Direct native HiTok dispatch

- Result: the 512-token microbenchmark improved from approximately 4.26 to
  3.35 microseconds (21 percent), but an exact same-build 11-run decoder test
  was flat to slightly slower (787.25 ms versus 787.26 ms).
- An intermediate helper shared by normal 4-result symbols and HiTok could
  not inline and regressed the normal symbol microbenchmark by about 5
  percent; duplicating the direct assembly call removed that regression but
  still produced no end-to-end gain.
- Moving scalar `c/r` preparation below the native dispatch, tested alone,
  also regressed the end-to-end median by 1.42 percent despite improving the
  symbol microbenchmark.
- Reason: HiTok escapes are not frequent enough in the measured stream, and
  small source/layout changes around this already assembly-heavy path alter
  instruction-cache behavior more than they save arithmetic.
- Revisit only as part of a fused coefficient-token decoder that removes
  surrounding Go calls and context work, not as another MSAC wrapper change.

### Fixed four-symbol MSAC wrapper for coefficient base tokens

- Result: the fixed `[5]uint16` entry bypassed generic argument validation,
  slice extraction, and symbol-count dispatch, and passed 2,048-step native
  versus scalar differential tests plus the 599-frame FrameMD5 check. An
  exact same-toolchain, same-commit five-run alternating comparison was flat:
  6968.66 ms versus 6971.74 ms, only 0.04 percent faster.
- A comparison against an older retained binary initially appeared about 4
  percent faster. Rebuilding the baseline from `HEAD` made that result vanish,
  again demonstrating that old/new executable layout is not reliable for
  accepting small decoder changes.
- Reason: base-token decoding is frequent, but the already-specialized
  assembly dominates each call; removing its Go wrapper checks alone does not
  remove enough work. Revisit only by fusing surrounding coefficient context
  calculation or multiple token operations into the native kernel.

### Split 2D and 1D coefficient AC loops

- Result: moving the transform-class branch outside the reverse AC loop and
  specializing the common 2D scan/context/high-token calculations preserved
  the complete 599-frame FrameMD5, but regressed a same-build five-run median
  by 1.68 percent: 7399.92 ms versus 7275.42 ms.
- Reason: duplicating the sizeable tracing, token update, and escape handling
  body expanded `decodeCoeffTokens`; the extra instruction footprint cost
  more than removing its highly predictable transform-class branches.
- Revisit only as a compact assembly fusion that also consumes context
  samples and performs the base-token MSAC operation, not as duplicated Go
  loops or another helper called once per coefficient.

### Expanded Bool normalization fast path

- Result: about 0.22% regression.
- Reason: the additional branch/code footprint was not recovered by fewer
  normalization operations.

### CDEF tap-vector hoisting in assembly

- Result: about 0.07% improvement.
- Reason: tap setup was not a material part of total CDEF time.

### AVX2 two-row combined CDEF

- Result: approximately 410 ns per 8x8 block versus 381 ns for the retained
  SSE4.1 kernel, a 7-8% microbenchmark regression.
- Reason: each neighbor vector required assembling two non-contiguous 12-wide
  scratch rows into separate 128-bit YMM lanes, followed by lane extraction
  for the two destination rows. That shuffle/load cost exceeded the benefit
  of processing twice as many arithmetic lanes.
- Revisit only after changing the CDEF scratch layout so paired rows or paired
  blocks are already contiguous in SIMD-friendly lanes.

### AVX2 two-row direct-source combined CDEF

- Result: after removing the int16 scratch entirely, a second two-row AVX2
  implementation passed the complete direction/strength differential suite
  but still measured approximately 216-239 ns per 8x8 block versus 194-215 ns
  for the retained direct-source SSE4.1 kernel, a roughly 10-20% regression.
- Reason: every one of the twelve neighbor positions still needs two
  non-contiguous 8-byte row loads plus a lane-pack before widening to YMM.
  Halving the arithmetic instruction count does not repay that load/pack work
  or the AVX transition cost.
- Revisit only with a genuinely row-paired source layout populated upstream;
  do not attempt another kernel-side row assembly variant.

### Paired U/V 4x4 CDEF SSE4.1

- Result: approximately 158 ns for a paired block versus 158 ns for two
  retained single-plane calls; an exact same-build 11-run end-to-end test
  regressed by 0.12 percent (757.15 ms versus 756.21 ms).
- Reason: packing four pixels from each plane filled all eight SIMD lanes, but
  required two independent scratch/source address streams. The two padding
  expansions and paired loads consumed the saved call overhead.
- An initial comparison against an older executable incorrectly appeared 3.6
  percent faster. Rebuilding the scalar-call baseline from the same source and
  compiler configuration removed that apparent gain; do not use cross-build
  decoder binaries for sub-five-percent acceptance decisions.
- Revisit only if U/V padding is produced directly in one interleaved scratch
  layout, so pairing removes memory work as well as one filter call.

### Plane-wide padded CDEF source

- Result: 1.805 s versus a 1.802 s same-session baseline in the earlier
  benchmark environment.
- Reason: full-plane padding increased memory traffic without improving the
  per-block filter enough.
- Superseded by the retained row-stripe source design in commit `6ba718e`.

### Skip CDEF direction-grid clearing

- Result: 599-frame output remained exact and the 120-frame median initially
  improved by 0.34 percent, but five alternating 599-frame runs regressed by
  1.30 percent (7.217 s versus 7.125 s).
- Reason: the removed writes are small relative to CDEF filtering, and stale
  direction/variance state changes cache behavior without a stable wall-time
  benefit even though all subsequently read entries are overwritten.

### Scalar width-6 loop-filter specialization

- Result: no stable end-to-end improvement.
- Reason: the remaining cost is the filtering arithmetic and edge traversal,
  not generic width dispatch.
- Revisit as a vector kernel processing the four parallel edge lanes.

### SSE2 wide-loop-filter reject precheck

- Result: a synthetic rejected width-8 edge improved from approximately
  43-47 ns to 9 ns and passed 10,000 randomized width 6/8/16 differential
  cases. Applying it to all vertical wide edges regressed end-to-end by 0.62
  percent; restricting it to width 16 still regressed by 0.51 percent.
- Reason: most real candidates that reach this stage continue into the full
  generic filter, so the SSE2 mask check becomes duplicate work.
- Revisit only as a complete SIMD implementation that reuses its mask and
  loaded pixels for the actual 6/8/16-wide filtering operation.

### Direct intra prediction into the frame buffer

- Result: rejected for correctness. Predicting full, unclipped transform
  blocks directly into the strided destination instead of a compact scratch
  block changed the 599-frame framemd5 beginning at decoded frame 2.
- The public predictor entry points accept a stride, but the decoder's compact
  prediction-and-copy sequence currently has observable semantics for at least
  one real prediction/reconstruction path. Do not remove that copy based only
  on the apparent API contract.
- Revisit with a mode-by-mode compact-versus-strided differential test and a
  first-pixel decoder trace before enabling direct output for any subset.

### Direct-source CDEF for low-frequency strength paths

- Result: the retained direct byte-source combined primary+secondary kernel
  was extended to primary-only and secondary-only blocks, but the 11-run
  120-frame median regressed by 0.47 percent (754.65 ms versus 751.11 ms).
- Reason: these strength combinations are uncommon in the measured stream;
  their additional dispatch and assembly footprint cost more instruction
  cache than the avoided 12x12 scratch expansion saves.
- Keep direct-source filtering restricted to the hot combined path. Revisit
  the other cases only if a different corpus shows them as material flat
  profile entries.

## Current large targets

The remaining plausible single-thread gains are complete 8x8/16x16 inverse
transform SIMD, vector 6/8/16-wide loop filters, wider CDEF kernels, and a
generated or assembly coefficient/MSAC pipeline. Small scalar rewrites should
be profiled before implementation.
