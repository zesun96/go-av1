// Package obu parses AV1 Open Bitstream Units.
//
// It frames OBUs from an arbitrary byte stream (Annex-B size-prefixed or
// length-delimited) and decodes per-OBU headers into the structures defined
// by package header.
//
// Reference implementation:
//
//   - dav1d/src/obu.{c,h}
//
// Milestone: M2.
package obu
