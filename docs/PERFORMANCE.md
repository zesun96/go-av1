# Decoder Performance Comparison

This document defines and reproduces the end-to-end performance comparison
between go-av1 and dav1d. It is a controlled local baseline, not a general
performance promise across machines or AV1 content.

## Baseline Results

Baseline date: 2026-07-26.

Environment:

- Windows/amd64;
- Intel Core i7-13700KF, 24 logical CPUs;
- Go 1.24.0;
- go-av1 commit `670cfdca6e05f4086f3f9b04b8a3b255ca0621e6`, plus the
  benchmark harness changes documented here;
- dav1d `1.5.3-46-g1718ff9a`;
- dav1d release build: GCC 14.1.0, Meson `release`, optimization level 3,
  normal x86-64 assembly dispatch enabled;
- one decoder thread and `GOMAXPROCS=1`;
- all in-loop filters enabled;
- film grain disabled;
- decoded output discarded;
- two warmup runs followed by seven measured runs in alternating decoder
  order.

Correctness was checked before timing. The first 120 frames of the dynamic
WebRTC input had identical visible Y/U/V plane MD5 values in go-av1 and dav1d.
The AOM input is also part of the 174-vector 8-bit suite that matches dav1d.

| Input | Scope | Decoder | Median | Range | Packets/s | Nominal MP/s |
|---|---:|---|---:|---:|---:|---:|
| AOM all-intra, 352x288 | 39 packets | go-av1 | 1.212s | 1.196-1.229s | 32.17 | 3.26 |
| AOM all-intra, 352x288 | 39 packets | dav1d | 0.098s | 0.097-0.108s | 397.95 | 40.34 |
| WebRTC dynamic desktop | first 120/996 packets | go-av1 | 10.522s | 9.942-10.757s | 11.40 | n/a |
| WebRTC dynamic desktop | first 120/996 packets | dav1d | 0.319s | 0.311-0.322s | 376.26 | n/a |

On these inputs:

- go-av1 used 12.37 times the dav1d median wall time for the small all-intra
  vector, reaching about 8.1 percent of dav1d throughput;
- go-av1 used 32.99 times the dav1d median wall time for the high-resolution
  dynamic WebRTC segment, reaching about 3.0 percent of dav1d throughput.

The WebRTC IVF header intentionally carries `0x0` dimensions because frame
sizes change in-band. Its compared frames range from `1276x1136` to
`2560x1392`, so a single nominal megapixel rate would be misleading.

These results show that high-resolution inter reconstruction and in-loop
processing remain the primary performance work. They do not measure
multi-thread scaling because go-av1's decoder scheduling is still
single-threaded; its `Threads` option does not yet provide dav1d-style frame or
tile parallelism.

## Optimization Progress

On 2026-07-28, the first allocation-focused implementation reused coefficient
scratch per tile, guarded every tile trace payload at the call site, cached
debug environment settings outside block loops, reused the durable frame state
for single-tile frames, and avoided replacing an already correctly sized
chroma grid.

Using the same 120-packet WebRTC input, one thread, all filters, two warmups,
and seven measured runs:

| Decoder state | Median | Packets/s | dav1d median | Wall-time ratio |
|---|---:|---:|---:|---:|
| Original baseline | 10.522s | 11.40 | 0.319s | 32.99x |
| Coefficient scratch and single-state slice | 5.094s | 23.56 | 0.245s | 20.83x |
| All tile trace guards | 3.914s | 30.66 | 0.240s | 16.29x |

The absolute go-av1 median fell by 62.8 percent relative to the recorded
baseline and by 23.2 percent relative to the preceding scratch/state slice.
The dav1d result also moved between measurement sessions, so the go-av1
absolute time and throughput are the primary before/after indicators.

The new `-memstats` measurement reported:

```text
total_alloc_bytes=9722571072
mallocs=3264568
bytes_per_frame=81021425
mallocs_per_frame=27204
```

Compared with the original profile's approximate 16.0 GB and 50.6 million
objects, cumulative allocation fell by about 39 percent and object count by
about 94 percent. The scalar throughput gate of 30 packets/s is now met on
this input, but the allocation target remains unmet. Frame-state ownership and
remaining block/prediction temporaries are continuing work.

On 2026-07-29, the durable block grid was changed from a complete
`Av1Block` value per 4x4 cell to a 32-bit one-based index into per-block
metadata. CDEF, restoration-boundary, and inter-prediction scratch storage is
now reused, and the loop-filter trace environment switch is cached when the
decoder is constructed.

Using the same WebRTC input and measurement rules, the new seven-run result
was:

| Decoder state | Median | Packets/s | Allocation | Objects |
|---|---:|---:|---:|---:|
| Trace-guard baseline | 3.914s | 30.66 | 9.723 GB | 3.265 million |
| Indexed metadata and reused filter/prediction scratch | 2.803s | 42.81 | 1.852 GB | 2.298 million |
| Compact transform boundaries and adaptive 16-bit block grids | 2.655s | 45.19 | 1.188 GB | 2.296 million |
| Pooled frame and temporary tile state | 2.664s | 45.04 | 0.679 GB | 2.237 million |
| Optimized scalar CDEF | 2.201s | 54.53 | 0.683 GB | 2.237 million |
| Row-wise inter-prediction padding | 1.891s | 63.47 | 0.678 GB | 2.237 million |
| Direct CDEF output | 1.767s | 67.91 | 0.679 GB | 2.237 million |
| Allocation-free CDEF edges and inter padding | 1.792s | 66.95 | 0.655 GB | 0.628 million |
| Unrolled scalar CDEF strength paths | 1.757s | 68.31 | 0.657 GB | 0.628 million |
| SSE4.1 primary-only CDEF | 1.713s | 70.04 | 0.657 GB | 0.628 million |
| SSE4.1 combined CDEF | 1.553s | 77.28 | 0.656 GB | 0.628 million |
| Complete 4/8-wide SSE4.1 CDEF | 1.470s | 81.63 | 0.656 GB | 0.628 million |
| SSE4.1 two-pass 8-tap inter prediction | 1.397s | 85.91 | 0.641 GB | 0.627 million |
| Complete single-axis SSE4.1 8-tap inter | 1.360s | 88.22 | 0.641 GB | 0.627 million |
| Hoisted CDEF padding row boundaries | 1.333s | 90.02 | 0.641 GB | 0.627 million |
| SSE2 inverse-transform DC add | 1.332s | 90.12 | 0.641 GB | 0.627 million |

This is a further 28.4 percent wall-time reduction and an 81.0 percent
allocation-volume reduction. Relative to the original 10.522-second baseline,
median wall time is down 73.4 percent. A contemporaneous local clang dav1d
build measured 0.373 seconds, making this go-av1 build 7.51 times its wall
time; this ratio is not directly comparable with the GCC dav1d baseline above.

The final allocation counter reported:

```text
total_alloc_bytes=1852014968
mallocs=2298338
bytes_per_frame=15433458
mallocs_per_frame=19152
```

The complete local AOM differential run remained at 197 passed, zero failed,
and 66 unsupported high-bit-depth vectors. All 120 selected dynamic WebRTC
frames remained byte-exact against dav1d.

The follow-up compact-grid slice encodes transform-leaf left/top boundaries
inside the existing transform-size byte, removing two 16-bit origin grids.
Block indexes now use 16 bits on ordinary frames and automatically promote to
32 bits if a tile exceeds 65,535 metadata entries. This reduced allocation by
another 35.9 percent and median wall time by 5.3 percent from the preceding
row. Relative to the original baseline, median wall time is down 74.8 percent.
The contemporaneous clang dav1d median was 0.352 seconds, for a 7.54x ratio.

Frame-state pooling then reused fully reset entropy, block, transform, and
filter metadata across equal-sized frames. Multi-tile groups also reuse one
temporary tile state after merging each tile. Allocation fell another 42.8
percent to 679 MB, or 5.66 MB per frame. The 2.664-second median is effectively
flat against 2.655 seconds; the contemporaneous clang dav1d median was 0.365
seconds, for a 7.29x ratio.

The scalar CDEF follow-up hoists row-boundary calculations out of padding
pixel loops, expands the common primary-plus-secondary tap loop, and replaces
the small AV1 constrain domain with read-only lookup tables. Median wall time
fell 17.4 percent from 2.664 to 2.201 seconds while allocation remained
effectively unchanged. Relative to the original baseline, median wall time is
down 79.1 percent. The contemporaneous clang dav1d median was 0.372 seconds,
for a 5.92x ratio.

Inter-prediction edge extension now computes the horizontal clipped interval
once per prediction block. Each padded row copies its in-frame span in one
operation and fills only the left and right replicated edges, replacing a
clamp and indexed source load for every padded pixel. In a same-session
seven-run comparison, median wall time fell 14.8 percent from 2.220 to 1.891
seconds, while throughput rose from 54.04 to 63.47 packets/s. Allocation
remained effectively flat at 678 MB. The contemporaneous clang dav1d median
was 0.377 seconds, for a 5.02x ratio. Relative to the original 10.522-second
baseline, median wall time is down 82.0 percent.

CDEF now filters directly into the destination plane while retaining one
immutable source snapshot for all neighboring samples. The former second
full-plane work buffer, initial work copy, and per-block copy-in/copy-out
passes were redundant because CDEF blocks do not overlap and padding already
reads neighboring pixels from the source snapshot. Removing them reduced the
seven-run median another 6.6 percent, from 1.891 to 1.767 seconds, and raised
throughput to 67.91 packets/s. Allocation counters remained effectively flat.
Relative to the original baseline, median wall time is down 83.2 percent.

The next allocation-focused slice replaces per-CDEF-block dynamic left-edge
buffers with fixed local storage, omits unused top and bottom edge buffers,
and computes each luma CDEF block's non-skip state once for reuse by all three
planes. The inter-prediction padding pool now stores stable buffer pointers
instead of boxing byte slices on every prediction. In a same-session
comparison against the preceding committed binary, median wall time fell 1.3
percent from 1.816 to 1.792 seconds. Cumulative allocation fell from about 679
MB to 655 MB, while allocated objects fell 71.9 percent from 2.237 million to
0.628 million. The absolute median varied slightly from the preceding
measurement session; the same-session comparison is the relevant throughput
result.

The scalar CDEF primary-only and secondary-only pixel paths now hoist all
direction offsets out of the pixel loops and expand their fixed two-tap
iterations. This removes loop control, repeated direction-table indexing, and
per-tap weight selection from the current largest CPU hotspot. In a
same-session seven-run comparison, median wall time fell 2.5 percent from
1.802 to 1.757 seconds and throughput increased from 66.58 to 68.31 packets/s.
Allocation remained effectively unchanged at 657 MB and 0.628 million
objects. Relative to the original 10.522-second baseline, median wall time is
down 83.3 percent.

The first architecture-specific decoder kernel adds runtime amd64
CPUID/XGETBV detection and an SSSE3/SSE4.1 implementation of the eight-pixel
primary-only CDEF path. Unsupported CPUs, non-amd64 platforms, four-pixel
blocks, other CDEF strength combinations, and builds using the `purego` tag
retain the scalar implementation. Across five microbenchmark runs, the kernel
fell from approximately 294--297 ns to 152--155 ns, a reduction of about 48
percent. Its deliberately narrow coverage reduced the same-session end-to-end
median by 0.8 percent, from 1.727 to 1.713 seconds, and raised throughput from
69.50 to 70.04 packets/s. This establishes the tested SIMD dispatch and
fallback structure for wider CDEF and inter-prediction kernels.

The second SIMD slice covers eight-pixel CDEF blocks with both primary and
secondary strengths. It vectorizes all twelve constrained neighbors, weighted
accumulation, sentinel-aware neighborhood min/max, rounding, clipping, and
output packing. Three microbenchmark runs reduced this combined kernel from
approximately 691--694 ns to 249--252 ns, about 64 percent. In a same-session
comparison against the primary-only SIMD binary, the end-to-end median fell
9.6 percent from 1.717 to 1.553 seconds and throughput increased from 69.89 to
77.28 packets/s. The contemporaneous clang dav1d median was 0.355 seconds, for
a 4.37x wall-time ratio. Relative to the original 10.522-second baseline,
median wall time is down 85.2 percent.

The completed SSE4.1 CDEF path covers four- and eight-pixel-wide primary-only,
combined primary+secondary, and secondary-only filtering. More than 100,000
randomized scalar/SIMD comparisons cover both supported heights, every
direction, all supported strengths and damping values, and every edge mask.
In a stable same-session comparison, the decoder median fell 5.1 percent from
1.549 to 1.470 seconds and throughput increased from 77.45 to 81.63 packets/s.
The matching dav1d median was 0.353 seconds, for a 4.17x wall-time ratio.
Relative to the original 10.522-second baseline, median wall time is down
approximately 86.0 percent.

The first inter-prediction SIMD slice accelerates the common two-dimensional
8-tap path for widths divisible by eight. Its SSSE3/SSE4.1 horizontal pass
uses pairwise byte/coefficient multiply-adds to produce the Q4 intermediate,
and its vertical pass accumulates in 32-bit lanes before normative rounding
and clipping. Four-pixel blocks, single-axis filtering, unsupported CPUs, and
`purego` builds retain the scalar implementation. The optimized path passed
60,750 randomized scalar/SIMD comparisons across block sizes, all nine 8-tap
filter combinations, and every nonzero horizontal and vertical subpixel
phase. A same-session end-to-end comparison reduced the decoder median from
1.526 to 1.397 seconds (8.5 percent) and increased throughput from 78.66 to
85.91 packets/s. CPU profiling reduced `Put8Tap` cumulative time from about
14.8 to 4.3 percent. Relative to the original baseline, median wall time is
down approximately 86.7 percent.

The next inter SIMD slice adds horizontal-only and vertical-only kernels for
the same widths. It also hardens the shared horizontal convolution by widening
the four pairwise results to 32-bit lanes before accumulation. This is needed
for legal extreme sharp-filter pixel patterns even though each individual
`PMADDUBSW` pair is within range. In addition to the original 60,750
two-dimensional comparisons, 8,100 randomized single-axis cases and 180
coefficient-directed extreme patterns now cover this behavior. A stable
same-session comparison reduced the median from 1.433 to 1.360 seconds (5.1
percent), raised throughput from 83.73 to 88.22 packets/s, and measured 5.68x
the matching dav1d wall time. Relative to the original baseline, median wall
time is down approximately 87.1 percent.

CDEF padding now computes the row boundaries for its source, top, and bottom
planes once per block instead of repeating integer remainder operations for
every row. The fixed four- and eight-pixel active copies and two right-edge
samples are also specialized, removing small inner loops and a branch whose
condition is already encoded by the edge extent. A reference implementation
comparison covers 320 combinations of block size, row position, and edge
availability. The 8x8 all-edge microbenchmark fell from approximately 121 to
82 ns (32 percent), while a same-session end-to-end comparison fell from
1.349 to 1.333 seconds (1.2 percent) and reached 90.02 packets/s. Relative to
the original baseline, median wall time is down approximately 87.3 percent.

The inverse-transform DC-only path now applies its signed constant with SSE2
unsigned saturating byte arithmetic for every transform width of at least
eight pixels. Four-pixel transforms and non-amd64 or `purego` builds retain
the scalar path. The general transform path also stops clearing rows twice:
the pooled scratch buffer is cleared once before the populated rows are
transformed, so untouched rows are already zero. Across all supported square
and rectangular sizes, 336 scalar/SIMD comparisons cover three shifts and
large positive and negative DC extremes. The 16x16 DC-add microbenchmark fell
from approximately 120 to 31--35 ns (about 72 percent). A conservative
same-session end-to-end comparison fell from 1.381 to 1.357 seconds (1.7
percent); the final candidate rerun measured 1.332 seconds and 90.12
packets/s. The sampled inverse-transform cumulative share fell from
approximately 13.7 to 8.5 percent.

The general 8-bit inverse-transform output stage now rounds two groups of four
Q4 residuals, packs them to signed words, adds eight destination pixels, and
clips the result with SSE4.1. Width-four transforms and non-amd64 or `purego`
builds retain the scalar loop. Across all eligible square and rectangular
sizes, 1,600 randomized comparisons cover the complete signed 16-bit residual
range and destination row padding. The 16x16 output-stage microbenchmark fell
from approximately 211 to 96 ns (55 percent). A same-session end-to-end
comparison fell from 1.368 to 1.356 seconds (0.9 percent), while the final
candidate rerun measured 1.333 seconds and 90.05 packets/s. The former scalar
pixel-add loop no longer appears as a standalone profile hotspot; the SIMD
wrapper accounts for approximately 0.8 percent in the sampled run.

CDEF direction search now accumulates two rows at a time. Fixed pixel loads
replace the nested 8x8 indexing loop, horizontal pixel pairs are shared by the
shallow-direction sums, and vertical row pairs are shared by the remaining
directions. An exact reference comparison covers 20,000 randomized blocks
with varied strides and offsets plus every constant byte value. The 8x8
microbenchmark fell from approximately 165 to 106 ns (36 percent). A
same-session end-to-end comparison fell from 1.345 to 1.324 seconds (1.6
percent), increasing throughput from 89.23 to 90.64 packets/s. A final
candidate rerun measured 1.335 seconds and 89.85 packets/s, or 5.45x the
matching dav1d wall time. The sampled `FindDir` share fell from approximately
5.3 to 2.4 percent.

Non-zero frame-state resets now seed 32 bytes and grow the initialized region
with overlap-safe copies instead of storing every byte in a scalar loop.
`SetTxState` similarly fills one transform row at a time, hoists its boundary
decisions, and keeps a direct single-column path for 4x4 transforms. Boundary
tests cover fill sizes around the scalar/copy threshold, and 10,000 randomized
transform rectangles match the previous cell-wise implementation exactly.
The 256 KiB fill microbenchmark fell from approximately 148 to 8.7
microseconds (94 percent), while the 64x64 transform-state benchmark fell
from approximately 350 to 205 ns (41 percent); aligned 4x4 writes improved
from approximately 7 to 3.1 ns. A same-session all-filter comparison reduced
the median from 1.3146 to 1.3017 seconds (1.0 percent), increasing throughput
from 91.28 to 92.19 packets/s. The standard seven-run result measured 1.306
seconds and 91.91 packets/s, or 5.36x the matching dav1d wall time.

Palette index ordering now completes its neighbor-derived prefix from a
2 KiB table indexed by the eight-color used mask. This replaces eight
branching color-presence checks per decoded palette pixel with one small
copy. A retained reference implementation matches the optimized output for
1,000 randomized blocks across varied widths, heights, strides, and every
diagonal. The 64x64 ordering microbenchmark fell from approximately 67 to
38 microseconds (43 percent). On a 599-frame palette-bearing profile,
`orderPalette` fell from approximately 0.41 seconds (3.7 percent) to 0.08
seconds (0.5 percent), leaving entropy symbol decoding as the main palette
index cost. The corresponding long wall-clock A/B was inconclusive under
heavy frequency variation: the medians differed by less than 0.1 percent,
so no end-to-end gain is claimed for this content.

## Measurement Rules

`cmd/av1-benchcmp` enforces the following command-level methodology:

1. Both decoders are prebuilt. Compiler time is never included.
2. The exact input is identified by SHA-256.
3. Both decoders receive the same packet limit, filter selection, film-grain
   selection, and thread count.
4. Output writing and frame hashing are disabled during timed runs.
5. Each decoder is warmed before measurement.
6. Decoder execution order alternates each round to reduce ordering and
   temperature bias.
7. The median of repeated wall-clock samples is the primary result.
8. Process exit failures abort the comparison.
9. Executable versions and SHA-256 values are stored in the JSON report.

`packets/s` means IVF packets consumed per second, not necessarily display
frames per second. For the measured WebRTC segment, the correctness run
confirmed 120 compared output frames for 120 input packets.

dav1d uses its normal optimized release configuration, including runtime SIMD
dispatch. This measures the performance users actually receive from dav1d. It
is not intended to isolate C versus Go language cost or scalar kernel quality.

## Prerequisites

The commands below assume this sibling checkout layout:

```text
E:\workspace\goworkspace\av1
|-- go-av1
`-- dav1d
```

Required tools:

- Go 1.22 or newer;
- Meson and Ninja;
- GCC/MinGW and NASM for the Windows dav1d build.

Use a quiet machine, close video playback and browser capture, select a fixed
Windows power mode, and avoid comparing results produced under different
thermal or power conditions.

## Build dav1d

The comparison requires a release build. Do not use a Meson debug build:

```powershell
Set-Location E:\workspace\goworkspace\av1\dav1d

$env:CC = 'gcc'
$env:CCACHE_DISABLE = '1'

meson setup build-release-gcc `
  --buildtype=release `
  -Denable_tests=false `
  -Denable_examples=false

meson compile -C build-release-gcc --jobs 4
```

For an existing build directory, reconfigure and rebuild:

```powershell
meson setup build-release-gcc --reconfigure --buildtype=release
meson compile -C build-release-gcc --jobs 4
```

Verify the important Meson settings:

```powershell
meson configure build-release-gcc |
  Select-String -Pattern 'buildtype|optimization|debug\s'
```

The expected values are `buildtype=release`, `optimization=3`, and
`debug=false`.

## Build go-av1

Build once so repeated measurements do not include `go run` compilation:

```powershell
Set-Location E:\workspace\goworkspace\av1\go-av1

New-Item -ItemType Directory -Force logs\bin | Out-Null

go build -trimpath -ldflags='-s -w' `
  -o logs\bin\go-av1d.exe `
  ./cmd/go-av1d
```

The `logs` directory is ignored by Git and is intended for local binaries and
raw result files.

## Correctness Gate

Set `PATH` so the release dav1d executable can find `libdav1d.dll`, then
compare the timed segment:

```powershell
$dav1dBuild = 'E:\workspace\goworkspace\av1\dav1d\build-release-gcc'
$env:PATH = "$dav1dBuild\src;$env:PATH"

go run ./cmd/av1-conformance `
  -i cmd/webrtc-av1d/output.ivf `
  -dav1d "$dav1dBuild\tools\dav1d.exe" `
  -limit 120 `
  -report logs\benchmark-correctness.json
```

The report must contain:

```json
{
  "comparison": {
    "passed": true,
    "compared_frames": 120
  }
}
```

Do not publish performance numbers when this comparison fails.

## Reproduce Allocation Result

The decoder CLI can report cumulative allocations for the decode loop without
changing normal decoder behavior:

```powershell
$env:GOMAXPROCS = '1'

go run ./cmd/go-av1d `
  -i cmd/webrtc-av1d/output.ivf `
  -limit 120 `
  -threads 1 `
  -memstats
```

The `memstats` line reports total allocated bytes and objects, plus per-output
frame averages. It includes IVF packet reads and decoder output handling inside
the measured loop, but excludes decoder and demuxer construction. Run a
correctness comparison first and use the same input, packet limit, filters,
film-grain setting, thread count, Go version, and `GOMAXPROCS` when comparing
results.

## Reproduce WebRTC Result

The measured local input has:

```text
path: cmd/webrtc-av1d/output.ivf
size: 14,859,769 bytes
IVF packets: 996
SHA-256: 29be066919442a2a9b247938634917df44eb7cc28cf92d3aaf8819e612c2a794
```

Run:

```powershell
$dav1dBuild = 'E:\workspace\goworkspace\av1\dav1d\build-release-gcc'

go run ./cmd/av1-benchcmp `
  -input cmd/webrtc-av1d/output.ivf `
  -go-decoder logs/bin/go-av1d.exe `
  -dav1d "$dav1dBuild/tools/dav1d.exe" `
  -dav1d-libdir "$dav1dBuild/src" `
  -limit 120 `
  -threads 1 `
  -warmup 2 `
  -runs 7 `
  -json logs/performance-go-vs-dav1d.json `
  -markdown logs/performance-go-vs-dav1d.md
```

Remove `-limit 120` to measure the complete 996-packet recording. A complete
run takes substantially longer and must not be compared directly with this
120-packet baseline.

## Reproduce AOM All-Intra Result

The second baseline uses:

```text
path: test/conformance/vectors/aom/av1-1-b8-02-allintra.ivf
size: 1,488,725 bytes
dimensions: 352x288
IVF packets: 39
SHA-256: 5fcd265fd9f9bdd0d3179340b4c4532f1422ca5e5d97741c7481b84cb5dc122f
```

Run:

```powershell
$dav1dBuild = 'E:\workspace\goworkspace\av1\dav1d\build-release-gcc'

go run ./cmd/av1-benchcmp `
  -input test/conformance/vectors/aom/av1-1-b8-02-allintra.ivf `
  -go-decoder logs/bin/go-av1d.exe `
  -dav1d "$dav1dBuild/tools/dav1d.exe" `
  -dav1d-libdir "$dav1dBuild/src" `
  -threads 1 `
  -warmup 2 `
  -runs 7 `
  -json logs/performance-allintra-go-vs-dav1d.json `
  -markdown logs/performance-allintra-go-vs-dav1d.md
```

## Reading the Report

The JSON report is the machine-readable artifact. It includes:

- environment and Go runtime;
- input path, byte size, SHA-256, IVF dimensions, and packet count;
- warmup, sample count, thread count, filters, and film-grain setting;
- every raw wall-time sample;
- median, mean, minimum, maximum, packets/s, and nominal MP/s;
- decoder version and executable SHA-256;
- the go-av1/dav1d median wall-time ratio.

The Markdown report is a compact local summary generated from the same data.
Raw reports stay under ignored `logs/`; stable baseline values and the exact
methodology are maintained in this document.

## Extending the Corpus

Future committed baselines should add separate rows for:

- fixed-resolution 720p camera capture;
- fixed-resolution 1080p camera and desktop capture;
- 4K inter-heavy content;
- a short dynamic-resolution stream;
- film-grain content after film grain is implemented;
- multi-thread configurations after decoder scheduling is implemented.

Never combine inputs into a single average ratio. AV1 tool usage, resolution,
reference structure, and filter work materially change the performance gap.
