# go-av1

[![Go Reference](https://pkg.go.dev/badge/github.com/zesun96/go-av1.svg)](https://pkg.go.dev/github.com/zesun96/go-av1) [![Go Report Card](https://goreportcard.com/badge/github.com/zesun96/go-av1)](https://goreportcard.com/report/github.com/zesun96/go-av1) [![License: BSD-2-Clause](https://img.shields.io/badge/license-BSD--2--Clause-blue.svg)](LICENSE)

A pure-Go AV1 video codec. No cgo and no required system libraries.

> Status: active development. The decoder currently targets AV1 Profile 0,
> 8-bit, 4:2:0 streams. The API is usable, but unsupported AV1 profiles,
> pixel formats, and bit depths return `av1.ErrUnsupported`.

## Features

- Pure Go AV1 Profile 0 decoder for 8-bit, 4:2:0 video.
- Streaming `SendData` / `GetPicture` API plus an `io.Reader` convenience helper.
- Reference-counted picture pool to keep GC pressure low.
- Deblocking, CDEF, loop restoration, intra/inter prediction, compound
  prediction, warped motion, and dynamic frame dimensions.
- 174 applicable 8-bit AOM conformance vectors match dav1d at the native
  visible-plane level.
- Optional `amd64` / `arm64` SIMD fast paths are planned.
- Experimental encoder implementation under active development.

Not currently supported:

- 10-bit and 12-bit sample storage and reconstruction;
- Profile 1/2 formats, including 4:2:2 and 4:4:4 output;
- every AV1 scalability and operating-point configuration.

High-bit-depth support is planned as an additive API extension; existing
8-bit `Picture.Y`, `Picture.U`, and `Picture.V` fields will remain `[]byte`.

## Installation

```sh
go get github.com/zesun96/go-av1
```

Requires Go 1.22 or newer.

## Usage

```go
package main

import (
    "log"
    "os"

    "github.com/zesun96/go-av1/pkg/av1"
)

func main() {
    err := av1.DecodeReader(os.Stdin, func(pic *av1.Picture, err error) bool {
        if err != nil {
            log.Print(err)
            return false
        }
        // pic.Y, pic.U, and pic.V contain 8-bit planar samples. DecodeReader
        // releases pic after this callback returns.
        return true
    })
    if err != nil {
        log.Fatal(err)
    }
}
```

## Command-line tools

| Tool | Description | Install |
|---|---|---|
| [`go-av1d`](cmd/go-av1d) | AV1 decoder: IVF to Y4M | `go install github.com/zesun96/go-av1/cmd/go-av1d@latest` |
| [`av1-benchcmp`](cmd/av1-benchcmp) | Repeated go-av1 versus dav1d performance comparison | developer tool |
| [`go-av1enc`](cmd/go-av1enc) | Experimental AV1 encoder: Y4M to IVF | `go install github.com/zesun96/go-av1/cmd/go-av1enc@latest` |
| [`webrtc-av1d`](cmd/webrtc-av1d) | Browser AV1 WebRTC receiver, recorder, and decoder | see [cmd/webrtc-av1d](cmd/webrtc-av1d/README.md) |

See [`cmd/README.md`](cmd/README.md) for full usage details.

## Documentation

- [Design](docs/DESIGN.md) - architecture, concurrency, memory model, and API.
- [Roadmap](docs/ROADMAP.md) - milestones and exit criteria.
- [Performance comparison](docs/PERFORMANCE.md) - local go-av1 versus dav1d
  baseline and reproduction steps.
- API reference: <https://pkg.go.dev/github.com/zesun96/go-av1/pkg/av1>.

## Contributing

Bug reports and pull requests are welcome. Please run `go vet ./...` and
`go test ./...` before submitting.

## License

BSD 2-Clause. See [`LICENSE`](LICENSE).
