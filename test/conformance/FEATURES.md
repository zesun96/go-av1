# Decoder Feature Matrix

This matrix describes the strict decoder path as of 2026-07-19. `Supported`
means exercised by unit tests or native pixel comparison. `Partial` means some
syntax or kernels exist but the normative tool is not complete. `Unsupported`
must produce a classified error rather than silently claim conformance.

| Area | Feature | Status | Evidence or missing work |
|---|---|---|---|
| Container | IVF input | Supported | Streaming demuxer tests and WebRTC recordings |
| Bitstream | OBU and sequence/frame headers | Supported | High package coverage; strict parsing tests |
| Format | Profile 0, 8-bit, 4:2:0 | Supported subset | Current WebRTC differential corpus |
| Format | 10/12-bit samples | Unsupported | Reconstruction storage is byte-based |
| Format | 4:2:2, 4:4:4, monochrome decode | Unsupported | Geometry types exist; live pipeline is 4:2:0 |
| Intra | DC, directional, smooth, Paeth | Supported | Predictor unit tests and live decode |
| Intra | CFL, palette, filter intra | Supported subset | Unit coverage; corpus breadth still limited |
| Inter | Single-reference translational MC | Supported | WebRTC native hashes match dav1d |
| Inter | Equal-average compound | Supported subset | Exercised paths match current recordings |
| Inter | Distance-weighted compound | Partial | Kernel exists; normative syntax wiring incomplete |
| Inter | Wedge/difference masks | Unsupported | Mask syntax and blending not wired |
| Inter | Inter-intra and OBMC | Unsupported | Decoder paths absent |
| Inter | Warped/affine/global motion | Partial | Header state exists; reconstruction incomplete |
| Inter | Intra-block copy | Unsupported | Syntax and reference constraints absent |
| Transform | Coefficients and inverse transforms | Supported subset | Strong unit coverage and WebRTC differential tests |
| State | Reference refresh/show-existing | Supported subset | WebRTC, resizing, and recovery recordings |
| State | Segmentation temporal persistence | Partial | Common paths work; corpus validation incomplete |
| Filter | Deblocking | Supported subset | Kernel and live-path tests |
| Filter | CDEF | Supported subset | Kernel and live-path tests |
| Filter | Loop restoration | Partial | Kernels exist; frame application is not wired |
| Display | Super-resolution | Unsupported | Header parsing exists; upscale stage absent |
| Display | Film grain | Unsupported | Parameters parse; synthesis is absent |
| Scheduling | Tile/frame parallel decode | Unsupported | Decoder is currently serialized |
| API | Threads/MaxFrameDelay | Unsupported | Options are accepted but not scheduled |
| Robustness | Strict failed-frame isolation | Supported subset | Main decode errors do not refresh references |
| Robustness | Exact MSAC exhaustion detection | Partial | Additional syntax-boundary validation required |

The authoritative completion gate is corpus pass rate, not this table. Update
the matrix when a tool is wired and its native output is verified against a
reference corpus.
