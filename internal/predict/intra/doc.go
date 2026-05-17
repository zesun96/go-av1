// Package intra implements AV1 intra prediction modes:
//
//   - DC, SMOOTH, SMOOTH_H, SMOOTH_V, PAETH
//   - Directional prediction with all angle deltas
//   - Recursive intra prediction
//   - Chroma-from-luma (CFL)
//   - Palette mode
//   - Intra block copy (IBC)
//
// Reference implementation:
//
//   - dav1d/src/ipred.h
//   - dav1d/src/ipred_prepare_tmpl.c
//   - dav1d/src/ipred_tmpl.c
//   - dav1d/src/pal.{c,h}
//
// Milestone: M3 (skeleton modes), M4 (palette + IBC), M9 (assembly).
package intra
