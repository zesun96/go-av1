# Conformance Vectors

This directory hosts the harness and notes for AV1 conformance / regression
testing. Vectors themselves are **not** vendored; they are large and licensed
separately from go-av1.

## Sources

| Source                                                                                                | When to use            |
|-------------------------------------------------------------------------------------------------------|------------------------|
| AOM `av1-test-vectors` (https://storage.googleapis.com/aom-test-data/test_vectors/av1.zip)            | M2 onward, primary set |
| dav1d `tests/dav1d-test-data` (https://code.videolan.org/videolan/dav1d-test-data)                    | M1 onward, daily smoke |
| SVT-AV1 `test/e2e_test`                                                                               | M11 onward, encoder    |

## Layout

```
test/conformance/
├── README.md           This file.
├── vectors/            (gitignored) Place downloaded streams here.
├── decoded/            (gitignored) Per-test YUV outputs for diffing.
└── known-issues.md     Track expected failures during early milestones.
```

## Bootstrap (manual today, scripted later)

```bash
mkdir -p test/conformance/vectors
cd test/conformance/vectors

# AOM official set
curl -L -o av1.zip https://storage.googleapis.com/aom-test-data/test_vectors/av1.zip
unzip av1.zip

# dav1d daily smoke set
git clone --depth 1 https://code.videolan.org/videolan/dav1d-test-data.git dav1d
```

## Running (scaffold)

There is no automated runner yet. Once milestone M2 lands the OBU parser, a
Go-based smoke runner will live under `test/conformance/` and be invoked as:

```bash
go test ./test/conformance/...
```

The eventual driver:

1. Iterates every `.ivf` / `.obu` under `test/conformance/vectors/`.
2. Decodes with go-av1 into `test/conformance/decoded/<name>.yuv`.
3. Re-decodes the same input with `dav1d` (must be on `$PATH`) into a
   reference YUV.
4. Diffs the two byte for byte.

Any deviation aborts the run and is recorded in `known-issues.md` if it is a
known limitation of the current milestone.
