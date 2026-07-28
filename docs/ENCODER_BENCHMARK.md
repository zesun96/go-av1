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
