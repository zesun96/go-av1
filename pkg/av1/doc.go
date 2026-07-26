// Package av1 is the public API of the go-av1 codec.
//
// The package exposes a streaming decoder modelled after the dav1d
// Send-Data / Get-Picture state machine, plus higher-level convenience
// helpers built on top of io.Reader.
//
// The current decoder targets Profile 0, 8-bit, 4:2:0 streams. Syntactically
// valid streams that require unsupported profiles, pixel formats, or bit
// depths return ErrUnsupported. The encoder remains experimental.
package av1
