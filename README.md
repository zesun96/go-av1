# go-av1

[![Go Reference](https://pkg.go.dev/badge/github.com/zesun96/go-av1.svg)](https://pkg.go.dev/github.com/zesun96/go-av1) [![Go Report Card](https://goreportcard.com/badge/github.com/zesun96/go-av1)](https://goreportcard.com/report/github.com/zesun96/go-av1) [![License: BSD-2-Clause](https://img.shields.io/badge/license-BSD--2--Clause-blue.svg)](LICENSE)

A pure-Go AV1 video codec. No cgo, no system libraries — just `go get`.

> Status: early development. The public API is stable in shape but every
> constructor currently returns `av1.ErrNotImplemented`. See
> [`docs/ROADMAP.md`](docs/ROADMAP.md) for milestones.

## Features

- Pure Go decoder targeting AV1 Profile 0 (Main), 8-bit, 4:2:0.
- Streaming `SendData` / `GetPicture` API plus an `io.Reader` convenience helper.
- Reference-counted picture pool to keep GC pressure low.
- Optional `amd64` / `arm64` SIMD fast paths (planned, opt-out via `-tags purego`).
- Encoder support is on the roadmap.

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
    dec, err := av1.NewDecoder(av1.DecoderOptions{})
    if err != nil {
        log.Fatal(err)
    }
    defer dec.Close()

    err = av1.DecodeReader(os.Stdin, func(pic *av1.Picture, err error) bool {
        if err != nil {
            log.Print(err)
            return false
        }
        defer pic.Release()
        // pic.Y / pic.U / pic.V hold the planar samples.
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
| [`go-av1d`](cmd/go-av1d) | AV1 decoder — IVF/Annex-B → Y4M | `go install github.com/zesun96/go-av1/cmd/go-av1d@latest` |
| [`go-av1enc`](cmd/go-av1enc) | AV1 encoder — Y4M → IVF *(planned)* | `go install github.com/zesun96/go-av1/cmd/go-av1enc@latest` |
| [`webrtc-av1d`](cmd/webrtc-av1d) | WebRTC receiver — browser AV1 → IVF + decode | see [cmd/webrtc-av1d](cmd/webrtc-av1d/README.md) |

See [`cmd/README.md`](cmd/README.md) for full usage details.

## Documentation

- [Design](docs/DESIGN.md) — architecture, concurrency, memory model, API.
- [Roadmap](docs/ROADMAP.md) — milestones and exit criteria.
- API reference: <https://pkg.go.dev/github.com/zesun96/go-av1/pkg/av1>.

## Contributing

Bug reports and pull requests are welcome. Please run `go vet ./...` and
`go test ./...` before submitting.

## License

BSD 2-Clause. See [`LICENSE`](LICENSE).
