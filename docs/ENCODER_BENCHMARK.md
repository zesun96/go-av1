# Encoder preset 12 comparison

This report tracks the M14 quality target against SVT-AV1 preset 12. It is
generated with `cmd/av1-encbench`, which encodes both curves, measures decoded
PSNR with FFmpeg, and integrates log-rate over the common PSNR interval.

## Reproduce

```powershell
go run ./cmd/av1-encbench `
  -i input.y4m `
  -out logs/encoder-p12 `
  -ffmpeg ffmpeg `
  -preset 12 `
  -crfs 12,24,36,48
```

FFmpeg must include the `libsvtav1` encoder. The command writes the encoded IVF
files plus machine-readable `report.json` and rendered `report.md`.

## Current smoke result

The first checked result uses a six-frame 64x64 `testsrc2` YUV420 sequence at
3 fps. This tiny synthetic clip is a smoke benchmark, not the final M14 corpus.

| Encoder | CRF | Rate (kbps) | PSNR (dB) |
|---|---:|---:|---:|
| go-av1 | 12 | 30.3 | 45.19 |
| SVT-AV1 | 12 | 32.3 | 45.48 |
| go-av1 | 24 | 22.7 | 40.06 |
| SVT-AV1 | 24 | 19.5 | 40.59 |
| go-av1 | 36 | 15.1 | 33.80 |
| SVT-AV1 | 36 | 12.3 | 34.70 |
| go-av1 | 48 | 8.3 | 27.42 |
| SVT-AV1 | 48 | 7.2 | 29.16 |

The measured go-av1 BD-rate relative to SVT-AV1 preset 12 is **+23.93%**.
This smoke sequence now meets the roadmap goal of approximately +25%; the
result still needs confirmation on the final multi-sequence M14 corpus.

The next quality work is larger inter partition coverage and a multi-sequence
benchmark corpus. Preset 12 currently enables hierarchical motion estimation,
regular 8-tap reconstruction-domain sub-pixel refinement, all seven
single-reference types, rotating references, conservative 8x8/16x16
intra/inter partition RDO, conservative 32x32 inter RDO with transform-size
aware quantization, GLOBAL_GLOBAL and NEW_NEWMV compound prediction, calibrated
deblocking, EOB-aware coefficient-rate/lambda RDO, and coarse-to-fine
reconstruction-domain motion refinement. OBMC remains an explicit opt-in.

## Local multi-sequence acceptance suite

`cmd/av1-encbench` accepts a comma-separated input list and writes per-sequence
reports plus `suite-report.json` and `suite-report.md`. `-reuse` resumes a
partially completed suite from matching per-sequence `report.json` files.

The first local suite adds deterministic FFmpeg `life` and moving-box sources
to the smoke input. All sequences are six-frame 64x64 YUV420 clips, so this is
a reproducible regression suite rather than a substitute for a standard
real-content corpus.

| Sequence | preset 12 BD-rate | preset 11 BD-rate |
|---|---:|---:|
| testsrc2 | +23.93% | +47.66% |
| life | +8.35% | +8.39% |
| moving boxes | -12.71% | -12.71% |
| Arithmetic mean | **+6.52%** | **+14.45%** |

Preset 12 meets the approximately +25% target on every sequence in this local
suite. Preset 11 meets it on two of three sequences, but the testsrc2 result
does not pass; preset 11 alignment therefore remains a quality-enhancement
task rather than a completed per-sequence exit criterion.

## Rate-control acceptance

A 120-frame, 64x64 `testsrc2` sequence at 30 fps was encoded with preset 13 and
a 40 kbit/s target. The four-second averages were:

| Mode | Measured rate | Target error |
|---|---:|---:|
| VBR | 43.43 kbit/s | +8.6% |
| CBR | 43.25 kbit/s | +8.1% |

Both modes land within 10% of the requested average. The controller tests also
run a deterministic 180-frame closed loop and verify convergence and CBR
virtual-buffer bounds.

## Remaining encoder work

The Phase 2 preset-12 acceptance path is complete for the local suite. The
remaining work is broader validation and performance rather than a missing
basic encode path:

- run the suite on standard real-content sequences and publish those inputs;
- close the preset-11 testsrc2 quality gap;
- prune/cache the coarse-to-fine reconstruction search, which is deliberately
  quality-first and substantially slower than SVT preset 12;
- optional format/scale extensions: 10/12-bit, 4:2:2/4:4:4, multi-tile and
  multithreaded encoding.
