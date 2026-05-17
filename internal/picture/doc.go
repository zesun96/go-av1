// Package picture owns the planar pixel buffer pool and lifecycle.
//
// All decoded plane buffers live in a sync.Pool-backed allocator with
// reference counting; the public *av1.Picture is a thin façade over the
// internal Frame defined here.
//
// Reference implementation:
//
//   - dav1d/src/picture.{c,h}
//   - dav1d/src/ref.{c,h}
//
// Milestone: M0 (interface), M3 (implementation).
package picture
