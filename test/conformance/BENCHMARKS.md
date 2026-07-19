# Decoder Microbenchmark Baseline

Baseline date: 2026-07-19

Environment:

- Go 1.24.0, windows/amd64;
- Intel Core i7-13700KF, 24 logical CPUs visible to Go;
- generic pure-Go dispatch;
- `200ms` benchtime, three samples per benchmark.

Command:

```powershell
go test ./internal/bitstream ./internal/transform `
  ./internal/predict/intra ./internal/predict/inter ./internal/refmvs `
  ./internal/loopfilter ./internal/cdef ./internal/looprestoration `
  -run '^$' -bench . -benchmem -benchtime=200ms -count=3
```

Representative median results:

| Benchmark | Median ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| MSAC BoolEqui, 4096 symbols | 19,066 | 0 | 0 |
| MSAC 8-way symbol, 1024 symbols | 4,966 | 0 | 0 |
| inverse DCT 8x8 | 1,999 | 16,384 | 1 |
| inverse DCT 16x16 | 3,406 | 16,384 | 1 |
| intra DC 32x32 | 543 | 0 | 0 |
| intra Paeth 32x32 | 1,508 | 0 | 0 |
| intra Smooth 32x32 | 1,389 | 0 | 0 |
| inter 8-tap 64x64 | 35,271 | 18,448 | 3 |
| compound Avg 64x64 | 3,656 | 0 | 0 |
| compound WAvg 64x64 | 3,983 | 0 | 0 |
| spatial reference-MV search | 86 | 0 | 0 |
| deblock horizontal 8 | 17.45 | 0 | 0 |
| deblock vertical 8 | 17.37 | 0 | 0 |
| CDEF 8x8 | 1,441 | 0 | 0 |
| Wiener restoration 64x4 | 3,577 | 0 | 0 |
| SGR 3x3 restoration 64x4 | 2,815 | 0 | 0 |

Interpretation:

- `InvTxfmAdd` allocates a fixed 16 KiB temporary buffer for every call. This
  is a high-priority decoder allocation target.
- the two-dimensional `Put8Tap` path allocates about 18 KiB in three objects
  per 64x64 block. Scratch reuse should remove these allocations before SIMD
  or assembly work is considered.
- the other measured kernels do not allocate on the heap in these paths.
- filter and restoration benchmarks restore their deterministic input inside
  the timed loop; their numbers include that small copy cost.
- results are comparison baselines, not performance promises. Use `benchstat`
  over repeated old/new samples before accepting an optimization.

The full raw output for this baseline is intentionally kept under ignored
`logs/benchmark-b0-2026-07-19.txt` rather than committed.

## Scratch Reuse Result

The first allocation pass replaced escaping/local MC and transform temporaries
with bounded, concurrency-safe scratch pools. Results on the same machine:

| Benchmark | Baseline | After | Allocation change |
|---|---:|---:|---:|
| inverse DCT 8x8 | 1,999 ns/op | 314 ns/op | 16,384 B/1 to 0 B/0 |
| inverse DCT 16x16 | 3,406 ns/op | 1,570 ns/op | 16,384 B/1 to 0 B/0 |
| inter 8-tap 64x64 | 35,271 ns/op | 32,768 ns/op | 18,448 B/3 to 0 B/0 |

Transform scratch is cleared over the complete active footprint before use;
this is required because large transforms intentionally load only the first 32
coefficient rows. A regression test contaminates pooled storage between 64x64
calls to enforce that invariant. The MC pool covers the maximum 128x135
intermediate footprint and retains an allocation fallback for oversized input.
