# go-av1 Roadmap

The roadmap is organised around milestones. Each milestone has a single,
testable exit criterion. We ship the decoder first; the encoder phase begins
at M10. Milestones M1–M9 follow the dav1d reference implementation closely;
M10+ follow SVT-AV1.

Legend:
- "Maps to" lists the dav1d (or SVT-AV1) source files that the milestone is
  porting / paraphrasing.
- "Exit" is the bit-exact / functional check that proves the milestone is
  done.

---

## Phase 1 — Decoder

### M0 — Project scaffold (this milestone)

- Goals
  - Repository layout, build / lint / test targets.
  - Public API surface frozen at the type-signature level.
  - Skeleton packages compile under `go build ./...` and pass `go vet`.
- Deliverables
  - `README.md`, `docs/DESIGN.md`, `docs/ROADMAP.md`.
  - `pkg/av1` types and stub constructors returning `ErrNotImplemented`.
  - `internal/*` packages with `doc.go` only.
  - `cmd/go-av1d` and `cmd/go-av1enc` printing version + usage banner.
- Maps to: project layout, no algorithm content.
- Exit
  - `go build ./...` succeeds.
  - `go vet ./...` reports zero issues.
  - `go test ./...` succeeds (no tests yet, but the harness is wired up).

### M1 — Bitstream primitives

- Goals
  - Bit reader with `F(n)`, `UVLC()`, `Leb128()`, `NS(n)`, `SU(n)`, `LE(n)`.
  - Multi-symbol arithmetic decoder (boolean + symbol + adaptive flavours).
  - Allocation-free, table-driven; goal of <1.5× dav1d throughput.
- Maps to
  - `dav1d/src/getbits.{c,h}`
  - `dav1d/src/msac.{c,h}` and `cdf.{c,h}`
- Exit
  - Unit tests covering every primitive with vectors lifted from
    `dav1d/tests/`.
  - `go test -fuzz` runs at least 60 seconds without finding diverging output
    against a reference implementation.

### M2 — OBU and header parsing

- Goals
  - Demuxers for IVF and Annex-B / size-prefixed OBU streams.
  - OBU framing: temporal delimiter, sequence header, frame header, tile
    group, padding, metadata.
  - Header decoders that populate `internal/header` structs.
- Maps to
  - `dav1d/src/obu.{c,h}`
  - `include/dav1d/headers.h`
- Exit
  - All headers from the dav1d testdata corpus parse without error.
  - Fuzz harness on `internal/obu` runs clean for 5 minutes locally.

### M3 — Intra-only key frames

- Goals
  - Block partition tree decoding.
  - Quantisation and inverse transform (`itx_1d` + 2D combinations, all
    transform sizes from 4×4 to 64×64).
  - Intra prediction modes: DC / SMOOTH / PAETH / directional / Recursive.
  - Reconstruction.
- Maps to
  - `dav1d/src/decode.c` (key-frame path)
  - `dav1d/src/recon_tmpl.c`
  - `dav1d/src/ipred_tmpl.c`, `ipred_prepare_tmpl.c`
  - `dav1d/src/itx_tmpl.c`, `itx_1d.c`
  - `dav1d/src/dequant_tables.{c,h}`, `qm.{c,h}`, `scan.{c,h}`
- Exit
  - `go-av1d` decodes a curated set of all-keyframe IVF clips bit-exactly
    against `dav1d`.

### M4 — Inter prediction and reference MVs

- Goals
  - Reference frame management, show-existing-frame.
  - `refmvs` construction and MV scan.
  - Motion compensation including OBMC, warped motion, compound, masked /
    wedge / smooth-mask compounds, intra-block-copy.
  - Palette and CFL prediction.
- Maps to
  - `dav1d/src/refmvs.{c,h}`
  - `dav1d/src/mc.h`, `mc_tmpl.c`
  - `dav1d/src/warpmv.{c,h}`
  - `dav1d/src/pal.{c,h}`, `wedge.{c,h}`
- Exit
  - Full inter test suite from dav1d testdata decodes bit-exactly.

### M5 — Post-processing

- Goals
  - Deblocking filter.
  - CDEF.
  - Loop restoration (Wiener + Self-Guided).
  - Super-resolution upscaler.
- Maps to
  - `dav1d/src/loopfilter_tmpl.c`, `lf_apply_tmpl.c`, `lf_mask.c`
  - `dav1d/src/cdef_tmpl.c`, `cdef_apply_tmpl.c`
  - `dav1d/src/looprestoration_tmpl.c`, `lr_apply_tmpl.c`
  - super-res code paths in `recon_tmpl.c`
- Exit
  - All vectors that exercise post-filters decode bit-exactly.

### M6 — Film grain

- Goals
  - Synthesis tables, AR autoregressive grain generation, scaling LUT,
    luma+chroma application.
- Maps to
  - `dav1d/src/filmgrain_tmpl.c`, `fg_apply_tmpl.c`
- Exit
  - Film-grain vectors from `tests/dav1d-test-data/8-bit/film_grain*` decode
    bit-exactly.

### M7 — Concurrency

- Goals
  - Frame-level parallelism with bounded `MaxFrameDelay`.
  - Tile-level parallelism inside a frame.
  - Row-pipelined post-filters.
- Maps to
  - `dav1d/src/thread_task.c`, `thread.h`, `thread_data.h`
- Exit
  - Decoding the dav1d sample suite with `Threads=N` produces identical YUV to
    `Threads=1`.
  - Linear-ish speed-up up to `min(N, num_tiles)`.

### M8 — Conformance pass

- Goals
  - Run the official AOM `av1-test-vectors` set.
  - Wire `go test ./test/conformance/...` to a runner that downloads, decodes, and diffs.
- Exit
  - 100% of Profile 0 / 8-bit / 4:2:0 vectors pass; deviations are tracked in
    `test/conformance/known-issues.md`.

### M9 — Performance: assembly fast paths

- Goals
  - Plan 9 assembly kernels for the hottest functions (itx, mc, cdef,
    deblock, loop restoration) on `amd64` and `arm64`.
  - Build tag `purego` keeps the pure-Go path always reachable.
  - Auto-dispatch via `internal/dispatch`.
- Exit
  - Benchmark harness shows ≥3× over the generic Go path on representative
    vectors, ≤3× slower than dav1d on the same input.

---

## Phase 2 — Encoder

### M10 — Bitstream writer

- Goals: OBU writer, header serialisation, IVF muxer.
- Maps to: SVT-AV1 `Source/Lib/Codec/EbBitstreamUnit*.c` and entropy writer.

### M11 — Intra-only encoder

- Goals: forward transform / quantisation, intra mode search (small set),
  CDF update, single-tile single-thread encode of key frames.
- Maps to: `Source/Lib/Codec/EbModeDecision*`, `EbCodingUnit*`,
  `EbEncDec*`.

### M12 — Inter encoder

- Goals: reference management, motion estimation (HME + sub-pel),
  basic compound, OBMC opt-in.
- Maps to: `Source/Lib/Codec/EbMotionEstimation*`, `EbMcp*`.

### M13 — Rate control + RDO

- Goals: VBR/CBR/CQP, RDO across modes, lambda calibration.
- Maps to: `Source/Lib/Codec/EbRateControl*`, `EbRateDistortionCost.c`.

### M14 — SVT-AV1 preset alignment

- Goals: deliver an encoder that, on a single thread, lands within ~25% BD-
  Rate of SVT-AV1 preset 12 / 11 on a benchmark set.
- Exit: published comparison report and `cmd/go-av1enc` defaulting to that
  preset.

---

## Per-milestone PR template

Each milestone PR description should include:

```
## Summary
<one paragraph>

## Maps to dav1d / SVT-AV1
<file list>

## Tests
- [ ] Unit tests
- [ ] Conformance vectors decoded
- [ ] Fuzz corpus runs clean (M1+, decoder)
- [ ] Benchmarks updated (M9, M14)

## Exit criteria
<copy from this roadmap>
```

## Test vectors

| Source                                                                | Used from |
|-----------------------------------------------------------------------|-----------|
| AOM `av1-test-vectors` (https://storage.googleapis.com/aom-test-data) | M2 onward |
| dav1d `tests/dav1d-test-data`                                         | M1 onward |
| SVT-AV1 conformance tests                                             | M11 onward|

Vectors are not vendored; `test/conformance/README.md` documents the bootstrap
script.
