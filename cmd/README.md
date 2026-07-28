# cmd — Command-line tools

This directory contains the CLI programs that ship with go-av1.
Each subdirectory is a standalone `main` package.

| Directory | Binary | Status |
|---|---|---|
| [`go-av1d`](go-av1d/) | `go-av1d` | Active |
| [`av1-benchcmp`](av1-benchcmp/) | `av1-benchcmp` | Developer tool |
| [`go-av1enc`](go-av1enc/) | `go-av1enc` | Active |
| [`webrtc-av1d`](webrtc-av1d/) | `webrtc-av1d` | Active |

---

## go-av1d

AV1 file decoder. Reads an IVF bitstream, decodes every frame with
the go-av1 pipeline, and writes raw planar output as Y4M.

### Install

```sh
go install github.com/zesun96/go-av1/cmd/go-av1d@latest
```

### Usage

```
go-av1d -i <input> [-o <output>] [-threads <n>]
```

| Flag | Default | Description |
|---|---|---|
| `-i` | *(required)* | Input AV1 file (IVF) |
| `-o` | *(discard)* | Output Y4M file; use `-` for stdout |
| `-threads` | `0` (NumCPU) | Reserved decoder worker count; scheduling is not implemented yet |

### Examples

```sh
# Decode to Y4M file
go-av1d -i clip.ivf -o clip.y4m

# Pipe into ffplay for immediate playback
go-av1d -i clip.ivf -o - | ffplay -i -

# Discard output, just measure decode speed
go-av1d -i clip.ivf
```

---

## av1-benchcmp

Developer tool for repeated end-to-end wall-time comparison of prebuilt
`go-av1d` and dav1d executables. It alternates decoder order, records raw
samples, and writes JSON and Markdown reports.

Build `go-av1d` and release-mode dav1d before running it. Correctness must be
checked separately with `cmd/av1-conformance`.

```sh
go run ./cmd/av1-benchcmp \
  -input clip.ivf \
  -go-decoder ./go-av1d \
  -dav1d /path/to/dav1d \
  -threads 1 -warmup 2 -runs 7 \
  -json benchmark.json -markdown benchmark.md
```

See [`../docs/PERFORMANCE.md`](../docs/PERFORMANCE.md) for the controlled
baseline and complete Windows reproduction steps.

---

## go-av1enc

AV1 file encoder. Reads Y4M input and writes an IVF bitstream.

> **Status:** experimental 8-bit 4:2:0 encoder with intra/inter prediction,
> frame-level rate control, and SVT-style speed presets.

### Install

```sh
go install github.com/zesun96/go-av1/cmd/go-av1enc@latest
```

### Usage

```
go-av1enc -i <input.y4m> -o <output.ivf> [-preset <0-13>] [-crf <n>]
```

| Flag | Default | Description |
|---|---|---|
| `-i` | *(required)* | Input Y4M file |
| `-o` | *(required)* | Output IVF file |
| `-preset` | `12` | Encoder speed preset (0 = slowest, 13 = fastest) |
| `-crf` | `30` | Constant rate factor (quality) |

---

## webrtc-av1d

WebRTC server that receives AV1 video from a browser, writes it to an IVF
file, and decodes each frame through the go-av1 pipeline in real time.

This tool lives in its **own Go module** (`cmd/webrtc-av1d/go.mod`) so that
its [pion/webrtc](https://github.com/pion/webrtc) dependency does **not**
propagate to consumers of the core `go-av1` library.

See [`webrtc-av1d/README.md`](webrtc-av1d/README.md) for full details.

### Quick start

```sh
cd cmd/webrtc-av1d
go run . -port 8080 -out output.ivf -yuv output.y4m
# Open http://localhost:8080 in Chrome, click "Start Stream"
```
