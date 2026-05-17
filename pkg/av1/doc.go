// Package av1 is the public API of the go-av1 codec.
//
// The package exposes a streaming decoder modelled after the dav1d
// Send-Data / Get-Picture state machine, plus higher-level convenience
// helpers built on top of io.Reader.
//
// Two stages are planned:
//
//   - Phase 1 (current scaffold): a Profile 0 / Main, 8-bit, 4:2:0 decoder
//     aligned with dav1d's default capability set.
//   - Phase 2: an encoder taking inspiration from SVT-AV1.
//
// At milestone M0 every constructor returns ErrNotImplemented. The shape of
// the API is frozen so internal packages can be filled in incrementally.
package av1
