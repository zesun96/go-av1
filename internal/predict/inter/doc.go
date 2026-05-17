// Package inter implements AV1 inter prediction:
//
//   - 8-tap and 12-tap subpel motion compensation filters
//   - Compound prediction with weighted, masked, and wedge variants
//   - Overlapped block motion compensation (OBMC)
//   - Warped motion (affine) compensation
//
// Reference implementation:
//
//   - dav1d/src/mc.{h,c} (mc_tmpl.c)
//   - dav1d/src/warpmv.{c,h}
//   - dav1d/src/wedge.{c,h}
//
// Milestone: M4 (generic), M9 (assembly).
package inter
