# Conformance Vectors

This directory hosts the harness and notes for AV1 conformance / regression
testing. Vectors themselves are **not** vendored; they are large and licensed
separately from go-av1.

Repository-owned metadata:

- `FEATURES.md` records the explicit supported/partial/unsupported surface;
- `project-recordings.json` pins retained regression inputs and native hashes;
- `BENCHMARKS.md` records the controlled microbenchmark baseline.

## Sources

| Source | When to use |
|---|---|
| Argon (https://streams.videolan.org/argon/) | Primary conformance corpus |
| dav1d test data (https://code.videolan.org/videolan/dav1d-test-data) | Daily decoder regressions |
| libaom test data (https://storage.googleapis.com/aom-test-data) | Supplemental unit/parser vectors |
| SVT-AV1 `test/e2e_test` | Encoder work |

## Layout

```
test/conformance/
├── README.md           This file.
├── vectors/            (gitignored) Place downloaded streams here.
├── decoded/            (gitignored) Per-test YUV outputs for diffing.
└── known-issues.md     Track expected failures during early milestones.
```

## Bootstrap

The old `test_vectors/av1.zip` object no longer exists. The bucket stores
individual objects and does not provide one replacement archive. The current
libaom source tree declares filenames in `test/test_data_util.cmake` and pins
their hashes in `test/test-data.sha1`.

The official libaom workflow is to clone libaom and build its `testdata` CMake
target. To download only decoder-shaped files for go-av1, use the repository
bootstrap command with libaom's SHA-1 manifest:

```powershell
git clone --depth 1 https://aomedia.googlesource.com/aom ../aom

# Inspect the selected individual objects first.
go run ./cmd/aom-testdata `
  -manifest ../aom/test/test-data.sha1 -list

# Download and verify currently supported IVF objects one at a time.
go run ./cmd/aom-testdata `
  -manifest ../aom/test/test-data.sha1 `
  -out test/conformance/vectors/aom
```

`-include` accepts a regular expression when a narrower named subset or a
future `.obu`/`.webm` input path is required. Existing files are SHA-1 verified
and skipped. A mismatching existing file or download aborts without overwriting
the destination. This bootstrap is explicit and is never run by `go test`.

Clone dav1d-test-data separately for the daily regression corpus:

```powershell
git clone --depth 1 `
  https://code.videolan.org/videolan/dav1d-test-data.git `
  test/conformance/vectors/dav1d
```

## Running

Generate native visible-plane hashes for an IVF stream without resampling:

```powershell
go run ./cmd/av1-conformance -i test.ivf -report test.json
```

Compare against a local dav1d executable directly:

```powershell
go run ./cmd/av1-conformance -i test.ivf -dav1d E:/dav1d/tools/dav1d.exe -report test.json
```

Run a checked-in manifest and produce both machine and human-readable reports:

```powershell
go run ./cmd/av1-conformance `
  -manifest test/conformance/project-recordings.json `
  -dav1d E:/dav1d/tools/dav1d.exe `
  -report results.json -markdown results.md
```

Manifest paths are relative to the manifest file. Inputs remain outside Git;
each entry pins its SHA-256, expected frame count, format, feature tags, expected
status, and optional sparse native frame MD5 values. A manifest can therefore
run offline without dav1d when it contains sufficient reference hashes.

Run every downloaded IVF against dav1d, or select one syntax family by its
relative path:

```powershell
go run ./cmd/av1-conformance `
  -vectors test/conformance/vectors/aom `
  -include 'b8-00-quantizer' `
  -dav1d E:/dav1d/tools/dav1d.exe `
  -limit 1 -report results.json -markdown results.md
```

Directory mode infers AOM `-b10-` and `-b12-` files as expected unsupported
until the decoder has high-bit-depth storage. `-include` is a Go regular
expression matched against slash-separated relative paths.

The JSON report records the input SHA-256, container metadata, native output
dimensions, and separate Y/U/V plus whole-frame MD5 values. Row padding is
excluded. This makes the report safe for dynamic-resolution streams.

The runner invokes dav1d's per-frame MD5 muxer directly. The reference output
filename carries each frame's native width and height, so no fixed-size video
filter or resampling stage is involved. The JSON `comparison` field identifies
the first frame-count, dimension, or complete-frame hash difference.

For a complete-frame mismatch, the runner decodes only through the failing
frame again. It uses an automatically removed temporary binary file because
dav1d stdout is not binary-safe on Windows. The JSON report then includes the
first differing plane, sample coordinate, and both sample values under
`first_difference.sample`.

Directory and manifest runs emit aggregate JSON and Markdown reports. Any
deviation makes the command fail after all selected vectors have run, so the
report still contains the complete corpus result.
