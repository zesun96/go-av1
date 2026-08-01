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
- Revisit only with one call covering the complete rectangle and explicit
  sentinel tests.

### MSAC 2/3-symbol manual unrolling

- Result: about 0.13% improvement for roughly 155 lines of specialized code.
- Reason: added instruction footprint and maintenance cost for a noise-level
  gain.
- Revisit only with generated or assembly kernels covering the dominant CDF
  families together.

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

### Plane-wide padded CDEF source

- Result: 1.805 s versus a 1.802 s same-session baseline in the earlier
  benchmark environment.
- Reason: full-plane padding increased memory traffic without improving the
  per-block filter enough.
- Superseded by the retained row-stripe source design in commit `6ba718e`.

### Scalar width-6 loop-filter specialization

- Result: no stable end-to-end improvement.
- Reason: the remaining cost is the filtering arithmetic and edge traversal,
  not generic width dispatch.
- Revisit as a vector kernel processing the four parallel edge lanes.

## Current large targets

The remaining plausible single-thread gains are complete 8x8/16x16 inverse
transform SIMD, vector 6/8/16-wide loop filters, wider CDEF kernels, and a
generated or assembly coefficient/MSAC pipeline. Small scalar rewrites should
be profiled before implementation.
