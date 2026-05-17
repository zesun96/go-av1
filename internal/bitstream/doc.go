// Package bitstream provides AV1 entropy primitives: a fixed-width bit
// reader and a multi-symbol arithmetic decoder (MSAC).
//
// Reference implementation:
//
//   - dav1d/src/getbits.{c,h}
//   - dav1d/src/msac.{c,h}
//   - dav1d/src/cdf.{c,h}
//
// Milestone: M1.
package bitstream
