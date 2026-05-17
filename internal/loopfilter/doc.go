// Package loopfilter implements the AV1 deblocking filter.
//
// Reference implementation:
//
//   - dav1d/src/loopfilter.h
//   - dav1d/src/loopfilter_tmpl.c
//   - dav1d/src/lf_apply_tmpl.c, lf_mask.c
//
// Milestone: M5 (generic), M9 (assembly).
package loopfilter
