// Package tile owns the per-tile decoding state: superblock and block
// partition trees, coefficient buffers, and per-tile MSAC contexts.
//
// Tile decoding is the unit of intra-frame parallelism (M7).
//
// Reference implementation:
//
//   - dav1d/src/decode.c (tile group decoding)
//   - dav1d/src/recon_tmpl.c
//
// Milestone: M3 (skeleton), M4 (inter), M7 (parallel).
package tile
