// Package transform implements AV1 inverse transforms.
//
// It exposes a kernel table indexed by (transform size, transform type) so
// SIMD fast paths can be plugged in without touching call sites.
//
// Reference implementation:
//
//   - dav1d/src/itx.h
//   - dav1d/src/itx_1d.{c,h}
//   - dav1d/src/itx_tmpl.c
//
// Milestone: M3 (generic), M9 (assembly fast paths).
package transform
