// Package header defines the AV1 sequence, frame, and tile header structs.
//
// Field naming follows the AV1 specification (section 5) and the dav1d
// reference. Concrete decoding logic lives in package obu; this package is
// intentionally data-only so it can be imported widely without cycles.
//
// Reference implementation:
//
//   - dav1d/include/dav1d/headers.h
//   - dav1d/src/levels.h
//
// Milestone: M2.
package header
